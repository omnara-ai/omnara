package openaichatcompletions

import (
	"bytes"
	"encoding/json"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

// OpenRouter reports both account charges and upstream inference costs in USD.
// https://openrouter.ai/docs/cookbook/administration/usage-accounting#cost-breakdown
func openRouterReportedCost(
	usage chatUsage,
) (modelenvelope.ProviderReportedCostUSD, openRouterCostIssue) {
	openRouterCost, valid := optionalProviderCost(usage.OpenRouterCost)
	if !valid {
		return "", openRouterCostIssueInvalid
	}
	isBYOK, valid := optionalProviderBool(usage.OpenRouterIsBYOK)
	if !valid {
		return openRouterCost.normalized, openRouterCostIssueBYOKStateInvalid
	}
	if openRouterCost.raw == "" {
		if isBYOK != nil && *isBYOK {
			return "", openRouterCostIssueBYOKComponentMissing
		}
		return "", openRouterCostIssueNone
	}
	if isBYOK == nil {
		return openRouterCost.normalized, openRouterCostIssueBYOKStateMissing
	}
	if !*isBYOK {
		return openRouterCost.normalized, openRouterCostIssueNone
	}

	upstreamCost, valid := openRouterUpstreamCost(usage.OpenRouterCostDetails)
	if !valid {
		return "", openRouterCostIssueInvalid
	}
	if upstreamCost.raw == "" {
		return "", openRouterCostIssueBYOKComponentMissing
	}
	total, valid := modelenvelope.SumProviderReportedCostUSD(openRouterCost.raw, upstreamCost.raw)
	if !valid {
		return "", openRouterCostIssueInvalid
	}
	return total, openRouterCostIssueNone
}

type openRouterCostIssue uint8

const (
	openRouterCostIssueNone openRouterCostIssue = iota
	openRouterCostIssueInvalid
	openRouterCostIssueBYOKStateMissing
	openRouterCostIssueBYOKStateInvalid
	openRouterCostIssueBYOKComponentMissing
)

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
