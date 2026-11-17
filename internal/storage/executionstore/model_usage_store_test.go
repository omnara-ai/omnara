package executionstore

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

func TestUsageTotalsExceedOneCallsRange(t *testing.T) {
	const largeTotal = "1999999999999999.999999999999998"
	reported, effective, ok := usageCosts(largeTotal, largeTotal)
	if !ok || reported != largeTotal || effective != largeTotal {
		t.Fatalf("usageCosts = %q, %q, %v; want %q", reported, effective, ok, largeTotal)
	}
	item := ModelUsageTotals{
		ProviderReportedCostUSD: modelenvelope.ProviderReportedCostUSD(largeTotal),
		EffectiveCostUSD:        modelenvelope.ProviderReportedCostUSD(largeTotal),
	}
	totals, err := sumModelUsageTotals([]ModelUsageTotals{item, item})
	if err != nil {
		t.Fatal(err)
	}
	const want = "3999999999999999.999999999999996"
	if totals.ProviderReportedCostUSD != want || totals.EffectiveCostUSD != want {
		t.Fatalf("totals = %q, %q; want %q", totals.ProviderReportedCostUSD, totals.EffectiveCostUSD, want)
	}
}
