package anthropicmessages

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func TestRespondParsesToolUseStopReasonAndUsage(t *testing.T) {
	var sent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "test-key" || r.Header.Get("Anthropic-Version") == "" {
			t.Fatalf("missing anthropic headers")
		}
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Fatalf("Accept = %q, want text/event-stream", r.Header.Get("Accept"))
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		sent = string(body)
		w.Header().Set("Request-Id", "req_123")
		_, _ = w.Write(
			[]byte(
				`{"id":"msg_1","model":"claude-served","content":[{"type":"text","text":"I'll run it."},{"type":"tool_use","id":"toolu_1","name":"run_command","input":{"command":"cat a.txt"}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":3,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":1},"cache_read_input_tokens":7}}`,
			),
		)
	}))
	defer server.Close()
	client := testRespondClient(server)
	stored := json.RawMessage(
		`{"model":"claude-test","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}`,
	)
	resp, err := client.Respond(context.Background(), model.Request{ProviderRequest: stored})
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	var sentBody map[string]any
	if err := json.Unmarshal([]byte(sent), &sentBody); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if sentBody["model"] != "claude-test" || sentBody["stream"] != true || sentBody["max_tokens"] != float64(64) {
		t.Fatalf("streaming wire body = %v, want exact prepared fields", sentBody)
	}
	if sent != string(stored) {
		t.Fatalf("sent bytes = %s, want exact prepared bytes %s", sent, stored)
	}
	toolCalls := resp.ToolCalls()
	if resp.StopReason != model.StopReasonToolUse ||
		len(toolCalls) != 1 ||
		toolCalls[0].ID != "toolu_1" ||
		toolCalls[0].Name != "run_command" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.ServedProviderModelSlug != "claude-served" {
		t.Fatalf("served provider model slug = %q, want provider response model slug", resp.ServedProviderModelSlug)
	}
	if resp.ProviderMetadata.Anthropic.CacheCreation != (modelenvelope.AnthropicCacheCreation{
		Ephemeral5mInputTokens: 2,
		Ephemeral1hInputTokens: 1,
	}) {
		t.Fatalf("provider metadata = %+v, want cache_creation ttl breakdown", resp.ProviderMetadata)
	}
	if resp.Usage.InputTokens != 20 || resp.Usage.UncachedInputTokens != 10 || resp.Usage.OutputTokens != 5 ||
		resp.Usage.CacheWriteTokens != 3 ||
		resp.Usage.CacheReadTokens != 7 {
		t.Fatalf("usage not normalized: %+v", resp.Usage)
	}
	batchRaw := resp.ProviderReplay
	if !strings.Contains(string(batchRaw), `"id":"toolu_1"`) {
		t.Fatalf("replay batch missing tool_use block: %s", batchRaw)
	}
	var replayBlocks []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(batchRaw, &replayBlocks); err != nil ||
		len(replayBlocks) != 2 ||
		replayBlocks[0].Type != "text" ||
		replayBlocks[1].Type != "tool_use" {
		t.Fatalf("text/tool-use replay order = %+v err=%v", replayBlocks, err)
	}
}

func TestRespondPreservesOrderedTextContentParts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(
			[]byte(
				`{"id":"msg_1","content":[{"type":"text","text":"first"},{"type":"text","text":"second"}],"stop_reason":"end_turn"}`,
			),
		)
	}))
	defer server.Close()
	client := testRespondClient(server)
	resp, err := client.Respond(context.Background(), model.Request{ProviderRequest: json.RawMessage(`{"messages":[]}`)})
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if len(resp.Content) != 2 || resp.Content[0].Text != "first" || resp.Content[1].Text != "second" {
		t.Fatalf("ordered content parts not preserved: %+v", resp.Content)
	}
}

func TestRespondEmitsThinkingAsReasoningPartAndBatchReplay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(
			[]byte(
				`{"id":"msg_1","content":[{"type":"thinking","thinking":"reasoning step","signature":"sig_1"},{"type":"tool_use","id":"toolu_1","name":"run_command","input":{"command":"cat a.txt"}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`,
			),
		)
	}))
	defer server.Close()
	client := testRespondClient(server)
	resp, err := client.Respond(context.Background(), model.Request{ProviderRequest: json.RawMessage(`{"messages":[]}`)})
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if len(resp.Content) != 2 {
		t.Fatalf("expected reasoning + tool_call parts, got %+v", resp.Content)
	}
	if resp.Content[0].Type != "reasoning" || resp.Content[0].Text != "reasoning step" {
		t.Fatalf("visible thinking not surfaced as reasoning content part: %+v", resp.Content[0])
	}
	if resp.Content[1].Type != "tool_call" {
		t.Fatalf("tool_call part missing: %+v", resp.Content)
	}
	batchRaw := resp.ProviderReplay
	if !json.Valid(batchRaw) {
		t.Fatalf("expected message_content_blocks replay envelope, got %s", resp.ProviderReplay)
	}
	if !strings.Contains(string(batchRaw), `"signature":"sig_1"`) ||
		!strings.Contains(string(batchRaw), `"type":"thinking"`) ||
		!strings.Contains(string(batchRaw), `"type":"tool_use"`) {
		t.Fatalf("batch replay dropped thinking signature or tool_use: %s", batchRaw)
	}
}

func TestRespondPreservesThinkingReplayWithoutToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(
			`{"id":"msg_1","content":[` +
				`{"type":"thinking","thinking":"reasoning step","signature":"sig_1"},` +
				`{"type":"text","text":"done"}],"stop_reason":"end_turn"}`,
		))
	}))
	defer server.Close()

	client := testRespondClient(server)
	resp, err := client.Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"messages":[]}`)},
	)
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if len(resp.ToolCalls()) != 0 || resp.Text() != "done" {
		t.Fatalf("unexpected text-only response: %+v", resp)
	}
	batchRaw := resp.ProviderReplay
	if !json.Valid(batchRaw) || !strings.Contains(string(batchRaw), `"signature":"sig_1"`) {
		t.Fatalf("text-only response lost thinking replay: %s", resp.ProviderReplay)
	}

	replay := testProviderReplay(
		"claude-test",
		modelprotocol.APIFormatAnthropicMessages,
		resp.ProviderReplay,
	)
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{Messages: []modelcontext.Message{{
			Role:                 modelprotocol.RoleAssistant,
			Sequence:             1,
			ModelCallContextID:   "mcc_reasoning",
			Content:              json.RawMessage(`[{"type":"reasoning","text":"reasoning step"},{"type":"text","text":"done"}]`),
			ProviderReplay:       replay.payload,
			ProviderReplaySource: replay.source,
		}}},
		Policy: model.RequestPolicy{MaxOutputTokens: 64},
	})
	if err != nil {
		t.Fatalf("prepare replay: %v", err)
	}
	body := string(prepared.Body)
	if !strings.Contains(body, `"type":"thinking"`) ||
		!strings.Contains(body, `"signature":"sig_1"`) ||
		!strings.Contains(body, `"text":"done"`) {
		t.Fatalf("text-only thinking replay not restored with assistant text: %s", body)
	}
}

func TestRespondPreservesRedactedThinkingSignatureWithoutVisibleText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(
			[]byte(
				`{"id":"msg_1","content":[{"type":"redacted_thinking","data":"opaque-blob"},{"type":"tool_use","id":"toolu_1","name":"run_command","input":{"command":"true"}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`,
			),
		)
	}))
	defer server.Close()
	client := testRespondClient(server)
	resp, err := client.Respond(context.Background(), model.Request{ProviderRequest: json.RawMessage(`{"messages":[]}`)})
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	for _, part := range resp.Content {
		if part.Type == "reasoning" {
			t.Fatalf("redacted_thinking must not emit a visible reasoning part: %+v", part)
		}
	}
	var toolCall model.ResponsePart
	for _, part := range resp.Content {
		if part.Type == "tool_call" {
			toolCall = part
		}
	}
	if toolCall.Type == "" {
		t.Fatalf("tool_call missing: %+v", resp.Content)
	}
	batchRaw := resp.ProviderReplay
	if !json.Valid(batchRaw) {
		t.Fatalf("expected message_content_blocks replay envelope, got %s", resp.ProviderReplay)
	}
	if !strings.Contains(string(batchRaw), `"data":"opaque-blob"`) {
		t.Fatalf("redacted_thinking data dropped from batch replay: %s", batchRaw)
	}
}

func TestUsageFromResponseDropsNegativeTokenBuckets(t *testing.T) {
	usage := usageFromResponse(usage{
		InputTokens:              10,
		OutputTokens:             3,
		CacheCreationInputTokens: -1,
	})
	if usage != (model.Usage{}) {
		t.Fatalf("usage = %+v, want empty usage for impossible cache creation token count", usage)
	}
}

func TestUsageFromResponseDropsOverflowingTokenTotal(t *testing.T) {
	usage := usageFromResponse(usage{
		InputTokens:              math.MaxInt,
		CacheCreationInputTokens: 1,
	})
	if usage != (model.Usage{}) {
		t.Fatalf("usage = %+v, want empty usage for overflowing input token total", usage)
	}
}

func TestRespondMapsStopSequenceToEndTurn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(
			[]byte(
				`{"id":"msg_stop","content":[{"type":"text","text":"done"}],"stop_reason":"stop_sequence","usage":{"input_tokens":1,"output_tokens":1}}`,
			),
		)
	}))
	defer server.Close()
	client := testRespondClient(server)
	resp, err := client.Respond(
		context.Background(),
		model.Request{
			ProviderRequest: json.RawMessage(
				`{"model":"claude-test","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`,
			),
		},
	)
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if resp.StopReason != model.StopReasonEndTurn {
		t.Fatalf("stop reason = %q, want end_turn", resp.StopReason)
	}
}

func TestRespondTreatsMalformedCompleteAnthropicShapeAsRetryableUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Request-Id", "req_malformed_anthropic")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	_, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"messages":[]}`)},
	)
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindUnknown || providerErr.RequestID != "req_malformed_anthropic" {
		t.Fatalf("malformed complete response = %+v ok=%v err=%v", providerErr, ok, err)
	}
	if !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("malformed success response must be ambiguous: %T %v", err, err)
	}
}

func TestRespondRejectsUnsupportedAnthropicContentBlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(
			`{"id":"msg_unknown","content":[{"type":"server_tool_use","id":"srv_1"}],` +
				`"stop_reason":"end_turn"}`,
		))
	}))
	defer server.Close()

	_, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"messages":[]}`)},
	)
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Code != "malformed_success_response" || !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("unsupported content block = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestRespondRejectsMissingOrNullAnthropicToolInput(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
	}{
		{name: "missing"},
		{name: "null", input: `,"input":null`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(
					`{"id":"msg_bad_tool","content":[{"type":"tool_use","id":"toolu_1",` +
						`"name":"lookup"` + test.input + `}],"stop_reason":"tool_use"}`,
				))
			}))
			defer server.Close()

			response, err := testRespondClient(server).Respond(
				context.Background(),
				model.Request{ProviderRequest: json.RawMessage(`{"messages":[]}`)},
			)
			if err != nil {
				t.Fatalf("parse invalid tool input for boundary validation: %v", err)
			}
			if err := model.ValidateProviderResponse(response); err == nil ||
				!strings.Contains(err.Error(), "tool input must be a JSON object") {
				t.Fatalf("invalid tool input validation = %v", err)
			}
		})
	}
}

func TestRespondRejectsNULInSuccessfulAnthropicResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(
			`{"id":"msg_nul","content":[{"type":"text","text":"unsafe\u0000text"}],` +
				`"stop_reason":"end_turn"}`,
		))
	}))
	defer server.Close()

	_, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"messages":[]}`)},
	)
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Code != "malformed_success_response" || !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("NUL response = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestRespondTreatsAnthropicMaxTokensAsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(
			`{"id":"msg_partial","content":[{"type":"text","text":"partial"}],"stop_reason":"max_tokens"}`,
		))
	}))
	defer server.Close()

	resp, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"messages":[]}`)},
	)
	if err != nil {
		t.Fatalf("max_tokens response: %v", err)
	}
	if resp.StopReason != model.StopReasonMaxTokens || resp.Text() != "partial" {
		t.Fatalf("max_tokens response = %+v", resp)
	}
}
