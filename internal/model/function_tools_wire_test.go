package model_test

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/stretchr/testify/require"
)

func TestFunctionToolsPreserveNonStrictSchemas(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{
			"query":{"type":"string"},
			"limit":{"type":"integer"},
			"labels":{"type":"object","additionalProperties":{"type":"string"}}
		},
		"required":["query"]
	}`)
	for _, tc := range []struct {
		client          model.Client
		wantStrictField bool
	}{
		{client: openaichatcompletions.Client{EndpointPath: "/chat/completions", ProviderModelSlug: "test-model"}},
		{
			client: openaichatcompletions.Client{
				EndpointPath: "/chat/completions", ProviderModelSlug: "test-model", APIVariant: modelprotocol.APIVariantOpenRouter,
			},
			wantStrictField: true,
		},
		{client: openaichatcompletions.Client{
			EndpointPath: "/chat/completions", ProviderModelSlug: "test-model", APIVariant: modelprotocol.APIVariantBedrock,
		}},
		{
			client:          openairesponses.Client{EndpointPath: "/responses", ProviderModelSlug: "test-model"},
			wantStrictField: true,
		},
		{
			client: openairesponses.Client{
				EndpointPath: "/responses", ProviderModelSlug: "test-model", APIVariant: modelprotocol.APIVariantBedrock,
			},
			wantStrictField: true,
		},
	} {
		client := tc.client
		t.Run(string(client.APIFormat())+"/"+string(client.ModelAPIVariant()), func(t *testing.T) {
			prepared, err := client.Prepare(t.Context(), model.PrepareInput{
				Context: modelcontext.Bundle{
					ToolSpecs: []modelcontext.ToolSpec{
						{Name: "search", InputSchema: schema},
						{Name: "ping"},
					},
				},
			})
			require.NoError(t, err)
			var payload struct {
				Tools []map[string]json.RawMessage `json:"tools"`
			}
			require.NoError(t, json.Unmarshal(prepared.Body, &payload))
			require.Len(t, payload.Tools, 2)
			for i, tool := range payload.Tools {
				require.Equal(t, `"function"`, string(tool["type"]))
				if client.APIFormat() == modelprotocol.APIFormatOpenAIChatCompletions {
					var function map[string]json.RawMessage
					require.NoError(t, json.Unmarshal(tool["function"], &function))
					tool = function
				}
				if tc.wantStrictField {
					require.Equal(t, `false`, string(tool["strict"]))
				} else {
					require.NotContains(t, tool, "strict")
				}
				wantSchema := schema
				if i == 1 {
					wantSchema = json.RawMessage(`{"type":"object","properties":{}}`)
				}
				require.JSONEq(t, string(wantSchema), string(tool["parameters"]))
			}
		})
	}
}
