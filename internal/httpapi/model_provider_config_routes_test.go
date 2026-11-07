package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func TestApplyModelProviderPresetMaterializesEndpoints(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		preset       string
		apiFormat    modelprotocol.APIFormat
		apiVariant   modelprotocol.APIVariant
		baseURL      string
		endpointPath string
		authKind     string
	}{
		{"openai", modelprotocol.APIFormatOpenAIResponses, modelprotocol.APIVariantDefault, "https://api.openai.com/v1", "/responses", "bearer_token"},
		{"openrouter", modelprotocol.APIFormatOpenAIChatCompletions, modelprotocol.APIVariantOpenRouter, "https://openrouter.ai/api/v1", "/chat/completions", "bearer_token"},
		// OpenLLM speaks plain OpenAI Chat Completions and must not pick up the OpenRouter variant.
		{"openllm", modelprotocol.APIFormatOpenAIChatCompletions, modelprotocol.APIVariantDefault, "https://www.openllm.sh/v1", "/chat/completions", "bearer_token"},
		{"anthropic", modelprotocol.APIFormatAnthropicMessages, modelprotocol.APIVariantDefault, "https://api.anthropic.com/v1", "/messages", "api_key_header"},
	} {
		t.Run(tc.preset, func(t *testing.T) {
			t.Parallel()
			var (
				apiFormat   modelprotocol.APIFormat
				apiVariant  modelprotocol.APIVariant
				baseURL     string
				endpoint    string
				authKind    string
				authOptions json.RawMessage
			)
			if err := applyModelProviderPreset(tc.preset, &apiFormat, &apiVariant, &baseURL, &endpoint, &authKind, &authOptions); err != nil {
				t.Fatalf("apply preset: %v", err)
			}
			if apiFormat != tc.apiFormat || apiVariant != tc.apiVariant || baseURL != tc.baseURL ||
				endpoint != tc.endpointPath || authKind != tc.authKind {
				t.Fatalf("preset %q = format %q variant %q base %q path %q auth %q",
					tc.preset, apiFormat, apiVariant, baseURL, endpoint, authKind)
			}
			if err := validateModelProviderBaseURLNetworkPolicy(baseURL, false); err != nil {
				t.Fatalf("preset base URL rejected in normal mode: %v", err)
			}
		})
	}
}

func TestApplyModelProviderPresetRejectsUnknownPreset(t *testing.T) {
	t.Parallel()
	var (
		apiFormat   modelprotocol.APIFormat
		apiVariant  modelprotocol.APIVariant
		baseURL     string
		endpoint    string
		authKind    string
		authOptions json.RawMessage
	)
	if err := applyModelProviderPreset("openllm-local", &apiFormat, &apiVariant, &baseURL, &endpoint, &authKind, &authOptions); err == nil {
		t.Fatal("expected unknown preset to be rejected")
	}
}

func TestModelProviderNetworkPolicyBlocksLocalOpenLLMDaemonOutsideDevMode(t *testing.T) {
	t.Parallel()
	const daemon = "http://127.0.0.1:8787/v1"
	if err := validateModelProviderBaseURLNetworkPolicy(daemon, false); err == nil {
		t.Fatal("expected loopback base URL to be rejected in normal mode")
	}
	if err := validateModelProviderBaseURLNetworkPolicy(daemon, true); err != nil {
		t.Fatalf("loopback base URL rejected in insecure dev mode: %v", err)
	}
}
