package config

import (
	"fmt"
	"strings"
	"time"

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
	OIDCIssuer       string
	OIDCAuthURL      string
	OIDCTokenURL     string
	OIDCClientID     string
	OIDCClientSecret string
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

	// Security
	CasbinModelPath string

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
		OIDCRedirectURI:  viper.GetString("OIDC_REDIRECT_URI"),

		BootstrapAdminEmail: viper.GetString("API_BOOTSTRAP_ADMIN_EMAIL"),

		DeviceNameTemplate:        viper.GetString("DEVICE_NAME_TEMPLATE"),
		DeviceStaleAfterSeconds:   viper.GetInt("DEVICE_STALE_AFTER_SECONDS"),
		DeviceOfflineAfterSeconds: viper.GetInt("DEVICE_OFFLINE_AFTER_SECONDS"),

		// Zero means no limit, which is what the client assumes when the field
		// is missing.
		AddressBookMaxPeers: viper.GetInt("AB_MAX_PEER_ONE_AB"),

		AuditRetentionDays: viper.GetInt("AUDIT_RETENTION_DAYS"),


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

	return nil
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

	return nil
}
