package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/access"
	"github.com/OpenDeskViewer/platform/api/internal/apiv1"
	"github.com/OpenDeskViewer/platform/api/internal/audit"
	"github.com/OpenDeskViewer/platform/api/internal/auth"
	"github.com/OpenDeskViewer/platform/api/internal/config"
	"github.com/OpenDeskViewer/platform/api/internal/deviceauth"
	"github.com/OpenDeskViewer/platform/api/internal/devicepw"
	"github.com/OpenDeskViewer/platform/api/internal/enrollment"
	"github.com/OpenDeskViewer/platform/api/internal/fleet"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/OpenDeskViewer/platform/api/internal/identity"
	"github.com/OpenDeskViewer/platform/api/internal/migrations"
	"github.com/OpenDeskViewer/platform/api/internal/monitoring"
	postgres2 "github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/OpenDeskViewer/platform/api/internal/rustdeskapi"
	"github.com/OpenDeskViewer/platform/api/internal/telemetry"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// zerologPrinter adapts zerolog to the migrations runner's Logger.
type zerologPrinter struct{}

func (zerologPrinter) Printf(format string, v ...any) {
	log.Info().Msgf(format, v...)
}

func main() {
	cfg, err := config.LoadConfig(".env")
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to load configuration")
	}

	// Fail closed: no usable issuer means no server, rather than a server that
	// authenticates nobody.
	if err := cfg.ValidateAPI(); err != nil {
		log.Fatal().Err(err).Msg("Invalid API configuration")
	}

	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	if cfg.Debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout})

	log.Info().Msg("OpenDeskViewer API Server starting")

	// Migrations run first, and as the schema owner.
	//
	// The order is load-bearing since 000008: the pool below connects as
	// odv_app, and it is 000008 that grants odv_app the right to connect at all.
	// Opening the pool first would work today, because pgxpool connects lazily,
	// and would fail on the first request of a first deployment. This way a
	// database that has never been migrated fails at the migration, with the
	// migration's own error.
	//
	// Migrating before the router exists also means the process never serves
	// traffic against a schema it does not understand. The files are embedded in
	// the binary, so this does not depend on a CLI or a mounted directory.
	// ODV_MIGRATE=false is for operators who apply migrations out of band.
	//
	// Deliberately without a deadline. golang-migrate takes a session advisory
	// lock, so a second replica starting at the same time waits here; putting a
	// timeout on that would turn "another replica is migrating" into a crash
	// loop. A migration that genuinely hangs shows up as a container that never
	// becomes healthy, which is the correct signal.
	if os.Getenv("ODV_MIGRATE") != "false" {
		// Same builder as the pool, so the migration runner cannot end up on a
		// different TLS setting from the connection that serves traffic.
		dsn := postgres2.DSN(cfg.PostgresHost, cfg.PostgresPort,
			cfg.PostgresDB, cfg.PostgresUser, cfg.PostgresPassword, cfg.PostgresSSLMode)
		if err := migrations.Run(dsn, &zerologPrinter{}); err != nil {
			log.Fatal().Err(err).Msg("Failed to apply database migrations")
		}
	}

	// The request-serving pool, as odv_app rather than as the owner.
	//
	// This is the second half of 3.5. An owner is not restricted by its own
	// grants and can disable its own triggers, so the append-only audit log was
	// only append-only against accident. odv_app holds SELECT and INSERT on
	// audit_events and nothing else, and cannot TRUNCATE it or turn the trigger
	// off. Migration 000008 defines that; RuntimeDBUser only decides which
	// credential is used.
	dbUser, dbPassword := cfg.RuntimeDBUser()
	if dbUser == cfg.PostgresUser {
		log.Warn().Msg("POSTGRES_APP_PASSWORD is not set, so the API is serving requests as the schema owner; the append-only audit log is enforced by a trigger the owner could disable")
	}

	// This deadline is for opening the pool and nothing else, so it is cancelled
	// as soon as that is done rather than deferred to the end of main.
	poolCtx, cancelPool := context.WithTimeout(context.Background(), 10*time.Second)
	pgPool, err := postgres2.New(poolCtx,
		cfg.PostgresHost, cfg.PostgresPort,
		cfg.PostgresDB, dbUser, dbPassword,
		cfg.PostgresSSLMode,
	)
	cancelPool()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize PostgreSQL")
	}
	defer pgPool.Close()

	log.Info().Str("role", dbUser).Msg("PostgreSQL connection established")

	// Connectivity history and the notification outbox. The API only writes to
	// it, on the recovery half of the heartbeat; the worker is what delivers.
	monitoringService := monitoring.New(pgPool)

	// One fleet service, injected into everything that needs it, rather than
	// three built independently.
	fleetService := fleet.NewService(pgPool, cfg)

	telemetryService := telemetry.NewService(pgPool, cfg, fleetService, monitoringService)
	authService := identity.NewAuthService(pgPool, cfg.BootstrapAdminEmail)

	// Create access resolver
	accessResolver := access.NewSQLResolver(pgPool, cfg)

	// Token validator. Config validation guarantees a usable issuer, so this is
	// never nil: there is no code path that starts the server unauthenticated.
	//
	// The two URLs differ on purpose. Keycloak is configured with the public
	// hostname, so every token it mints carries iss=https://<public host>/realms/...
	// and validating against the internal service name would reject every real
	// token. The JWKS, on the other hand, is fetched over the compose network:
	// that keeps key retrieval off the public interface and independent of the
	// proxy and of public DNS resolving from inside the network.
	internalRealmURL := cfg.KeycloakURL + "/realms/" + cfg.KeycloakRealm
	jwtValidator := auth.NewJWTValidator(
		auth.NewJWKSProvider(internalRealmURL+"/protocol/openid-connect/certs"),
		auth.WithIssuer(cfg.OIDCIssuer),
		auth.WithAudience(cfg.KeycloakClientID),
		auth.WithAlgorithm("RS256"),
	)

	oidcBroker := rustdeskapi.NewOIDCBroker(jwtValidator, authService)
	// The RustDesk client's browser sign-in. It is separate from oidcBroker
	// because it needs the database and the deployment's public host, and
	// because it is a three-endpoint flow rather than a single handler.
	oidcLogin := rustdeskapi.NewOIDCLogin(pgPool, cfg, jwtValidator, authService)
	// Audit service for recording mutations
	auditService := audit.New(pgPool)

	// Device connection passwords. ValidateAPI has already checked the key
	// decodes, so this cannot fail for a reason the operator has not been told
	// about; it is still checked, because a nil service here would turn into a
	// fleet with no revocable credentials and nothing saying so.
	devicePasswordKey, err := devicepw.ParseKey(cfg.DevicePasswordKey)
	if err != nil {
		log.Fatal().Err(err).Msg("Invalid DEVICE_PASSWORD_KEY")
	}
	devicePasswords, err := devicepw.New(pgPool, devicePasswordKey)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialise device password storage")
	}

	// Account creation and removal, which reach into Keycloak as the odv-api
	// service account. NewKeycloakAdmin returns nil when the deployment has not
	// configured the client secret, and the two routes that need it answer 503
	// naming what is missing: an API that refused to start because accounts
	// cannot be created would take the whole fleet down for a portal feature.
	accountProvisioner := identity.NewKeycloakAdmin(
		cfg.KeycloakURL, cfg.KeycloakRealm, cfg.KeycloakClient, cfg.KeycloakSecret)
	if accountProvisioner == nil {
		log.Warn().Msg("Keycloak admin credentials are not configured; creating and removing user accounts from the portal is disabled")
	}

	// apiv1 handler for our REST API
	apiv1Handler := apiv1.NewHandler(pgPool, fleetService, accessResolver, auditService, cfg, devicePasswords, accountProvisioner)

	// Device credentials. This is what makes /api/heartbeat mean something: a
	// device proves who it is instead of asserting it.
	deviceAuthService := deviceauth.New(pgPool)

	rustdeskHandlers := rustdeskapi.NewHandlers(authService, telemetryService, auditService, deviceAuthService, devicePasswords)
	abHandler := rustdeskapi.NewAddressBookHandler(pgPool, accessResolver, cfg.AddressBookMaxPeers, auditService)
	peerHandler := rustdeskapi.NewPeerHandler(pgPool, accessResolver, auditService)
	auditHandler := rustdeskapi.NewAuditHandler(pgPool, accessResolver, auditService, cfg)
	usersHandler := rustdeskapi.NewUsersHandler(authService, accessResolver)

	// Enrollment service handles token operations
	enrollmentService := enrollment.NewService(pgPool, cfg, fleetService)

	enrollmentHandler := rustdeskapi.NewEnrollmentHandler(pgPool, accessResolver, enrollmentService, fleetService, auditService)
	sysinfoVerHandler := rustdeskapi.NewSysinfoVerHandler(pgPool)

	// public carries everything that must work without a token. protected adds
	// the JWT middleware on top. Splitting the muxes rather than allowlisting
	// paths inside the middleware means a route left off this list is
	// unreachable, not unauthenticated.
	//
	// The 1 MiB body cap is on the base router, so it covers every route
	// including ones added later. The largest legitimate body is an address
	// book push, which is a few hundred peers of JSON.
	//
	// The 10-second request deadline is there for the same reason: it covers
	// routes added later, and every handler passes r.Context() straight into
	// pgx, so this is what stops a blocked query holding a pool connection until
	// the client gives up. It sits below the server's
	// 15-second WriteTimeout on purpose, so a handler that runs long loses to
	// its own deadline and gets to write an error, rather than to the socket
	// deadline, which just closes the connection.
	public := httpx.NewRouter(
		httpx.RequestIDMiddleware(),
		httpx.LoggerMiddleware(),
		httpx.RecoveryMiddleware(),
		httpx.CORSMiddleware(cfg.CORSEOrigins),
		httpx.MaxBodyMiddleware(1<<20),
		httpx.TimeoutMiddleware(10*time.Second),
		httpx.ContextMiddleware(),
	)

	// Three limits, because the three groups have genuinely different traffic.
	//
	//   signIn:    a human starting a sign-in, so 10/min with a burst of 5 is
	//              generous. This is the group an attacker wants. Since 000012
	//              dropped password sign-in it is the only throttle on the
	//              path: guessing now happens against Keycloak, which has its
	//              own brute-force protection, and what is left here is the
	//              OIDC handshake and enrollment redemption.
	//   telemetry: devices, which heartbeat on a timer. A whole site can share
	//              one NAT address, so this is sized for a fleet rather than a
	//              machine. It is a ceiling on absurdity, not a per-device
	//              limit; the real fix for unauthenticated telemetry is device
	//              identity (Phase 3.1).
	//   user:      the portal, which fires a handful of calls per page.
	//
	// Health checks are deliberately unlimited: throttling them would take the
	// container out of rotation under exactly the load the limit exists for.
	signInLimit := httpx.RateLimitMiddleware(httpx.NewRateLimiter(10, 5))
	telemetryLimit := httpx.RateLimitMiddleware(httpx.NewRateLimiter(600, 120))
	userLimit := httpx.RateLimitMiddleware(httpx.NewRateLimiter(300, 60))

	signIn := public.Group(signInLimit)
	telemetry := public.Group(telemetryLimit)
	protected := public.Group(userLimit, httpx.JWTMiddleware(jwtValidator, authService))

	// Sign-in: a token cannot be required to obtain a token.
	signIn.HandleFunc("/api/login-options", rustdeskHandlers.HandleLoginOptions)
	signIn.HandleFunc("/api/login", oidcBroker.HandleLogin)
	signIn.HandleFunc("/api/logout", oidcBroker.HandleLogout)
	signIn.HandleFunc("/api/oidc/auth", oidcLogin.HandleAuth)
	signIn.HandleFunc("/api/oidc/auth-query", oidcLogin.HandleAuthQuery)
	// The browser lands here, not the client, so it is on the sign-in limiter
	// with the rest of the flow rather than the telemetry one. A human opening
	// a browser is the same shape of traffic as a human typing a password.
	signIn.HandleFunc("/api/oidc/callback", oidcLogin.HandleCallback)
	signIn.HandleFunc("/api/oidc/jwks", oidcBroker.HandleJWKS)

	// Device telemetry: managed devices post these with their enrollment
	// secret rather than a user token. An id with no valid credential is
	// recorded as an observation and refused, not registered.
	telemetry.HandleFunc("/api/heartbeat", rustdeskHandlers.HandleHeartbeat)
	telemetry.HandleFunc("/api/sysinfo", rustdeskHandlers.HandleSysinfo)
	telemetry.HandleFunc("/api/sysinfo_ver", sysinfoVerHandler.HandleSysinfoVer)

	// Enrollment redemption is the one device route that cannot require a
	// credential, because it is where the credential comes from. It is rate
	// limited with the sign-in group rather than the telemetry group: it is a
	// secret being presented, so it is guessing that has to be made expensive,
	// not throughput that has to be allowed.
	signIn.HandleFunc("/api/enroll", enrollmentHandler.HandleEnroll)

	// Health: the compose healthcheck gates every other service on these.
	// /healthz also publishes the count of dropped audit events, which is the
	// only way that number leaves the process.
	public.HandleFunc("/healthz", httpx.HealthzHandler(auditService))
	public.HandleFunc("/readyz", httpx.ReadyzHandler(pgPool))

	protected.HandleFunc("/api/currentUser", rustdeskHandlers.HandleCurrentUser)

	// Address book: the twelve paths flutter/lib/models/ab_model.dart calls.
	// Answering /api/ab/settings and /api/ab/personal is what takes the client
	// out of legacy mode, so those two are what make the rest reachable.
	protected.HandleFunc("/api/ab/settings", abHandler.HandleSettings)
	protected.HandleFunc("/api/ab/personal", abHandler.HandlePersonal)
	protected.HandleFunc("/api/ab/shared/profiles", abHandler.HandleSharedProfiles)
	protected.HandleFunc("/api/ab/peers", abHandler.HandlePeers)
	protected.HandleFunc("/api/ab/tags/{guid}", abHandler.HandleTags)
	protected.HandleFunc("/api/ab/peer/add/{guid}", abHandler.HandlePeerAdd)
	protected.HandleFunc("/api/ab/peer/update/{guid}", abHandler.HandlePeerUpdate)
	protected.HandleFunc("/api/ab/peer/{guid}", abHandler.HandlePeerDelete)
	protected.HandleFunc("/api/ab/tag/add/{guid}", abHandler.HandleTagAdd)
	protected.HandleFunc("/api/ab/tag/rename/{guid}", abHandler.HandleTagRename)
	protected.HandleFunc("/api/ab/tag/update/{guid}", abHandler.HandleTagUpdate)
	protected.HandleFunc("/api/ab/tag/{guid}", abHandler.HandleTagDelete)

	// Peers endpoints
	protected.HandleFunc("/api/peers", peerHandler.HandlePeers)
	protected.HandleFunc("/api/device-group/accessible", peerHandler.HandleDeviceGroupAccessible)

	// Users endpoint
	protected.HandleFunc("/api/users", usersHandler.HandleUsers)

	// Audit endpoints
	protected.HandleFunc("/api/audit/conn", auditHandler.HandleAuditConn)
	protected.HandleFunc("/api/audit/file", auditHandler.HandleAuditFile)
	protected.HandleFunc("/api/audit", auditHandler.HandleAuditNote)

	// apiv1 owns its own route table so it can be asserted on in a test. Every
	// pattern there names its method, because /api/v1/devices/{id} carries
	// three verbs and registering a bare pattern twice panics ServeMux at
	// startup rather than failing a single request.
	apiv1Handler.Register(protected)

	// Device deployment endpoints
	protected.HandleFunc("/api/devices/deploy", auditHandler.HandleDevicesDeploy)
	protected.HandleFunc("/api/devices/cli", auditHandler.HandleDevicesCLI)

	// Recording and plugin endpoints
	protected.HandleFunc("/api/record", auditHandler.HandleRecord)
	protected.HandleFunc("/api/switch-grant", auditHandler.HandleSwitchGrant)
	protected.HandleFunc("/lic/web/api/plugin-sign", auditHandler.HandlePluginSign)

	// Enrollment token endpoints. The portal reaches them under /api/v1 and the
	// deployment scripts under /api; both are the same handler, so there is one
	// definition of who may issue a token.
	protected.HandleFunc("/api/enrollment-tokens", enrollmentHandler.HandleEnrollmentTokens)
	protected.HandleFunc("/api/enrollment-tokens/{id}", enrollmentHandler.HandleEnrollmentToken)
	protected.HandleFunc("/api/v1/enrollment-tokens", enrollmentHandler.HandleEnrollmentTokens)
	protected.HandleFunc("/api/v1/enrollment-tokens/{id}", enrollmentHandler.HandleEnrollmentToken)

	addr := fmt.Sprintf("%s:%d", cfg.APIHost, cfg.APIPort)
	server := &http.Server{
		Addr:         addr,
		Handler:      public,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	log.Info().Str("address", addr).Msg("Starting HTTP server")

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("Server failed to start")
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)
	<-sigChan

	log.Info().Msg("Shutting down gracefully...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	server.Shutdown(shutdownCtx)
	log.Info().Msg("Server stopped")
}
