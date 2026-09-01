package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/modelprovider"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/textutil"
)

const (
	defaultOpenRouterSiteURL       = "https://omnara.com"
	defaultOpenRouterAppTitle      = "Omnara"
	defaultOpenRouterAppCategories = "cloud-agent"
	defaultDevDatabaseURL          = "postgres://omnara:omnara@127.0.0.1:55432/omnara?sslmode=disable"
)

type WebServingMode string

const (
	WebServingEmbedded WebServingMode = "embedded"
	WebServingDisabled WebServingMode = "disabled"
)

const defaultDaemonSocketFallbackDrainInterval = 30 * time.Second
const defaultDaemonSocketFallbackDrainJitter = 10 * time.Second
const defaultMigrationTimeout = 30 * time.Minute

const (
	DaemonReleaseURLEnv     = "OMNARA_DAEMON_RELEASE_URL"
	DefaultDaemonReleaseURL = "https://releases.omnara.com/omnarad/latest"
)

const (
	defaultGitHubIssuer           = "https://github.com"
	defaultGitHubAuthorizationURL = "https://github.com/login/oauth/authorize"
	defaultGitHubTokenURL         = "https://github.com/login/oauth/access_token"
	defaultGitHubUserinfoURL      = "https://api.github.com/user"
)

type Config struct {
	APIAddr                           string
	APIMetricsAddr                    string
	WorkerMetricsAddr                 string
	MaintenanceMetricsAddr            string
	LogLevel                          string
	DatabaseURL                       string
	RedisURL                          string
	MigrationsDir                     string
	MigrationTimeout                  time.Duration
	AllowInsecureDev                  bool
	WorkerCapacity                    int
	WorkerAsyncToolCapacity           int
	WorkerBackgroundToolCapacity      int
	DaemonSocketFallbackDrainInterval time.Duration
	DaemonSocketFallbackDrainJitter   time.Duration
	MaintenanceInterval               time.Duration
	ExaAPIKey                         string
	AuthSignupEnabled                 bool
	AuthPasswordResetEnabled          bool
	EmailDriver                       string
	SendGridAPIKey                    string
	EmailFrom                         string
	SMTPAddr                          string
	SMTPUsername                      string
	SMTPPassword                      string
	SMTPRequireTLS                    bool
	AuthConnectors                    []AuthConnectorConfig
	PublicURL                         string
	PublicAPIURL                      string
	BillingURL                        string
	TrustedProxyCIDRs                 []string
	DaemonReleaseURL                  string
	WebServing                        WebServingMode
	SecretEncryptionKeys              string
	SecretEncryptionActiveKeyID       string
	SkillDownloadSigningKey           string
	BlobS3Bucket                      string
	BlobS3Region                      string
	BlobS3Endpoint                    string
	BlobS3AccessKeyID                 string
	BlobS3SecretAccessKey             string
	BlobS3UsePathStyle                bool
	DefaultMachinePools               []executionstore.DefaultMachinePoolTemplate
	DefaultModelProvider              *modelstore.DefaultModelProviderTemplate
	OpenRouterSiteURL                 string
	OpenRouterAppTitle                string
	OpenRouterAppCategories           []string
	HostedAPIURL                      string
	HostedAPIToken                    string
	MCPRegistrySnapshotPath           string
}

type AuthConnectorConfig struct {
	Slug             string   `json:"slug"`
	Kind             string   `json:"kind"`
	DisplayName      string   `json:"display_name"`
	Issuer           string   `json:"issuer"`
	AuthorizationURL string   `json:"authorization_url"`
	TokenURL         string   `json:"token_url"`
	UserinfoURL      string   `json:"userinfo_url"`
	ClientID         string   `json:"client_id"`
	ClientSecret     string   `json:"client_secret"`
	Scopes           []string `json:"scopes"`
	EmailTrustPolicy string   `json:"email_trust_policy"`
	Enabled          *bool    `json:"enabled"`
}

func (c AuthConnectorConfig) EnabledValue() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

func (c AuthConnectorConfig) Normalized() AuthConnectorConfig {
	c = c.normalizedFields()
	if c.Kind == identitystore.AuthConnectorKindGitHub && !c.hasCustomGitHubProvider() {
		c.Issuer = defaultGitHubIssuer
		c.AuthorizationURL = defaultGitHubAuthorizationURL
		c.TokenURL = defaultGitHubTokenURL
		c.UserinfoURL = defaultGitHubUserinfoURL
	}
	return c
}

func (c AuthConnectorConfig) normalizedFields() AuthConnectorConfig {
	c.Slug = strings.TrimSpace(c.Slug)
	c.Kind = strings.TrimSpace(c.Kind)
	c.DisplayName = strings.TrimSpace(c.DisplayName)
	c.Issuer = strings.TrimSpace(c.Issuer)
	c.AuthorizationURL = strings.TrimSpace(c.AuthorizationURL)
	c.TokenURL = strings.TrimSpace(c.TokenURL)
	c.UserinfoURL = strings.TrimSpace(c.UserinfoURL)
	c.ClientID = strings.TrimSpace(c.ClientID)
	c.EmailTrustPolicy = strings.TrimSpace(c.EmailTrustPolicy)
	if c.EmailTrustPolicy == "" {
		switch c.Kind {
		case identitystore.AuthConnectorKindGitHub:
			c.EmailTrustPolicy = identitystore.AuthConnectorEmailTrustPolicyVerifiedEmail
		default:
			c.EmailTrustPolicy = identitystore.AuthConnectorEmailTrustPolicyNone
		}
	}
	return c
}

func (c AuthConnectorConfig) hasCustomGitHubProvider() bool {
	return (c.Issuer != "" && c.Issuer != defaultGitHubIssuer) ||
		(c.AuthorizationURL != "" && c.AuthorizationURL != defaultGitHubAuthorizationURL) ||
		(c.TokenURL != "" && c.TokenURL != defaultGitHubTokenURL) ||
		(c.UserinfoURL != "" && c.UserinfoURL != defaultGitHubUserinfoURL)
}

func (c AuthConnectorConfig) hasCompleteGitHubProvider() bool {
	return c.Issuer != "" && c.AuthorizationURL != "" && c.TokenURL != "" && c.UserinfoURL != ""
}

func validateGitHubProviderCompleteness(slug string, connector AuthConnectorConfig) error {
	if !connector.hasCustomGitHubProvider() {
		return nil
	}
	if !connector.hasCompleteGitHubProvider() {
		return fmt.Errorf(
			"OMNARA_AUTH_CONNECTORS_JSON[%s] custom github connectors require issuer, authorization_url, token_url, and userinfo_url",
			slug,
		)
	}
	return nil
}

func Load() (Config, error) {
	authSignupEnabled, err := getenvBool("OMNARA_AUTH_SIGNUP_ENABLED", true)
	if err != nil {
		return Config{}, err
	}
	authPasswordResetEnabled, err := getenvBool("OMNARA_AUTH_PASSWORD_RESET_ENABLED", true)
	if err != nil {
		return Config{}, err
	}
	smtpRequireTLS, err := getenvBool("OMNARA_SMTP_REQUIRE_TLS", true)
	if err != nil {
		return Config{}, err
	}
	authConnectors, err := getenvAuthConnectors("OMNARA_AUTH_CONNECTORS_JSON")
	if err != nil {
		return Config{}, err
	}
	workerCapacity, err := getenvInt("OMNARA_WORKER_CAPACITY", 4)
	if err != nil {
		return Config{}, err
	}
	workerAsyncToolCapacity, err := getenvInt("OMNARA_WORKER_ASYNC_TOOL_CAPACITY", 32)
	if err != nil {
		return Config{}, err
	}
	workerBackgroundToolCapacity, err := getenvInt(
		"OMNARA_WORKER_BACKGROUND_TOOL_CAPACITY",
		8,
	)
	if err != nil {
		return Config{}, err
	}
	openRouterSiteURL := normalizePublicURL(getenv("OMNARA_OPENROUTER_SITE_URL", defaultOpenRouterSiteURL))
	openRouterAppTitle := strings.TrimSpace(getenv("OMNARA_OPENROUTER_APP_TITLE", defaultOpenRouterAppTitle))
	openRouterAppCategories := getenvCSV("OMNARA_OPENROUTER_APP_CATEGORIES")
	if len(openRouterAppCategories) == 0 {
		openRouterAppCategories = getenvCSVValue(defaultOpenRouterAppCategories)
	}
	if err := validateOpenRouterAttributionConfig(
		openRouterSiteURL,
		openRouterAppTitle,
		openRouterAppCategories,
	); err != nil {
		return Config{}, err
	}
	publicURL := normalizePublicURL(getenv("OMNARA_PUBLIC_URL", ""))
	publicAPIURL := normalizePublicURL(getenv("OMNARA_PUBLIC_API_URL", ""))
	if publicAPIURL == "" && publicURL != "" {
		publicAPIURL = publicURL + "/api/v1"
	}
	cfg := Config{
		APIAddr:                           getenv("OMNARA_API_ADDR", ":8080"),
		APIMetricsAddr:                    getenv("OMNARA_API_METRICS_ADDR", ":8081"),
		WorkerMetricsAddr:                 getenv("OMNARA_WORKER_METRICS_ADDR", ":8082"),
		MaintenanceMetricsAddr:            getenv("OMNARA_MAINTENANCE_METRICS_ADDR", ":8083"),
		LogLevel:                          getenv("OMNARA_LOG_LEVEL", "info"),
		DatabaseURL:                       getenv("OMNARA_DATABASE_URL", ""),
		RedisURL:                          getenv("OMNARA_REDIS_URL", ""),
		AllowInsecureDev:                  os.Getenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS") == "1",
		WorkerCapacity:                    workerCapacity,
		WorkerAsyncToolCapacity:           workerAsyncToolCapacity,
		WorkerBackgroundToolCapacity:      workerBackgroundToolCapacity,
		DaemonSocketFallbackDrainInterval: defaultDaemonSocketFallbackDrainInterval,
		DaemonSocketFallbackDrainJitter:   defaultDaemonSocketFallbackDrainJitter,
		MaintenanceInterval:               time.Second,
		ExaAPIKey:                         getenv("EXA_API_KEY", ""),
		AuthSignupEnabled:                 authSignupEnabled,
		AuthPasswordResetEnabled:          authPasswordResetEnabled,
		EmailDriver:                       getenv("OMNARA_EMAIL_DRIVER", "none"),
		SendGridAPIKey:                    getenv("SENDGRID_API_KEY", ""),
		EmailFrom:                         getenv("OMNARA_EMAIL_FROM", ""),
		SMTPAddr:                          getenv("OMNARA_SMTP_ADDR", ""),
		SMTPUsername:                      getenv("OMNARA_SMTP_USERNAME", ""),
		SMTPPassword:                      getenv("OMNARA_SMTP_PASSWORD", ""),
		SMTPRequireTLS:                    smtpRequireTLS,
		AuthConnectors:                    authConnectors,
		PublicURL:                         publicURL,
		PublicAPIURL:                      publicAPIURL,
		BillingURL:                        normalizePublicURL(getenv("OMNARA_BILLING_URL", "")),
		TrustedProxyCIDRs:                 getenvCSV("OMNARA_TRUSTED_PROXY_CIDRS"),
		DaemonReleaseURL:                  getenv(DaemonReleaseURLEnv, DefaultDaemonReleaseURL),
		WebServing:                        WebServingMode(getenv("OMNARA_WEB_SERVING", string(WebServingDisabled))),
		SecretEncryptionKeys:              getenv("OMNARA_SECRET_ENCRYPTION_KEYS", ""),
		SecretEncryptionActiveKeyID:       getenv("OMNARA_SECRET_ENCRYPTION_ACTIVE_KEY_ID", ""),
		SkillDownloadSigningKey:           getenv("OMNARA_SKILL_DOWNLOAD_SIGNING_KEY", ""),
		BlobS3Bucket:                      getenv("OMNARA_BLOB_S3_BUCKET", ""),
		BlobS3Region:                      getenv("OMNARA_BLOB_S3_REGION", ""),
		BlobS3Endpoint:                    getenv("OMNARA_BLOB_S3_ENDPOINT", ""),
		BlobS3AccessKeyID:                 getenv("OMNARA_BLOB_S3_ACCESS_KEY_ID", ""),
		BlobS3SecretAccessKey:             getenv("OMNARA_BLOB_S3_SECRET_ACCESS_KEY", ""),
		BlobS3UsePathStyle:                os.Getenv("OMNARA_BLOB_S3_USE_PATH_STYLE") == "1",
		OpenRouterSiteURL:                 openRouterSiteURL,
		OpenRouterAppTitle:                openRouterAppTitle,
		OpenRouterAppCategories:           openRouterAppCategories,
		HostedAPIURL:                      getenv("OMNARA_HOSTED_API_URL", ""),
		HostedAPIToken:                    getenv("OMNARA_HOSTED_API_TOKEN", ""),
		MCPRegistrySnapshotPath:           getenv("OMNARA_MCP_REGISTRY_SNAPSHOT_PATH", "/app/mcp-registry/mcp-registry.json"),
	}
	if defaultMachinePoolTemplatesPath := getenv(
		"OMNARA_DEFAULT_MACHINE_POOL_TEMPLATES",
		"",
	); defaultMachinePoolTemplatesPath != "" {
		defaultPools, err := loadDefaultMachinePoolTemplates(defaultMachinePoolTemplatesPath)
		if err != nil {
			return Config{}, err
		}
		cfg.DefaultMachinePools = defaultPools
	}
	if defaultModelProviderTemplatePath := getenv(
		"OMNARA_DEFAULT_MODEL_PROVIDER_TEMPLATE",
		"",
	); defaultModelProviderTemplatePath != "" {
		defaultProvider, err := loadDefaultModelProviderTemplate(defaultModelProviderTemplatePath)
		if err != nil {
			return Config{}, err
		}
		cfg.DefaultModelProvider = &defaultProvider
	}
	if cfg.AllowInsecureDev && cfg.DatabaseURL == "" {
		cfg.DatabaseURL = defaultDevDatabaseURL
	}
	if cfg.AllowInsecureDev && cfg.RedisURL == "" {
		cfg.RedisURL = "redis://127.0.0.1:6379/0"
	}
	if cfg.AllowInsecureDev && cfg.EmailDriver == "none" {
		cfg.EmailDriver = "console"
	}
	if cfg.AllowInsecureDev && cfg.BlobS3Bucket == "" {
		cfg.BlobS3Bucket = "omnara"
		cfg.BlobS3Region = "us-east-1"
		cfg.BlobS3Endpoint = "http://127.0.0.1:59000"
		cfg.BlobS3AccessKeyID = "omnara"
		cfg.BlobS3SecretAccessKey = "omnara-blobs"
		cfg.BlobS3UsePathStyle = true
	}
	if cfg.AllowInsecureDev && cfg.SecretEncryptionKeys == "" && cfg.SecretEncryptionActiveKeyID == "" {
		devKey := base64.StdEncoding.EncodeToString([]byte("omnara-insecure-dev-secret-key!!"))
		cfg.SecretEncryptionKeys = fmt.Sprintf(`{"dev-local":"%s"}`, devKey)
		cfg.SecretEncryptionActiveKeyID = "dev-local"
	}
	if cfg.AllowInsecureDev && cfg.SkillDownloadSigningKey == "" {
		cfg.SkillDownloadSigningKey = base64.StdEncoding.EncodeToString(
			[]byte("omnara-insecure-dev-skill-key!!!"),
		)
	}
	if raw := os.Getenv("OMNARA_DAEMON_SOCKET_FALLBACK_DRAIN_INTERVAL"); raw != "" {
		duration, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse OMNARA_DAEMON_SOCKET_FALLBACK_DRAIN_INTERVAL: %w", err)
		}
		if duration <= 0 {
			return Config{}, fmt.Errorf("OMNARA_DAEMON_SOCKET_FALLBACK_DRAIN_INTERVAL must be positive")
		}
		cfg.DaemonSocketFallbackDrainInterval = duration
	}
	if raw := os.Getenv("OMNARA_DAEMON_SOCKET_FALLBACK_DRAIN_JITTER"); raw != "" {
		duration, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse OMNARA_DAEMON_SOCKET_FALLBACK_DRAIN_JITTER: %w", err)
		}
		if duration < 0 {
			return Config{}, fmt.Errorf("OMNARA_DAEMON_SOCKET_FALLBACK_DRAIN_JITTER must be non-negative")
		}
		cfg.DaemonSocketFallbackDrainJitter = duration
	}
	if cfg.DaemonSocketFallbackDrainJitter > cfg.DaemonSocketFallbackDrainInterval {
		return Config{}, fmt.Errorf(
			"OMNARA_DAEMON_SOCKET_FALLBACK_DRAIN_JITTER must be less than or equal to " +
				"OMNARA_DAEMON_SOCKET_FALLBACK_DRAIN_INTERVAL",
		)
	}
	if raw := os.Getenv("OMNARA_MAINTENANCE_INTERVAL"); raw != "" {
		duration, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse OMNARA_MAINTENANCE_INTERVAL: %w", err)
		}
		if duration <= 0 {
			return Config{}, fmt.Errorf("OMNARA_MAINTENANCE_INTERVAL must be positive")
		}
		cfg.MaintenanceInterval = duration
	}
	return cfg, nil
}

func LoadMigrate() (Config, error) {
	cfg := Config{
		DatabaseURL:      getenv("OMNARA_DATABASE_URL", ""),
		MigrationsDir:    getenv("OMNARA_MIGRATIONS_DIR", "migrations"),
		MigrationTimeout: defaultMigrationTimeout,
		AllowInsecureDev: os.Getenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS") == "1",
	}
	if raw := os.Getenv("OMNARA_MIGRATION_TIMEOUT"); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse OMNARA_MIGRATION_TIMEOUT: %w", err)
		}
		if timeout <= 0 {
			return Config{}, fmt.Errorf("OMNARA_MIGRATION_TIMEOUT must be positive")
		}
		cfg.MigrationTimeout = timeout
	}
	if cfg.AllowInsecureDev && cfg.DatabaseURL == "" {
		cfg.DatabaseURL = defaultDevDatabaseURL
	}
	return cfg, nil
}

func normalizePublicURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func (cfg Config) ValidateAPI() error {
	if err := validatePortWithName("OMNARA_API_ADDR", cfg.APIAddr); err != nil {
		return err
	}
	if err := validatePortWithName("OMNARA_API_METRICS_ADDR", cfg.APIMetricsAddr); err != nil {
		return err
	}
	if !filepath.IsAbs(cfg.MCPRegistrySnapshotPath) {
		return errors.New("OMNARA_MCP_REGISTRY_SNAPSHOT_PATH must be an absolute path")
	}
	if !cfg.AllowInsecureDev && cfg.DatabaseURL == "" {
		return fmt.Errorf(
			"OMNARA_DATABASE_URL is required; set OMNARA_ALLOW_INSECURE_DEV_DEFAULTS=1 only for local development",
		)
	}
	if !cfg.AllowInsecureDev && cfg.RedisURL == "" {
		return fmt.Errorf(
			"OMNARA_REDIS_URL is required; set OMNARA_ALLOW_INSECURE_DEV_DEFAULTS=1 only for local development",
		)
	}
	if err := cfg.validatePublicURL(true); err != nil {
		return err
	}
	if err := cfg.validateWebServingConfig(); err != nil {
		return err
	}
	if err := validateSecretEncryptionConfig(cfg); err != nil {
		return err
	}
	if err := validateSkillDownloadSigningKeyConfig(cfg); err != nil {
		return err
	}
	if err := validateBlobStoreConfig(cfg); err != nil {
		return err
	}
	if cfg.EmailDriver != "" && cfg.EmailDriver != "none" && cfg.EmailDriver != "console" && cfg.EmailDriver != "smtp" &&
		cfg.EmailDriver != "sendgrid" {
		return fmt.Errorf("OMNARA_EMAIL_DRIVER must be one of none, console, smtp, or sendgrid")
	}
	if cfg.EmailDriver == "console" && !cfg.AllowInsecureDev {
		return fmt.Errorf("OMNARA_EMAIL_DRIVER=console is only allowed with OMNARA_ALLOW_INSECURE_DEV_DEFAULTS=1")
	}
	if cfg.EmailDriver == "smtp" {
		if cfg.SMTPAddr == "" || cfg.EmailFrom == "" {
			return fmt.Errorf("OMNARA_SMTP_ADDR and OMNARA_EMAIL_FROM are required when OMNARA_EMAIL_DRIVER=smtp")
		}
		if (cfg.SMTPUsername == "") != (cfg.SMTPPassword == "") {
			return fmt.Errorf("OMNARA_SMTP_USERNAME and OMNARA_SMTP_PASSWORD must be configured together")
		}
		if !cfg.AllowInsecureDev && !cfg.SMTPRequireTLS {
			return fmt.Errorf("OMNARA_SMTP_REQUIRE_TLS=false is only allowed with OMNARA_ALLOW_INSECURE_DEV_DEFAULTS=1")
		}
	}
	if cfg.EmailDriver == "sendgrid" && (cfg.SendGridAPIKey == "" || cfg.EmailFrom == "") {
		return fmt.Errorf("SENDGRID_API_KEY and OMNARA_EMAIL_FROM are required when OMNARA_EMAIL_DRIVER=sendgrid")
	}
	if !cfg.AllowInsecureDev && (cfg.AuthSignupEnabled || cfg.AuthPasswordResetEnabled) && cfg.EmailDriver != "smtp" &&
		cfg.EmailDriver != "sendgrid" {
		return fmt.Errorf(
			"OMNARA_EMAIL_DRIVER must be smtp or sendgrid when signup or password reset is enabled outside local development",
		)
	}
	if err := validateAuthConnectors(cfg.AuthConnectors, cfg.AllowInsecureDev); err != nil {
		return err
	}
	for _, cidr := range cfg.TrustedProxyCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("parse OMNARA_TRUSTED_PROXY_CIDRS: %w", err)
		}
	}
	if err := validateExternalHTTPSURL(DaemonReleaseURLEnv, cfg.DaemonReleaseURL); err != nil {
		return err
	}
	if cfg.BillingURL != "" {
		if err := validateExternalHTTPSURL("OMNARA_BILLING_URL", cfg.BillingURL); err != nil {
			return err
		}
	}
	if err := cfg.validateDefaultModelProviderTemplateWireSize(); err != nil {
		return err
	}
	if err := cfg.validateHostedAPIConfig(); err != nil {
		return err
	}
	return nil
}

func validateSecretEncryptionConfig(cfg Config) error {
	if cfg.SecretEncryptionKeys == "" && cfg.SecretEncryptionActiveKeyID == "" {
		return fmt.Errorf("OMNARA_SECRET_ENCRYPTION_KEYS and OMNARA_SECRET_ENCRYPTION_ACTIVE_KEY_ID are required")
	}
	if cfg.SecretEncryptionKeys == "" {
		return fmt.Errorf(
			"OMNARA_SECRET_ENCRYPTION_KEYS is required when OMNARA_SECRET_ENCRYPTION_ACTIVE_KEY_ID is set",
		)
	}
	if cfg.SecretEncryptionActiveKeyID == "" {
		return fmt.Errorf(
			"OMNARA_SECRET_ENCRYPTION_ACTIVE_KEY_ID is required when OMNARA_SECRET_ENCRYPTION_KEYS is set",
		)
	}
	keys, err := cfg.SecretEncryptionKeyMap()
	if err != nil {
		return err
	}
	if _, ok := keys[cfg.SecretEncryptionActiveKeyID]; !ok {
		return fmt.Errorf(
			"OMNARA_SECRET_ENCRYPTION_ACTIVE_KEY_ID %q is not present in OMNARA_SECRET_ENCRYPTION_KEYS",
			cfg.SecretEncryptionActiveKeyID,
		)
	}
	return nil
}

func validateSkillDownloadSigningKeyConfig(cfg Config) error {
	if cfg.SkillDownloadSigningKey == "" {
		return fmt.Errorf("OMNARA_SKILL_DOWNLOAD_SIGNING_KEY is required")
	}
	_, err := cfg.SkillDownloadSigningKeyBytes()
	return err
}

func (cfg Config) SkillDownloadSigningKeyBytes() ([]byte, error) {
	if cfg.SkillDownloadSigningKey == "" {
		return nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(cfg.SkillDownloadSigningKey)
	if err != nil {
		return nil, fmt.Errorf("OMNARA_SKILL_DOWNLOAD_SIGNING_KEY must be base64-encoded: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("OMNARA_SKILL_DOWNLOAD_SIGNING_KEY must decode to 32 bytes")
	}
	return raw, nil
}

func (cfg Config) SecretEncryptionKeyMap() (map[string][]byte, error) {
	var encoded map[string]string
	if err := json.Unmarshal([]byte(cfg.SecretEncryptionKeys), &encoded); err != nil {
		return nil, fmt.Errorf("OMNARA_SECRET_ENCRYPTION_KEYS must be a JSON object of key id to base64 key: %w", err)
	}
	if len(encoded) == 0 {
		return nil, fmt.Errorf("OMNARA_SECRET_ENCRYPTION_KEYS must include at least one key")
	}
	keys := make(map[string][]byte, len(encoded))
	seenKeys := make(map[string]string, len(encoded))
	for keyID, value := range encoded {
		if keyID == "" {
			return nil, fmt.Errorf("OMNARA_SECRET_ENCRYPTION_KEYS cannot include an empty key id")
		}
		raw, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return nil, fmt.Errorf("OMNARA_SECRET_ENCRYPTION_KEYS[%q] must be base64-encoded: %w", keyID, err)
		}
		if len(raw) != 32 {
			return nil, fmt.Errorf("OMNARA_SECRET_ENCRYPTION_KEYS[%q] must decode to 32 bytes", keyID)
		}
		canonical := base64.StdEncoding.EncodeToString(raw)
		if existingKeyID, ok := seenKeys[canonical]; ok {
			return nil, fmt.Errorf(
				"OMNARA_SECRET_ENCRYPTION_KEYS[%q] duplicates key material from %q",
				keyID,
				existingKeyID,
			)
		}
		seenKeys[canonical] = keyID
		keys[keyID] = raw
	}
	return keys, nil
}

func (cfg Config) SecretKeyWrapper() (secrets.KeyWrapper, error) {
	keys, err := cfg.SecretEncryptionKeyMap()
	if err != nil {
		return nil, err
	}
	return secrets.NewLocalKeyWrapper(cfg.SecretEncryptionActiveKeyID, keys)
}

func (cfg Config) ValidateWorker() error {
	if err := validatePortWithName("OMNARA_WORKER_METRICS_ADDR", cfg.WorkerMetricsAddr); err != nil {
		return err
	}
	if cfg.WorkerCapacity <= 0 {
		return fmt.Errorf("OMNARA_WORKER_CAPACITY must be positive")
	}
	if cfg.WorkerAsyncToolCapacity <= 0 {
		return fmt.Errorf("OMNARA_WORKER_ASYNC_TOOL_CAPACITY must be positive")
	}
	if cfg.WorkerBackgroundToolCapacity <= 0 {
		return fmt.Errorf("OMNARA_WORKER_BACKGROUND_TOOL_CAPACITY must be positive")
	}
	if !cfg.AllowInsecureDev && cfg.DatabaseURL == "" {
		return fmt.Errorf(
			"OMNARA_DATABASE_URL is required; set OMNARA_ALLOW_INSECURE_DEV_DEFAULTS=1 only for local development",
		)
	}
	if !cfg.AllowInsecureDev && cfg.RedisURL == "" {
		return fmt.Errorf(
			"OMNARA_REDIS_URL is required; set OMNARA_ALLOW_INSECURE_DEV_DEFAULTS=1 only for local development",
		)
	}
	if err := cfg.validatePublicURL(true); err != nil {
		return err
	}
	if err := validateBlobStoreConfig(cfg); err != nil {
		return err
	}
	if err := validateSkillDownloadSigningKeyConfig(cfg); err != nil {
		return err
	}
	return nil
}

func (cfg Config) ValidateMigrate() error {
	if !cfg.AllowInsecureDev && cfg.DatabaseURL == "" {
		return fmt.Errorf(
			"OMNARA_DATABASE_URL is required; set OMNARA_ALLOW_INSECURE_DEV_DEFAULTS=1 only for local development",
		)
	}
	if strings.TrimSpace(cfg.MigrationsDir) == "" {
		return fmt.Errorf("OMNARA_MIGRATIONS_DIR is required")
	}
	if cfg.MigrationTimeout <= 0 {
		return fmt.Errorf("OMNARA_MIGRATION_TIMEOUT must be positive")
	}
	return nil
}

func validateBlobStoreConfig(cfg Config) error {
	if cfg.BlobS3Bucket == "" {
		return fmt.Errorf(
			"OMNARA_BLOB_S3_BUCKET is required; set OMNARA_ALLOW_INSECURE_DEV_DEFAULTS=1 only for local development",
		)
	}
	if cfg.BlobS3Endpoint != "" {
		if err := validateHTTPURL("OMNARA_BLOB_S3_ENDPOINT", cfg.BlobS3Endpoint); err != nil {
			return err
		}
		if !cfg.AllowInsecureDev {
			if err := requireHTTPSOrLocalhost("OMNARA_BLOB_S3_ENDPOINT", cfg.BlobS3Endpoint); err != nil {
				return err
			}
		}
	}
	if (cfg.BlobS3AccessKeyID == "") != (cfg.BlobS3SecretAccessKey == "") {
		return fmt.Errorf(
			"OMNARA_BLOB_S3_ACCESS_KEY_ID and OMNARA_BLOB_S3_SECRET_ACCESS_KEY must be configured together",
		)
	}
	return nil
}

func (cfg Config) BlobStoreS3Config() blobstore.S3Config {
	return blobstore.S3Config{
		Bucket:          cfg.BlobS3Bucket,
		Region:          cfg.BlobS3Region,
		Endpoint:        cfg.BlobS3Endpoint,
		AccessKeyID:     cfg.BlobS3AccessKeyID,
		SecretAccessKey: cfg.BlobS3SecretAccessKey,
		UsePathStyle:    cfg.BlobS3UsePathStyle,
	}
}

func (cfg Config) EffectiveWebServing() WebServingMode {
	if cfg.WebServing == "" {
		return WebServingDisabled
	}
	return cfg.WebServing
}

func (cfg Config) validatePublicURL(required bool) error {
	if required && !cfg.AllowInsecureDev && cfg.PublicURL == "" {
		return fmt.Errorf("OMNARA_PUBLIC_URL is required outside local development")
	}
	if cfg.PublicAPIURL != "" && cfg.PublicURL == "" {
		return errors.New("OMNARA_PUBLIC_URL is required when OMNARA_PUBLIC_API_URL is set")
	}
	if cfg.PublicAPIURL != "" {
		if err := validateHTTPBaseURL("OMNARA_PUBLIC_API_URL", cfg.PublicAPIURL); err != nil {
			return err
		}
		if !cfg.AllowInsecureDev {
			if err := requireHTTPSOrLocalhost("OMNARA_PUBLIC_API_URL", cfg.PublicAPIURL); err != nil {
				return err
			}
		}
	}
	if cfg.PublicURL != "" {
		if err := validateHTTPOrigin("OMNARA_PUBLIC_URL", cfg.PublicURL); err != nil {
			return err
		}
		if !cfg.AllowInsecureDev {
			if err := requireHTTPSOrLocalhost("OMNARA_PUBLIC_URL", cfg.PublicURL); err != nil {
				return err
			}
		}
	}
	return nil
}

func (cfg Config) validateWebServingConfig() error {
	switch cfg.EffectiveWebServing() {
	case WebServingEmbedded, WebServingDisabled:
	default:
		return fmt.Errorf("OMNARA_WEB_SERVING must be embedded or disabled")
	}
	return nil
}

func (cfg Config) ValidateMaintenance() error {
	if err := validatePortWithName("OMNARA_MAINTENANCE_METRICS_ADDR", cfg.MaintenanceMetricsAddr); err != nil {
		return err
	}
	if !cfg.AllowInsecureDev && cfg.DatabaseURL == "" {
		return fmt.Errorf(
			"OMNARA_DATABASE_URL is required; set OMNARA_ALLOW_INSECURE_DEV_DEFAULTS=1 only for local development",
		)
	}
	if !cfg.AllowInsecureDev && cfg.RedisURL == "" {
		return fmt.Errorf(
			"OMNARA_REDIS_URL is required; set OMNARA_ALLOW_INSECURE_DEV_DEFAULTS=1 only for local development",
		)
	}
	if err := cfg.validatePublicURL(true); err != nil {
		return err
	}
	if cfg.MaintenanceInterval <= 0 {
		return fmt.Errorf("OMNARA_MAINTENANCE_INTERVAL must be positive")
	}
	if err := cfg.validateDefaultModelProviderTemplateWireSize(); err != nil {
		return err
	}
	if err := cfg.validateHostedAPIConfig(); err != nil {
		return err
	}
	return nil
}

func (cfg Config) validateDefaultModelProviderTemplateWireSize() error {
	if cfg.DefaultModelProvider == nil {
		return nil
	}
	if err := modelprovider.ValidateHostedCredentialTemplateWireSize(*cfg.DefaultModelProvider); err != nil {
		return fmt.Errorf("default model provider %q: %w", cfg.DefaultModelProvider.Name, err)
	}
	return nil
}

func (cfg Config) validateHostedAPIConfig() error {
	required := cfg.DefaultModelProvider != nil
	configured := cfg.HostedAPIURL != "" || cfg.HostedAPIToken != ""
	if !required && !configured {
		return nil
	}
	if cfg.DefaultModelProvider == nil {
		return errors.New(
			"OMNARA_DEFAULT_MODEL_PROVIDER_TEMPLATE is required when hosted API access is configured",
		)
	}
	if cfg.HostedAPIURL == "" {
		return errors.New("OMNARA_HOSTED_API_URL is required when hosted API access is configured")
	}
	if cfg.HostedAPIToken == "" {
		return errors.New("OMNARA_HOSTED_API_TOKEN is required when hosted API access is configured")
	}
	if err := modelprovider.ValidateHostedAPIBaseURL(cfg.HostedAPIURL); err != nil {
		return err
	}
	return modelprovider.ValidateHostedAPIToken(cfg.HostedAPIToken)
}

func validateExternalHTTPSURL(key, raw string) error {
	if err := validateHTTPURL(key, raw); err != nil {
		return err
	}
	parsed, _ := url.Parse(raw)
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("%s must include a host", key)
	}
	if parsed.User != nil || strings.Contains(raw, "#") {
		return fmt.Errorf("%s must not contain credentials or a fragment", key)
	}
	if port := parsed.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("%s contains an invalid port", key)
		}
	} else if strings.HasSuffix(parsed.Host, ":") {
		return fmt.Errorf("%s contains an invalid port", key)
	}
	if strings.ContainsAny(raw, "\"\\") || strings.IndexFunc(raw, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) >= 0 {
		return fmt.Errorf("%s contains unsupported characters", key)
	}
	if parsed.Scheme == "http" && host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return fmt.Errorf("%s must use https outside localhost development", key)
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return fmt.Errorf("%s must not contain query parameters", key)
	}
	return nil
}

func getenvInt(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return value, nil
}

func getenvBool(key string, fallback bool) (bool, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("parse %s: expected boolean", key)
	}
}

func getenvCSV(key string) []string {
	return getenvCSVValue(os.Getenv(key))
}

func getenvCSVValue(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value != "" {
			values = append(values, value)
		}
	}
	return values
}

func validateOpenRouterAttributionConfig(siteURL, appTitle string, categories []string) error {
	if err := validateHTTPOrigin("OMNARA_OPENROUTER_SITE_URL", siteURL); err != nil {
		return err
	}
	if appTitle == "" {
		return fmt.Errorf("OMNARA_OPENROUTER_APP_TITLE is required")
	}
	if len(appTitle) > 1024 {
		return fmt.Errorf("OMNARA_OPENROUTER_APP_TITLE cannot exceed 1024 characters")
	}
	for _, r := range appTitle {
		if r < 0x20 {
			return fmt.Errorf("OMNARA_OPENROUTER_APP_TITLE cannot contain control characters")
		}
	}
	if err := modelstore.ValidateOpenRouterAppCategories("OMNARA_OPENROUTER_APP_CATEGORIES", categories); err != nil {
		return err
	}
	return nil
}

func getenvAuthConnectors(key string) ([]AuthConnectorConfig, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil, nil
	}
	var connectors []AuthConnectorConfig
	if err := json.Unmarshal([]byte(raw), &connectors); err != nil {
		return nil, fmt.Errorf("%s must be a JSON array of auth connectors: %w", key, err)
	}
	for i := range connectors {
		connectors[i] = connectors[i].Normalized()
	}
	return connectors, nil
}

func getenv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func validatePortWithName(name, addr string) error {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			port := addr[i+1:]
			if port == "" {
				return fmt.Errorf("missing port in %s %q", name, addr)
			}
			n, err := strconv.Atoi(port)
			if err != nil || n < 1 || n > 65535 {
				return fmt.Errorf("invalid port in %s %q", name, addr)
			}
			return nil
		}
	}
	return fmt.Errorf("%s must include a port: %q", name, addr)
}

func validateHTTPURL(key, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%s must be an absolute URL", key)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("%s must use http or https", key)
	}
	return nil
}

func validateHTTPBaseURL(key, raw string) error {
	if err := validateHTTPURL(key, raw); err != nil {
		return err
	}
	parsed, _ := url.Parse(raw)
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return fmt.Errorf("%s must not contain credentials, a query, or a fragment", key)
	}
	return nil
}

func validateHTTPOrigin(key, raw string) error {
	if err := validateHTTPURL(key, raw); err != nil {
		return err
	}
	parsed, _ := url.Parse(raw)
	if parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s must be an origin URL without path, query, fragment, or credentials", key)
	}
	return nil
}

func validateAuthConnectors(connectors []AuthConnectorConfig, allowInsecureDev bool) error {
	seenSlugs := map[string]bool{}
	seenIssuers := map[string]string{}
	for _, connector := range connectors {
		rawConnector := connector.normalizedFields()
		connector = rawConnector.Normalized()
		slug := connector.Slug
		if slug == "" {
			return fmt.Errorf("OMNARA_AUTH_CONNECTORS_JSON connector slug is required")
		}
		if !textutil.IsLowerURLSafeLabel(slug) {
			return fmt.Errorf("OMNARA_AUTH_CONNECTORS_JSON[%s] slug must be a lowercase URL-safe label", slug)
		}
		if seenSlugs[slug] {
			return fmt.Errorf("OMNARA_AUTH_CONNECTORS_JSON contains duplicate connector slug %q", slug)
		}
		seenSlugs[slug] = true
		switch connector.Kind {
		case identitystore.AuthConnectorKindOIDC:
			if connector.Issuer == "" {
				return fmt.Errorf("OMNARA_AUTH_CONNECTORS_JSON[%s] issuer is required for oidc", slug)
			}
			if err := validateAuthConnectorURL(
				"OMNARA_AUTH_CONNECTORS_JSON issuer",
				connector.Issuer,
				allowInsecureDev,
			); err != nil {
				return err
			}
			if connector.AuthorizationURL != "" || connector.TokenURL != "" || connector.UserinfoURL != "" {
				return fmt.Errorf(
					"OMNARA_AUTH_CONNECTORS_JSON[%s] oidc connectors use issuer discovery; endpoint overrides are not supported",
					slug,
				)
			}
		case identitystore.AuthConnectorKindGitHub:
			if err := validateGitHubProviderCompleteness(slug, rawConnector); err != nil {
				return err
			}
			if connector.Issuer == "" {
				return fmt.Errorf("OMNARA_AUTH_CONNECTORS_JSON[%s] issuer is required for github", slug)
			}
			if err := validateAuthConnectorURL(
				"OMNARA_AUTH_CONNECTORS_JSON issuer",
				connector.Issuer,
				allowInsecureDev,
			); err != nil {
				return err
			}
			for _, endpoint := range []struct {
				name  string
				value string
			}{
				{name: "authorization_url", value: connector.AuthorizationURL},
				{name: "token_url", value: connector.TokenURL},
				{name: "userinfo_url", value: connector.UserinfoURL},
			} {
				if endpoint.value != "" {
					if err := validateAuthConnectorURL(
						"OMNARA_AUTH_CONNECTORS_JSON "+endpoint.name,
						endpoint.value,
						allowInsecureDev,
					); err != nil {
						return err
					}
				}
			}
		default:
			return fmt.Errorf("OMNARA_AUTH_CONNECTORS_JSON[%s] kind must be oidc or github", slug)
		}
		if strings.TrimSpace(connector.DisplayName) == "" || connector.ClientID == "" || connector.ClientSecret == "" {
			return fmt.Errorf("OMNARA_AUTH_CONNECTORS_JSON[%s] display_name, client_id, and client_secret are required", slug)
		}
		if connector.EmailTrustPolicy != identitystore.AuthConnectorEmailTrustPolicyNone &&
			connector.EmailTrustPolicy != identitystore.AuthConnectorEmailTrustPolicyVerifiedEmail {
			return fmt.Errorf("OMNARA_AUTH_CONNECTORS_JSON[%s] email_trust_policy must be none or verified_email", slug)
		}
		if previousSlug, ok := seenIssuers[connector.Issuer]; ok {
			return fmt.Errorf(
				"OMNARA_AUTH_CONNECTORS_JSON contains duplicate connector issuer %q for %q and %q",
				connector.Issuer,
				previousSlug,
				slug,
			)
		}
		seenIssuers[connector.Issuer] = slug
	}
	return nil
}

func validateAuthConnectorURL(key, raw string, allowInsecureDev bool) error {
	if err := validateHTTPURL(key, raw); err != nil {
		return err
	}
	if allowInsecureDev {
		return nil
	}
	return requireHTTPSOrLocalhost(key, raw)
}

func requireHTTPSOrLocalhost(key, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%s must be an absolute URL", key)
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" && isLocalhost(parsed.Hostname()) {
		return nil
	}
	return fmt.Errorf("%s must use https outside localhost development", key)
}

func isLocalhost(host string) bool {
	return strings.EqualFold(host, "localhost") || host == "127.0.0.1" || host == "::1"
}
