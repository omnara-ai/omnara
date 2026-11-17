package modelprice

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

func TestCustomCost(t *testing.T) {
	cached := modelenvelope.Usage{
		InputTokens: 3_400, UncachedInputTokens: 1_000, CacheReadTokens: 2_000, CacheWriteTokens: 400, OutputTokens: 500,
	}
	tests := []struct {
		name    string
		pricing string
		usage   modelenvelope.Usage
		cost    modelenvelope.ProviderReportedCostUSD
	}{
		{
			name: "prices each token kind",
			pricing: `{"input_usd_per_million":"3","cache_read_input_usd_per_million":"0.3",` +
				`"cache_write_input_usd_per_million":"3.75","output_usd_per_million":"15"}`,
			usage: cached,
			cost:  "0.0126",
		},
		{
			name:    "cache prices default to the input price",
			pricing: `{"input_usd_per_million":"2","output_usd_per_million":"10"}`,
			usage:   cached,
			cost:    "0.0118",
		},
		{
			name:    "keeps sub-cent precision",
			pricing: `{"input_usd_per_million":"0.000001","output_usd_per_million":"0"}`,
			usage:   modelenvelope.Usage{InputTokens: 1, UncachedInputTokens: 1},
			cost:    "0.000000000001",
		},
		{
			name:    "incomplete usage",
			pricing: `{"input_usd_per_million":"3","output_usd_per_million":"15"}`,
			usage:   modelenvelope.Usage{InputTokens: 10, UncachedInputTokens: 10, Incomplete: true},
		},
		{
			name:    "missing output price",
			pricing: `{"input_usd_per_million":"3"}`,
			usage:   cached,
		},
		{
			name:    "negative price",
			pricing: `{"input_usd_per_million":"-1","output_usd_per_million":"15"}`,
			usage:   cached,
		},
		{
			name:    "cost too large to store",
			pricing: `{"input_usd_per_million":"1000000000000000000000","output_usd_per_million":"0"}`,
			usage:   cached,
		},
		{
			name:    "cost that rounds past the largest storable cost",
			pricing: `{"input_usd_per_million":"999999999999999999999.9999999999","output_usd_per_million":"0"}`,
			usage:   modelenvelope.Usage{InputTokens: 1, UncachedInputTokens: 1},
		},
		{
			name:    "malformed pricing",
			pricing: `[]`,
			usage:   cached,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.cost, CustomCost(json.RawMessage(test.pricing), test.usage))
		})
	}
}

func TestPricingFits(t *testing.T) {
	for _, test := range []struct {
		name    string
		pricing string
		fits    bool
	}{
		{
			name:    "fits at the largest stored token counts",
			pricing: `{"input_usd_per_million":"232830643762","output_usd_per_million":"232830643762"}`,
			fits:    true,
		},
		{
			name:    "overflows at the largest stored token counts",
			pricing: `{"input_usd_per_million":"232830643763","output_usd_per_million":"232830643763"}`,
		},
		{
			name: "input tokens fill one input price at a time",
			pricing: `{"input_usd_per_million":"300000000000","cache_read_input_usd_per_million":"300000000000",` +
				`"cache_write_input_usd_per_million":"300000000000","output_usd_per_million":"0"}`,
			fits: true,
		},
		{
			name: "the priciest input price decides",
			pricing: `{"input_usd_per_million":"1","cache_write_input_usd_per_million":"500000000000",` +
				`"output_usd_per_million":"0"}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.fits, PricingFits(json.RawMessage(test.pricing)))
		})
	}
}

func TestFreePricing(t *testing.T) {
	for _, test := range []struct {
		name    string
		pricing string
		free    bool
	}{
		{name: "every price is 0", pricing: `{"input_usd_per_million":"0","output_usd_per_million":"0.000"}`, free: true},
		{name: "a paid cache price", pricing: `{"input_usd_per_million":"0",` +
			`"cache_write_input_usd_per_million":"1.25","output_usd_per_million":"0"}`},
		{name: "a paid output price", pricing: `{"input_usd_per_million":"0","output_usd_per_million":"1"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.free, FreePricing(json.RawMessage(test.pricing)))
		})
	}
}
