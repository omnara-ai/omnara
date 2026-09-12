package openaichatcompletions

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/stretchr/testify/require"
)

func TestOutputLimitReasonsAndPerCallValidation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		variant    modelprotocol.APIVariant
		finish     string
		native     any
		arguments  string
		output     int
		wantReason model.StopReason
		refusal    string
	}{
		{"router rewritten length", modelprotocol.APIVariantOpenRouter,
			"tool_calls", "length", "{", 8192, model.StopReasonMaxTokens, ""},
		{"router native max tokens", modelprotocol.APIVariantOpenRouter,
			"tool_calls", "max_tokens", "{", 8192, model.StopReasonMaxTokens, ""},
		{"router native max output", modelprotocol.APIVariantOpenRouter,
			"tool_calls", "max_output_tokens", "{", 8192, model.StopReasonMaxTokens, ""},
		{"router uppercase zero usage", modelprotocol.APIVariantOpenRouter,
			"tool_calls", "MAX_TOKENS", "{", 0, model.StopReasonMaxTokens, ""},
		{"router missing normalized reason", modelprotocol.APIVariantOpenRouter,
			"", "length", "{", 8192, model.StopReasonMaxTokens, ""},
		{"generic normalized length", modelprotocol.APIVariantDefault,
			"length", nil, "{", 8192, model.StopReasonMaxTokens, ""},
		{"other route ignores native reason", modelprotocol.APIVariantDefault,
			"tool_calls", "length", "{", 8192, model.StopReasonToolUse, ""},
		{"malformed without cutoff", modelprotocol.APIVariantOpenRouter,
			"tool_calls", "stop", "{", 8192, model.StopReasonToolUse, ""},
		{"complete call at same usage", modelprotocol.APIVariantOpenRouter,
			"tool_calls", "stop", "{}", 8192, model.StopReasonToolUse, ""},
		{"invalid optional telemetry", modelprotocol.APIVariantOpenRouter,
			"tool_calls", 17, "{}", 8192, model.StopReasonToolUse, ""},
		{"explicit stop with tools", modelprotocol.APIVariantOpenRouter,
			"stop", "stop", "{}", 100, model.StopReasonEndTurn, ""},
		{"content filter takes precedence", modelprotocol.APIVariantOpenRouter,
			"content_filter", "length", "{}", 100, model.StopReasonContentFilter, ""},
		{"unknown reason takes precedence", modelprotocol.APIVariantOpenRouter,
			"unsupported", "length", "{}", 100, model.StopReasonUnknown, ""},
		{"refusal takes precedence", modelprotocol.APIVariantOpenRouter,
			"length", "length", "{}", 100, model.StopReasonRefusal, "Refused"},
	} {
		for _, mode := range []string{
			"response",
			"stream-late-native",
			"stream-same-chunk",
			"stream-repeated-normalized",
		} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				tool := map[string]any{
					"id": "call_partial", "type": "function",
					"function": map[string]any{"name": "run_command", "arguments": tc.arguments},
				}
				completeTool := map[string]any{
					"id":   "call_complete",
					"type": "function",
					"function": map[string]any{
						"name":      "run_command",
						"arguments": "{}",
					},
				}
				message := map[string]any{
					"role":    "assistant",
					"content": "partial",
					"refusal": tc.refusal,
					"tool_calls": []any{
						completeTool,
						tool,
					},
				}
				usage := map[string]any{"prompt_tokens": 100, "completion_tokens": tc.output}
				var response model.Response
				var err error
				if mode != "response" {
					completeTool["index"] = 0
					tool["index"] = 1
					firstChoice := map[string]any{"index": 0, "delta": message, "finish_reason": tc.finish}
					lastChoice := map[string]any{
						"index":                0,
						"delta":                map[string]any{},
						"native_finish_reason": tc.native,
					}
					if mode == "stream-same-chunk" {
						firstChoice["native_finish_reason"] = tc.native
						delete(lastChoice, "native_finish_reason")
					}
					if mode == "stream-repeated-normalized" {
						lastChoice["finish_reason"] = tc.finish
					}
					first, marshalErr := json.Marshal(map[string]any{
						"id": "chatcmpl_limit", "choices": []any{firstChoice},
					})
					require.NoError(t, marshalErr)
					// OpenRouter can report the native reason with final usage, after
					// the normalized tool_calls reason and all argument deltas.
					last, marshalErr := json.Marshal(map[string]any{
						"id": "chatcmpl_limit", "usage": usage, "choices": []any{lastChoice},
					})
					require.NoError(t, marshalErr)
					response, err = consumeChatCompletionsStream(t,
						chatCompletionsSSE(string(first), string(last), "[DONE]"), &chatRecordingSink{}, tc.variant)
				} else {
					body, marshalErr := json.Marshal(map[string]any{
						"id": "chatcmpl_limit", "usage": usage, "choices": []any{map[string]any{
							"index": 0, "message": message, "finish_reason": tc.finish, "native_finish_reason": tc.native,
						}},
					})
					require.NoError(t, marshalErr)
					response, err = (protocol{client: Client{APIVariant: tc.variant}}).ParseResponse(
						context.Background(), route.Response{StatusCode: http.StatusOK, Body: body})
				}
				require.NoError(t, err)
				require.Equal(t, tc.wantReason, response.StopReason)
				require.Len(t, response.Content, 3)
				require.Empty(t, response.Content[1].ToolCallError)
				require.Equal(t, tc.arguments != "{}", response.Content[2].ToolCallError != "")
				require.JSONEq(t, "{}", string(response.Content[2].ToolInput))
				require.NotEmpty(t, response.ProviderReplay)
				require.Contains(t, response.Text(), "partial")
				require.Equal(t, tc.output, response.Usage.OutputTokens)
			})
		}
	}
}
