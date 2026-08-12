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
	openRouterCost, valid := optionalProviderCost(usage.OpenRouterCost)
	if !valid || openRouterCost.raw == "" {
		return openRouterCost.normalized, valid
	}
	isBYOK, valid := optionalProviderBool(usage.OpenRouterIsBYOK)
	if !valid || isBYOK == nil || !*isBYOK {
		return openRouterCost.normalized, true
	}

	upstreamCost, valid := openRouterUpstreamCost(usage.OpenRouterCostDetails)
	if !valid {
		return "", false
	}
	if upstreamCost.raw == "" {
		return "", true
	}
	return modelenvelope.SumProviderReportedCostUSD(openRouterCost.raw, upstreamCost.raw)
}

type openRouterCostDetails struct {
	UpstreamInferenceCost json.RawMessage `json:"upstream_inference_cost"`
}

func openRouterUpstreamCost(raw json.RawMessage) (providerCost, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return providerCost{}, true
	}
	var details openRouterCostDetails
	if err := json.Unmarshal(raw, &details); err != nil {
		return providerCost{}, false
	}
	return optionalProviderCost(details.UpstreamInferenceCost)
}

func optionalProviderBool(raw json.RawMessage) (*bool, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, true
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	return &value, true
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
