package openaichatcompletions

import (
	"bytes"
	"encoding/json"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

// https://openrouter.ai/docs/cookbook/administration/usage-accounting#cost-breakdown
func openRouterReportedCost(
	usage chatUsage,
) (modelenvelope.ProviderReportedCostUSD, bool) {
	openRouterCost, valid := optionalProviderCost(usage.Cost)
	if !valid || !usage.IsBYOK {
		return openRouterCost.normalized, valid
	}
	if openRouterCost.raw == "" {
		return "", true
	}

	upstreamCost, valid := optionalProviderCost(usage.CostDetails.UpstreamInferenceCost)
	if !valid {
		return "", false
	}
	if upstreamCost.raw == "" {
		return "", true
	}
	return modelenvelope.SumProviderReportedCostUSD(openRouterCost.raw, upstreamCost.raw)
}

type providerCost struct {
	raw        string
	normalized modelenvelope.ProviderReportedCostUSD
}

func optionalProviderCost(
	raw json.RawMessage,
) (providerCost, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return providerCost{}, true
	}
	normalized, valid := modelenvelope.ParseProviderReportedCostUSD(string(raw))
	return providerCost{raw: string(raw), normalized: normalized}, valid
}
