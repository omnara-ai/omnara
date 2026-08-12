package openaichatcompletions

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

func TestOpenRouterReportedCost(t *testing.T) {
	for _, test := range []struct {
		name  string
		usage chatUsage
		want  modelenvelope.ProviderReportedCostUSD
		valid bool
	}{
		{
			name: "OpenRouter-funded request uses account charge",
			usage: chatUsage{
				Cost: json.RawMessage(`0.0000125`),
				CostDetails: chatCostDetails{
					UpstreamInferenceCost: json.RawMessage(`0.00001`),
				},
			},
			want:  "0.0000125",
			valid: true,
		},
		{
			name: "BYOK request includes free routing and upstream inference",
			usage: chatUsage{
				Cost:   json.RawMessage(`0`),
				IsBYOK: true,
				CostDetails: chatCostDetails{
					UpstreamInferenceCost: json.RawMessage(`0.0000076`),
				},
			},
			want:  "0.0000076",
			valid: true,
		},
		{
			name: "BYOK request includes routing fee and upstream inference",
			usage: chatUsage{
				Cost:   json.RawMessage(`0.95`),
				IsBYOK: true,
				CostDetails: chatCostDetails{
					UpstreamInferenceCost: json.RawMessage(`19`),
				},
			},
			want:  "19.95",
			valid: true,
		},
		{
			name: "BYOK request without upstream evidence remains unavailable",
			usage: chatUsage{
				Cost:   json.RawMessage(`0`),
				IsBYOK: true,
			},
			valid: true,
		},
		{
			name: "BYOK request without an OpenRouter charge remains unavailable",
			usage: chatUsage{
				IsBYOK: true,
				CostDetails: chatCostDetails{
					UpstreamInferenceCost: json.RawMessage(`0.0000076`),
				},
			},
			valid: true,
		},
		{
			name: "malformed BYOK upstream cost is invalid",
			usage: chatUsage{
				Cost:   json.RawMessage(`0`),
				IsBYOK: true,
				CostDetails: chatCostDetails{
					UpstreamInferenceCost: json.RawMessage(`-1`),
				},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, valid := openRouterReportedCost(test.usage)
			if got != test.want || valid != test.valid {
				t.Fatalf("openRouterReportedCost() = %q, %v; want %q, %v", got, valid, test.want, test.valid)
			}
		})
	}
}
