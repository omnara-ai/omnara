//go:build live

package model_test

import (
	"context"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprice"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
)

func TestLiveOpenRouterReportedCostMatchesTheEstimate(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if apiKey == "" {
		t.Skip("OPENROUTER_API_KEY is not set")
	}
	baseURL := os.Getenv("OPENROUTER_BASE_URL")
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}
	pricer, err := modelprice.New()
	if err != nil {
		t.Fatalf("load prices: %v", err)
	}
	for _, slug := range []string{"anthropic/claude-haiku-5.5", "openai/gpt-6-luna"} {
		t.Run(slug, func(t *testing.T) {
			client := openaichatcompletions.Client{
				Auth: route.Chain{
					route.BearerToken{Token: apiKey},
					route.Headers{"HTTP-Referer": "https://omnara.com", "X-OpenRouter-Title": "Omnara Live Test"},
				},
				BaseURL:           baseURL,
				EndpointPath:      modelstore.DefaultModelProviderEndpointPath(modelprotocol.APIFormatOpenAIChatCompletions),
				ProviderModelSlug: slug,
				APIVariant:        modelprotocol.APIVariantOpenRouter,
			}
			var first, second model.Response
			for attempt := range 3 {
				first, second = liveConversationPair(t, client)
				if second.Usage.CacheReadTokens > 0 {
					break
				}
				if attempt < 2 {
					time.Sleep(3 * time.Second) //nolint:omnaralint // provider cache propagation has no notification API
				}
			}
			if second.Usage.CacheReadTokens == 0 {
				t.Fatalf("second call read nothing from the cache: %+v", second.Usage)
			}
			for name, response := range map[string]model.Response{"uncached": first, "cached": second} {
				reported, err := strconv.ParseFloat(string(response.ProviderReportedCostUSD), 64)
				if err != nil || reported <= 0 {
					t.Fatalf("%s call reported no cost: %q", name, response.ProviderReportedCostUSD)
				}
				estimate := pricer.Estimate(context.Background(), modelprice.Input{
					BaseURL:         baseURL,
					APIFormat:       modelprotocol.APIFormatOpenAIChatCompletions,
					APIVariant:      modelprotocol.APIVariantOpenRouter,
					ConfiguredModel: slug,
					ServedModel:     response.ServedProviderModelSlug,
					Usage:           modelenvelope.NormalizeUsage(response.Usage),
					Metadata:        response.ProviderMetadata,
					CreatedAt:       time.Now(),
				})
				estimated, err := strconv.ParseFloat(string(estimate), 64)
				if err != nil {
					t.Fatalf("%s call has no estimate for usage %+v", name, response.Usage)
				}
				if math.Abs(estimated-reported) > 0.05*reported {
					t.Fatalf("%s call: estimated %v, OpenRouter reported %v, usage %+v",
						name, estimated, reported, response.Usage)
				}
				t.Logf("%s call: estimated %v, reported %v, usage %+v", name, estimated, reported, response.Usage)
			}
		})
	}
}
