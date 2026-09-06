package config

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func TestLoadUsesDefaults(t *testing.T) {
	t.Setenv("OMNARA_API_ADDR", "")
	t.Setenv("OMNARA_LOG_LEVEL", "")
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.APIAddr != ":8080" {
		t.Fatalf("expected default addr, got %q", cfg.APIAddr)
	}
	if cfg.APIMetricsAddr != ":8081" {
		t.Fatalf("expected default api metrics addr, got %q", cfg.APIMetricsAddr)
	}
	if cfg.WorkerMetricsAddr != ":8082" {
		t.Fatalf("expected default worker health and metrics addr, got %q", cfg.WorkerMetricsAddr)
	}
	if cfg.MaintenanceMetricsAddr != ":8083" {
		t.Fatalf("expected default maintenance health and metrics addr, got %q", cfg.MaintenanceMetricsAddr)
	}
	if cfg.LogLevel != "info" {
		t.Fatalf("expected default log level, got %q", cfg.LogLevel)
	}
	if cfg.DatabaseURL == "" {
		t.Fatal("expected insecure dev default database url")
	}
	if cfg.RedisURL != "redis://127.0.0.1:6379/0" {
		t.Fatalf("expected insecure dev default redis url, got %q", cfg.RedisURL)
	}
	if cfg.WorkerCapacity != 4 {
		t.Fatalf("expected default worker capacity 4, got %d", cfg.WorkerCapacity)
	}
	if cfg.WorkerAsyncToolCapacity != 32 {
		t.Fatalf("expected default worker async tool capacity 32, got %d", cfg.WorkerAsyncToolCapacity)
	}
	if cfg.WorkerBackgroundToolCapacity != 8 {
		t.Fatalf(
			"expected default worker background tool capacity 8, got %d",
			cfg.WorkerBackgroundToolCapacity,
		)
	}
	if cfg.DaemonSocketFallbackDrainInterval != defaultDaemonSocketFallbackDrainInterval {
		t.Fatalf("expected default daemon socket fallback drain interval, got %s", cfg.DaemonSocketFallbackDrainInterval)
	}
	if cfg.DaemonSocketFallbackDrainJitter != defaultDaemonSocketFallbackDrainJitter {
		t.Fatalf("expected default daemon socket fallback drain jitter, got %s", cfg.DaemonSocketFallbackDrainJitter)
	}
	if cfg.DaemonReleaseURL != DefaultDaemonReleaseURL {
		t.Fatalf("expected default daemon release url, got %q", cfg.DaemonReleaseURL)
	}
	if cfg.WebServing != WebServingDisabled {
		t.Fatalf("expected disabled web serving by default, got %q", cfg.WebServing)
	}
	if cfg.OpenRouterSiteURL != "https://omnara.com" ||
		cfg.OpenRouterAppTitle != "Omnara" ||
		len(cfg.OpenRouterAppCategories) != 1 ||
		cfg.OpenRouterAppCategories[0] != "cloud-agent" {
		t.Fatalf("unexpected OpenRouter attribution defaults: %+v", cfg)
	}
}

func TestLoadPublicURL(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_PUBLIC_URL", " http://localhost:5173/ ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.PublicURL != "http://localhost:5173" {
		t.Fatalf("expected public URL, got %q", cfg.PublicURL)
	}
	if cfg.PublicAPIURL != "http://localhost:5173/api/v1" {
		t.Fatalf("expected derived public API URL, got %q", cfg.PublicAPIURL)
	}
}

func TestLoadPublicAPIURL(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_PUBLIC_URL", "https://app.example.com")
	t.Setenv("OMNARA_PUBLIC_API_URL", " https://api.example.com/v1/ ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.PublicAPIURL != "https://api.example.com/v1" {
		t.Fatalf("expected configured public API URL, got %q", cfg.PublicAPIURL)
	}
}

func TestValidateAPIRejectsInvalidPublicAPIURL(t *testing.T) {
	for _, value := range []string{
		"api.example.com/v1",
		"https://user@api.example.com/v1",
		"https://api.example.com/v1?region=us",
		"https://api.example.com/v1#fragment",
	} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
			t.Setenv("OMNARA_PUBLIC_URL", "https://app.example.com")
			t.Setenv("OMNARA_PUBLIC_API_URL", value)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if err := cfg.ValidateAPI(); err == nil || !strings.Contains(err.Error(), "OMNARA_PUBLIC_API_URL") {
				t.Fatalf("expected OMNARA_PUBLIC_API_URL validation error, got %v", err)
			}
		})
	}
}

func TestValidateAPIRejectsPublicAPIURLWithoutPublicURL(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_PUBLIC_URL", "")
	t.Setenv("OMNARA_PUBLIC_API_URL", "https://api.example.com/v1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil || !strings.Contains(err.Error(), "OMNARA_PUBLIC_URL") {
		t.Fatalf("expected OMNARA_PUBLIC_URL dependency error, got %v", err)
	}
}

func TestLoadOpenRouterAttributionEnv(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_OPENROUTER_SITE_URL", " https://self-host.example/ ")
	t.Setenv("OMNARA_OPENROUTER_APP_TITLE", " Self Host ")
	t.Setenv("OMNARA_OPENROUTER_APP_CATEGORIES", "cloud-agent,programming-app")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.OpenRouterSiteURL != "https://self-host.example" ||
		cfg.OpenRouterAppTitle != "Self Host" ||
		len(cfg.OpenRouterAppCategories) != 2 ||
		cfg.OpenRouterAppCategories[0] != "cloud-agent" ||
		cfg.OpenRouterAppCategories[1] != "programming-app" {
		t.Fatalf("unexpected OpenRouter attribution config: %+v", cfg)
	}
}

func TestLoadOpenRouterAttributionAcceptsFutureCategories(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_OPENROUTER_APP_CATEGORIES", "devtools")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.OpenRouterAppCategories) != 1 || cfg.OpenRouterAppCategories[0] != "devtools" {
		t.Fatalf("unexpected OpenRouter categories: %+v", cfg.OpenRouterAppCategories)
	}
}

func TestLoadInsecureDevDefaultsProduceUsableSecretEncryptionKey(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_SECRET_ENCRYPTION_KEYS", "")
	t.Setenv("OMNARA_SECRET_ENCRYPTION_ACTIVE_KEY_ID", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	keys, err := cfg.SecretEncryptionKeyMap()
	if err != nil {
		t.Fatalf("secret encryption key map: %v", err)
	}
	key, ok := keys["dev-local"]
	if !ok {
		t.Fatalf("expected dev-local key, got keys %+v", keys)
	}
	if cfg.SecretEncryptionActiveKeyID != "dev-local" || len(key) != 32 {
		t.Fatalf("unexpected dev key config: active=%q key_len=%d", cfg.SecretEncryptionActiveKeyID, len(key))
	}
	skillKey, err := cfg.SkillDownloadSigningKeyBytes()
	if err != nil || len(skillKey) != 32 {
		t.Fatalf("unexpected dev skill signing key: len=%d err=%v", len(skillKey), err)
	}
}

func TestLoadRequiresDatabaseURLWithoutInsecureDevOptIn(t *testing.T) {
	t.Setenv("OMNARA_DATABASE_URL", "")
	t.Setenv("OMNARA_REDIS_URL", "redis://example:6379")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected missing database url error")
	}
}

func TestValidateMigrateRequiresOnlyDatabaseAndMigrationsDir(t *testing.T) {
	cfg := Config{
		DatabaseURL:      "postgres://example/db",
		MigrationsDir:    "migrations",
		MigrationTimeout: time.Minute,
	}
	if err := cfg.ValidateMigrate(); err != nil {
		t.Fatalf("validate migrate: %v", err)
	}
}

func TestValidateMigrateRequiresDatabaseURLOutsideDev(t *testing.T) {
	cfg := Config{MigrationsDir: "migrations", MigrationTimeout: time.Minute}
	if err := cfg.ValidateMigrate(); err == nil || !strings.Contains(err.Error(), "OMNARA_DATABASE_URL") {
		t.Fatalf("expected database url error, got %v", err)
	}
}

func TestValidateMigrateRequiresMigrationsDir(t *testing.T) {
	cfg := Config{
		DatabaseURL:      "postgres://example/db",
		MigrationsDir:    " ",
		MigrationTimeout: time.Minute,
	}
	if err := cfg.ValidateMigrate(); err == nil || !strings.Contains(err.Error(), "OMNARA_MIGRATIONS_DIR") {
		t.Fatalf("expected migrations dir error, got %v", err)
	}
}

func TestLoadMigrateIgnoresUnrelatedConfiguration(t *testing.T) {
	t.Setenv("OMNARA_DATABASE_URL", "postgres://example.invalid/omnara")
	t.Setenv("OMNARA_MIGRATIONS_DIR", "/tmp/migrations")
	t.Setenv("OMNARA_WORKER_CAPACITY", "not-an-int")
	t.Setenv("OMNARA_AUTH_CONNECTORS_JSON", "not-json")

	cfg, err := LoadMigrate()
	if err != nil {
		t.Fatalf("load migrate config: %v", err)
	}
	if err := cfg.ValidateMigrate(); err != nil {
		t.Fatalf("validate migrate: %v", err)
	}
	if cfg.DatabaseURL != "postgres://example.invalid/omnara" {
		t.Fatalf("unexpected database url: %q", cfg.DatabaseURL)
	}
	if cfg.MigrationsDir != "/tmp/migrations" {
		t.Fatalf("unexpected migrations dir: %q", cfg.MigrationsDir)
	}
	if cfg.MigrationTimeout != defaultMigrationTimeout {
		t.Fatalf("migration timeout = %s, want %s", cfg.MigrationTimeout, defaultMigrationTimeout)
	}
}

func TestLoadMigrateUsesInsecureDevDefaults(t *testing.T) {
	t.Setenv("OMNARA_DATABASE_URL", "")
	t.Setenv("OMNARA_MIGRATIONS_DIR", "")
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")

	cfg, err := LoadMigrate()
	if err != nil {
		t.Fatalf("load migrate config: %v", err)
	}
	if cfg.DatabaseURL != defaultDevDatabaseURL {
		t.Fatalf("unexpected database url: %q", cfg.DatabaseURL)
	}
	if cfg.MigrationsDir != "migrations" {
		t.Fatalf("unexpected migrations dir: %q", cfg.MigrationsDir)
	}
}

func TestLoadMigrateParsesTimeout(t *testing.T) {
	t.Setenv("OMNARA_DATABASE_URL", "postgres://example.invalid/omnara")
	t.Setenv("OMNARA_MIGRATION_TIMEOUT", "12m30s")

	cfg, err := LoadMigrate()
	if err != nil {
		t.Fatalf("load migrate config: %v", err)
	}
	if cfg.MigrationTimeout != 12*time.Minute+30*time.Second {
		t.Fatalf("migration timeout = %s", cfg.MigrationTimeout)
	}
}

func TestLoadMigrateRejectsInvalidTimeout(t *testing.T) {
	for _, value := range []string{"invalid", "0s", "-1s"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("OMNARA_MIGRATION_TIMEOUT", value)
			if _, err := LoadMigrate(); err == nil {
				t.Fatal("expected migration timeout error")
			}
		})
	}
}

func TestValidateAPIRequiresRedisURLWithoutInsecureDevOptIn(t *testing.T) {
	t.Setenv("OMNARA_DATABASE_URL", "postgres://example/db")
	t.Setenv("OMNARA_PUBLIC_URL", "https://app.example.com")
	t.Setenv("OMNARA_REDIS_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected missing redis url error")
	}
}

func TestValidateAPIDoesNotRequireWorkerConfig(t *testing.T) {
	t.Setenv("OMNARA_DATABASE_URL", "postgres://example/db")
	t.Setenv("OMNARA_REDIS_URL", "redis://example:6379")
	t.Setenv("OMNARA_PUBLIC_URL", "https://app.example.com")
	t.Setenv("OMNARA_SECRET_ENCRYPTION_KEYS", testSecretEncryptionKeys())
	t.Setenv("OMNARA_SECRET_ENCRYPTION_ACTIVE_KEY_ID", "test-key")
	t.Setenv("OMNARA_SKILL_DOWNLOAD_SIGNING_KEY", testSkillDownloadSigningKey())
	t.Setenv("OMNARA_EMAIL_DRIVER", "smtp")
	t.Setenv("OMNARA_SMTP_ADDR", "smtp.example.com:587")
	t.Setenv("OMNARA_EMAIL_FROM", "noreply@example.com")
	t.Setenv("OMNARA_BLOB_S3_BUCKET", "omnara-prod")
	t.Setenv("OMNARA_EMAIL_DRIVER", "smtp")
	t.Setenv("OMNARA_SMTP_ADDR", "smtp.example.com:587")
	t.Setenv("OMNARA_EMAIL_FROM", "noreply@example.com")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateAPI(); err != nil {
		t.Fatalf("validate api: %v", err)
	}
}

func TestValidateAPIBlobStoreConfig(t *testing.T) {
	t.Setenv("OMNARA_DATABASE_URL", "postgres://example/db")
	t.Setenv("OMNARA_PUBLIC_URL", "https://app.example.com")
	t.Setenv("OMNARA_SECRET_ENCRYPTION_KEYS", testSecretEncryptionKeys())
	t.Setenv("OMNARA_SECRET_ENCRYPTION_ACTIVE_KEY_ID", "test-key")
	t.Setenv("OMNARA_SKILL_DOWNLOAD_SIGNING_KEY", testSkillDownloadSigningKey())
	t.Setenv("OMNARA_EMAIL_DRIVER", "smtp")
	t.Setenv("OMNARA_SMTP_ADDR", "smtp.example.com:587")
	t.Setenv("OMNARA_EMAIL_FROM", "noreply@example.com")

	// Artifact content lives in the blob store, so a bucket is required.
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected missing blob bucket to fail outside dev")
	}

	// Non-https endpoints outside localhost fail outside dev.
	t.Setenv("OMNARA_BLOB_S3_BUCKET", "omnara-prod")
	t.Setenv("OMNARA_BLOB_S3_ENDPOINT", "http://blobs.example.com")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected non-local http blob endpoint to fail outside dev")
	}

	// Access keys must be configured together.
	t.Setenv("OMNARA_BLOB_S3_ENDPOINT", "")
	t.Setenv("OMNARA_BLOB_S3_ACCESS_KEY_ID", "only-id")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected lone blob access key id to fail")
	}
}

func TestBlobStoreDevDefaultsMatchCompose(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	s3cfg := cfg.BlobStoreS3Config()
	if s3cfg.Bucket != "omnara" || s3cfg.Endpoint != "http://127.0.0.1:59000" || !s3cfg.UsePathStyle {
		t.Fatalf("unexpected dev blob defaults: %+v", s3cfg)
	}
	if err := cfg.ValidateAPI(); err != nil {
		t.Fatalf("validate api with dev blob defaults: %v", err)
	}
}

func TestValidateAPIAuthEmailDriverGate(t *testing.T) {
	t.Setenv("OMNARA_DATABASE_URL", "postgres://example/db")
	t.Setenv("OMNARA_REDIS_URL", "redis://example:6379")
	t.Setenv("OMNARA_PUBLIC_URL", "https://app.example.com")
	t.Setenv("OMNARA_SECRET_ENCRYPTION_KEYS", testSecretEncryptionKeys())
	t.Setenv("OMNARA_SECRET_ENCRYPTION_ACTIVE_KEY_ID", "test-key")
	t.Setenv("OMNARA_SKILL_DOWNLOAD_SIGNING_KEY", testSkillDownloadSigningKey())
	t.Setenv("OMNARA_BLOB_S3_BUCKET", "omnara-prod")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil || !strings.Contains(err.Error(), "OMNARA_EMAIL_DRIVER") {
		t.Fatalf("expected auth email driver gate, got %v", err)
	}

	t.Setenv("OMNARA_AUTH_SIGNUP_ENABLED", "0")
	t.Setenv("OMNARA_AUTH_PASSWORD_RESET_ENABLED", "0")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load config with auth disabled: %v", err)
	}
	if err := cfg.ValidateAPI(); err != nil {
		t.Fatalf("validate api with auth disabled: %v", err)
	}
}

func TestValidateAPIEmailDrivers(t *testing.T) {
	t.Setenv("OMNARA_DATABASE_URL", "postgres://example/db")
	t.Setenv("OMNARA_REDIS_URL", "redis://example:6379")
	t.Setenv("OMNARA_PUBLIC_URL", "https://app.example.com")
	t.Setenv("OMNARA_SECRET_ENCRYPTION_KEYS", testSecretEncryptionKeys())
	t.Setenv("OMNARA_SECRET_ENCRYPTION_ACTIVE_KEY_ID", "test-key")
	t.Setenv("OMNARA_SKILL_DOWNLOAD_SIGNING_KEY", testSkillDownloadSigningKey())
	t.Setenv("OMNARA_BLOB_S3_BUCKET", "omnara-prod")

	t.Setenv("OMNARA_EMAIL_DRIVER", "console")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load console config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil || !strings.Contains(err.Error(), "console") {
		t.Fatalf("expected console outside dev error, got %v", err)
	}

	t.Setenv("OMNARA_EMAIL_DRIVER", "smtp")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load smtp config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil || !strings.Contains(err.Error(), "OMNARA_SMTP_ADDR") {
		t.Fatalf("expected missing smtp config error, got %v", err)
	}
	t.Setenv("OMNARA_SMTP_ADDR", "smtp.example.com:587")
	t.Setenv("OMNARA_EMAIL_FROM", "noreply@example.com")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load complete smtp config: %v", err)
	}
	if err := cfg.ValidateAPI(); err != nil {
		t.Fatalf("validate complete smtp config: %v", err)
	}
	t.Setenv("OMNARA_SMTP_REQUIRE_TLS", "false")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load plaintext smtp config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil || !strings.Contains(err.Error(), "OMNARA_SMTP_REQUIRE_TLS") {
		t.Fatalf("expected smtp tls config error, got %v", err)
	}
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load insecure plaintext smtp config: %v", err)
	}
	if err := cfg.ValidateAPI(); err != nil {
		t.Fatalf("validate insecure plaintext smtp config: %v", err)
	}
}

func TestValidateAPIExternalEmailRequiresPublicURLInDev(t *testing.T) {
	tests := []struct {
		name   string
		driver string
	}{
		{name: "smtp", driver: "smtp"},
		{name: "sendgrid", driver: "sendgrid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
			t.Setenv("OMNARA_PUBLIC_URL", "")
			t.Setenv("OMNARA_EMAIL_DRIVER", tt.driver)
			t.Setenv("OMNARA_EMAIL_FROM", "noreply@example.com")
			t.Setenv("OMNARA_SMTP_ADDR", "smtp.example.com:587")
			t.Setenv("SENDGRID_API_KEY", "sg-secret")

			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if err := cfg.ValidateAPI(); err == nil || !strings.Contains(err.Error(), "OMNARA_PUBLIC_URL") {
				t.Fatalf("expected missing public URL error, got %v", err)
			}
		})
	}
}

func TestValidateAPITrustedProxyCIDRs(t *testing.T) {
	t.Setenv("OMNARA_DATABASE_URL", "postgres://example/db")
	t.Setenv("OMNARA_REDIS_URL", "redis://example:6379")
	t.Setenv("OMNARA_PUBLIC_URL", "https://app.example.com")
	t.Setenv("OMNARA_SECRET_ENCRYPTION_KEYS", testSecretEncryptionKeys())
	t.Setenv("OMNARA_SECRET_ENCRYPTION_ACTIVE_KEY_ID", "test-key")
	t.Setenv("OMNARA_SKILL_DOWNLOAD_SIGNING_KEY", testSkillDownloadSigningKey())
	t.Setenv("OMNARA_BLOB_S3_BUCKET", "omnara-prod")
	t.Setenv("OMNARA_EMAIL_DRIVER", "smtp")
	t.Setenv("OMNARA_SMTP_ADDR", "smtp.example.com:587")
	t.Setenv("OMNARA_EMAIL_FROM", "noreply@example.com")
	t.Setenv("OMNARA_TRUSTED_PROXY_CIDRS", "10.0.0.0/24, 192.0.2.0/24")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load trusted proxy config: %v", err)
	}
	if len(cfg.TrustedProxyCIDRs) != 2 {
		t.Fatalf("trusted proxy cidrs=%v, want 2 entries", cfg.TrustedProxyCIDRs)
	}
	if err := cfg.ValidateAPI(); err != nil {
		t.Fatalf("validate trusted proxy config: %v", err)
	}
	t.Setenv("OMNARA_TRUSTED_PROXY_CIDRS", "not-a-cidr")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load invalid trusted proxy config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil || !strings.Contains(err.Error(), "OMNARA_TRUSTED_PROXY_CIDRS") {
		t.Fatalf("expected trusted proxy config error, got %v", err)
	}
}

func TestLoadAuthConnectors(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_AUTH_CONNECTORS_JSON", `[
		{"slug":"google","kind":"oidc","display_name":"Google","issuer":"https://accounts.google.com","client_id":"google-client","client_secret":"google-secret"},
		{"slug":"github","kind":"github","display_name":"GitHub","client_id":"github-client","client_secret":"github-secret","enabled":false,"scopes":["read:user","user:email"]}
	]`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.AuthConnectors) != 2 {
		t.Fatalf("auth connectors = %+v, want 2", cfg.AuthConnectors)
	}
	if cfg.AuthConnectors[0].Slug != "google" || !cfg.AuthConnectors[0].EnabledValue() {
		t.Fatalf("google connector = %+v", cfg.AuthConnectors[0])
	}
	if cfg.AuthConnectors[0].EmailTrustPolicy != identitystore.AuthConnectorEmailTrustPolicyNone {
		t.Fatalf("google email trust policy = %q, want none by default", cfg.AuthConnectors[0].EmailTrustPolicy)
	}
	if cfg.AuthConnectors[1].Slug != "github" || cfg.AuthConnectors[1].EnabledValue() {
		t.Fatalf("github connector = %+v", cfg.AuthConnectors[1])
	}
	if cfg.AuthConnectors[1].EmailTrustPolicy != identitystore.AuthConnectorEmailTrustPolicyVerifiedEmail {
		t.Fatalf("github email trust policy = %q, want verified_email by default", cfg.AuthConnectors[1].EmailTrustPolicy)
	}
	if cfg.AuthConnectors[1].Issuer != "https://github.com" ||
		cfg.AuthConnectors[1].AuthorizationURL != "https://github.com/login/oauth/authorize" ||
		cfg.AuthConnectors[1].TokenURL != "https://github.com/login/oauth/access_token" ||
		cfg.AuthConnectors[1].UserinfoURL != "https://api.github.com/user" {
		t.Fatalf("github connector defaults = %+v", cfg.AuthConnectors[1])
	}
	if err := cfg.ValidateAPI(); err != nil {
		t.Fatalf("validate api: %v", err)
	}
}

func TestValidateAPIAuthConnectorConfig(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv(
		"OMNARA_AUTH_CONNECTORS_JSON",
		`[{"slug":"bad","kind":"oidc","display_name":"Bad","client_id":"client","client_secret":"secret"}]`,
	)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil || !strings.Contains(err.Error(), "issuer") {
		t.Fatalf("expected issuer validation error, got %v", err)
	}

	t.Setenv(
		"OMNARA_AUTH_CONNECTORS_JSON",
		`[{"slug":"bad","kind":"unknown","display_name":"Bad","client_id":"client","client_secret":"secret"}]`,
	)
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load unknown connector config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("expected kind validation error, got %v", err)
	}

	t.Setenv(
		"OMNARA_AUTH_CONNECTORS_JSON",
		`[{"slug":"Bad Slug","kind":"github","display_name":"Bad","client_id":"client","client_secret":"secret"}]`,
	)
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load bad slug connector config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil || !strings.Contains(err.Error(), "slug") {
		t.Fatalf("expected slug validation error, got %v", err)
	}

	t.Setenv(
		"OMNARA_AUTH_CONNECTORS_JSON",
		`[{"slug":"bad","kind":"oidc","display_name":"Bad","issuer":"https://idp.example.com","client_id":"client","client_secret":"secret","email_trust_policy":"trust_me"}]`,
	)
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load bad trust policy connector config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil || !strings.Contains(err.Error(), "email_trust_policy") {
		t.Fatalf("expected email trust policy validation error, got %v", err)
	}

	t.Setenv(
		"OMNARA_AUTH_CONNECTORS_JSON",
		`[{"slug":"local","kind":"oidc","display_name":"Local","issuer":"http://idp.test","client_id":"client","client_secret":"secret"}]`,
	)
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load insecure connector config: %v", err)
	}
	if err := cfg.ValidateAPI(); err != nil {
		t.Fatalf("insecure dev should allow http connector URLs: %v", err)
	}

	t.Setenv(
		"OMNARA_AUTH_CONNECTORS_JSON",
		`[{"slug":"manual-oidc","kind":"oidc","display_name":"Manual OIDC","issuer":"https://idp.example.com","authorization_url":"https://idp.example.com/auth","client_id":"client","client_secret":"secret"}]`,
	)
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load manual oidc connector config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil || !strings.Contains(err.Error(), "issuer discovery") {
		t.Fatalf("expected oidc discovery validation error, got %v", err)
	}

	t.Setenv(
		"OMNARA_AUTH_CONNECTORS_JSON",
		`[{"slug":"ghe","kind":"github","display_name":"GitHub Enterprise","issuer":"https://github.example.com","client_id":"client","client_secret":"secret"}]`,
	)
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load partial github connector config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil || !strings.Contains(err.Error(), "custom github connectors require") {
		t.Fatalf("expected github custom endpoint validation error, got %v", err)
	}

	t.Setenv("OMNARA_AUTH_CONNECTORS_JSON", `[
		{"slug":"github-oidc","kind":"oidc","display_name":"GitHub OIDC","issuer":"https://github.com","client_id":"oidc-client","client_secret":"oidc-secret"},
		{"slug":"github","kind":"github","display_name":"GitHub","client_id":"github-client","client_secret":"github-secret"}
	]`)
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load duplicate issuer connector config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil || !strings.Contains(err.Error(), "duplicate connector issuer") {
		t.Fatalf("expected duplicate connector issuer validation error, got %v", err)
	}
}

func TestValidateAPIAuthConnectorURLsRequireHTTPSOutsideDev(t *testing.T) {
	t.Setenv("OMNARA_DATABASE_URL", "postgres://example/db")
	t.Setenv("OMNARA_REDIS_URL", "redis://example:6379")
	t.Setenv("OMNARA_PUBLIC_URL", "https://app.example.com")
	t.Setenv("OMNARA_SECRET_ENCRYPTION_KEYS", testSecretEncryptionKeys())
	t.Setenv("OMNARA_SECRET_ENCRYPTION_ACTIVE_KEY_ID", "test-key")
	t.Setenv("OMNARA_SKILL_DOWNLOAD_SIGNING_KEY", testSkillDownloadSigningKey())
	t.Setenv("OMNARA_BLOB_S3_BUCKET", "omnara-prod")
	t.Setenv("OMNARA_EMAIL_DRIVER", "smtp")
	t.Setenv("OMNARA_SMTP_ADDR", "smtp.example.com:587")
	t.Setenv("OMNARA_EMAIL_FROM", "noreply@example.com")
	t.Setenv(
		"OMNARA_AUTH_CONNECTORS_JSON",
		`[{"slug":"remote","kind":"oidc","display_name":"Remote","issuer":"http://idp.example.com","client_id":"client","client_secret":"secret"}]`,
	)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load remote connector config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("expected https connector URL validation error, got %v", err)
	}

	t.Setenv(
		"OMNARA_AUTH_CONNECTORS_JSON",
		`[{"slug":"local","kind":"oidc","display_name":"Local","issuer":"http://localhost:9999","client_id":"client","client_secret":"secret"}]`,
	)
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load localhost connector config: %v", err)
	}
	if err := cfg.ValidateAPI(); err != nil {
		t.Fatalf("localhost connector URL should be allowed outside dev: %v", err)
	}
}

func TestValidateAPIRequiresSecretEncryptionKeyOutsideDev(t *testing.T) {
	t.Setenv("OMNARA_DATABASE_URL", "postgres://example/db")
	t.Setenv("OMNARA_PUBLIC_URL", "https://app.example.com")
	t.Setenv("OMNARA_SECRET_ENCRYPTION_KEYS", "")
	t.Setenv("OMNARA_SECRET_ENCRYPTION_ACTIVE_KEY_ID", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected missing secret encryption key error")
	}
}

func TestValidateAPIRejectsMalformedSecretEncryptionKey(t *testing.T) {
	t.Setenv("OMNARA_DATABASE_URL", "postgres://example/db")
	t.Setenv("OMNARA_PUBLIC_URL", "https://app.example.com")
	t.Setenv("OMNARA_SECRET_ENCRYPTION_KEYS", `{"test-key":"`+base64.StdEncoding.EncodeToString([]byte("short"))+`"}`)
	t.Setenv("OMNARA_SECRET_ENCRYPTION_ACTIVE_KEY_ID", "test-key")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected malformed secret encryption key error")
	}
}

func TestValidateAPIRejectsMalformedSecretEncryptionKeyRing(t *testing.T) {
	t.Setenv("OMNARA_DATABASE_URL", "postgres://example/db")
	t.Setenv("OMNARA_PUBLIC_URL", "https://app.example.com")
	t.Setenv("OMNARA_SECRET_ENCRYPTION_KEYS", "not-json")
	t.Setenv("OMNARA_SECRET_ENCRYPTION_ACTIVE_KEY_ID", "test-key")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected malformed key ring error")
	}
}

func TestValidateAPIRejectsUnknownActiveSecretEncryptionKey(t *testing.T) {
	t.Setenv("OMNARA_DATABASE_URL", "postgres://example/db")
	t.Setenv("OMNARA_PUBLIC_URL", "https://app.example.com")
	t.Setenv("OMNARA_SECRET_ENCRYPTION_KEYS", testSecretEncryptionKeys())
	t.Setenv("OMNARA_SECRET_ENCRYPTION_ACTIVE_KEY_ID", "missing")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected unknown active secret encryption key error")
	}
}

func TestValidateAPIRejectsDuplicateSecretEncryptionKeyMaterial(t *testing.T) {
	t.Setenv("OMNARA_DATABASE_URL", "postgres://example/db")
	t.Setenv("OMNARA_PUBLIC_URL", "https://app.example.com")
	encoded := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	t.Setenv("OMNARA_SECRET_ENCRYPTION_KEYS", `{"key-a":"`+encoded+`","key-b":"`+encoded+`"}`)
	t.Setenv("OMNARA_SECRET_ENCRYPTION_ACTIVE_KEY_ID", "key-a")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected duplicate secret encryption key material error")
	}
}

func TestValidateAPIRejectsPartialSecretEncryptionConfigInDev(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_SECRET_ENCRYPTION_KEYS", testSecretEncryptionKeys())
	t.Setenv("OMNARA_SECRET_ENCRYPTION_ACTIVE_KEY_ID", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected partial secret encryption config error")
	}
}

func TestValidateAPIRejectsMissingSecretEncryptionConfigEvenInDevConfig(t *testing.T) {
	cfg := Config{
		APIAddr:          ":8080",
		APIMetricsAddr:   ":8081",
		AllowInsecureDev: true,
	}
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected missing secret encryption config error")
	}
}

func TestValidateSkillDownloadSigningKey(t *testing.T) {
	config := Config{}
	err := validateSkillDownloadSigningKeyConfig(config)
	if err == nil || !strings.Contains(err.Error(), "OMNARA_SKILL_DOWNLOAD_SIGNING_KEY") {
		t.Fatalf("missing skill signing key error = %v", err)
	}
	config.SkillDownloadSigningKey = base64.StdEncoding.EncodeToString([]byte("short"))
	err = validateSkillDownloadSigningKeyConfig(config)
	if err == nil || !strings.Contains(err.Error(), "decode to 32 bytes") {
		t.Fatalf("short skill signing key error = %v", err)
	}
	config.SkillDownloadSigningKey = testSkillDownloadSigningKey()
	if err := validateSkillDownloadSigningKeyConfig(config); err != nil {
		t.Fatalf("valid skill signing key rejected: %v", err)
	}
}

func TestLoadDaemonReleaseOverride(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv(DaemonReleaseURLEnv, "http://localhost:9090/omnarad/latest")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateAPI(); err != nil {
		t.Fatalf("validate api: %v", err)
	}
	if cfg.DaemonReleaseURL != "http://localhost:9090/omnarad/latest" {
		t.Fatalf("daemon release url = %q", cfg.DaemonReleaseURL)
	}
}

func TestValidateAPIRejectsInvalidDaemonReleaseURL(t *testing.T) {
	tests := []struct {
		name       string
		releaseURL string
	}{
		{name: "invalid release url", releaseURL: "not-a-url"},
		{
			name:       "release url credentials",
			releaseURL: "https://user:pass@releases.omnara.test/omnarad",
		},
		{name: "release url fragment", releaseURL: "https://releases.omnara.test/omnarad#current"},
		{name: "release url empty fragment", releaseURL: "https://releases.omnara.test/omnarad#"},
		{name: "release url empty port", releaseURL: "https://releases.omnara.test:/omnarad"},
		{name: "release url invalid port", releaseURL: "https://releases.omnara.test:65536/omnarad"},
		{name: "release url query", releaseURL: "https://releases.omnara.test/omnarad?channel=stable"},
		{
			name:       "release url malformed query",
			releaseURL: "https://releases.omnara.test/omnarad?channel=stable&",
		},
		{name: "remote http release url", releaseURL: "http://releases.omnara.test/omnarad"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
			t.Setenv(DaemonReleaseURLEnv, tt.releaseURL)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if err := cfg.ValidateAPI(); err == nil {
				t.Fatal("expected invalid daemon release URL error")
			}
		})
	}
}

func TestValidateAPIRequiresPublicURLOutsideDev(t *testing.T) {
	t.Setenv("OMNARA_DATABASE_URL", "postgres://example/db")
	t.Setenv("OMNARA_REDIS_URL", "redis://example:6379")
	t.Setenv("OMNARA_PUBLIC_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected missing public URL error")
	}
}

func TestValidateAPIAcceptsPublicURL(t *testing.T) {
	tests := []struct {
		name       string
		billingURL string
		wantErr    bool
	}{
		{name: "billing url unset"},
		{name: "billing url https with path", billingURL: "https://billing.example.com/credits"},
		{name: "billing url http outside localhost", billingURL: "http://billing.example.com", wantErr: true},
		{
			name:       "billing url query parameters",
			billingURL: "https://billing.example.com/credits?plan=pro",
			wantErr:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OMNARA_DATABASE_URL", "postgres://example/db")
			t.Setenv("OMNARA_REDIS_URL", "redis://example:6379")
			t.Setenv("OMNARA_PUBLIC_URL", "https://app.example.com")
			t.Setenv("OMNARA_SECRET_ENCRYPTION_KEYS", testSecretEncryptionKeys())
			t.Setenv("OMNARA_SECRET_ENCRYPTION_ACTIVE_KEY_ID", "test-key")
			t.Setenv("OMNARA_SKILL_DOWNLOAD_SIGNING_KEY", testSkillDownloadSigningKey())
			t.Setenv("OMNARA_BLOB_S3_BUCKET", "omnara-prod")
			t.Setenv("OMNARA_EMAIL_DRIVER", "smtp")
			t.Setenv("OMNARA_SMTP_ADDR", "smtp.example.com:587")
			t.Setenv("OMNARA_EMAIL_FROM", "noreply@example.com")
			t.Setenv("OMNARA_BILLING_URL", tt.billingURL)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			err = cfg.ValidateAPI()
			if tt.wantErr && err == nil {
				t.Fatal("expected billing URL validation error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("validate api: %v", err)
			}
		})
	}
}

func TestValidateAPIRejectsNonOriginPublicURLs(t *testing.T) {
	tests := []struct {
		name string
		env  string
	}{
		{name: "path", env: "https://app.example.com/app"},
		{name: "credentials", env: "https://user@app.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OMNARA_DATABASE_URL", "postgres://example/db")
			t.Setenv("OMNARA_REDIS_URL", "redis://example:6379")
			t.Setenv("OMNARA_PUBLIC_URL", tt.env)
			t.Setenv("OMNARA_SECRET_ENCRYPTION_KEYS", testSecretEncryptionKeys())
			t.Setenv("OMNARA_SECRET_ENCRYPTION_ACTIVE_KEY_ID", "test-key")
			t.Setenv("OMNARA_BLOB_S3_BUCKET", "omnara-prod")
			t.Setenv("OMNARA_EMAIL_DRIVER", "smtp")
			t.Setenv("OMNARA_SMTP_ADDR", "smtp.example.com:587")
			t.Setenv("OMNARA_EMAIL_FROM", "noreply@example.com")

			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if err := cfg.ValidateAPI(); err == nil {
				t.Fatal("expected non-origin public URL to fail")
			}
		})
	}
}

func TestValidateAPIValidatesWebServingConfig(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		wantErr bool
	}{
		{name: "disabled default"},
		{name: "embedded", mode: string(WebServingEmbedded)},
		{name: "disabled", mode: string(WebServingDisabled)},
		{name: "filesystem is not a public mode", mode: "filesystem", wantErr: true},
		{name: "invalid mode", mode: "assets", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
			t.Setenv("OMNARA_WEB_SERVING", tt.mode)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			err = cfg.ValidateAPI()
			if tt.wantErr && err == nil {
				t.Fatal("expected web serving config to fail")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected web serving config to pass: %v", err)
			}
		})
	}
}

func testSecretEncryptionKeys() string {
	return `{"test-key":"` + base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")) + `"}`
}

func testSkillDownloadSigningKey() string {
	return base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
}

func TestWorkerConfigAllowsEnvironmentSelectedExecutors(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateWorker(); err != nil {
		t.Fatalf("validate worker: %v", err)
	}
}

func TestValidateWorkerRequiresPublicURLOutsideDev(t *testing.T) {
	t.Setenv("OMNARA_DATABASE_URL", "postgres://example/db")
	t.Setenv("OMNARA_REDIS_URL", "redis://example:6379")
	t.Setenv("OMNARA_BLOB_S3_BUCKET", "omnara-prod")
	t.Setenv("OMNARA_SKILL_DOWNLOAD_SIGNING_KEY", testSkillDownloadSigningKey())
	t.Setenv("OMNARA_PUBLIC_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateWorker(); err == nil {
		t.Fatal("expected missing public URL error")
	}

	t.Setenv("OMNARA_PUBLIC_URL", "https://app.example.com")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateWorker(); err != nil {
		t.Fatalf("expected worker public URL to pass: %v", err)
	}
}

func TestValidateMaintenanceDoesNotRequireProjectOrModel(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_MAINTENANCE_INTERVAL", "2s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateMaintenance(); err != nil {
		t.Fatalf("validate maintenance: %v", err)
	}
	if cfg.MaintenanceInterval != 2*time.Second {
		t.Fatalf("expected maintenance interval from env, got %s", cfg.MaintenanceInterval)
	}
}

func TestValidateWorkerLoadsExplicitWorkerCapacity(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_WORKER_CAPACITY", "8")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateWorker(); err != nil {
		t.Fatalf("validate worker: %v", err)
	}
	if cfg.WorkerCapacity != 8 {
		t.Fatalf("unexpected worker capacity: %+v", cfg)
	}
}

func TestValidateWorkerLoadsExplicitAsyncToolCapacity(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_WORKER_ASYNC_TOOL_CAPACITY", "64")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateWorker(); err != nil {
		t.Fatalf("validate worker: %v", err)
	}
	if cfg.WorkerAsyncToolCapacity != 64 {
		t.Fatalf("unexpected async tool capacity: %+v", cfg)
	}
}

func TestValidateWorkerLoadsExplicitBackgroundToolCapacity(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_WORKER_BACKGROUND_TOOL_CAPACITY", "16")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateWorker(); err != nil {
		t.Fatalf("validate worker: %v", err)
	}
	if cfg.WorkerBackgroundToolCapacity != 16 {
		t.Fatalf("unexpected background tool capacity: %+v", cfg)
	}
}

func TestLoadRejectsInvalidDaemonSocketFallbackDrainTiming(t *testing.T) {
	t.Setenv("OMNARA_DAEMON_SOCKET_FALLBACK_DRAIN_INTERVAL", "0s")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid daemon socket fallback drain interval error")
	}

	t.Setenv("OMNARA_DAEMON_SOCKET_FALLBACK_DRAIN_INTERVAL", "30s")
	t.Setenv("OMNARA_DAEMON_SOCKET_FALLBACK_DRAIN_JITTER", "-1s")
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid daemon socket fallback drain jitter error")
	}

	t.Setenv("OMNARA_DAEMON_SOCKET_FALLBACK_DRAIN_INTERVAL", "1s")
	t.Setenv("OMNARA_DAEMON_SOCKET_FALLBACK_DRAIN_JITTER", "3s")
	if _, err := Load(); err == nil {
		t.Fatal("expected too-large daemon socket fallback drain jitter error")
	}
}

func TestValidateWorkerRejectsInvalidWorkerCapacity(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_WORKER_CAPACITY", "0")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateWorker(); err == nil {
		t.Fatal("expected invalid worker capacity error")
	}
}

func TestValidateWorkerRejectsInvalidAsyncToolCapacity(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_WORKER_ASYNC_TOOL_CAPACITY", "0")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateWorker(); err == nil {
		t.Fatal("expected invalid async tool capacity error")
	}
}

func TestValidateWorkerRejectsInvalidBackgroundToolCapacity(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_WORKER_BACKGROUND_TOOL_CAPACITY", "0")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateWorker(); err == nil {
		t.Fatal("expected invalid background tool capacity error")
	}
}

func TestValidateAPIRejectsInvalidPort(t *testing.T) {
	t.Setenv("OMNARA_API_ADDR", ":99999")
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected invalid port error")
	}
}

func TestValidateAPIRejectsInvalidMetricsPort(t *testing.T) {
	t.Setenv("OMNARA_API_METRICS_ADDR", ":99999")
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected invalid api metrics port error")
	}
}

func TestValidateWorkerIgnoresAPIAddr(t *testing.T) {
	t.Setenv("OMNARA_API_ADDR", ":99999")
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateWorker(); err != nil {
		t.Fatalf("validate worker: %v", err)
	}
}

func TestValidateWorkerRejectsInvalidWorkerMetricsAddr(t *testing.T) {
	t.Setenv("OMNARA_WORKER_METRICS_ADDR", ":99999")
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateWorker(); err == nil {
		t.Fatal("expected invalid worker health and metrics port error")
	}
}

func TestValidateMaintenanceRequiresPublicURLOutsideDev(t *testing.T) {
	t.Setenv("OMNARA_DATABASE_URL", "postgres://example")
	t.Setenv("OMNARA_PUBLIC_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateMaintenance(); err == nil {
		t.Fatal("expected public URL error")
	}
}

func TestValidateMaintenanceRejectsInvalidMaintenanceMetricsAddr(t *testing.T) {
	t.Setenv("OMNARA_MAINTENANCE_METRICS_ADDR", ":99999")
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateMaintenance(); err == nil {
		t.Fatal("expected invalid maintenance health and metrics port error")
	}
}

func TestValidateAPIRequiresDatabaseURLWithoutInsecureDevOptIn(t *testing.T) {
	t.Setenv("OMNARA_DATABASE_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected missing database url error")
	}
}
