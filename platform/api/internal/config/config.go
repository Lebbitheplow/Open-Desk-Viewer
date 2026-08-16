package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/OpenDeskViewer/platform/api/internal/devicepw"
	"github.com/spf13/viper"
)

// Config holds all application configuration
type Config struct {
	// PostgreSQL
	PostgresHost     string
	PostgresPort     int
	PostgresDB       string
	PostgresUser     string
	PostgresPassword string
	// PostgresSSLMode is a libpq sslmode. It defaults to "require" rather than
	// "disable" so that a database reached over anything but a private network
	// is encrypted unless the operator says otherwise.
	PostgresSSLMode string

	// PostgresAppPassword is the password of odv_app, the non-owning role the
	// API serves requests as. PostgresUser above stays the owner and is used
	// only to run migrations.
	//
	// Empty means "serve requests as the owner", which is what a deployment
	// predating migration 000008 does and what the test fixtures do. It is a
	// weaker configuration, not a broken one, and the API says so at startup
	// rather than refusing: an operator upgrading in place should not have the
	// service fail to come back because a role does not exist yet.
	PostgresAppPassword string

	// Keycloak
	KeycloakHost     string
	KeycloakPort     int
	KeycloakRealm    string
	KeycloakClient   string
	KeycloakSecret   string
	KeycloakURL      string
	KeycloakClientID string

	// RustDesk
	PublicHost        string
	RustdeskPublicKey string
	RendezvousPort    int
	RelayPort         int

	// API Server
	APIHost          string
	APIPort          int
	JWTSecret        string
	JWTAccessExpiry  time.Duration
	JWTRefreshExpiry time.Duration

	// OIDC
	OIDCIssuer   string
	OIDCAuthURL  string
	OIDCTokenURL string
	OIDCClientID string
	// OIDCClientSecret belongs to odv-api, the confidential client. Nothing
	// exchanges an authorization code with it: the RustDesk client's browser
	// sign-in runs against OIDCClientPortal, which is public and uses PKCE.
	OIDCClientSecret string
	// OIDCClientPortal is the public Keycloak client the browser sign-in runs
	// as, for both the React portal and the RustDesk client's flow.
	OIDCClientPortal string
	OIDCRedirectURI  string

	// Bootstrap
	BootstrapAdminEmail string

	// Fleet
	DeviceNameTemplate        string
	DeviceStaleAfterSeconds   int
	DeviceOfflineAfterSeconds int

	// Address book
	AddressBookMaxPeers int

	// Audit
	AuditRetentionDays int

	// DevicePasswordKey is the base64 AES-256 key that encrypts every managed
	// device's connection password at rest. It is required: without it the
	// platform cannot own device passwords, and a deployment that silently ran
	// without them would present a portal offering rotation that does nothing.
	DevicePasswordKey string

	// Worker
	WorkerIntervalHeartbeatCheckSeconds int
	WorkerIntervalTokenCleanupHours     int
	WorkerIntervalAuditCleanupDays      int

	// CORS
	CORSEOrigins []string

	// Debug
	Debug bool
}

// LoadConfig loads configuration from environment variables and .env file
func LoadConfig(envFile string) (*Config, error) {
	if envFile != "" {
		viper.SetConfigFile(envFile)
		viper.SetConfigType("env")
		if err := viper.ReadInConfig(); err != nil {
			if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
				fmt.Printf("Warning: failed to read config file: %v\n", err)
			}
		}
	}

	viper.SetDefault("POSTGRES_PORT", 5432)
	viper.SetDefault("POSTGRES_SSLMODE", "require")
	viper.SetDefault("KEYCLOAK_PORT", 8080)
	viper.SetDefault("RENDEZVOUS_PORT", 21116)
	viper.SetDefault("RELAY_PORT", 21117)
	viper.SetDefault("API_PORT", 8000)
	viper.SetDefault("JWT_ACCESS_EXPIRY_SECONDS", 3600)
	viper.SetDefault("JWT_REFRESH_EXPIRY_SECONDS", 86400)
	viper.SetDefault("API_BOOTSTRAP_ADMIN_EMAIL", "")
	viper.SetDefault("DEVICE_NAME_TEMPLATE", "{customer}-{location}-{serial}")
	viper.SetDefault("DEVICE_STALE_AFTER_SECONDS", 300)
	viper.SetDefault("DEVICE_OFFLINE_AFTER_SECONDS", 900)
	viper.SetDefault("AB_MAX_PEER_ONE_AB", 0)
	viper.SetDefault("AUDIT_RETENTION_DAYS", 90)
	viper.SetDefault("WORKER_INTERVAL_HEARTBEAT_CHECK_SECONDS", 60)
	viper.SetDefault("WORKER_INTERVAL_TOKEN_CLEANUP_HOURS", 24)
	viper.SetDefault("WORKER_INTERVAL_AUDIT_CLEANUP_DAYS", 90)
	viper.SetDefault("DEBUG", false)

	// No env prefix: docker-compose passes POSTGRES_HOST, KEYCLOAK_REALM and the
	// rest under their bare names, and a prefix here would silently read none of
	// them.
	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	cfg := &Config{
		PostgresHost:     viper.GetString("POSTGRES_HOST"),
		PostgresPort:     viper.GetInt("POSTGRES_PORT"),
		PostgresDB:       viper.GetString("POSTGRES_DB"),
		PostgresUser:     viper.GetString("POSTGRES_USER"),
		PostgresPassword: viper.GetString("POSTGRES_PASSWORD"),
		PostgresSSLMode:  viper.GetString("POSTGRES_SSLMODE"),

		PostgresAppPassword: viper.GetString("POSTGRES_APP_PASSWORD"),

		KeycloakHost:     viper.GetString("KEYCLOAK_HOST"),
		KeycloakPort:     viper.GetInt("KEYCLOAK_PORT"),
		KeycloakRealm:    viper.GetString("KEYCLOAK_REALM"),
		KeycloakClient:   viper.GetString("KEYCLOAK_CLIENT_API"),
		KeycloakSecret:   viper.GetString("KEYCLOAK_CLIENT_SECRET"),
		KeycloakURL:      fmt.Sprintf("http://%s:%d", viper.GetString("KEYCLOAK_HOST"), viper.GetInt("KEYCLOAK_PORT")),
		KeycloakClientID: viper.GetString("KEYCLOAK_CLIENT_API"),

		PublicHost:        viper.GetString("PUBLIC_HOST"),
		RustdeskPublicKey: viper.GetString("RUSTDESK_PUBLIC_KEY"),
		RendezvousPort:    viper.GetInt("RENDEZVOUS_PORT"),
		RelayPort:         viper.GetInt("RELAY_PORT"),

		APIHost:          viper.GetString("API_HOST"),
		APIPort:          viper.GetInt("API_PORT"),
		JWTSecret:        viper.GetString("JWT_SECRET"),
		JWTAccessExpiry:  time.Duration(viper.GetInt("JWT_ACCESS_EXPIRY_SECONDS")) * time.Second,
		JWTRefreshExpiry: time.Duration(viper.GetInt("JWT_REFRESH_EXPIRY_SECONDS")) * time.Second,

		OIDCIssuer:       viper.GetString("OIDC_ISSUER"),
		OIDCAuthURL:      viper.GetString("OIDC_AUTH_URL"),
		OIDCTokenURL:     viper.GetString("OIDC_TOKEN_URL"),
		OIDCClientID:     viper.GetString("OIDC_CLIENT_ID"),
		OIDCClientSecret: viper.GetString("OIDC_CLIENT_SECRET"),
		OIDCClientPortal: viper.GetString("OIDC_CLIENT_PORTAL"),
		OIDCRedirectURI:  viper.GetString("OIDC_REDIRECT_URI"),

		BootstrapAdminEmail: viper.GetString("API_BOOTSTRAP_ADMIN_EMAIL"),

		DeviceNameTemplate:        viper.GetString("DEVICE_NAME_TEMPLATE"),
		DeviceStaleAfterSeconds:   viper.GetInt("DEVICE_STALE_AFTER_SECONDS"),
		DeviceOfflineAfterSeconds: viper.GetInt("DEVICE_OFFLINE_AFTER_SECONDS"),

		// Zero means no limit, which is what the client assumes when the field
		// is missing.
		AddressBookMaxPeers: viper.GetInt("AB_MAX_PEER_ONE_AB"),

		AuditRetentionDays: viper.GetInt("AUDIT_RETENTION_DAYS"),

		DevicePasswordKey: viper.GetString("DEVICE_PASSWORD_KEY"),

		WorkerIntervalHeartbeatCheckSeconds: viper.GetInt("WORKER_INTERVAL_HEARTBEAT_CHECK_SECONDS"),
		WorkerIntervalTokenCleanupHours:     viper.GetInt("WORKER_INTERVAL_TOKEN_CLEANUP_HOURS"),
		WorkerIntervalAuditCleanupDays:      viper.GetInt("WORKER_INTERVAL_AUDIT_CLEANUP_DAYS"),

		CORSEOrigins: viper.GetStringSlice("CORS_ORIGINS"),

		Debug: viper.GetBool("DEBUG"),
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Validate checks the configuration every binary needs. The worker does not
// serve requests and has no issuer, so the authentication settings are checked
// separately by ValidateAPI.
func (c *Config) Validate() error {
	if c.PostgresHost == "" {
		return fmt.Errorf("POSTGRES_HOST is required")
	}
	if c.PostgresDB == "" {
		return fmt.Errorf("POSTGRES_DB is required")
	}
	if c.PostgresUser == "" {
		return fmt.Errorf("POSTGRES_USER is required")
	}
	// A typo here fails at connect time with a libpq error that does not name
	// the variable, and "disabled" instead of "disable" is an easy one to make.
	switch c.PostgresSSLMode {
	case "", "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
	default:
		return fmt.Errorf("POSTGRES_SSLMODE %q is not a libpq sslmode (disable, allow, prefer, require, verify-ca, verify-full)", c.PostgresSSLMode)
	}

	return nil
}

// RuntimeRole is the non-owning database role the API serves requests as.
//
// A constant rather than a setting: migration 000008 grants its privileges in
// plain SQL and has to name it, so a configurable name would produce a role
// that can log in and cannot read anything, which fails at the first query
// instead of at deployment.
const RuntimeRole = "odv_app"

// RuntimeDBUser returns the role the request-serving pool should connect as,
// and its password. It is the owner when no application password is set, which
// is the pre-000008 behaviour.
func (c *Config) RuntimeDBUser() (user, password string) {
	if c.PostgresAppPassword == "" {
		return c.PostgresUser, c.PostgresPassword
	}
	return RuntimeRole, c.PostgresAppPassword
}

// ValidateAPI checks the settings the API server needs on top of Validate.
//
// The issuer settings are mandatory: without them the server cannot verify a
// token, and the only alternatives are refusing to start or serving every route
// unauthenticated. It refuses to start.
func (c *Config) ValidateAPI() error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.KeycloakHost == "" {
		return fmt.Errorf("KEYCLOAK_HOST is required")
	}
	if c.KeycloakRealm == "" {
		return fmt.Errorf("KEYCLOAK_REALM is required")
	}
	if c.KeycloakClientID == "" {
		return fmt.Errorf("KEYCLOAK_CLIENT_API is required")
	}
	if c.PublicHost == "" {
		return fmt.Errorf("PUBLIC_HOST is required")
	}
	if c.JWTSecret == "" {
		return fmt.Errorf("JWT_SECRET is required (must be at least 64 characters)")
	}
	if len(c.JWTSecret) < 64 {
		return fmt.Errorf("JWT_SECRET must be at least 64 characters")
	}

	// Checked here rather than at first use. A key that does not decode would
	// otherwise surface as a failed device enrollment weeks after deployment,
	// which is the worst moment to discover a configuration error: the device is
	// already at a customer site.
	if _, err := devicepw.ParseKey(c.DevicePasswordKey); err != nil {
		return fmt.Errorf("DEVICE_PASSWORD_KEY is required and must be 32 random bytes, base64 encoded (openssl rand -base64 32): %w", err)
	}

	// The issuer is the value Keycloak stamps into every token's iss claim,
	// which is the public realm URL, not the internal service address. Deriving
	// it from KEYCLOAK_HOST would reject every token a browser ever presents,
	// so it is configured explicitly and checked here rather than guessed.
	if c.OIDCIssuer == "" {
		return fmt.Errorf("OIDC_ISSUER is required (the public realm URL, e.g. https://host/realms/%s)", c.KeycloakRealm)
	}
	if !strings.HasSuffix(strings.TrimSuffix(c.OIDCIssuer, "/"), "/realms/"+c.KeycloakRealm) {
		return fmt.Errorf("OIDC_ISSUER %q must end in /realms/%s to match the tokens Keycloak issues", c.OIDCIssuer, c.KeycloakRealm)
	}

	// Empty is valid and is the intended default: the portal shares an origin
	// with the API, so no CORS headers are needed. What is not valid is a
	// wildcard, because the middleware pairs the origin with
	// Access-Control-Allow-Credentials, and "*" with credentials is exactly the
	// combination that lets any site call the API as a signed-in user.
	for _, origin := range c.CORSEOrigins {
		if origin == "*" {
			return fmt.Errorf("CORS_ORIGINS may not contain \"*\": the API sends Access-Control-Allow-Credentials, so every origin would be able to call it with a user's credentials")
		}
		if !strings.HasPrefix(origin, "http://") && !strings.HasPrefix(origin, "https://") {
			return fmt.Errorf("CORS_ORIGINS entry %q must be a scheme-qualified origin such as https://portal.example.com", origin)
		}
		if strings.HasSuffix(origin, "/") {
			return fmt.Errorf("CORS_ORIGINS entry %q must not end in a slash: browsers send the Origin header without a trailing slash, so this would never match", origin)
		}
	}

	return nil
}
