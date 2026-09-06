package openaichatcompletions

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func TestOutputLimitReasonsBeforeToolValidation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		variant   modelprotocol.APIVariant
		finish    string
		native    any
		arguments string
		output    int
		wantLimit bool
		wantError bool
	}{
		{"router rewritten length", modelprotocol.APIVariantOpenRouter, "tool_calls", "length", "{", 8192, true, false},
		{
			"router native max tokens",
			modelprotocol.APIVariantOpenRouter,
			"tool_calls",
			"max_tokens",
			"{",
			8192,
			true,
			false,
		},
		{
			"router native max output",
			modelprotocol.APIVariantOpenRouter,
			"tool_calls",
			"max_output_tokens",
			"{",
			8192,
			true,
			false,
		},
		{
			"router native uppercase zero usage",
			modelprotocol.APIVariantOpenRouter,
			"tool_calls",
			"MAX_TOKENS",
			"{",
			0,
			true,
			false,
		},
		{"router missing normalized reason", modelprotocol.APIVariantOpenRouter, "", "length", "{", 8192, true, false},
		{"generic normalized length", modelprotocol.APIVariantDefault, "length", nil, "{", 8192, true, false},
		{
			"other route ignores native reason",
			modelprotocol.APIVariantDefault,
			"tool_calls",
			"length",
			"{",
			8192,
			false,
			true,
		},
		{
			"malformed without limit evidence",
			modelprotocol.APIVariantOpenRouter,
			"tool_calls",
			"stop",
			"{",
			8192,
			false,
			true,
		},
		{
			"complete call at same usage",
			modelprotocol.APIVariantOpenRouter,
			"tool_calls",
			"stop",
			"{}",
			8192,
			false,
			false,
		},
		{
			"invalid optional native telemetry",
			modelprotocol.APIVariantOpenRouter,
			"tool_calls",
			17,
			"{}",
			8192,
			false,
			false,
		},
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
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					// OpenRouter can report the native reason with final usage, after
					// the normalized tool_calls reason and all argument deltas.
					last, marshalErr := json.Marshal(map[string]any{
						"id": "chatcmpl_limit", "usage": usage, "choices": []any{lastChoice},
					})
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					response, err = consumeChatCompletionsStream(t,
						chatCompletionsSSE(string(first), string(last), "[DONE]"), &chatRecordingSink{}, tc.variant)
				} else {
					body, marshalErr := json.Marshal(map[string]any{
						"id": "chatcmpl_limit", "usage": usage, "choices": []any{map[string]any{
							"index": 0, "message": message, "finish_reason": tc.finish, "native_finish_reason": tc.native,
						}},
					})
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					response, err = (protocol{client: Client{APIVariant: tc.variant}}).ParseResponse(
						context.Background(), route.Response{StatusCode: http.StatusOK, Body: body})
				}
				if tc.wantError {
					providerErr, ok := model.ClassifyError(err)
					if !ok ||
						providerErr.Code != "malformed_success_response" ||
						!model.IsAmbiguousProviderOutcome(err) {
						t.Fatalf("malformed response lost its classification: %+v, %v", providerErr, err)
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					if tc.wantLimit {
						if response.StopReason != model.StopReasonMaxTokens ||
							response.HasToolCalls() ||
							len(response.ProviderReplay) != 0 {
							t.Fatalf("truncated response retained executable calls/replay or lost cause: %+v", response)
						}
					} else if response.StopReason != model.StopReasonToolUse || !response.HasToolCalls() {
						t.Fatalf("complete tool call was discarded: %+v", response)
					}
					if response.Text() != "partial" || response.Usage.OutputTokens != tc.output {
						t.Fatalf("partial text/usage lost: %+v", response)
					}
				}
				if tc.variant == modelprotocol.APIVariantOpenRouter {
					native, _ := tc.native.(string)
					if response.ProviderMetadata.OpenRouter.FinishReason != tc.finish ||
						response.ProviderMetadata.OpenRouter.NativeFinishReason != native {
						t.Fatalf("finish evidence lost: %+v", response.ProviderMetadata)
					}
				}
			})
		}
	}
}
