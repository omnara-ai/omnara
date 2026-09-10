package openairesponses

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestOutputLimitKeepsMixedCallsAndReasoning(t *testing.T) {
	for _, tc := range []struct{ name, tool, status, arguments string }{
		{"malformed", "lookup", "completed", "{"},
		{"in progress", "lookup", "in_progress", "{}"},
		{"incomplete", "lookup", "incomplete", "{}"},
		{"missing name", "", "completed", "{}"},
	} {
		for _, streamed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, streamed), func(t *testing.T) {
				badItem, err := json.Marshal(map[string]any{
					"id": "fc_bad", "type": "function_call", "call_id": "call_bad",
					"name": tc.tool, "status": tc.status, "arguments": tc.arguments,
				})
				require.NoError(t, err)
				const reasoning = `{"id":"rs_1","type":"reasoning","encrypted_content":"opaque-reasoning",` +
					`"summary":[{"type":"summary_text","text":"thinking"}]}`
				body := []byte(
					`{"id":"resp_limit","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[` + reasoning +
						`,{"id":"fc_good","type":"function_call","call_id":"call_good","name":"lookup","arguments":"{\"query\":\"ok\"}","status":"completed"},` + string(badItem) + `]}`)
				var response model.Response
				if streamed {
					response, err = consumeOpenAIStream(t, openAISSE(
						[2]string{"response.output_item.added", `{"item":` + string(badItem) + `}`},
						[2]string{"response.output_item.done", `{"item":{"id":"fc_bad","type":"function_call"}}`},
						[2]string{"response.incomplete", `{"response":` + string(body) + `}`},
					), &recordingSink{})
				} else {
					response, err = (protocol{}).ParseResponse(context.Background(),
						route.Response{StatusCode: http.StatusOK, Body: body})
				}
				require.NoError(t, err)
				require.Equal(t, model.StopReasonMaxTokens, response.StopReason)
				require.Len(t, response.Content, 3)
				require.Empty(t, response.Content[1].ToolCallError)
				require.NotEmpty(t, response.Content[2].ToolCallError)
				replay := testProviderReplay("gpt-test", modelprotocol.APIFormatOpenAIResponses, response.ProviderReplay)
				message := openAIReplayMessage("mcc_1", replay)
				message.StopReason = response.StopReason
				message.Content = json.RawMessage(`[{"type":"reasoning","text":"thinking"},{"type":"tool_call","tool_call_id":"tcl_good"},{"type":"tool_call","tool_call_id":"tcl_bad"}]`)
				bundle := modelcontext.Bundle{Messages: []modelcontext.Message{message}}
				for index, id := range []string{"tcl_good", "tcl_bad"} {
					call := response.Content[index+1]
					result := modelcontext.ToolResultRef{
						ToolCallID: id, ModelCallContextID: "mcc_1", ProviderCallID: call.ProviderCallID,
						Name: call.ToolName, Input: call.ToolInput,
						Outcome: executionstore.ToolResultOutcomeSucceeded, ContentParts: json.RawMessage(`[{"type":"text","text":"found"}]`),
					}
					if index == 1 {
						result.Outcome = executionstore.ToolResultOutcomeFailed
						result.ContentParts = json.RawMessage(`[{"type":"structured_data","value":{"error":"retry","error_code":"malformed"}}]`)
					}
					bundle.ToolResults = append(bundle.ToolResults, result)
				}
				prepared, err := (Client{
					ModelProviderConfigID: testModelProviderConfigID, EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test",
				}).Prepare(
					context.Background(), model.PrepareInput{Context: bundle})
				require.NoError(t, err)
				var request struct {
					Input []json.RawMessage `json:"input"`
				}
				require.NoError(t, json.Unmarshal(prepared.Body, &request))
				require.Len(t, request.Input, 5)
				require.JSONEq(t, reasoning, string(request.Input[0]))
				require.Contains(t, string(request.Input[1]), `"call_id":"call_good"`)
				require.Contains(t, string(request.Input[2]), `"call_id":"call_bad"`)
				require.Contains(t, string(request.Input[2]), `"arguments":"{}"`)
				require.NotContains(t, string(request.Input[2]), `"status"`)
				require.Contains(t, string(request.Input[4]), `"call_id":"call_bad"`)
				require.Contains(t, string(request.Input[4]), "malformed")
				require.NotContains(t, string(prepared.Body), "Automatic Omnara harness notice")
			})
		}
	}
}

func TestOutputLimitTextReplayKeepsStableNotice(t *testing.T) {
	const reasoning = `{"id":"rs_1","type":"reasoning","status":"incomplete","encrypted_content":"opaque-reasoning"}`
	const partial = `{"id":"msg_1","type":"message","role":"assistant","status":"incomplete",` +
		`"content":[{"type":"output_text","text":"partial"}]}`
	response, err := (protocol{}).ParseResponse(context.Background(), route.Response{StatusCode: http.StatusOK,
		Body: []byte(
			`{"id":"resp_limit","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[` + reasoning + `,` + partial + `]}`)})
	require.NoError(t, err)
	replay := testProviderReplay("gpt-test", modelprotocol.APIFormatOpenAIResponses, response.ProviderReplay)
	message := openAIReplayMessage("mcc_1", replay)
	message.StopReason = response.StopReason
	message.Content = json.RawMessage(`[{"type":"text","text":"partial"}]`)
	bundle := modelcontext.Bundle{Messages: []modelcontext.Message{message}}
	client := Client{
		ModelProviderConfigID: testModelProviderConfigID, EndpointPath: testEndpointPath, ProviderModelSlug: "gpt-test",
	}
	var prefix []json.RawMessage
	for attempt := range 2 {
		prepared, err := client.Prepare(context.Background(), model.PrepareInput{Context: bundle})
		require.NoError(t, err)
		var request struct {
			Input []json.RawMessage `json:"input"`
		}
		require.NoError(t, json.Unmarshal(prepared.Body, &request))
		if attempt == 0 {
			require.Len(t, request.Input, 3)
			require.JSONEq(t, reasoning, string(request.Input[0]))
			require.JSONEq(t, partial, string(request.Input[1]))
			require.Contains(t, string(request.Input[2]), "Automatic Omnara harness notice")
			prefix = request.Input
		} else {
			require.Equal(t, prefix, request.Input[:len(prefix)])
		}
		bundle.Messages = append(bundle.Messages, modelcontext.Message{Role: modelprotocol.RoleUser, Sequence: 2,
			Content: json.RawMessage(`[{"type":"text","text":"later input"}]`)})
	}
}
