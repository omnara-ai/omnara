package executionstore

import (
	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

func providerReportedCostUSDFromSQLC(value string) modelenvelope.ProviderReportedCostUSD {
	if value == "" {
		return ""
	}
	cost, _ := modelenvelope.ParseProviderReportedCostUSD(value)
	return cost
}
