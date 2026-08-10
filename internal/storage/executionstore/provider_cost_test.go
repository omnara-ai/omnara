package executionstore

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

func TestProviderReportedCostUSDSQLCRoundTrip(t *testing.T) {
	for _, cost := range []modelenvelope.ProviderReportedCostUSD{
		"",
		"0",
		"0.0000125",
		"100",
	} {
		t.Run(string(cost), func(t *testing.T) {
			sqlValue := providerReportedCostUSDToSQLC(cost)
			stored := ""
			if sqlValue != nil {
				stored = *sqlValue
			}
			if got := providerReportedCostUSDFromSQLC(stored); got != cost {
				t.Fatalf("provider-reported cost round trip = %q, want %q", got, cost)
			}
		})
	}
}
