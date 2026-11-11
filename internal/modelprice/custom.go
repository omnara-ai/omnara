package modelprice

import (
	"encoding/json"
	"math/big"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

var tokensPerPrice = big.NewRat(1_000_000, 1)

func CustomCost(pricing json.RawMessage, usage modelenvelope.Usage) modelenvelope.ProviderReportedCostUSD {
	var prices struct {
		Input      string `json:"input_usd_per_million"`
		CacheRead  string `json:"cache_read_input_usd_per_million"`
		CacheWrite string `json:"cache_write_input_usd_per_million"`
		Output     string `json:"output_usd_per_million"`
	}
	if usage.Incomplete || json.Unmarshal(pricing, &prices) != nil {
		return ""
	}
	if prices.CacheRead == "" {
		prices.CacheRead = prices.Input
	}
	if prices.CacheWrite == "" {
		prices.CacheWrite = prices.Input
	}
	cost := new(big.Rat)
	for _, part := range []struct {
		tokens int
		price  string
	}{
		{usage.UncachedInputTokens, prices.Input},
		{usage.CacheReadTokens, prices.CacheRead},
		{usage.CacheWriteTokens, prices.CacheWrite},
		{usage.OutputTokens, prices.Output},
	} {
		price, ok := new(big.Rat).SetString(part.price)
		if !ok || price.Sign() < 0 {
			return ""
		}
		cost.Add(cost, price.Mul(price, big.NewRat(int64(part.tokens), 1)))
	}
	cost.Quo(cost, tokensPerPrice)
	rounded, ok := modelenvelope.ParseProviderReportedCostUSD(cost.FloatString(15))
	if !ok {
		return ""
	}
	return rounded
}
