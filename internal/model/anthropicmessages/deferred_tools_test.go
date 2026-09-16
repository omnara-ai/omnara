package anthropicmessages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestPrepareDefersToolsAndAddsDiscoveredToolsMidConversation(t *testing.T) {
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
			Role    string            `json:"role"`
			Content []json.RawMessage `json:"content"`
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

	require.Len(t, payload.Messages, 4)
	require.Equal(t, "user", payload.Messages[2].Role)
	require.Contains(t, string(payload.Messages[2].Content[0]), `"tool_use_id":"mcc_1_toolu_search_`)
	require.Contains(t, string(payload.Messages[2].Content[0]), `"cache_control"`)
	require.Equal(t, "system", payload.Messages[3].Role)
	require.JSONEq(
		t,
		`{"type":"tool_addition","tool":{"type":"tool_reference","name":"get_weather"}}`,
		string(payload.Messages[3].Content[0]),
	)

	headers, err := (protocol{client: client}).RequestHeaders(prepared.Body)
	require.NoError(t, err)
	require.Equal(t, MidConversationToolChangesBeta, headers["Anthropic-Beta"])
}

func TestPrepareFlushesToolAdditionsBeforeNextAssistantTurn(t *testing.T) {
	client := Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "claude-test",
	}
	bundle := toolSearchBundle()
	bundle.Messages = append(
		bundle.Messages,
		messageAtSequence(anthropicTextMessage(modelprotocol.RoleUser, "hurry up"), 3),
		messageAtSequence(anthropicTextMessage(modelprotocol.RoleAssistant, "on it"), 4),
	)
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: bundle,
		Policy:  model.RequestPolicy{MaxOutputTokens: 1024},
	})
	require.NoError(t, err)
	var payload struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(prepared.Body, &payload))
	roles := make([]string, 0, len(payload.Messages))
	for _, message := range payload.Messages {
		roles = append(roles, message.Role)
	}
	require.Equal(t, []string{"user", "assistant", "user", "system", "assistant"}, roles)
}

func TestPrepareWithoutDeferredToolsSendsNoBetaHeader(t *testing.T) {
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
	headers, err := (protocol{client: client}).RequestHeaders(prepared.Body)
	require.NoError(t, err)
	require.Empty(t, headers)
}

func TestRespondSendsBetaHeaderOnlyForToolChanges(t *testing.T) {
	var betaHeaders []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		betaHeaders = append(betaHeaders, r.Header.Get("Anthropic-Beta"))
		_, _ = w.Write([]byte(`{"id":"msg_1","model":"claude-served","content":[{"type":"text","text":"ok"}],` +
			`"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()
	client := testRespondClient(server)
	withChanges := json.RawMessage(`{"model":"claude-test","max_tokens":10,"stream":true,"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"hi"}]},` +
		`{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_reference","name":"get_weather"}}]}]}`)
	_, err := client.Respond(context.Background(), model.Request{ProviderRequest: withChanges})
	require.NoError(t, err)
	without := json.RawMessage(`{"model":"claude-test","max_tokens":10,"stream":true,"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	_, err = client.Respond(context.Background(), model.Request{ProviderRequest: without})
	require.NoError(t, err)
	require.Equal(t, []string{MidConversationToolChangesBeta, ""}, betaHeaders)
}
