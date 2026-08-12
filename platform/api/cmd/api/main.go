package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/access"
	"github.com/OpenDeskViewer/platform/api/internal/auth"
	"github.com/OpenDeskViewer/platform/api/internal/audit"
	"github.com/OpenDeskViewer/platform/api/internal/config"
	"github.com/OpenDeskViewer/platform/api/internal/enrollment"
	"github.com/OpenDeskViewer/platform/api/internal/fleet"
	"github.com/OpenDeskViewer/platform/api/internal/httpx"
	"github.com/OpenDeskViewer/platform/api/internal/identity"
	postgres2 "github.com/OpenDeskViewer/platform/api/internal/postgres"
	"github.com/OpenDeskViewer/platform/api/internal/rustdeskapi"
	"github.com/OpenDeskViewer/platform/api/internal/telemetry"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pgPool, err := postgres2.New(ctx,
		cfg.PostgresHost, cfg.PostgresPort,
		cfg.PostgresDB, cfg.PostgresUser, cfg.PostgresPassword,
	)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize PostgreSQL")
	}
	defer pgPool.Close()

	log.Info().Msg("PostgreSQL connection established")

	// Run migrations unless explicitly disabled
	if os.Getenv("ODV_MIGRATE") != "false" {
		log.Info().Msg("Running database migrations...")
		
		// Check if migrate is available
		if _, err := exec.LookPath("migrate"); err != nil {
			log.Warn().Err(err).Msg("migrate CLI not found, skipping automatic migrations (run manually or set ODV_MIGRATE=false)")
		} else {
			// Use the migrate CLI with the migrations directory
			cmd := exec.Command("migrate", "-database", 
				fmt.Sprintf("postgresql://%s:%s@%s:%d/%s?sslmode=disable", 
					cfg.PostgresUser, cfg.PostgresPassword, cfg.PostgresHost, cfg.PostgresPort, cfg.PostgresDB),
				"-path", "./migrations", "up")
			cmd.Dir = "/app"
			output, err := cmd.CombinedOutput()
			if err != nil {
				log.Warn().Err(err).Msgf("Database migration warning: %s", string(output))
			} else {
				log.Info().Msg("Database migrations completed")
			}
		}
	}

	telemetryService := telemetry.NewService(pgPool, cfg)
	authService := identity.NewAuthService(pgPool, cfg.BootstrapAdminEmail)

	// Create access resolver
	accessResolver := access.NewSQLResolver(pgPool, cfg)

	// Token validator. Config validation guarantees a usable issuer, so this is
	// never nil: there is no code path that starts the server unauthenticated.
	realmURL := cfg.KeycloakURL + "/realms/" + cfg.KeycloakRealm
	jwtValidator := auth.NewJWTValidator(
		auth.NewJWKSProvider(realmURL+"/protocol/openid-connect/certs"),
		auth.WithIssuer(realmURL),
		auth.WithAudience(cfg.KeycloakClientID),
		auth.WithAlgorithm("RS256"),
	)

	oidcBroker := rustdeskapi.NewOIDCBroker(jwtValidator, authService)
	// Audit service for recording mutations
	auditService := audit.New(pgPool)
	rustdeskHandlers := rustdeskapi.NewHandlers(authService, telemetryService, auditService)
	abHandler := rustdeskapi.NewAddressBookHandler(pgPool, accessResolver, cfg.AddressBookMaxPeers, auditService)
	peerHandler := rustdeskapi.NewPeerHandler(pgPool, accessResolver, auditService)
	auditHandler := rustdeskapi.NewAuditHandler(pgPool, accessResolver, auditService)
	usersHandler := rustdeskapi.NewUsersHandler(authService, accessResolver)

	// Enrollment service handles token operations
	enrollmentService := enrollment.NewService(pgPool, cfg)
	fleetService := fleet.NewService(pgPool, cfg)

	enrollmentHandler := rustdeskapi.NewEnrollmentHandler(pgPool, accessResolver, enrollmentService, fleetService, auditService)
	sysinfoVerHandler := rustdeskapi.NewSysinfoVerHandler(pgPool)

	// public carries everything that must work without a token. protected adds
	// the JWT middleware on top. Splitting the muxes rather than allowlisting
	// paths inside the middleware means a route left off this list is
	// unreachable, not unauthenticated.
	public := httpx.NewRouter(
		httpx.RequestIDMiddleware(),
		httpx.LoggerMiddleware(),
		httpx.RecoveryMiddleware(),
		httpx.CORSMiddleware(cfg.CORSEOrigins),
		httpx.ContextMiddleware(),
	)
	protected := public.Group(httpx.JWTMiddleware(jwtValidator, authService))

	// Sign-in: a token cannot be required to obtain a token.
	public.HandleFunc("/api/login-options", rustdeskHandlers.HandleLoginOptions)
	public.HandleFunc("/api/login", oidcBroker.HandleLogin)
	public.HandleFunc("/api/logout", oidcBroker.HandleLogout)
	public.HandleFunc("/api/oidc/auth", oidcBroker.HandleAuth)
	public.HandleFunc("/api/oidc/auth-query", oidcBroker.HandleAuthQuery)
	public.HandleFunc("/api/oidc/jwks", oidcBroker.HandleJWKS)

	// Device telemetry: managed devices post these without a user token.
	public.HandleFunc("/api/heartbeat", rustdeskHandlers.HandleHeartbeat)
	public.HandleFunc("/api/sysinfo", rustdeskHandlers.HandleSysinfo)
	public.HandleFunc("/api/sysinfo_ver", sysinfoVerHandler.HandleSysinfoVer)

	// Health: the compose healthcheck gates every other service on these.
	public.HandleFunc("/healthz", httpx.HealthzHandler())
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
	
	// Device deployment endpoints
	protected.HandleFunc("/api/devices/deploy", auditHandler.HandleDevicesDeploy)
	protected.HandleFunc("/api/devices/cli", auditHandler.HandleDevicesCLI)
	
	// Recording and plugin endpoints
	protected.HandleFunc("/api/record", auditHandler.HandleRecord)
	protected.HandleFunc("/api/switch-grant", auditHandler.HandleSwitchGrant)
	protected.HandleFunc("/lic/web/api/plugin-sign", auditHandler.HandlePluginSign)

	// Enrollment token endpoints
	protected.HandleFunc("/api/enrollment-tokens", enrollmentHandler.HandleEnrollmentTokens)
	protected.HandleFunc("/api/enrollment-tokens/{id}", enrollmentHandler.HandleEnrollmentToken)

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
