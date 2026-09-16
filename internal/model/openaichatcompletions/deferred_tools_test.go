package openaichatcompletions

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
		{Name: "get_weather", Description: "Get the weather.", InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`), Deferred: true},
		{Name: "get_time", Description: "Get the time.", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: toolcatalog.ToolNameToolSearch, Description: "Search tools.", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
}

func userText(sequence int64, text string) modelcontext.Message {
	return modelcontext.Message{Role: modelprotocol.RoleUser, Sequence: sequence, Content: json.RawMessage(`[{"type":"text","text":"` + text + `"}]`)}
}

func TestPrepareOmitsDeferredToolsAndAddsCallDeferredTool(t *testing.T) {
	client := Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "gpt-test",
	}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{Context: modelcontext.Bundle{
		Messages:  []modelcontext.Message{userText(1, "hi")},
		ToolSpecs: deferredToolSpecs(),
	}})
	require.NoError(t, err)
	var payload struct {
		Tools []struct {
			Function struct {
				Name       string          `json:"name"`
				Parameters json.RawMessage `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(prepared.Body, &payload))
	names := make([]string, 0, len(payload.Tools))
	for _, tool := range payload.Tools {
		names = append(names, tool.Function.Name)
	}
	require.Equal(t, []string{"get_time", toolcatalog.ToolNameToolSearch, toolcatalog.ToolNameCallDeferredTool}, names)
	require.Contains(t, string(payload.Tools[2].Function.Parameters), `"tool_name"`)
	require.NotContains(t, string(prepared.Body), "defer_loading")
}

func TestPrepareRendersToolSearchResultWithDefinitionsAndWrapsDeferredCalls(t *testing.T) {
	client := Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "gpt-test",
	}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{Context: modelcontext.Bundle{
		Messages: []modelcontext.Message{
			userText(1, "weather?"),
			messageAtSequence(assistantToolCallMessage("mcc_1", "tcl_1"), 2),
			messageAtSequence(assistantToolCallMessage("mcc_2", "tcl_2"), 3),
		},
		ToolSpecs: deferredToolSpecs(),
		ToolResults: []modelcontext.ToolResultRef{
			{
				ToolCallID:         "tcl_1",
				ModelCallContextID: "mcc_1",
				ProviderCallID:     "call_search",
				Name:               toolcatalog.ToolNameToolSearch,
				Input:              json.RawMessage(`{"pattern":"weather"}`),
				Outcome:            executionstore.ToolResultOutcomeSucceeded,
				ContentParts: json.RawMessage(`[{"type":"structured_data","value":{"outcome":"succeeded"}},` +
					`{"type":"text","text":"Loaded 1 tool(s)"},` +
					`{"type":"structured_data","value":{"pattern":"weather","tool_names":["get_weather"],"total_deferred_tools":1}}]`),
			},
			{
				ToolCallID:         "tcl_2",
				ModelCallContextID: "mcc_2",
				ProviderCallID:     "call_weather",
				Name:               "get_weather",
				Input:              json.RawMessage(`{"city":"Tokyo"}`),
				Outcome:            executionstore.ToolResultOutcomeSucceeded,
				ContentParts:       json.RawMessage(`[{"type":"structured_data","value":{"outcome":"succeeded"}},{"type":"text","text":"21C"}]`),
			},
		},
	}})
	require.NoError(t, err)
	var payload struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(prepared.Body, &payload))
	require.Len(t, payload.Messages, 5)
	require.Equal(t, "tool", payload.Messages[2].Role)
	require.Equal(t, "call_search", payload.Messages[2].ToolCallID)
	require.JSONEq(
		t,
		`{"pattern":"weather","tool_names":["get_weather"],"total_deferred_tools":1,`+
			`"tools":[{"name":"get_weather","description":"Get the weather.","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}]}`,
		payload.Messages[2].Content,
	)
	require.Equal(t, "assistant", payload.Messages[3].Role)
	require.Len(t, payload.Messages[3].ToolCalls, 1)
	require.Equal(t, toolcatalog.ToolNameCallDeferredTool, payload.Messages[3].ToolCalls[0].Function.Name)
	require.JSONEq(
		t,
		`{"tool_name":"get_weather","arguments":{"city":"Tokyo"}}`,
		payload.Messages[3].ToolCalls[0].Function.Arguments,
	)
	require.Equal(t, "call_weather", payload.Messages[4].ToolCallID)
}

func TestRespondUnwrapsCallDeferredToolAndKeepsWrapperInReplay(t *testing.T) {
	body := `{"id":"chatcmpl_1","model":"gpt-served","choices":[{"index":0,"message":` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call_weather","type":"function",` +
		`"function":{"name":"call_deferred_tool","arguments":"{\"tool_name\":\"get_weather\",\"arguments\":{\"city\":\"Tokyo\"}}"}}]},` +
		`"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	client := testRespondClient(server)
	resp, err := client.Respond(context.Background(), model.Request{
		ProviderRequest: json.RawMessage(`{"model":"gpt-test","messages":"x","stream":true}`),
	})
	require.NoError(t, err)
	calls := resp.ToolCalls()
	require.Len(t, calls, 1)
	require.Equal(t, "call_weather", calls[0].ID)
	require.Equal(t, "get_weather", calls[0].Name)
	require.JSONEq(t, `{"city":"Tokyo"}`, string(calls[0].Input))
	require.Contains(t, string(resp.ProviderReplay), `"name":"call_deferred_tool"`)

	var replay chatResponseMessage
	require.NoError(t, json.Unmarshal(resp.ProviderReplay, &replay))
	semantics, ok := chatReplaySemantics(replay)
	require.True(t, ok)
	require.Len(t, semantics, 1)
	require.Equal(t, "get_weather", semantics[0].name)
	require.JSONEq(t, `{"city":"Tokyo"}`, string(semantics[0].arguments))
}

func TestUnwrapDeferredToolCallLeavesMalformedWrappersAlone(t *testing.T) {
	name, arguments := unwrapDeferredToolCall(toolcatalog.ToolNameCallDeferredTool, json.RawMessage(`{"arguments":{}}`))
	require.Equal(t, toolcatalog.ToolNameCallDeferredTool, name)
	require.JSONEq(t, `{"arguments":{}}`, string(arguments))

	name, arguments = unwrapDeferredToolCall(toolcatalog.ToolNameCallDeferredTool, json.RawMessage(`{"tool_name":"get_weather"}`))
	require.Equal(t, "get_weather", name)
	require.JSONEq(t, `{}`, string(arguments))

	name, arguments = unwrapDeferredToolCall("run_command", json.RawMessage(`{"command":"ls"}`))
	require.Equal(t, "run_command", name)
	require.JSONEq(t, `{"command":"ls"}`, string(arguments))
}
