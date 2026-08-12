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
				OpenRouterCost:        json.RawMessage(`0.0000125`),
				OpenRouterCostDetails: json.RawMessage(`{"upstream_inference_cost":0.00001}`),
				OpenRouterIsBYOK:      json.RawMessage(`false`),
			},
			want:  "0.0000125",
			valid: true,
		},
		{
			name: "missing BYOK identity preserves known account charge",
			usage: chatUsage{
				OpenRouterCost:        json.RawMessage(`0.0000125`),
				OpenRouterCostDetails: json.RawMessage(`{"upstream_inference_cost":0.00001}`),
			},
			want:  "0.0000125",
			valid: true,
		},
		{
			name: "null BYOK identity preserves known account charge",
			usage: chatUsage{
				OpenRouterCost:        json.RawMessage(`0.0000125`),
				OpenRouterCostDetails: json.RawMessage(`{"upstream_inference_cost":0.00001}`),
				OpenRouterIsBYOK:      json.RawMessage(`null`),
			},
			want:  "0.0000125",
			valid: true,
		},
		{
			name: "malformed BYOK identity preserves known account charge",
			usage: chatUsage{
				OpenRouterCost:        json.RawMessage(`0.0000125`),
				OpenRouterCostDetails: json.RawMessage(`{"upstream_inference_cost":0.00001}`),
				OpenRouterIsBYOK:      json.RawMessage(`"unknown"`),
			},
			want:  "0.0000125",
			valid: true,
		},
		{
			name: "BYOK request includes free routing and upstream inference",
			usage: chatUsage{
				OpenRouterCost:        json.RawMessage(`0`),
				OpenRouterCostDetails: json.RawMessage(`{"upstream_inference_cost":0.0000076}`),
				OpenRouterIsBYOK:      json.RawMessage(`true`),
			},
			want:  "0.0000076",
			valid: true,
		},
		{
			name: "BYOK request includes routing fee and upstream inference",
			usage: chatUsage{
				OpenRouterCost:        json.RawMessage(`0.95`),
				OpenRouterCostDetails: json.RawMessage(`{"upstream_inference_cost":19}`),
				OpenRouterIsBYOK:      json.RawMessage(`true`),
			},
			want:  "19.95",
			valid: true,
		},
		{
			name: "BYOK request without upstream evidence remains unavailable",
			usage: chatUsage{
				OpenRouterCost:   json.RawMessage(`0`),
				OpenRouterIsBYOK: json.RawMessage(`true`),
			},
			valid: true,
		},
		{
			name: "BYOK request without an OpenRouter charge remains unavailable",
			usage: chatUsage{
				OpenRouterCostDetails: json.RawMessage(`{"upstream_inference_cost":0.0000076}`),
				OpenRouterIsBYOK:      json.RawMessage(`true`),
			},
			valid: true,
		},
		{
			name: "malformed BYOK upstream cost is invalid",
			usage: chatUsage{
				OpenRouterCost:        json.RawMessage(`0`),
				OpenRouterCostDetails: json.RawMessage(`{"upstream_inference_cost":-1}`),
				OpenRouterIsBYOK:      json.RawMessage(`true`),
			},
		},
		{
			name: "malformed BYOK cost details are invalid",
			usage: chatUsage{
				OpenRouterCost:        json.RawMessage(`0`),
				OpenRouterCostDetails: json.RawMessage(`[]`),
				OpenRouterIsBYOK:      json.RawMessage(`true`),
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
