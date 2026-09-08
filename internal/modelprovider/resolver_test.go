package modelprovider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/metrics"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/stretchr/testify/require"
)

func TestCapabilitiesForRevisionMapsRuntimePolicyFields(t *testing.T) {
	record := modelstore.ConfiguredModelRevisionRecord{
		ContextWindowTokens:    200000,
		MaxOutputTokens:        new(64000),
		DefaultMaxOutputTokens: intPtr(32000),
		DefaultCacheRetention:  modelstore.ModelCacheRetentionShort,
		SupportsTools:          true,
		SupportsReasoning:      true,
		DefaultReasoningEffort: "high",
		SupportedReasoningEfforts: []string{
			"low",
			"medium",
			"high",
		},
		InputModalities:  []string{"text", "image"},
		OutputModalities: []string{"text"},
	}

	got := capabilitiesForRevision(record)
	if got.ContextWindowTokens != 200000 ||
		(got.MaxOutputTokens == nil || *got.MaxOutputTokens != 64000) ||
		got.DefaultMaxOutputTokens != 32000 ||
		got.DefaultCacheRetention != model.CacheRetentionShort ||
		got.SupportsTools == nil || !*got.SupportsTools ||
		!got.SupportsReasoning ||
		got.DefaultReasoningEffort != "high" ||
		len(got.SupportedReasoningEfforts) != 3 || got.SupportedReasoningEfforts[2] != "high" ||
		len(got.InputModalities) != 2 || got.InputModalities[1] != "image" ||
		len(got.OutputModalities) != 1 || got.OutputModalities[0] != "text" {
		t.Fatalf("capabilities = %+v", got)
	}
	if policy := model.RequestPolicyFromCapabilities(got); policy.MaxOutputTokens != 32000 {
		t.Fatalf("request policy max_output_tokens = %d, want configured default 32000", policy.MaxOutputTokens)
	}
	*got.MaxOutputTokens = 1
	if *record.MaxOutputTokens != 64000 {
		t.Fatal("capability mutation changed source revision")
	}
}

func TestCacheRetentionForModelDefaultsUnknownToNone(t *testing.T) {
	if got := cacheRetentionForModel("future-retention"); got != model.CacheRetentionNone {
		t.Fatalf("cache retention = %q, want %q", got, model.CacheRetentionNone)
	}
}

func TestCacheRetentionForModelKeepsEmptyUnset(t *testing.T) {
	if got := cacheRetentionForModel(""); got != model.CacheRetentionUnset {
		t.Fatalf("cache retention = %q, want unset", got)
	}
}

func TestRouteAuthForProviderConfig(t *testing.T) {
	bearer, err := routeAuthForProviderConfig(modelstore.ModelProviderConfigRecord{
		AuthKind:    modelstore.ModelProviderAuthKindBearerToken,
		AuthOptions: json.RawMessage(`{}`),
	}, "sk-test")
	if err != nil {
		t.Fatalf("bearer route auth: %v", err)
	}
	bearerAuth, ok := bearer.(route.BearerToken)
	if !ok || bearerAuth.Token != "sk-test" {
		t.Fatalf("bearer route auth = %#v", bearer)
	}

	header, err := routeAuthForProviderConfig(modelstore.ModelProviderConfigRecord{
		AuthKind:    modelstore.ModelProviderAuthKindAPIKeyHeader,
		AuthOptions: json.RawMessage(`{"header_name":"api-key"}`),
	}, "sk-test")
	if err != nil {
		t.Fatalf("header route auth: %v", err)
	}
	headerAuth, ok := header.(route.HeaderAuth)
	if !ok || headerAuth.Header != "api-key" || headerAuth.Value != "sk-test" {
		t.Fatalf("header route auth = %#v", header)
	}

	if _, err := routeAuthForProviderConfig(modelstore.ModelProviderConfigRecord{
		AuthKind:    modelstore.ModelProviderAuthKindAPIKeyHeader,
		AuthOptions: json.RawMessage(`{"header_name":"Content-Type"}`),
	}, "sk-test"); err == nil {
		t.Fatal("reserved auth header should fail")
	}
}

func TestRouteHeadersForProviderConfig(t *testing.T) {
	resolver := Resolver{
		OpenRouterAttribution: OpenRouterAttribution{
			SiteURL:       "https://omnara.com",
			AppTitle:      "Omnara",
			AppCategories: []string{"cloud-agent"},
		},
	}
	headers := resolver.routeHeadersForProviderConfig(modelstore.ModelProviderConfigRecord{
		APIFormat:  modelprotocol.APIFormatOpenAIChatCompletions,
		APIVariant: modelprotocol.APIVariantDefault,
	})
	if len(headers) != 0 {
		t.Fatalf("default route headers = %+v, want none", headers)
	}

	headers = resolver.routeHeadersForProviderConfig(modelstore.ModelProviderConfigRecord{
		APIFormat:  modelprotocol.APIFormatOpenAIChatCompletions,
		APIVariant: modelprotocol.APIVariantOpenRouter,
	})
	if headers["HTTP-Referer"] != "https://omnara.com" ||
		headers["X-OpenRouter-Title"] != "Omnara" ||
		headers["X-OpenRouter-Categories"] != "cloud-agent" {
		t.Fatalf("openrouter route headers = %+v", headers)
	}
}

func TestHTTPClientForProviderConfigUsesEndpointTimeout(t *testing.T) {
	client, err := (Resolver{}).httpClientForProviderConfig(modelstore.ModelProviderConfigRecord{
		APIFormat:        modelprotocol.APIFormatOpenAIResponses,
		RequestTimeoutMS: 2500,
	})
	if err != nil {
		t.Fatalf("http client for provider config: %v", err)
	}
	if client.Timeout != 2500*time.Millisecond {
		t.Fatalf("timeout = %s, want 2.5s", client.Timeout)
	}
}

func TestHTTPClientForProviderConfigReusesGuardedTransport(t *testing.T) {
	first, err := (Resolver{}).httpClientForProviderConfig(modelstore.ModelProviderConfigRecord{
		APIFormat:        modelprotocol.APIFormatOpenAIResponses,
		RequestTimeoutMS: 2500,
	})
	if err != nil {
		t.Fatalf("first http client for provider config: %v", err)
	}
	second, err := (Resolver{}).httpClientForProviderConfig(modelstore.ModelProviderConfigRecord{
		APIFormat:        modelprotocol.APIFormatAnthropicMessages,
		RequestTimeoutMS: 5000,
	})
	if err != nil {
		t.Fatalf("second http client for provider config: %v", err)
	}
	if first.Transport == nil || first.Transport != second.Transport {
		t.Fatalf(
			"transports not reused: first=%T/%p second=%T/%p",
			first.Transport,
			first.Transport,
			second.Transport,
			second.Transport,
		)
	}
	transport, ok := first.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", first.Transport)
	}
	if transport.ResponseHeaderTimeout != 0 {
		t.Fatalf(
			"response header timeout = %s, want disabled for model generation budget",
			transport.ResponseHeaderTimeout,
		)
	}
	if first.Timeout != 2500*time.Millisecond || second.Timeout != 5*time.Second {
		t.Fatalf("timeouts = %s/%s, want 2.5s/5s", first.Timeout, second.Timeout)
	}
}

func TestHTTPClientForProviderConfigBlocksPrivateAddresses(t *testing.T) {
	client, err := (Resolver{}).httpClientForProviderConfig(modelstore.ModelProviderConfigRecord{
		APIFormat: modelprotocol.APIFormatOpenAIResponses,
	})
	if err != nil {
		t.Fatalf("http client for provider config: %v", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:1/responses", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	_, err = client.Do(req)
	if !errors.Is(err, ssrf.ErrBlockedAddress) {
		t.Fatalf("private address error = %v, want ssrf.ErrBlockedAddress", err)
	}
}

func TestHTTPClientForProviderConfigAllowsLoopbackInDevMode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client, err := (Resolver{AllowLoopback: true}).httpClientForProviderConfig(modelstore.ModelProviderConfigRecord{
		APIFormat: modelprotocol.APIFormatOpenAIResponses,
	})
	if err != nil {
		t.Fatalf("http client for provider config: %v", err)
	}
	resp, err := client.Get(server.URL + "/responses")
	if err != nil {
		t.Fatalf("loopback request in dev mode: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
}

func TestHTTPClientForProviderConfigDefersRedirectHandlingToRoute(t *testing.T) {
	tests := []struct {
		name     string
		resolver Resolver
	}{
		{name: "plain", resolver: Resolver{AllowLoopback: true}},
		{
			name: "observed",
			resolver: Resolver{
				AllowLoopback: true,
				HTTPRecorder:  metrics.NewHTTPClientRecorder(metrics.New(), metrics.SubsystemHTTPClient),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := tt.resolver.httpClientForProviderConfig(modelstore.ModelProviderConfigRecord{
				APIFormat:    modelprotocol.APIFormatOpenAIResponses,
				EndpointPath: "/custom-responses",
			})
			if err != nil {
				t.Fatalf("http client for provider config: %v", err)
			}
			if client.CheckRedirect != nil {
				t.Fatal("resolver installed redirect policy; route owns redirect handling")
			}
		})
	}
}

func intPtr(value int) *int {
	return &value
}

func TestEffectiveRevisionReachesAnthropicWire(t *testing.T) {
	for _, narrowed := range []bool{false, true} {
		name := "unknown capacity with project allowance"
		if narrowed {
			name = "inherited capacity with project and agent narrowing"
		}
		t.Run(name, func(t *testing.T) {
			revision := modelstore.ConfiguredModelRevisionRecord{ContextWindowTokens: 128000}
			grant := modelstore.ProjectModelGrantRecord{DefaultMaxOutputTokens: new(32000)}
			overrides := agentconfig.ModelOverrides{}
			if narrowed {
				revision.MaxOutputTokens = new(64000)
				grant = modelstore.ProjectModelGrantRecord{ContextWindowTokens: new(48000)}
				overrides.ContextWindowTokens = new(32000)
			}
			effective, err := modelstore.EffectiveConfiguredModelRevisionForProjectGrant(
				modelprotocol.APIFormatAnthropicMessages, revision, grant,
			)
			require.NoError(t, err)
			effective, err = modelstore.EffectiveConfiguredModelRevisionForAgentOptions(
				modelprotocol.APIFormatAnthropicMessages, effective, overrides,
			)
			require.NoError(t, err)
			caps := capabilitiesForRevision(effective)
			client := anthropicmessages.Client{
				EndpointPath: "/messages", ProviderModelSlug: "custom-model", ModelCapabilities: caps,
			}
			prepared, err := model.PrepareForSend(t.Context(), client, model.PrepareForSendInput{
				Context: modelcontext.Bundle{Messages: []modelcontext.Message{{
					ID: "input", Sequence: 1, Role: modelprotocol.RoleUser,
					Content: json.RawMessage(`[{"type":"text","text":"hello"}]`),
				}}},
				Policy: model.RequestPolicyFromCapabilities(caps), ErrorSource: "test",
			})
			require.NoError(t, err)
			var wire struct {
				MaxTokens int `json:"max_tokens"`
			}
			require.NoError(t, json.Unmarshal(prepared.Body, &wire))
			want := 32000
			if narrowed {
				if caps.ContextWindowTokens != 32000 {
					t.Fatalf("context = %d, want 32000", caps.ContextWindowTokens)
				}
				if diff := cmp.Diff(new(64000), caps.MaxOutputTokens); diff != "" {
					t.Fatalf("inherited capacity (-want +got):\n%s", diff)
				}
				want = 32000 - modelcontext.DefaultSafetyMarginTokens(32000) - prepared.InputTokenEstimate
			} else if caps.MaxOutputTokens != nil {
				t.Fatalf("capacity = %d, want unknown", *caps.MaxOutputTokens)
			}
			if !prepared.InputBudget.Fits() || prepared.MaxOutputTokens != want || wire.MaxTokens != want {
				t.Fatalf("budget=%+v recorded=%d wire=%d, want fitting allowance %d",
					prepared.InputBudget, prepared.MaxOutputTokens, wire.MaxTokens, want)
			}
		})
	}
}
