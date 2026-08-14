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
		issue openRouterCostIssue
	}{
		{
			name: "OpenRouter-funded request uses account charge",
			usage: chatUsage{
				OpenRouterCost:        json.RawMessage(`0.0000125`),
				OpenRouterCostDetails: json.RawMessage(`{"upstream_inference_cost":0.00001}`),
				OpenRouterIsBYOK:      json.RawMessage(`false`),
			},
			want: "0.0000125",
		},
		{
			name: "missing BYOK identity preserves known account charge",
			usage: chatUsage{
				OpenRouterCost:        json.RawMessage(`0.0000125`),
				OpenRouterCostDetails: json.RawMessage(`{"upstream_inference_cost":0.00001}`),
			},
			want:  "0.0000125",
			issue: openRouterCostIssueBYOKStateMissing,
		},
		{
			name: "null BYOK identity preserves known account charge",
			usage: chatUsage{
				OpenRouterCost:        json.RawMessage(`0.0000125`),
				OpenRouterCostDetails: json.RawMessage(`{"upstream_inference_cost":0.00001}`),
				OpenRouterIsBYOK:      json.RawMessage(`null`),
			},
			want:  "0.0000125",
			issue: openRouterCostIssueBYOKStateMissing,
		},
		{
			name: "malformed BYOK identity preserves known account charge",
			usage: chatUsage{
				OpenRouterCost:        json.RawMessage(`0.0000125`),
				OpenRouterCostDetails: json.RawMessage(`{"upstream_inference_cost":0.00001}`),
				OpenRouterIsBYOK:      json.RawMessage(`"unknown"`),
			},
			want:  "0.0000125",
			issue: openRouterCostIssueBYOKStateInvalid,
		},
		{
			name: "malformed BYOK identity remains observable without a reported cost",
			usage: chatUsage{
				OpenRouterIsBYOK: json.RawMessage(`"unknown"`),
			},
			issue: openRouterCostIssueBYOKStateInvalid,
		},
		{
			name: "BYOK request includes free routing and upstream inference",
			usage: chatUsage{
				OpenRouterCost:        json.RawMessage(`0`),
				OpenRouterCostDetails: json.RawMessage(`{"upstream_inference_cost":0.0000076}`),
				OpenRouterIsBYOK:      json.RawMessage(`true`),
			},
			want: "0.0000076",
		},
		{
			name: "BYOK request includes routing fee and upstream inference",
			usage: chatUsage{
				OpenRouterCost:        json.RawMessage(`0.95`),
				OpenRouterCostDetails: json.RawMessage(`{"upstream_inference_cost":19}`),
				OpenRouterIsBYOK:      json.RawMessage(`true`),
			},
			want: "19.95",
		},
		{
			name: "BYOK request without upstream evidence remains unavailable",
			usage: chatUsage{
				OpenRouterCost:   json.RawMessage(`0`),
				OpenRouterIsBYOK: json.RawMessage(`true`),
			},
			issue: openRouterCostIssueBYOKComponentMissing,
		},
		{
			name: "BYOK request without an OpenRouter charge remains unavailable",
			usage: chatUsage{
				OpenRouterCostDetails: json.RawMessage(`{"upstream_inference_cost":0.0000076}`),
				OpenRouterIsBYOK:      json.RawMessage(`true`),
			},
			issue: openRouterCostIssueBYOKComponentMissing,
		},
		{
			name: "malformed BYOK upstream cost is invalid",
			usage: chatUsage{
				OpenRouterCost:        json.RawMessage(`0`),
				OpenRouterCostDetails: json.RawMessage(`{"upstream_inference_cost":-1}`),
				OpenRouterIsBYOK:      json.RawMessage(`true`),
			},
			issue: openRouterCostIssueInvalid,
		},
		{
			name: "malformed BYOK cost details are invalid",
			usage: chatUsage{
				OpenRouterCost:        json.RawMessage(`0`),
				OpenRouterCostDetails: json.RawMessage(`[]`),
				OpenRouterIsBYOK:      json.RawMessage(`true`),
			},
			issue: openRouterCostIssueInvalid,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, issue := openRouterReportedCost(test.usage)
			if got != test.want || issue != test.issue {
				t.Fatalf("openRouterReportedCost() = %q, %v; want %q, %v", got, issue, test.want, test.issue)
			}
		})
	}
}
