package modelprice

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	genai "github.com/pydantic/genai-prices/packages/go"
	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

const (
	openAIURL    = "https://api.openai.com/v1"
	anthropicURL = "https://api.anthropic.com/v1"
	mantleURL    = "https://bedrock-mantle.us-east-1.api.aws/anthropic/v1"
	runtimeURL   = "https://bedrock-runtime.us-east-1.amazonaws.com"
	geminiURL    = "https://generativelanguage.googleapis.com/v1beta/openai"
	groqURL      = "https://api.groq.com/openai/v1"
	mistralURL   = "https://api.mistral.ai/v1"
	openRouter   = "https://openrouter.ai/api/v1"
)

var callTime = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func intPointer(value int) *int {
	return &value
}

func tokens(input, output int) modelenvelope.Usage {
	return modelenvelope.Usage{InputTokens: input, UncachedInputTokens: input, OutputTokens: output}
}

func anthropicCacheUsage() modelenvelope.Usage {
	return modelenvelope.Usage{
		InputTokens:         10_000,
		UncachedInputTokens: 4_000,
		CacheReadTokens:     2_000,
		CacheWriteTokens:    4_000,
		OutputTokens:        1_000,
	}
}

func builtin(t *testing.T) *genai.Calculator {
	t.Helper()
	calculator, err := genai.NewCalculator()
	require.NoError(t, err)
	return calculator
}

func TestEstimatePricesFromBuiltInData(t *testing.T) {
	calculator := builtin(t)
	tests := []struct {
		name       string
		input      Input
		cost       modelenvelope.ProviderReportedCostUSD
		model      string
		assumption string
		reason     string
	}{
		{
			name: "openai served model",
			input: Input{BaseURL: openAIURL, ServedModel: "gpt-4o-2024-08-06", ConfiguredModel: "gpt-4o",
				Usage: tokens(1_000, 1_000)},
			cost:  "0.0125",
			model: "gpt-4o",
		},
		{
			name:  "configured model when the response has no model",
			input: Input{BaseURL: openAIURL, ConfiguredModel: "gpt-4o", Usage: tokens(1_000, 1_000)},
			cost:  "0.0125",
			model: "gpt-4o",
		},
		{
			name:  "last path segment of the configured model",
			input: Input{BaseURL: openAIURL, ConfiguredModel: "team/gpt-4o", Usage: tokens(1_000, 1_000)},
			cost:  "0.0125",
			model: "gpt-4o",
		},
		{
			name: "openai flex tier",
			input: Input{BaseURL: openAIURL, ServedModel: "gpt-5", Usage: tokens(1_000, 1_000),
				Metadata: modelenvelope.ProviderMetadata{OpenAI: modelenvelope.OpenAIMetadata{ServiceTier: "flex"}}},
			cost:  "0.005625",
			model: "gpt-5",
		},
		{
			name: "openai priority tier",
			input: Input{BaseURL: openAIURL, ServedModel: "gpt-5", Usage: tokens(1_000, 1_000),
				Metadata: modelenvelope.ProviderMetadata{OpenAI: modelenvelope.OpenAIMetadata{ServiceTier: "priority"}}},
			cost:  "0.0225",
			model: "gpt-5",
		},
		{
			name: "tier without its own price falls back to the standard price",
			input: Input{BaseURL: openAIURL, ServedModel: "gpt-4o", Usage: tokens(1_000, 1_000),
				Metadata: modelenvelope.ProviderMetadata{OpenAI: modelenvelope.OpenAIMetadata{ServiceTier: "flex"}}},
			cost:  "0.0125",
			model: "gpt-4o",
		},
		{
			name: "openai default tier",
			input: Input{BaseURL: openAIURL, ServedModel: "gpt-5", Usage: tokens(1_000, 1_000),
				Metadata: modelenvelope.ProviderMetadata{OpenAI: modelenvelope.OpenAIMetadata{ServiceTier: "default"}}},
			cost:  "0.01125",
			model: "gpt-5",
		},
		{
			name: "groq on_demand tier",
			input: Input{BaseURL: groqURL, ServedModel: "llama-3.3-70b-versatile", Usage: tokens(1_000, 1_000),
				Metadata: modelenvelope.ProviderMetadata{OpenAI: modelenvelope.OpenAIMetadata{ServiceTier: "on_demand"}}},
			cost:  "0.00138",
			model: "llama-3.3-70b-versatile",
		},
		{
			name: "anthropic mixed 5-minute and 1-hour writes",
			input: Input{BaseURL: anthropicURL, APIFormat: modelprotocol.APIFormatAnthropicMessages,
				ServedModel: "claude-sonnet-4-5-20250929", Usage: anthropicCacheUsage(),
				Metadata: modelenvelope.ProviderMetadata{Anthropic: modelenvelope.AnthropicMetadata{
					CacheWrite1hInputTokens: intPointer(1_000),
				}}},
			cost:  "0.04485",
			model: "claude-sonnet-4-5",
		},
		{
			name: "anthropic explicit zero 1-hour writes",
			input: Input{BaseURL: anthropicURL, APIFormat: modelprotocol.APIFormatAnthropicMessages,
				ServedModel: "claude-sonnet-4-5-20250929", Usage: anthropicCacheUsage(),
				Metadata: modelenvelope.ProviderMetadata{Anthropic: modelenvelope.AnthropicMetadata{
					CacheWrite1hInputTokens: intPointer(0),
				}}},
			cost:  "0.0426",
			model: "claude-sonnet-4-5",
		},
		{
			name: "anthropic missing split prices writes at the 1-hour rate",
			input: Input{BaseURL: anthropicURL, APIFormat: modelprotocol.APIFormatAnthropicMessages,
				ServedModel: "claude-sonnet-4-5-20250929", Usage: anthropicCacheUsage()},
			cost:       "0.0516",
			model:      "claude-sonnet-4-5",
			assumption: "cache_write_split_missing_priced_as_1h",
		},
		{
			name: "haiku 5.5 below the long-context threshold",
			input: Input{BaseURL: anthropicURL, APIFormat: modelprotocol.APIFormatAnthropicMessages,
				ServedModel: "claude-haiku-5-5", Usage: tokens(100_000, 1_000)},
			cost:  "0.0105",
			model: "claude-haiku-5-5",
		},
		{
			name: "haiku 5.5 above the long-context threshold",
			input: Input{BaseURL: anthropicURL, APIFormat: modelprotocol.APIFormatAnthropicMessages,
				ServedModel: "claude-haiku-5-5", Usage: tokens(100_001, 1_000)},
			cost:  "0.0525005",
			model: "claude-haiku-5-5",
		},
		{
			name: "bedrock global configured ID prices at the global rate",
			input: Input{BaseURL: runtimeURL, APIVariant: modelprotocol.APIVariantBedrock,
				APIFormat:       modelprotocol.APIFormatAnthropicMessages,
				ConfiguredModel: "global.anthropic.claude-haiku-4-5-20251001-v1:0",
				ServedModel:     "claude-haiku-4-5-20251001", Usage: tokens(1_000, 1_000)},
			cost:  "0.006",
			model: "global.anthropic.claude-haiku-4-5-20251001-v1:0",
		},
		{
			name: "bedrock mantle bare ID falls back to the served name at the regional rate",
			input: Input{BaseURL: mantleURL, APIVariant: modelprotocol.APIVariantBedrock,
				APIFormat:       modelprotocol.APIFormatAnthropicMessages,
				ConfiguredModel: "anthropic.claude-haiku-4-5",
				ServedModel:     "claude-haiku-4-5-20251001", Usage: tokens(1_000, 1_000)},
			cost:  "0.0066",
			model: "regional.anthropic.claude-haiku-4-5-20251001-v1:0",
		},
		{
			name: "bedrock missing split prices writes at the 1-hour rate",
			input: Input{BaseURL: mantleURL, APIVariant: modelprotocol.APIVariantBedrock,
				APIFormat: modelprotocol.APIFormatAnthropicMessages, ServedModel: "claude-sonnet-4-5-20250929",
				Usage: anthropicCacheUsage()},
			cost:       "0.05676",
			model:      "regional.anthropic.claude-sonnet-4-5-20250929-v1:0",
			assumption: "cache_write_split_missing_priced_as_1h",
		},
		{
			name:  "gemini through the OpenAI-compatible endpoint",
			input: Input{BaseURL: geminiURL, ServedModel: "gemini-2.5-flash", Usage: tokens(1_000, 1_000)},
			cost:  "0.0028",
			model: "gemini-2.5-flash",
		},
		{
			name:  "openrouter model without a reported cost",
			input: Input{BaseURL: openRouter, ServedModel: "openai/gpt-4o", Usage: tokens(1_000, 1_000)},
			cost:  "0.0125",
			model: "openai/gpt-4o",
		},
		{
			name:  "concrete mistral model",
			input: Input{BaseURL: mistralURL, ServedModel: "mistral-large-2512", Usage: tokens(1_000, 1_000)},
			cost:  "0.002",
			model: "mistral-large-2512",
		},
		{
			name:   "known-bad mistral alias",
			input:  Input{BaseURL: mistralURL, ServedModel: "mistral-large-latest", Usage: tokens(1_000, 1_000)},
			reason: "known_bad_alias",
		},
		{
			name:  "other mistral -latest aliases still price",
			input: Input{BaseURL: mistralURL, ServedModel: "mistral-small-latest", Usage: tokens(1_000, 1_000)},
			cost:  "0.00075",
			model: "mistral-small-latest",
		},
		{
			name:   "unknown provider",
			input:  Input{BaseURL: "https://llm.internal.example/v1", ServedModel: "gpt-4o", Usage: tokens(1_000, 1_000)},
			reason: "unknown_provider",
		},
		{
			name:   "unknown model",
			input:  Input{BaseURL: openAIURL, ServedModel: "not-a-model", Usage: tokens(1_000, 1_000)},
			reason: "unknown_model",
		},
		{
			name: "incomplete usage",
			input: Input{BaseURL: openAIURL, ServedModel: "gpt-4o", Usage: modelenvelope.Usage{
				InputTokens: 1_000, UncachedInputTokens: 1_000, OutputTokens: 1, Incomplete: true,
			}},
			reason: "incomplete_usage",
		},
		{
			name:   "no usage",
			input:  Input{BaseURL: openAIURL, ServedModel: "gpt-4o"},
			reason: "no_usage",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.input.CreatedAt = callTime
			result := estimate(calculator, test.input)
			require.Equal(t, test.reason, result.Reason)
			require.Equal(t, test.cost, result.CostUSD)
			require.Equal(t, test.model, result.Model)
			require.Equal(t, test.assumption, result.Assumption)
		})
	}
}

type panickingCalculator struct{}

func (panickingCalculator) Calculate(genai.PriceRequest) (genai.PriceCalculation, error) {
	panic("boom")
}

func TestEstimateRecoversFromLibraryPanics(t *testing.T) {
	result := estimate(panickingCalculator{}, Input{BaseURL: openAIURL, ServedModel: "gpt-4o", Usage: tokens(1, 1)})
	require.Equal(t, Result{Reason: "price_lookup_panic"}, result)
}

func usdPerMTok(price float64) map[string]any {
	return map[string]any{"input_mtok": price, "output_mtok": price}
}

func priceData(t *testing.T, models int, prices map[string]any) []byte {
	t.Helper()
	entries := make([]map[string]any, 0, models)
	for index := range models {
		id := "gpt-4o"
		if index > 0 {
			id = fmt.Sprintf("model-%d", index)
		}
		entries = append(entries, map[string]any{"id": id, "match": map[string]string{"equals": id}, "prices": prices})
	}
	data, err := json.Marshal([]map[string]any{{
		"id": "openai", "name": "OpenAI", "api_pattern": "https://api\\.openai\\.com", "models": entries,
	}})
	require.NoError(t, err)
	return data
}

func priceServer(t *testing.T, status int, body []byte) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func gpt4oCost(t *testing.T, pricer *Pricer) modelenvelope.ProviderReportedCostUSD {
	t.Helper()
	return pricer.Estimate(context.Background(), Input{
		BaseURL: openAIURL, ServedModel: "gpt-4o", Usage: tokens(1_000_000, 1_000_000), CreatedAt: callTime,
	})
}

func fetchPriceData(t *testing.T, status int, body []byte) ([]byte, error) {
	t.Helper()
	server, _ := priceServer(t, status, body)
	return Source{URL: server.URL, Client: server.Client()}.fetch(context.Background())
}

func TestLoadSwapsInValidPriceData(t *testing.T) {
	data, err := fetchPriceData(t, http.StatusOK, priceData(t, minPricedModels, usdPerMTok(1)))
	require.NoError(t, err)
	version, pricedModels, err := validate(data, 0)
	require.NoError(t, err)
	require.Equal(t, minPricedModels, pricedModels)

	pricer, err := New()
	require.NoError(t, err)
	require.Equal(t, builtinSource, pricer.Version())
	require.Equal(t, modelenvelope.ProviderReportedCostUSD("12.5"), gpt4oCost(t, pricer))
	require.NoError(t, pricer.Load(data))
	require.Equal(t, modelenvelope.ProviderReportedCostUSD("2"), gpt4oCost(t, pricer))
	require.Equal(t, version, pricer.Version())
}

func TestUnusablePriceDataIsRejected(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   []byte
	}{
		{name: "empty provider list", status: http.StatusOK, body: []byte(`[]`)},
		{name: "models without prices", status: http.StatusOK, body: priceData(t, minPricedModels, map[string]any{})},
		{name: "price keys the library doesn't know", status: http.StatusOK,
			body: priceData(t, minPricedModels, map[string]any{"future_mtok": 1})},
		{name: "zero prices", status: http.StatusOK, body: priceData(t, minPricedModels, usdPerMTok(0))},
		{name: "fewer than the minimum priced models", status: http.StatusOK,
			body: priceData(t, minPricedModels-1, usdPerMTok(1))},
		{name: "invalid JSON", status: http.StatusOK, body: []byte(`{"not":`)},
		{name: "server error", status: http.StatusInternalServerError, body: priceData(t, minPricedModels, usdPerMTok(1))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, err := fetchPriceData(t, test.status, test.body)
			if err == nil {
				_, _, err = validate(data, 0)
				pricer, newErr := New()
				require.NoError(t, newErr)
				require.Error(t, pricer.Load(data))
				require.Equal(t, builtinSource, pricer.Version())
				require.Equal(t, modelenvelope.ProviderReportedCostUSD("12.5"), gpt4oCost(t, pricer))
			}
			require.Error(t, err)
		})
	}
}

func TestRefreshStoresOnlyNewValidPriceData(t *testing.T) {
	ctx := context.Background()
	server, _ := priceServer(t, http.StatusOK, priceData(t, minPricedModels, usdPerMTok(1)))
	source := Source{URL: server.URL, Client: server.Client()}
	stored, storedData, saves := "", []byte(nil), 0
	current := func(context.Context) (string, []byte, error) { return stored, storedData, nil }
	save := func(_ context.Context, data []byte, version, expected string) (bool, error) {
		if expected != stored {
			return false, nil
		}
		stored, storedData, saves = version, data, saves+1
		return true, nil
	}
	version, pricedModels, err := source.Refresh(ctx, current, save)
	require.NoError(t, err)
	require.NotEmpty(t, version)
	require.Equal(t, minPricedModels, pricedModels)
	version, _, err = source.Refresh(ctx, current, save)
	require.NoError(t, err)
	require.Empty(t, version, "unchanged price data is not saved again")
	require.Equal(t, 1, saves)

	stored, storedData = "larger", priceData(t, 200, usdPerMTok(1))
	_, _, err = source.Refresh(ctx, current, save)
	require.ErrorContains(t, err, "shrank from 200 to 50", "the stored copy guards against shrinking data")
	stored, storedData = "unreadable", []byte(`{"not":`)
	version, _, err = source.Refresh(ctx, current, save)
	require.NoError(t, err)
	require.NotEmpty(t, version, "a stored copy that can't be counted leaves only the minimum check")
	require.Equal(t, 2, saves)

	stored = "older"
	racing := func(context.Context) (string, []byte, error) {
		read, readData := stored, storedData
		stored = "concurrent"
		return read, readData, nil
	}
	version, _, err = source.Refresh(ctx, racing, save)
	require.NoError(t, err)
	require.Empty(t, version, "a refresh that loses to a concurrent one stores nothing")
	require.Equal(t, 2, saves)
}

func TestValidateRejectsPriceDataThatShrinksByMoreThanHalf(t *testing.T) {
	_, _, err := validate(priceData(t, 99, usdPerMTok(5)), 200)
	require.ErrorContains(t, err, "shrank from 200 to 99")
	_, pricedModels, err := validate(priceData(t, 100, usdPerMTok(3)), 200)
	require.NoError(t, err)
	require.Equal(t, 100, pricedModels)
}

func TestPricesReportsWhetherTheCatalogPricesAModel(t *testing.T) {
	pricer, err := New()
	require.NoError(t, err)
	require.True(t, pricer.Prices(Input{BaseURL: openAIURL, ConfiguredModel: "gpt-4o"}))
	require.False(t, pricer.Prices(Input{BaseURL: openAIURL, ConfiguredModel: "self-hosted-model"}))
	require.False(t, pricer.Prices(Input{BaseURL: "https://models.example.test/v1", ConfiguredModel: "gpt-4o"}))
}

func TestLoadRacesWithEstimates(t *testing.T) {
	data := priceData(t, minPricedModels, usdPerMTok(1))
	pricer, err := New()
	require.NoError(t, err)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 20 {
				require.NoError(t, pricer.Load(data))
			}
		})
		wg.Go(func() {
			for range 20 {
				require.NotEmpty(t, gpt4oCost(t, pricer))
			}
		})
	}
	wg.Wait()
}

func TestEstimatePassesRateClassAsPriceContext(t *testing.T) {
	variant := func(key, value string, price int) map[string]any {
		return map[string]any{
			"when": map[string]string{key: value}, "prices": map[string]any{"input_mtok": price, "output_mtok": price},
		}
	}
	models := make([]map[string]any, 0, minPricedModels)
	for index := range minPricedModels {
		id := fmt.Sprintf("claude-%d", index)
		models = append(models, map[string]any{
			"id": id, "match": map[string]string{"equals": id},
			"prices": map[string]any{"input_mtok": 1, "output_mtok": 1},
			"price_variants": []map[string]any{
				variant("speed", "fast", 6), variant("inference_geo", "us", 3), variant("service_tier", "priority", 4),
			},
		})
	}
	data, err := json.Marshal([]map[string]any{{
		"id": "anthropic", "name": "Anthropic", "api_pattern": `https://api\.anthropic\.com`, "models": models,
	}})
	require.NoError(t, err)
	pricer, err := New()
	require.NoError(t, err)
	require.NoError(t, pricer.Load(data))
	for _, test := range []struct {
		metadata modelenvelope.AnthropicMetadata
		cost     modelenvelope.ProviderReportedCostUSD
	}{
		{modelenvelope.AnthropicMetadata{Speed: "fast"}, "12"},
		{modelenvelope.AnthropicMetadata{InferenceGeo: "us"}, "6"},
		{modelenvelope.AnthropicMetadata{ServiceTier: "priority"}, "8"},
		{modelenvelope.AnthropicMetadata{}, "2"},
		{modelenvelope.AnthropicMetadata{Speed: "standard", InferenceGeo: "global", ServiceTier: "standard"}, "2"},
	} {
		require.Equal(t, test.cost, pricer.Estimate(context.Background(), Input{
			BaseURL: anthropicURL, APIFormat: modelprotocol.APIFormatAnthropicMessages, ServedModel: "claude-0",
			Usage: tokens(1_000_000, 1_000_000), CreatedAt: callTime,
			Metadata: modelenvelope.ProviderMetadata{Anthropic: test.metadata},
		}), "%+v", test.metadata)
	}
}
