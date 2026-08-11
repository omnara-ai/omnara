package modelprovider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
)

func discoveryProviderConfig(
	baseURL string,
	apiFormat modelprotocol.APIFormat,
	authKind, authOptions string,
) modelstore.ModelProviderConfigRecord {
	return modelstore.ModelProviderConfigRecord{
		APIFormat:   apiFormat,
		BaseURL:     baseURL,
		AuthKind:    authKind,
		AuthOptions: []byte(authOptions),
	}
}

func TestDiscoveryRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", discoveryMaxResponseSize+1)))
	}))
	defer server.Close()

	_, err := getDiscoveryEndpoint(
		context.Background(),
		server.Client(),
		server.URL,
		"/models",
		"models endpoint",
		route.Headers{},
		discoveryMaxResponseSize,
	)
	if err == nil || !strings.Contains(err.Error(), "response exceeds the byte limit") {
		t.Fatalf("discovery error = %v, want response limit error", err)
	}
}

func TestDiscoverModelsOpenAIBearer(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-good" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[
			{"id":"gpt-a","created":100},
			{"id":"gpt-b","created":300},
			{"id":"gpt-c","created":200},
			{"id":""},
			{"id":"whisper-1","created":900},
			{"id":"text-embedding-3-large","created":900},
			{"id":"gpt-4o-mini-tts","created":900},
			{"id":"gpt-4o-realtime-preview","created":900},
			{"id":"dall-e-3","created":900},
			{"id":"gpt-3.5-turbo-instruct","created":900}
		]}`))
	}))
	defer server.Close()

	config := discoveryProviderConfig(
		server.URL+"/v1",
		modelprotocol.APIFormatOpenAIResponses,
		modelstore.ModelProviderAuthKindBearerToken,
		`{}`,
	)
	models, err := DiscoverModels(context.Background(), config, "sk-good", true)
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	if len(models) != 3 || models[0].Slug != "gpt-b" || models[1].Slug != "gpt-c" || models[2].Slug != "gpt-a" {
		t.Fatalf("unexpected models: %+v", models)
	}

	_, err = DiscoverModels(context.Background(), config, "sk-bad", true)
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "invalid api key") {
		t.Fatalf("expected 401 error with provider message, got %v", err)
	}
}

func TestDiscoverModelsNormalizesMalformedLimitsAndDuplicateSlugs(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
			{"id":"gpt-duplicate","created":100,"context_length":4096},
			{"id":" gpt-duplicate ","created":200,"context_length":8192},
			{"id":"gpt-malformed","created":150,
			 "context_length":"unknown","max_output_tokens":{"unexpected":true}},
			{"id":"gpt-out-of-range","created":125,
			 "context_length":1,"max_output_tokens":2147483648}
		]}`))
	}))
	defer server.Close()

	config := discoveryProviderConfig(
		server.URL,
		modelprotocol.APIFormatOpenAIResponses,
		modelstore.ModelProviderAuthKindBearerToken,
		`{}`,
	)
	models, err := DiscoverModels(context.Background(), config, "sk", true)
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	if len(models) != 3 || models[0].Slug != "gpt-duplicate" ||
		models[1].Slug != "gpt-malformed" || models[2].Slug != "gpt-out-of-range" {
		t.Fatalf("normalized models = %+v", models)
	}
	if models[0].ContextWindowTokens == nil || *models[0].ContextWindowTokens != 8192 {
		t.Fatalf("newest duplicate was not retained: %+v", models[0])
	}
	for _, model := range models[1:] {
		if model.ContextWindowTokens != nil || model.MaxOutputTokens != nil {
			t.Fatalf("unusable optional limits were not omitted: %+v", model)
		}
	}
}

func TestDiscoverModelsAnthropicHeadersAndDisplayNames(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Api-Key") != "sk-ant" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid x-api-key"}}`))
			return
		}
		if r.Header.Get("Anthropic-Version") == "" {
			t.Error("anthropic discovery request missing Anthropic-Version header")
		}
		if r.URL.Query().Get("limit") != "1000" {
			t.Errorf("anthropic discovery limit = %q, want 1000", r.URL.Query().Get("limit"))
		}
		_, _ = w.Write([]byte(`{"data":[
			{"id":"claude-x","display_name":"Claude X","created_at":"2025-01-01T00:00:00Z"},
			{"id":"claude-y","display_name":"Claude Y","created_at":"2026-02-01T00:00:00Z",
			 "max_input_tokens":200000,"max_tokens":64000}
		],"has_more":false}`))
	}))
	defer server.Close()

	config := discoveryProviderConfig(
		server.URL+"/v1",
		modelprotocol.APIFormatAnthropicMessages,
		modelstore.ModelProviderAuthKindAPIKeyHeader,
		`{"header_name":"x-api-key"}`,
	)
	models, err := DiscoverModels(context.Background(), config, "sk-ant", true)
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	if len(models) != 2 || models[0].Slug != "claude-y" || models[1].Slug != "claude-x" ||
		models[1].DisplayName != "Claude X" {
		t.Fatalf("expected newest-first anthropic models: %+v", models)
	}
	if models[0].ContextWindowTokens == nil || *models[0].ContextWindowTokens != 200000 ||
		models[0].MaxOutputTokens == nil || *models[0].MaxOutputTokens != 64000 {
		t.Fatalf("anthropic token limits were not parsed: %+v", models[0])
	}
	if models[1].ContextWindowTokens != nil || models[1].MaxOutputTokens != nil {
		t.Fatalf("model without reported limits should stay unset: %+v", models[1])
	}
}

func TestDiscoverModelsOpenRouterCapabilityMetadata(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-or" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
			return
		}
		if r.URL.Path == "/api/v1/key" {
			_, _ = w.Write([]byte(`{"data":{"label":"sk-or-v1-..."}}`))
			return
		}
		if r.URL.Path != "/api/v1/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[
			{"id":"maker/old-tools","created":100,
			 "context_length":32768,
			 "architecture":{"output_modalities":["text"]},"supported_parameters":["tools","temperature"],
			 "top_provider":{"context_length":32768,"max_completion_tokens":2048}},
			{"id":"maker/new-tools","created":200,
			 "context_length":131072,
			 "architecture":{"output_modalities":["text"]},"supported_parameters":["tools"],
			 "top_provider":{"context_length":131072,"max_completion_tokens":8192}},
			{"id":"maker/equal-limits","created":150,
			 "context_length":8192,
			 "architecture":{"output_modalities":["text"]},"supported_parameters":["tools"],
			 "top_provider":{"context_length":8192,"max_completion_tokens":8192}},
			{"id":"maker/no-tools","created":300,
			 "architecture":{"output_modalities":["text"]},"supported_parameters":["temperature"]},
			{"id":"maker/image-gen","created":300,
			 "architecture":{"output_modalities":["image"]},"supported_parameters":["tools"]}
		]}`))
	}))
	defer server.Close()

	config := discoveryProviderConfig(
		server.URL+"/api/v1",
		modelprotocol.APIFormatOpenAIChatCompletions,
		modelstore.ModelProviderAuthKindBearerToken,
		`{}`,
	)
	config.APIVariant = modelprotocol.APIVariantOpenRouter
	models, err := DiscoverModels(context.Background(), config, "sk-or", true)
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	if len(models) != 3 || models[0].Slug != "maker/new-tools" ||
		models[1].Slug != "maker/equal-limits" || models[2].Slug != "maker/old-tools" {
		t.Fatalf("unexpected openrouter models: %+v", models)
	}
	if models[0].ContextWindowTokens == nil || *models[0].ContextWindowTokens != 131072 ||
		models[0].MaxOutputTokens == nil || *models[0].MaxOutputTokens != 8192 ||
		models[1].ContextWindowTokens == nil || *models[1].ContextWindowTokens != 8192 ||
		models[1].MaxOutputTokens != nil ||
		models[2].ContextWindowTokens == nil || *models[2].ContextWindowTokens != 32768 ||
		models[2].MaxOutputTokens == nil || *models[2].MaxOutputTokens != 2048 {
		t.Fatalf("unexpected OpenRouter token limits: %+v", models)
	}

	if _, err := DiscoverModels(context.Background(), config, "sk-bad", true); err == nil ||
		!strings.Contains(err.Error(), "OpenRouter key endpoint returned status 401") {
		t.Fatalf("expected OpenRouter key validation error, got %v", err)
	}
}

func TestDiscoverModelsRejectsUnrecognizedResponse(t *testing.T) {
	t.Parallel()
	testCases := map[string]string{
		"non-JSON":     `<html>not json</html>`,
		"missing data": `{"models":[]}`,
		"null data":    `{"data":null}`,
	}
	for name, response := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(response))
			}))
			defer server.Close()

			config := discoveryProviderConfig(
				server.URL+"/v1",
				modelprotocol.APIFormatOpenAIChatCompletions,
				modelstore.ModelProviderAuthKindBearerToken,
				`{}`,
			)
			if _, err := DiscoverModels(context.Background(), config, "sk", true); err == nil {
				t.Fatalf("expected error for response %s", response)
			}
		})
	}
}

func TestDiscoverModelsAcceptsEmptyDataArray(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	config := discoveryProviderConfig(
		server.URL+"/v1",
		modelprotocol.APIFormatOpenAIChatCompletions,
		modelstore.ModelProviderAuthKindBearerToken,
		`{}`,
	)
	models, err := DiscoverModels(context.Background(), config, "sk", true)
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	if models == nil || len(models) != 0 {
		t.Fatalf("models = %#v, want non-nil empty slice", models)
	}
}

func TestDiscoverModelsBlocksLoopbackWithoutInsecureDev(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	config := discoveryProviderConfig(
		server.URL+"/v1",
		modelprotocol.APIFormatOpenAIResponses,
		modelstore.ModelProviderAuthKindBearerToken,
		`{}`,
	)
	if _, err := DiscoverModels(context.Background(), config, "sk", false); err == nil {
		t.Fatal("expected loopback models endpoint to be blocked outside insecure dev mode")
	}
}
