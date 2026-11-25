package openaichatcompletions

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func TestOpenRouterGenerationUsageLookup(t *testing.T) {
	for _, test := range []struct {
		name          string
		status        int
		contentLength string
		body          string
		want          model.GenerationUsage
		wantErr       error
		wantKind      modelprotocol.ErrorKind
	}{
		{
			name:   "canceled generation without cache usage keeps its input split",
			status: http.StatusOK,
			body: `{"data":{"id":"gen-1","total_cost":0.012806,"is_byok":false,"cancelled":true,"cache_discount":null,
				"model":"openai/gpt-4o-mini-2024-07-18","service_tier":"flex",
				"native_tokens_prompt":5253,"native_tokens_cached":0,
				"native_tokens_completion":257,"native_tokens_reasoning":111}}`,
			want: model.GenerationUsage{
				Usage: modelenvelope.Usage{
					InputTokens: 5253, UncachedInputTokens: 5253, OutputTokens: 257, ReasoningTokens: 111,
				},
				ProviderReportedCostUSD: "0.012806",
			},
		},
		{
			name:   "a generation that finished upstream leaves the input split unknown",
			status: http.StatusOK,
			body: `{"data":{"total_cost":0.0042,"is_byok":false,"cancelled":false,"cache_discount":null,
				"native_tokens_prompt":58,"native_tokens_cached":0,"native_tokens_completion":10}}`,
			want: model.GenerationUsage{
				Usage:                   modelenvelope.Usage{OutputTokens: 10, Incomplete: true},
				ProviderReportedCostUSD: "0.0042",
			},
		},
		{
			name:   "a cache discount, even zero, leaves the input split unknown",
			status: http.StatusOK,
			body: `{"data":{"total_cost":0.0155,"is_byok":false,"cancelled":true,"cache_discount":0,
				"native_tokens_prompt":5000,"native_tokens_cached":0,"native_tokens_completion":100}}`,
			want: model.GenerationUsage{
				Usage:                   modelenvelope.Usage{OutputTokens: 100, Incomplete: true},
				ProviderReportedCostUSD: "0.0155",
			},
		},
		{
			name:   "a missing cache discount leaves the input split unknown",
			status: http.StatusOK,
			body: `{"data":{"total_cost":0.0042,"is_byok":false,"cancelled":true,
				"native_tokens_prompt":58,"native_tokens_cached":0,"native_tokens_completion":10}}`,
			want: model.GenerationUsage{
				Usage:                   modelenvelope.Usage{OutputTokens: 10, Incomplete: true},
				ProviderReportedCostUSD: "0.0042",
			},
		},
		{
			name:   "reported cached tokens leave the input split unknown",
			status: http.StatusOK,
			body: `{"data":{"total_cost":0.012806,"is_byok":false,"cancelled":true,"cache_discount":null,
				"native_tokens_prompt":5253,"native_tokens_cached":1000,
				"native_tokens_completion":257,"native_tokens_reasoning":111}}`,
			want: model.GenerationUsage{
				Usage:                   modelenvelope.Usage{OutputTokens: 257, ReasoningTokens: 111, Incomplete: true},
				ProviderReportedCostUSD: "0.012806",
			},
		},
		{
			name:   "a missing cached-token count leaves the input split unknown",
			status: http.StatusOK,
			body: `{"data":{"total_cost":0.0042,"is_byok":false,"cancelled":true,"cache_discount":null,
				"native_tokens_prompt":58,"native_tokens_cached":null,"native_tokens_completion":10}}`,
			want: model.GenerationUsage{
				Usage:                   modelenvelope.Usage{OutputTokens: 10, Incomplete: true},
				ProviderReportedCostUSD: "0.0042",
			},
		},
		{
			name:   "explicit zero cost is final",
			status: http.StatusOK,
			body: `{"data":{"total_cost":0,"is_byok":false,"cancelled":true,"cache_discount":null,"service_tier":"default",
				"native_tokens_prompt":58,
				"native_tokens_completion":0,"native_tokens_reasoning":0,"native_tokens_cached":0}}`,
			want: model.GenerationUsage{
				Usage:                   modelenvelope.Usage{InputTokens: 58, UncachedInputTokens: 58},
				ProviderReportedCostUSD: "0",
			},
		},
		{
			name:   "BYOK generation adds the provider charge to the OpenRouter fee",
			status: http.StatusOK,
			body: `{"data":{"total_cost":0.0011,"is_byok":true,"upstream_inference_cost":0.02228638,"cancelled":true,
				"cache_discount":null,"native_tokens_prompt":58,"native_tokens_cached":0,"native_tokens_completion":10}}`,
			want: model.GenerationUsage{
				Usage:                   modelenvelope.Usage{InputTokens: 58, UncachedInputTokens: 58, OutputTokens: 10},
				ProviderReportedCostUSD: "0.02338638",
			},
		},
		{
			name:   "BYOK generation with a waived fee costs the provider charge",
			status: http.StatusOK,
			body: `{"data":{"total_cost":0,"is_byok":true,"upstream_inference_cost":0.02228638,"cancelled":true,
				"cache_discount":null,"native_tokens_prompt":58,"native_tokens_cached":0,"native_tokens_completion":0}}`,
			want: model.GenerationUsage{
				Usage:                   modelenvelope.Usage{InputTokens: 58, UncachedInputTokens: 58},
				ProviderReportedCostUSD: "0.02228638",
			},
		},
		{
			name:   "a missing token total marks usage incomplete",
			status: http.StatusOK,
			body: `{"data":{"total_cost":0.0042,"is_byok":false,"cancelled":true,"cache_discount":null,"native_tokens_prompt":58,
				"native_tokens_cached":0,"native_tokens_completion":null}}`,
			want: model.GenerationUsage{
				Usage:                   modelenvelope.Usage{InputTokens: 58, UncachedInputTokens: 58, Incomplete: true},
				ProviderReportedCostUSD: "0.0042",
			},
		},
		{
			name:    "BYOK generation without a provider charge stays unresolved",
			status:  http.StatusOK,
			body:    `{"data":{"total_cost":0,"is_byok":true,"upstream_inference_cost":null,"native_tokens_prompt":58}}`,
			wantErr: model.ErrGenerationUsageNotFound,
		},
		{
			name:    "unknown billing source stays unresolved",
			status:  http.StatusOK,
			body:    `{"data":{"total_cost":0.95,"native_tokens_prompt":58}}`,
			wantErr: model.ErrGenerationUsageNotFound,
		},
		{
			name:    "missing cost stays unresolved",
			status:  http.StatusOK,
			body:    `{"data":{"total_cost":null,"is_byok":false,"native_tokens_prompt":58}}`,
			wantErr: model.ErrGenerationUsageNotFound,
		},
		{
			name:    "generation not recorded yet",
			status:  http.StatusNotFound,
			body:    `{"error":{"message":"Generation gen-1 not found","code":404}}`,
			wantErr: model.ErrGenerationUsageNotFound,
		},
		{
			name:          "interrupted response body",
			status:        http.StatusOK,
			contentLength: "500",
			body:          `{"data":{"total_cost":0.012806,`,
			wantKind:      model.ErrorKindTransient,
		},
		{
			name:     "rejected credential",
			status:   http.StatusUnauthorized,
			body:     `{"error":{"message":"User not found.","code":401}}`,
			wantKind: model.ErrorKindAuth,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/generation" || r.URL.Query().Get("id") != "gen-1" {
					t.Errorf("request = %s %s, want GET /generation?id=gen-1", r.Method, r.URL)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
					t.Errorf("Authorization = %q", got)
				}
				if test.contentLength != "" {
					w.Header().Set("Content-Length", test.contentLength)
				}
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			client := testRespondClient(server)
			client.APIVariant = modelprotocol.APIVariantOpenRouter
			got, err := client.LookupGenerationUsage(t.Context(), "gen-1")
			if test.wantKind != "" {
				providerErr, classified := model.ClassifyError(err)
				if !classified || providerErr.Kind != test.wantKind {
					t.Fatalf("error = %v, want %s provider error", err, test.wantKind)
				}
				return
			}
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("usage = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestOpenRouterGenerationLookupReportsRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	client := testRespondClient(server)
	client.APIVariant = modelprotocol.APIVariantOpenRouter
	_, err := client.LookupGenerationUsage(t.Context(), "gen-1")
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.StatusCode != http.StatusTooManyRequests || providerErr.RetryAfter == nil ||
		providerErr.RetryAfter.DeltaSeconds == nil || *providerErr.RetryAfter.DeltaSeconds != 60 {
		t.Fatalf("rate-limited lookup error = %+v", providerErr)
	}
}
