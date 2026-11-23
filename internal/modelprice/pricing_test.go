package modelprice

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

func TestPricingCost(t *testing.T) {
	cached := modelenvelope.Usage{
		InputTokens: 3_400, UncachedInputTokens: 1_000, CacheReadTokens: 2_000, CacheWriteTokens: 400, OutputTokens: 500,
	}
	tests := []struct {
		name    string
		pricing Pricing
		usage   modelenvelope.Usage
		cost    modelenvelope.ProviderReportedCostUSD
	}{
		{
			name:    "prices each token kind",
			pricing: Pricing{Input: "3", CacheRead: "0.3", CacheWrite: "3.75", Output: "15"},
			usage:   cached,
			cost:    "0.0126",
		},
		{
			name:    "cache prices default to the input price",
			pricing: Pricing{Input: "2", Output: "10"},
			usage:   cached,
			cost:    "0.0118",
		},
		{
			name:    "keeps sub-cent precision",
			pricing: Pricing{Input: "0.000001", Output: "0"},
			usage:   modelenvelope.Usage{InputTokens: 1, UncachedInputTokens: 1},
			cost:    "0.000000000001",
		},
		{
			name:    "free prices cost 0",
			pricing: Pricing{Input: "0", Output: "0"},
			usage:   cached,
			cost:    "0",
		},
		{
			name:    "incomplete usage",
			pricing: Pricing{Input: "3", Output: "15"},
			usage:   modelenvelope.Usage{InputTokens: 10, UncachedInputTokens: 10, Incomplete: true},
		},
		{
			name:    "cost too large to store",
			pricing: Pricing{Input: "1000000000000000000000", Output: "0"},
			usage:   cached,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.cost, test.pricing.Cost(test.usage))
		})
	}
}

func TestPricingNormalized(t *testing.T) {
	normalized, err := Pricing{Input: "3.50", CacheRead: "0.300", Output: "15.0"}.Normalized()
	require.NoError(t, err)
	require.Equal(t, Pricing{Input: "3.5", CacheRead: "0.3", Output: "15"}, normalized,
		"equal prices written differently normalize to the same value")
	for _, test := range []struct {
		name    string
		pricing Pricing
	}{
		{name: "missing output price", pricing: Pricing{Input: "3"}},
		{name: "negative price", pricing: Pricing{Input: "-1", Output: "15"}},
		{name: "exponent", pricing: Pricing{Input: "1e3", Output: "15"}},
		{name: "leading zero", pricing: Pricing{Input: "03", Output: "15"}},
		{name: "more than 15 decimal places", pricing: Pricing{Input: "0.1234567890123456", Output: "15"}},
		{name: "overflows at the largest stored token counts",
			pricing: Pricing{Input: "232830643763", Output: "232830643763"}},
		{name: "the priciest input price decides", pricing: Pricing{Input: "1", CacheWrite: "500000000000", Output: "0"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.pricing.Normalized()
			require.Error(t, err)
		})
	}
	for _, pricing := range []Pricing{
		{Input: "232830643762", Output: "232830643762"},
		{Input: "300000000000", CacheRead: "300000000000", CacheWrite: "300000000000", Output: "0"},
	} {
		_, err := pricing.Normalized()
		require.NoError(t, err, "input tokens fill one input price at a time")
	}
}

func TestPricingFree(t *testing.T) {
	require.True(t, Pricing{Input: "0", Output: "0"}.Free())
	require.True(t, Pricing{Input: "0", CacheRead: "0", Output: "0"}.Free())
	require.False(t, Pricing{Input: "0", CacheWrite: "1.25", Output: "0"}.Free())
	require.False(t, Pricing{Input: "0", Output: "1"}.Free())
}
