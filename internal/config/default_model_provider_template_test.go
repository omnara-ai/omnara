package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
)

const testHostedAPIToken = "test-hosted-api-token-with-at-least-32-bytes"

func TestLoadDefaultModelProviderTemplate(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_HOSTED_API_URL", "https://saas.example.test/private")
	t.Setenv("OMNARA_HOSTED_API_TOKEN", testHostedAPIToken)
	path := filepath.Join(t.TempDir(), "default-model-provider.yaml")
	if err := os.WriteFile(path, []byte(`
provisioner: " openrouter "
name: " omnara-openrouter "
credential_secret_name: " omnara-openrouter-key "
api_format: " openai-chat-completions "
api_variant: " openrouter "
base_url: " https://openrouter.ai/api/v1/ "
models:
  - name: " claude-sonnet-4.5 "
    provider_model_slug: " anthropic/claude-sonnet-4.5 "
    context_window_tokens: 200000
    max_output_tokens: 64000
    default_max_output_tokens: 8192
    supports_reasoning: true
    default_reasoning_effort: medium
    supported_reasoning_efforts: [low, medium, high]
    input_modalities: [text, image]
    output_modalities: [text]
`), 0o600); err != nil {
		t.Fatalf("write default model provider template: %v", err)
	}
	t.Setenv("OMNARA_DEFAULT_MODEL_PROVIDER_TEMPLATE", path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.DefaultModelProvider == nil {
		t.Fatal("default model provider was not loaded")
	}
	template := *cfg.DefaultModelProvider
	if template.Provisioner != "openrouter" ||
		template.APIFormat != modelprotocol.APIFormatOpenAIChatCompletions ||
		template.APIVariant != modelprotocol.APIVariantOpenRouter ||
		template.Name != "omnara-openrouter" ||
		template.CredentialSecretName != "omnara-openrouter-key" ||
		template.BaseURL != "https://openrouter.ai/api/v1" ||
		template.EndpointPath != "/chat/completions" ||
		template.AuthKind != modelstore.ModelProviderAuthKindBearerToken ||
		template.RequestTimeoutMS != int(modelstore.DefaultModelProviderRequestTimeoutMS) {
		t.Fatalf("unexpected template: %+v", template)
	}
	if len(template.Models) != 1 || template.Models[0].ProviderModelSlug != "anthropic/claude-sonnet-4.5" {
		t.Fatalf("unexpected models: %+v", template.Models)
	}
	if cfg.HostedAPIURL != "https://saas.example.test/private" ||
		cfg.HostedAPIToken != testHostedAPIToken {
		t.Fatalf(
			"unexpected hosted API config: url=%q token=%q",
			cfg.HostedAPIURL,
			cfg.HostedAPIToken,
		)
	}
}

func TestLoadDefaultModelProviderExample(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_HOSTED_API_URL", "https://saas.example.test")
	t.Setenv("OMNARA_HOSTED_API_TOKEN", testHostedAPIToken)
	t.Setenv("OMNARA_DEFAULT_MODEL_PROVIDER_TEMPLATE", filepath.Join("..", "..", "default-model-provider-example.yaml"))

	if _, err := Load(); err != nil {
		t.Fatalf("load config: %v", err)
	}
}

func TestDefaultModelProviderTemplateOnlyRequiresCredentialServiceInMaintenance(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	path := filepath.Join(t.TempDir(), "default-model-provider.yaml")
	if err := os.WriteFile(path, []byte(`
provisioner: openrouter
name: omnara-openrouter
credential_secret_name: omnara-openrouter-key
api_format: openai-chat-completions
api_variant: openrouter
base_url: https://openrouter.ai/api/v1
models:
  - name: glm-4.6
    provider_model_slug: z-ai/glm-4.6
    context_window_tokens: 128000
`), 0o600); err != nil {
		t.Fatalf("write default model provider template: %v", err)
	}
	t.Setenv("OMNARA_DEFAULT_MODEL_PROVIDER_TEMPLATE", path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateAPI(); err != nil {
		t.Fatalf("ValidateAPI error = %v, want nil", err)
	}
	if err := cfg.ValidateMaintenance(); err == nil ||
		!strings.Contains(err.Error(), "OMNARA_HOSTED_API_URL") {
		t.Fatalf("ValidateMaintenance error = %v, want missing hosted API URL", err)
	}
	if cfg.DefaultModelProvider == nil ||
		len(cfg.DefaultModelProvider.Models) != 1 ||
		cfg.DefaultModelProvider.Models[0].MaxOutputTokens != 8_192 ||
		cfg.DefaultModelProvider.Models[0].DefaultMaxOutputTokens == nil ||
		*cfg.DefaultModelProvider.Models[0].DefaultMaxOutputTokens != 4_096 {
		t.Fatalf(
			"default model output limits = %+v, want 8192/4096",
			cfg.DefaultModelProvider,
		)
	}
}

func TestHostedCredentialServiceRequiresDefaultModelProviderTemplate(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	t.Setenv("OMNARA_HOSTED_API_URL", "https://saas.example.test")
	t.Setenv("OMNARA_HOSTED_API_TOKEN", testHostedAPIToken)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateMaintenance(); err == nil ||
		!strings.Contains(err.Error(), "OMNARA_DEFAULT_MODEL_PROVIDER_TEMPLATE") {
		t.Fatalf("ValidateMaintenance error = %v, want missing default model provider template", err)
	}
}

func TestLoadDefaultModelProviderTemplateRejectsUnknownFieldsAndTrailingDocuments(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "unknown provider field",
			body: `
provisioner: openrouter
name: omnara-openrouter
credential_secret_name: omnara-openrouter-key
api_format: openai-chat-completions
api_variant: openrouter
base_url: https://openrouter.ai/api/v1
unexpected_field: true
models:
  - name: glm-4.6
    provider_model_slug: z-ai/glm-4.6
    context_window_tokens: 128000
`,
			want: "field unexpected_field not found",
		},
		{
			name: "trailing document",
			body: `
provisioner: openrouter
name: omnara-openrouter
credential_secret_name: omnara-openrouter-key
api_format: openai-chat-completions
api_variant: openrouter
base_url: https://openrouter.ai/api/v1
models:
  - name: glm-4.6
    provider_model_slug: z-ai/glm-4.6
    context_window_tokens: 128000
---
`,
			want: "trailing YAML document",
		},
		{
			name: "invalid model template",
			body: `
provisioner: openrouter
name: omnara-openrouter
credential_secret_name: omnara-openrouter-key
api_format: openai-chat-completions
api_variant: openrouter
base_url: https://openrouter.ai/api/v1
models:
  - name: glm-4.6
    context_window_tokens: 128000
`,
			want: "model name and provider model slug are required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeDefaultModelProviderTemplateTestFile(t, tt.body)
			t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
			t.Setenv("OMNARA_DEFAULT_MODEL_PROVIDER_TEMPLATE", path)
			t.Setenv("OMNARA_HOSTED_API_URL", "https://saas.example.test")
			t.Setenv("OMNARA_HOSTED_API_TOKEN", testHostedAPIToken)

			if _, err := Load(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestDefaultModelProviderTemplateMaintenanceRequiresHostedAPIToken(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	path := writeDefaultModelProviderTemplateTestFile(t, `
provisioner: openrouter
name: omnara-openrouter
credential_secret_name: omnara-openrouter-key
api_format: openai-chat-completions
api_variant: openrouter
base_url: https://openrouter.ai/api/v1
models:
  - name: glm-4.6
    provider_model_slug: z-ai/glm-4.6
    context_window_tokens: 128000
`)
	t.Setenv("OMNARA_DEFAULT_MODEL_PROVIDER_TEMPLATE", path)
	t.Setenv("OMNARA_HOSTED_API_URL", "https://saas.example.test")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := cfg.ValidateAPI(); err != nil {
		t.Fatalf("ValidateAPI error = %v, want nil", err)
	}
	if err := cfg.ValidateMaintenance(); err == nil ||
		!strings.Contains(err.Error(), "OMNARA_HOSTED_API_TOKEN") {
		t.Fatalf("ValidateMaintenance error = %v, want missing hosted API token", err)
	}
}

func TestDefaultModelProviderTemplateRequiresProvisioner(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	path := writeDefaultModelProviderTemplateTestFile(t, `
name: hosted-provider
credential_secret_name: hosted-provider-key
api_format: openai-chat-completions
base_url: https://gateway.example.test/v1
models:
  - name: model
    provider_model_slug: vendor/model
    context_window_tokens: 128000
`)
	t.Setenv("OMNARA_DEFAULT_MODEL_PROVIDER_TEMPLATE", path)
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "provisioner is required") {
		t.Fatalf("Load error = %v, want required provisioner", err)
	}
}

func TestDefaultModelProviderTemplateRejectsUnsatisfiableHostedRequestSize(t *testing.T) {
	t.Setenv("OMNARA_ALLOW_INSECURE_DEV_DEFAULTS", "1")
	path := writeDefaultModelProviderTemplateTestFile(t, `
provisioner: custom-provisioner
name: hosted-provider
credential_secret_name: hosted-provider-key
api_format: openai-chat-completions
base_url: https://gateway.example.test/v1
models:
  - name: model
    provider_model_slug: `+strings.Repeat("x", 300*1024)+`
    context_window_tokens: 128000
`)
	t.Setenv("OMNARA_DEFAULT_MODEL_PROVIDER_TEMPLATE", path)
	t.Setenv("OMNARA_HOSTED_API_URL", "https://saas.example.test")
	t.Setenv("OMNARA_HOSTED_API_TOKEN", testHostedAPIToken)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.ValidateAPI(); err == nil ||
		!strings.Contains(err.Error(), "hosted credential request exceeds size limit") {
		t.Fatalf("ValidateAPI error = %v, want hosted request size limit", err)
	}
}

func writeDefaultModelProviderTemplateTestFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "default-model-provider.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write default model provider template: %v", err)
	}
	return path
}
