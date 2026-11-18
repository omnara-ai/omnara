package anthropicmessages

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func deferredToolSpecs() []modelcontext.ToolSpec {
	return []modelcontext.ToolSpec{
		{Name: "get_weather", Description: "Get the weather.", InputSchema: json.RawMessage(`{"type":"object"}`), Deferred: true},
		{Name: "get_time", Description: "Get the time.", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: toolcatalog.ToolNameToolSearch, Description: "Search tools.", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
}

func toolSearchBundle() modelcontext.Bundle {
	return modelcontext.Bundle{
		SystemPrompt: "sys",
		Messages: []modelcontext.Message{
			anthropicTextMessage(modelprotocol.RoleUser, "weather in Tokyo?"),
			messageAtSequence(assistantToolCallMessage("mcc_1", "tcl_1"), 2),
		},
		ToolSpecs: deferredToolSpecs(),
		ToolResults: []modelcontext.ToolResultRef{{
			ToolCallID:         "tcl_1",
			ModelCallContextID: "mcc_1",
			ProviderCallID:     "toolu_search",
			Name:               toolcatalog.ToolNameToolSearch,
			Input:              json.RawMessage(`{"pattern":"weather"}`),
			Outcome:            executionstore.ToolResultOutcomeSucceeded,
			ContentParts: json.RawMessage(`[{"type":"structured_data","value":{"outcome":"succeeded"}},` +
				`{"type":"text","text":"Loaded 1 tool(s) matching \"weather\":\n- get_weather: Get the weather."},` +
				`{"type":"structured_data","value":{"pattern":"weather","tool_names":["get_weather"],"total_deferred_tools":1,"tools":[{"name":"get_weather","description":"Get the weather.","input_schema":{"type":"object"}}]}}]`),
		}},
	}
}

func TestPrepareDefersToolsAndReturnsDiscoveredToolReferences(t *testing.T) {
	client := Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "claude-test",
	}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: toolSearchBundle(),
		Policy:  model.RequestPolicy{MaxOutputTokens: 1024, CacheRetention: model.CacheRetentionShort},
	})
	require.NoError(t, err)
	var payload struct {
		Tools []struct {
			Name         string          `json:"name"`
			DeferLoading bool            `json:"defer_loading"`
			CacheControl json.RawMessage `json:"cache_control"`
		} `json:"tools"`
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type         string            `json:"type"`
				ToolUseID    string            `json:"tool_use_id"`
				CacheControl json.RawMessage   `json:"cache_control"`
				Content      []json.RawMessage `json:"content"`
			} `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(prepared.Body, &payload))
	require.Len(t, payload.Tools, 3)
	require.Equal(t, "get_time", payload.Tools[0].Name)
	require.Equal(t, toolcatalog.ToolNameToolSearch, payload.Tools[1].Name)
	require.NotEmpty(t, payload.Tools[1].CacheControl)
	require.Equal(t, "get_weather", payload.Tools[2].Name)
	require.True(t, payload.Tools[2].DeferLoading)
	require.Empty(t, payload.Tools[2].CacheControl)

	require.Len(t, payload.Messages, 3)
	require.Equal(t, "user", payload.Messages[2].Role)
	require.Len(t, payload.Messages[2].Content, 1)
	result := payload.Messages[2].Content[0]
	require.Equal(t, "tool_result", result.Type)
	require.Contains(t, result.ToolUseID, "mcc_1_toolu_search_")
	require.NotEmpty(t, result.CacheControl)
	require.Len(t, result.Content, 1)
	require.JSONEq(
		t,
		`{"type":"tool_reference","tool_name":"get_weather"}`,
		string(result.Content[0]),
	)
	require.NotContains(t, string(prepared.Body), "tool_addition")
}

func TestPrepareSkipsToolReferencesForUndeclaredTools(t *testing.T) {
	client := Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "claude-test",
	}
	bundle := toolSearchBundle()
	bundle.ToolSpecs = modelcontext.LoadedToolSpecs(bundle.ToolSpecs)
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: bundle,
		Policy:  model.RequestPolicy{MaxOutputTokens: 1024},
	})
	require.NoError(t, err)
	require.NotContains(t, string(prepared.Body), "tool_reference")
}

func TestPrepareWithoutDeferredToolsSendsNoDeferLoading(t *testing.T) {
	client := Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "claude-test",
	}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{
			Messages:  []modelcontext.Message{anthropicTextMessage(modelprotocol.RoleUser, "hi")},
			ToolSpecs: modelcontext.LoadedToolSpecs(deferredToolSpecs()),
		},
		Policy: model.RequestPolicy{MaxOutputTokens: 1024},
	})
	require.NoError(t, err)
	require.NotContains(t, string(prepared.Body), "defer_loading")
}
