package openairesponses

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/providererrors"
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
		{Name: toolcatalog.ToolNameToolSearch, Description: "Search tools.", InputSchema: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"}}}`)},
	}
}

func toolSearchResult() modelcontext.ToolResultRef {
	return modelcontext.ToolResultRef{
		ToolCallID:         "tcl_1",
		ModelCallContextID: "mcc_1",
		ProviderCallID:     "call_search",
		Name:               toolcatalog.ToolNameToolSearch,
		Input:              json.RawMessage(`{"pattern":"weather"}`),
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		ContentParts: json.RawMessage(`[{"type":"structured_data","value":{"outcome":"succeeded"}},` +
			`{"type":"text","text":"Loaded 1 tool(s)"},` +
			`{"type":"structured_data","value":{"pattern":"weather","tool_names":["get_weather"],"total_deferred_tools":1}}]`),
	}
}

type responsesToolWire struct {
	Type         string          `json:"type"`
	Name         string          `json:"name"`
	Execution    string          `json:"execution"`
	DeferLoading bool            `json:"defer_loading"`
	Strict       *bool           `json:"strict"`
	Parameters   json.RawMessage `json:"parameters"`
}

func TestPrepareRendersClientToolSearchAndDeferredFunctions(t *testing.T) {
	client := Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "gpt-test",
	}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{Context: modelcontext.Bundle{
		Messages: []modelcontext.Message{
			{Role: modelprotocol.RoleUser, Sequence: 1, Content: json.RawMessage(`[{"type":"text","text":"weather?"}]`)},
			messageAtSequenceResponses(assistantToolCallMessage("mcc_1", "tcl_1"), 2),
		},
		ToolSpecs:   deferredToolSpecs(),
		ToolResults: []modelcontext.ToolResultRef{toolSearchResult()},
	}})
	require.NoError(t, err)
	var payload struct {
		Tools []responsesToolWire `json:"tools"`
		Input []json.RawMessage   `json:"input"`
	}
	require.NoError(t, json.Unmarshal(prepared.Body, &payload))
	require.Len(t, payload.Tools, 3)
	require.Equal(t, "function", payload.Tools[0].Type)
	require.Equal(t, "get_weather", payload.Tools[0].Name)
	require.True(t, payload.Tools[0].DeferLoading)
	require.NotNil(t, payload.Tools[0].Strict)
	require.Equal(t, "get_time", payload.Tools[1].Name)
	require.False(t, payload.Tools[1].DeferLoading)
	require.Equal(t, "tool_search", payload.Tools[2].Type)
	require.Equal(t, "client", payload.Tools[2].Execution)
	require.Empty(t, payload.Tools[2].Name)
	require.Nil(t, payload.Tools[2].Strict)
	require.JSONEq(t, `{"type":"object","properties":{"pattern":{"type":"string"}}}`, string(payload.Tools[2].Parameters))

	require.Len(t, payload.Input, 3)
	require.JSONEq(
		t,
		`{"type":"tool_search_call","execution":"client","call_id":"call_search","status":"completed","arguments":{"pattern":"weather"}}`,
		string(payload.Input[1]),
	)
	var output struct {
		Type      string              `json:"type"`
		Execution string              `json:"execution"`
		CallID    string              `json:"call_id"`
		Status    string              `json:"status"`
		Tools     []responsesToolWire `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(payload.Input[2], &output))
	require.Equal(t, "tool_search_output", output.Type)
	require.Equal(t, "client", output.Execution)
	require.Equal(t, "call_search", output.CallID)
	require.Equal(t, "completed", output.Status)
	require.Len(t, output.Tools, 1)
	require.Equal(t, "get_weather", output.Tools[0].Name)
	require.True(t, output.Tools[0].DeferLoading)
	require.JSONEq(t, `{"type":"object","properties":{"city":{"type":"string"}}}`, string(output.Tools[0].Parameters))
}

func TestPrepareKeepsToolSearchAsFunctionWithoutDeferredTools(t *testing.T) {
	client := Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "gpt-test",
	}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{Context: modelcontext.Bundle{
		Messages:  []modelcontext.Message{{Role: modelprotocol.RoleUser, Sequence: 1, Content: json.RawMessage(`[{"type":"text","text":"hi"}]`)}},
		ToolSpecs: modelcontext.LoadedToolSpecs(deferredToolSpecs()),
	}})
	require.NoError(t, err)
	var payload struct {
		Tools []responsesToolWire `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(prepared.Body, &payload))
	require.Len(t, payload.Tools, 2)
	require.Equal(t, "function", payload.Tools[1].Type)
	require.Equal(t, toolcatalog.ToolNameToolSearch, payload.Tools[1].Name)
}

func TestRespondParsesClientToolSearchCallAsToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"resp_1","model":"gpt-served","status":"completed","output":[` +
			`{"id":"tsc_1","type":"tool_search_call","execution":"client","call_id":"call_search","status":"completed","arguments":{"pattern":"weather"}}],` +
			`"usage":{"input_tokens":10,"output_tokens":2}}`))
	}))
	defer server.Close()
	client := testRespondClient(server)
	resp, err := client.Respond(context.Background(), model.Request{
		ProviderRequest: json.RawMessage(`{"model":"gpt-test","input":"x","stream":true}`),
	})
	require.NoError(t, err)
	calls := resp.ToolCalls()
	require.Len(t, calls, 1)
	require.Equal(t, "call_search", calls[0].ID)
	require.Equal(t, toolcatalog.ToolNameToolSearch, calls[0].Name)
	require.JSONEq(t, `{"pattern":"weather"}`, string(calls[0].Input))
	require.Equal(t, model.StopReasonToolUse, resp.StopReason)
	require.Contains(t, string(resp.ProviderReplay), `"type":"tool_search_call"`)
}

func TestRespondKeepsServerToolSearchAsProviderOnlyItem(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"resp_1","model":"gpt-served","status":"completed","output":[` +
			`{"id":"st_1","type":"tool_search_call","execution":"server","status":"completed","arguments":{"paths":["crm"]}},` +
			`{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"done"}]}],` +
			`"usage":{"input_tokens":10,"output_tokens":2}}`))
	}))
	defer server.Close()
	client := testRespondClient(server)
	resp, err := client.Respond(context.Background(), model.Request{
		ProviderRequest: json.RawMessage(`{"model":"gpt-test","input":"x","stream":true}`),
	})
	require.NoError(t, err)
	require.Empty(t, resp.ToolCalls())
	require.Equal(t, "done", resp.Text())
}

func TestClientToolSearchReplayMatchesCanonicalHistory(t *testing.T) {
	client := Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "gpt-test",
	}
	replay := testProviderReplay(
		"gpt-test",
		modelprotocol.APIFormatOpenAIResponses,
		json.RawMessage(`[{"id":"tsc_1","type":"tool_search_call","execution":"client","call_id":"call_search","status":"completed","arguments":{"pattern":"weather"}}]`),
	)
	assistant := withToolCallLinks(openAIReplayMessage("mcc_1", replay), "tcl_1")
	assistant.Sequence = 2
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{Context: modelcontext.Bundle{
		Messages: []modelcontext.Message{
			{Role: modelprotocol.RoleUser, Sequence: 1, Content: json.RawMessage(`[{"type":"text","text":"weather?"}]`)},
			assistant,
		},
		ToolSpecs:   deferredToolSpecs(),
		ToolResults: []modelcontext.ToolResultRef{toolSearchResult()},
	}})
	require.NoError(t, err)
	require.Contains(t, string(prepared.Body), `"id":"tsc_1"`)
}

func messageAtSequenceResponses(message modelcontext.Message, sequence int64) modelcontext.Message {
	message.Sequence = sequence
	return message
}

func TestRespondRewritesUnsupportedToolSearchError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(
			`{"error":{"message":"Tool 'tool_search' is not supported with gpt-4.1.",` +
				`"type":"invalid_request_error","param":"tools","code":null}}`,
		))
	}))
	defer server.Close()

	_, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)},
	)
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindInvalidRequest ||
		providerErr.Code != providererrors.DeferredToolsUnsupportedCode ||
		providerErr.Message != providererrors.DeferredToolsUnsupportedMessage {
		t.Fatalf("openai deferred tools error = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestStreamRewritesUnsupportedToolSearchError(t *testing.T) {
	stream := openAISSE([2]string{
		"response.error",
		`{"type":"response.error","error":{"type":"invalid_request_error",` +
			`"message":"Tool 'tool_search' is not supported with gpt-5-mini."}}`,
	})
	_, err := consumeOpenAIStream(t, stream, &recordingSink{})
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Code != providererrors.DeferredToolsUnsupportedCode ||
		providerErr.Message != providererrors.DeferredToolsUnsupportedMessage {
		t.Fatalf("openai stream deferred tools error = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestClientToolSearchFailureReplaysAsEmptyToolSearchOutput(t *testing.T) {
	client := Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "gpt-test",
	}
	replay := testProviderReplay(
		"gpt-test",
		modelprotocol.APIFormatOpenAIResponses,
		json.RawMessage(`[{"id":"tsc_1","type":"tool_search_call","execution":"client","call_id":"call_search","status":"completed","arguments":{"pattern":"("}}]`),
	)
	assistant := withToolCallLinks(openAIReplayMessage("mcc_1", replay), "tcl_1")
	assistant.Sequence = 2
	failed := toolSearchResult()
	failed.Input = json.RawMessage(`{"pattern":"("}`)
	failed.Outcome = executionstore.ToolResultOutcomeFailed
	failed.ContentParts = json.RawMessage(`[{"type":"structured_data","value":{"error_code":"invalid_tool_input",` +
		`"error":"invalid regular expression pattern","message":"invalid regular expression pattern","retryable":true}}]`)
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{Context: modelcontext.Bundle{
		Messages: []modelcontext.Message{
			{Role: modelprotocol.RoleUser, Sequence: 1, Content: json.RawMessage(`[{"type":"text","text":"weather?"}]`)},
			assistant,
		},
		ToolSpecs:   deferredToolSpecs(),
		ToolResults: []modelcontext.ToolResultRef{failed},
	}})
	require.NoError(t, err)
	var body struct {
		Input []struct {
			Type   string            `json:"type"`
			CallID string            `json:"call_id"`
			Tools  []json.RawMessage `json:"tools"`
		} `json:"input"`
	}
	require.NoError(t, json.Unmarshal(prepared.Body, &body))
	var outputs []string
	for _, item := range body.Input {
		if item.CallID == "call_search" && item.Type != "tool_search_call" {
			outputs = append(outputs, item.Type)
			require.Empty(t, item.Tools)
		}
	}
	require.Equal(t, []string{"tool_search_output"}, outputs)
}
