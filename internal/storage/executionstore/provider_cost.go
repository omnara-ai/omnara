package executionstore

import (
	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

func providerReportedCostUSDToSQLC(cost modelenvelope.ProviderReportedCostUSD) *string {
	if cost == "" {
		return nil
	}
	value := string(cost)
	return &value
}

func providerReportedCostUSDFromSQLC(value string) modelenvelope.ProviderReportedCostUSD {
	if value == "" {
		return ""
	}
	cost, _ := modelenvelope.ParseProviderReportedCostUSD(value)
	return cost
}
