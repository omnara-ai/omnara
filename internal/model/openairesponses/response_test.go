package openairesponses

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func TestRespondSendsStoredBytesAndParsesToolCalls(t *testing.T) {
	var sent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("missing auth header")
		}
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Fatalf("Accept = %q, want text/event-stream", r.Header.Get("Accept"))
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		sent = string(body)
		_, _ = w.Write(
			[]byte(
				`{"id":"resp_1","model":"gpt-served","status":"completed","output":[{"id":"fc_1","type":"function_call","call_id":"call_1","name":"run_command","arguments":"{\"command\":\"cat a.txt\"}"},{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":40,"cache_write_tokens":10},"output_tokens_details":{"reasoning_tokens":5}}}`,
			),
		)
	}))
	defer server.Close()
	client := testRespondClient(server)
	stored := json.RawMessage(`{"model":"gpt-test","input":"exact","stream":true}`)
	resp, err := client.Respond(context.Background(), model.Request{ProviderRequest: stored})
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	var sentBody map[string]any
	if err := json.Unmarshal([]byte(sent), &sentBody); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if sentBody["model"] != "gpt-test" || sentBody["input"] != "exact" || sentBody["stream"] != true {
		t.Fatalf("streaming wire body = %v, want exact prepared fields", sentBody)
	}
	if sent != string(stored) {
		t.Fatalf("sent bytes = %s, want exact prepared bytes %s", sent, stored)
	}
	if resp.ID != "resp_1" ||
		len(resp.Content) != 2 ||
		resp.Content[1].Text != "done" ||
		len(resp.ToolCalls()) != 1 ||
		resp.ToolCalls()[0].ID != "call_1" ||
		resp.ToolCalls()[0].Name != "run_command" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.ServedProviderModelSlug != "gpt-served" {
		t.Fatalf("served provider model slug = %q, want provider response model slug", resp.ServedProviderModelSlug)
	}
	if resp.StopReason != model.StopReasonToolUse {
		t.Fatalf("stop reason = %q, want tool_use", resp.StopReason)
	}
	if resp.Usage.InputTokens != 100 || resp.Usage.UncachedInputTokens != 50 || resp.Usage.OutputTokens != 20 ||
		resp.Usage.CacheReadTokens != 40 ||
		resp.Usage.CacheWriteTokens != 10 ||
		resp.Usage.ReasoningTokens != 5 {
		t.Fatalf("usage not normalized: %+v", resp.Usage)
	}
	replayItem := resp.ProviderReplay
	if !json.Valid(replayItem) || !strings.Contains(string(replayItem), `"type":"function_call"`) {
		t.Fatalf("expected provider replay envelope to preserve raw function call: %s", resp.ProviderReplay)
	}
	items := providerReplayItemsForTest(t, replayItem)
	var types []string
	for _, raw := range items {
		var item struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatalf("decode replay item: %v", err)
		}
		types = append(types, item.Type)
	}
	if strings.Join(types, ",") != "function_call,message" {
		t.Fatalf("function-call/message replay order = %v", types)
	}
}

func TestUsageFromResponseDropsIncoherentTokenDetails(t *testing.T) {
	for _, details := range []responsesTokenDetails{
		{CachedTokens: 11},
		{CachedTokens: 6, CacheWriteTokens: 5},
	} {
		usage := usageFromResponse(responsesUsage{
			InputTokens:        10,
			OutputTokens:       3,
			InputTokensDetails: details,
		})
		if usage != (model.Usage{}) {
			t.Fatalf("usage = %+v, want empty usage for impossible token details %+v", usage, details)
		}
	}
}

func TestRespondPreservesEncryptedReasoningItemsInFunctionCallReplay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		responseBody := `{"id":"resp_1","status":"completed","output":[` +
			`{"id":"rs_1","type":"reasoning","encrypted_content":"enc_1"},` +
			`{"id":"fc_1","type":"function_call","call_id":"call_1","name":"run_command",` +
			`"arguments":"{\"command\":\"cat a.txt\"}"}]}`
		_, _ = w.Write([]byte(responseBody))
	}))
	defer server.Close()
	client := testRespondClient(server)
	resp, err := client.Respond(context.Background(), model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)})
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if len(resp.ToolCalls()) != 1 {
		t.Fatalf("expected one tool call, got %+v", resp.ToolCalls())
	}
	replayItems := resp.ProviderReplay
	if !json.Valid(replayItems) {
		t.Fatalf("expected response_items replay envelope, got %s", resp.ProviderReplay)
	}
	var items []struct {
		Type             string `json:"type"`
		EncryptedContent string `json:"encrypted_content"`
	}
	for _, raw := range providerReplayItemsForTest(t, replayItems) {
		var item struct {
			Type             string `json:"type"`
			EncryptedContent string `json:"encrypted_content"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatalf("decode replay item: %v", err)
		}
		items = append(items, item)
	}
	if len(items) != 2 || items[0].Type != "reasoning" || items[0].EncryptedContent != "enc_1" ||
		items[1].Type != "function_call" {
		t.Fatalf("encrypted reasoning replay items not preserved: %s", replayItems)
	}
}

func TestRespondStoresWholeOutputWhenReasoningCannotBeReplayed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		responseBody := `{"id":"resp_1","status":"completed","output":[` +
			`{"id":"rs_1","type":"reasoning","summary":[]},` +
			`{"id":"fc_1","type":"function_call","call_id":"call_1","name":"run_command",` +
			`"arguments":"{\"command\":\"cat a.txt\"}"}]}`
		_, _ = w.Write([]byte(responseBody))
	}))
	defer server.Close()

	client := testRespondClient(server)
	resp, err := client.Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)},
	)
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if len(resp.ToolCalls()) != 1 || resp.ToolCalls()[0].ID != "call_1" {
		t.Fatalf("canonical tool call was not retained: %+v", resp.ToolCalls())
	}
	if !json.Valid(resp.ProviderReplay) ||
		!strings.Contains(string(resp.ProviderReplay), `"type":"reasoning"`) {
		t.Fatalf("whole provider output was not retained: %s", resp.ProviderReplay)
	}
	if _, ok := responseReplaySemantics(providerReplayItemsForTest(t, resp.ProviderReplay)); ok {
		t.Fatalf("reasoning without encrypted content must not be reusable: %s", resp.ProviderReplay)
	}
}

func TestRespondStoresWholeOutputWhenEmptyMessageHasNoCanonicalContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		responseBody := `{"id":"resp_1","status":"completed","output":[` +
			`{"id":"rs_1","type":"reasoning","encrypted_content":"enc_1"},` +
			`{"id":"msg_empty","type":"message","content":[]},` +
			`{"id":"fc_1","type":"function_call","call_id":"call_1","name":"run_command",` +
			`"arguments":"{\"command\":\"true\"}"}]}`
		_, _ = w.Write([]byte(responseBody))
	}))
	defer server.Close()

	client := testRespondClient(server)
	resp, err := client.Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)},
	)
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if len(resp.ToolCalls()) != 1 || resp.ToolCalls()[0].ID != "call_1" {
		t.Fatalf("canonical tool call was not retained: %+v", resp.ToolCalls())
	}
	items := providerReplayItemsForTest(t, resp.ProviderReplay)
	var retained struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if len(items) != 3 || json.Unmarshal(items[1], &retained) != nil ||
		retained.ID != "msg_empty" || retained.Type != "message" {
		t.Fatalf("whole provider output was not retained: %s", resp.ProviderReplay)
	}
}

func TestRespondPreservesEncryptedReasoningReplayWithoutToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"status":"completed",
			"output":[
				{"id":"rs_1","type":"reasoning","encrypted_content":"enc_1"},
				{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"done"}]}
			]
		}`))
	}))
	defer server.Close()

	client := testRespondClient(server)
	resp, err := client.Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)},
	)
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if len(resp.ToolCalls()) != 0 || resp.Text() != "done" {
		t.Fatalf("unexpected text-only response: %+v", resp)
	}
	replayItems := resp.ProviderReplay
	if !json.Valid(replayItems) || !strings.Contains(string(replayItems), `"encrypted_content":"enc_1"`) {
		t.Fatalf("text-only response lost encrypted reasoning replay: %s", resp.ProviderReplay)
	}

	replay := testProviderReplay(
		"gpt-test",
		modelprotocol.APIFormatOpenAIResponses,
		resp.ProviderReplay,
	)
	message := openAIReplayMessage("mcc_reasoning", replay)
	message.Content = json.RawMessage(`[{"type":"text","text":"done"}]`)
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{Messages: []modelcontext.Message{message}},
	})
	if err != nil {
		t.Fatalf("prepare replay: %v", err)
	}
	var payload struct {
		Input []json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode prepared payload: %v", err)
	}
	if len(payload.Input) != 2 ||
		!strings.Contains(string(payload.Input[0]), `"type":"reasoning"`) ||
		!strings.Contains(string(payload.Input[0]), `"encrypted_content":"enc_1"`) ||
		!strings.Contains(string(payload.Input[1]), `"text":"done"`) {
		t.Fatalf("text-only reasoning replay not restored before assistant text: %s", prepared.Body)
	}
}

func TestRespondPreservesEncryptedReasoningItemsFromParallelFunctionCallsReplay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		responseBody := `{"id":"resp_1","status":"completed","output":[` +
			`{"id":"rs_1","type":"reasoning","encrypted_content":"enc_1"},` +
			`{"id":"fc_1","type":"function_call","call_id":"call_1","name":"run_command",` +
			`"arguments":"{\"command\":\"one\"}"},` +
			`{"id":"fc_2","type":"function_call","call_id":"call_2","name":"run_command",` +
			`"arguments":"{\"command\":\"two\"}"}]}`
		_, _ = w.Write([]byte(responseBody))
	}))
	defer server.Close()
	client := testRespondClient(server)
	resp, err := client.Respond(context.Background(), model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)})
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if len(resp.ToolCalls()) != 2 {
		t.Fatalf("expected two tool calls, got %+v", resp.ToolCalls())
	}
	replayItems := resp.ProviderReplay
	if !json.Valid(replayItems) {
		t.Fatalf("expected response_items replay envelope, got %s", resp.ProviderReplay)
	}
	var items []struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
	}
	for _, raw := range providerReplayItemsForTest(t, replayItems) {
		var item struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatalf("decode replay item: %v", err)
		}
		items = append(items, item)
	}
	if len(items) != 3 ||
		items[0].Type != "reasoning" ||
		items[1].CallID != "call_1" ||
		items[2].CallID != "call_2" {
		t.Fatalf("parallel replay items not preserved: %s", replayItems)
	}
}

func TestRespondMapsIncompleteStopReasons(t *testing.T) {
	tests := []struct {
		reason string
		want   model.StopReason
	}{
		{reason: "max_output_tokens", want: model.StopReasonMaxTokens},
		{reason: "content_filter", want: model.StopReasonContentFilter},
	}
	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(
					[]byte(
						`{"id":"resp_incomplete","status":"incomplete","incomplete_details":{"reason":"` + tt.reason + `"},"output":[],"usage":{"input_tokens":100,"output_tokens":0}}`,
					),
				)
			}))
			defer server.Close()
			client := testRespondClient(server)
			resp, err := client.Respond(context.Background(), model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)})
			if err != nil {
				t.Fatalf("respond: %v", err)
			}
			if resp.StopReason != tt.want {
				t.Fatalf("stop reason = %q, want %q", resp.StopReason, tt.want)
			}
		})
	}
}

func TestRespondKeepsMaxTokenTextAndDropsIncompleteFunctionCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(
			`{"id":"resp_incomplete","status":"incomplete",` +
				`"incomplete_details":{"reason":"max_output_tokens"},"output":[` +
				`{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"partial"}]},` +
				`{"id":"fc_1","type":"function_call","status":"incomplete",` +
				`"call_id":"call_1","name":"lookup","arguments":""}]}`,
		))
	}))
	defer server.Close()

	response, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)},
	)
	if err != nil {
		t.Fatalf("respond with truncated function call: %v", err)
	}
	if response.StopReason != model.StopReasonMaxTokens || response.Text() != "partial" ||
		response.HasToolCalls() || len(response.ProviderReplay) != 0 {
		t.Fatalf("truncated response = %+v, want partial text without tool call or replay", response)
	}
}

func TestRespondMapsFailedStatusToProviderError(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		want       model.ErrorKind
		code       string
		wantStatus int
	}{
		{
			name: "server",
			body: `{"id":"resp_failed","status":"failed",` +
				`"error":{"code":"server_error","message":"backend failed"},"output":[]}`,
			want: model.ErrorKindProviderUnavailable,
			code: "server_error",
		},
		{
			name: "billing quota",
			body: `{"id":"resp_failed","status":"failed",` +
				`"error":{"code":"insufficient_quota","message":"billing quota exceeded"},"output":[]}`,
			want: model.ErrorKindBillingAccount,
			code: "insufficient_quota",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			client := testRespondClient(server)
			_, err := client.Respond(context.Background(), model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)})
			providerErr, ok := model.ClassifyError(err)
			if !ok || providerErr.Kind != tt.want || providerErr.Code != tt.code {
				t.Fatalf("classified error = %+v ok=%v err=%v", providerErr, ok, err)
			}
			if tt.wantStatus != 0 && providerErr.StatusCode != tt.wantStatus {
				t.Fatalf("provider status = %d, want %d", providerErr.StatusCode, tt.wantStatus)
			}
		})
	}
}

func TestRespondTreatsNonTerminalStatusAsAmbiguous(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req_in_progress")
		_, _ = w.Write([]byte(
			`{"id":"resp_in_progress","status":"in_progress","request_id":"req_in_progress","output":[]}`,
		))
	}))
	defer server.Close()
	client := testRespondClient(server)
	_, err := client.Respond(context.Background(), model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)})
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindTransient || providerErr.Code != "in_progress" ||
		providerErr.RequestID != "req_in_progress" {
		t.Fatalf("nonterminal response error = %+v ok=%v err=%v", providerErr, ok, err)
	}
	if !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("nonterminal response = %T %v, want ambiguous outcome", err, err)
	}
}

func TestRespondMapsMessageRefusalContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		responseBody := `{"id":"resp_refusal","status":"completed","output":[` +
			`{"id":"msg_refusal","type":"message","content":[{"type":"refusal",` +
			`"refusal":"I can't help with that."}]}],` +
			`"usage":{"input_tokens":1,"output_tokens":1}}`
		_, _ = w.Write([]byte(responseBody))
	}))
	defer server.Close()
	client := testRespondClient(server)
	resp, err := client.Respond(context.Background(), model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)})
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if resp.StopReason != model.StopReasonRefusal ||
		len(resp.Content) != 1 ||
		resp.Content[0].Text != "I can't help with that." {
		t.Fatalf("unexpected refusal response: %+v", resp)
	}
}

func TestRespondEmitsVisibleReasoningSummaryContentPart(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		responseBody := `{"id":"resp_1","status":"completed","output":[` +
			`{"id":"rs_1","type":"reasoning",` +
			`"summary":[{"type":"summary_text","text":"Thinking about it"},` +
			`{"type":"summary_text","text":"then acting"}],` +
			`"encrypted_content":"enc_1"},` +
			`{"id":"fc_1","type":"function_call","call_id":"call_1","name":"run_command",` +
			`"arguments":"{\"command\":\"true\"}"}]}`
		_, _ = w.Write([]byte(responseBody))
	}))
	defer server.Close()
	client := testRespondClient(server)
	resp, err := client.Respond(context.Background(), model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)})
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if len(resp.Content) != 2 {
		t.Fatalf("expected reasoning + tool_call parts, got %+v", resp.Content)
	}
	if resp.Content[0].Type != "reasoning" || resp.Content[0].Text != "Thinking about it\n\nthen acting" {
		t.Fatalf(
			"visible reasoning summary not emitted as reasoning content part: %+v",
			resp.Content[0],
		)
	}
	if resp.Content[1].Type != "tool_call" {
		t.Fatalf("tool_call part missing: %+v", resp.Content)
	}
	replayItems := resp.ProviderReplay
	if !json.Valid(replayItems) {
		t.Fatalf("expected response_items replay envelope, got %s", resp.ProviderReplay)
	}
	if !strings.Contains(string(replayItems), `"encrypted_content":"enc_1"`) {
		t.Fatalf("encrypted_content dropped from replay batch: %s", replayItems)
	}
}

func TestRespondDoesNotEmitReasoningPartWithoutSummary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		responseBody := `{"id":"resp_1","status":"completed","output":[` +
			`{"id":"rs_1","type":"reasoning","encrypted_content":"enc_only"},` +
			`{"id":"fc_1","type":"function_call","call_id":"call_1","name":"run_command",` +
			`"arguments":"{\"command\":\"true\"}"}]}`
		_, _ = w.Write([]byte(responseBody))
	}))
	defer server.Close()
	client := testRespondClient(server)
	resp, err := client.Respond(context.Background(), model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)})
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	for _, part := range resp.Content {
		if part.Type == "reasoning" {
			t.Fatalf("reasoning part emitted for encrypted-only reasoning item: %+v", part)
		}
	}
	if len(resp.ProviderReplay) == 0 {
		t.Fatalf("response missing replay: %+v", resp)
	}
	replayItems := resp.ProviderReplay
	if !json.Valid(replayItems) || !strings.Contains(string(replayItems), `"encrypted_content":"enc_only"`) {
		t.Fatalf("encrypted-only reasoning replay lost: %s", replayItems)
	}
}

func TestRespondClassifiesProviderErrors(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       model.ErrorKind
	}{
		{
			name:       "context window",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"message":"This model's context length was exceeded","code":"context_length_exceeded"}}`,
			want:       model.ErrorKindContextWindow,
		},
		{
			name:       "encrypted replay rejected",
			statusCode: http.StatusBadRequest,
			body: `{"error":{"message":"The encrypted content could not be verified. ` +
				`Reason: Encrypted content could not be decrypted or parsed.","code":"invalid_encrypted_content"}}`,
			want: model.ErrorKindReplayRejected,
		},
		{
			name:       "rate limit",
			statusCode: http.StatusTooManyRequests,
			body:       `{"error":{"message":"slow down","code":"rate_limit_exceeded"}}`,
			want:       model.ErrorKindRateLimit,
		},
		{
			name:       "billing quota",
			statusCode: http.StatusTooManyRequests,
			body: `{"error":{"message":"You exceeded your current quota, ` +
				`please check your plan and billing details.","code":"insufficient_quota"}}`,
			want: model.ErrorKindBillingAccount,
		},
		{
			name:       "auth",
			statusCode: http.StatusUnauthorized,
			body:       `{"error":{"message":"bad key"}}`,
			want:       model.ErrorKindAuth,
		},
		{
			name:       "invalid",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"message":"bad request"}}`,
			want:       model.ErrorKindInvalidRequest,
		},
		{
			name:       "generic token prose is not context overflow",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"message":"max tokens must be positive","code":"invalid_request_error"}}`,
			want:       model.ErrorKindInvalidRequest,
		},
		{
			name:       "output cap error is not context overflow",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"message":"output token cap reached","code":"max_tokens_exceeded"}}`,
			want:       model.ErrorKindInvalidRequest,
		},
		{
			name:       "request too large",
			statusCode: http.StatusRequestEntityTooLarge,
			body:       `request entity too large`,
			want:       model.ErrorKindPayloadTooLarge,
		},
		{
			name:       "unavailable",
			statusCode: http.StatusBadGateway,
			body:       `{"error":{"message":"upstream"}}`,
			want:       model.ErrorKindProviderUnavailable,
		},
		{
			name:       "unavailable status beats quota prose",
			statusCode: http.StatusServiceUnavailable,
			body:       `{"error":{"message":"quota service temporarily unavailable"}}`,
			want:       model.ErrorKindProviderUnavailable,
		},
		{
			name:       "malformed rate limit",
			statusCode: http.StatusTooManyRequests,
			body:       `{"error":`,
			want:       model.ErrorKindRateLimit,
		},
		{
			name:       "partially decoded malformed rate limit",
			statusCode: http.StatusTooManyRequests,
			body:       `{"error":{"code":"context_length_exceeded"},`,
			want:       model.ErrorKindRateLimit,
		},
		{
			name:       "malformed unavailable",
			statusCode: http.StatusServiceUnavailable,
			body:       `<html>unavailable`,
			want:       model.ErrorKindProviderUnavailable,
		},
		{
			name:       "partially decoded malformed unavailable",
			statusCode: http.StatusServiceUnavailable,
			body:       `{"error":{"code":"invalid_request_error"},`,
			want:       model.ErrorKindProviderUnavailable,
		},
		{
			name:       "redirect",
			statusCode: http.StatusTemporaryRedirect,
			body:       `redirect refused`,
			want:       model.ErrorKindInvalidRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			client := testRespondClient(server)
			_, err := client.Respond(context.Background(), model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)})
			providerErr, ok := model.ClassifyError(err)
			if !ok || providerErr.Kind != tt.want || providerErr.Source != server.URL ||
				providerErr.StatusCode != tt.statusCode {
				t.Fatalf("classified error = %+v ok=%v err=%v", providerErr, ok, err)
			}
		})
	}
}

func TestRespondPreservesProviderErrorRequestIDAndRetryAfter(t *testing.T) {
	tests := []struct {
		name          string
		header        http.Header
		body          string
		wantRequestID string
		wantDelta     int64
		wantEvidence  bool
	}{
		{
			name: "headers take precedence",
			header: http.Header{
				"X-Request-Id": []string{"req_header"},
				"Retry-After":  []string{"17"},
			},
			body: `{"request_id":"req_body","retry_after":"Wed, 21 Oct 2015 07:28:00 GMT",` +
				`"error":{"message":"slow down","code":"rate_limit_exceeded"}}`,
			wantRequestID: "req_header",
			wantDelta:     17,
			wantEvidence:  true,
		},
		{
			name: "body fallback",
			body: `{"request_id":"req_body","retry_after":"Wed, 21 Oct 2015 07:28:00 GMT",` +
				`"error":{"message":"slow down","code":"rate_limit_exceeded"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for name, values := range tt.header {
					for _, value := range values {
						w.Header().Add(name, value)
					}
				}
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			_, err := testRespondClient(server).Respond(
				context.Background(),
				model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)},
			)
			providerErr, ok := model.ClassifyError(err)
			if !ok || providerErr.RequestID != tt.wantRequestID {
				t.Fatalf("provider evidence = %+v ok=%v", providerErr, ok)
			}
			if !tt.wantEvidence {
				if providerErr.RetryAfter != nil {
					t.Fatalf("Retry-After = %+v, want body evidence ignored", providerErr.RetryAfter)
				}
			} else if providerErr.RetryAfter == nil || providerErr.RetryAfter.DeltaSeconds == nil ||
				*providerErr.RetryAfter.DeltaSeconds != tt.wantDelta {
				t.Fatalf("Retry-After = %+v, want %d seconds", providerErr.RetryAfter, tt.wantDelta)
			}
		})
	}
}

func TestRespondClassifiesCompleteMid200ResponsesError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(
			`{"request_id":"req_mid_200","retry_after":7,` +
				`"error":{"code":"server_error","message":"upstream unavailable"}}`,
		))
	}))
	defer server.Close()

	_, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)},
	)
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindProviderUnavailable ||
		providerErr.RequestID != "" || providerErr.RetryAfter != nil {
		t.Fatalf("mid-200 provider error = %+v ok=%v err=%v", providerErr, ok, err)
	}
	if model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("complete mid-200 provider error must be explicit: %T %v", err, err)
	}
}

func TestRespondTreatsMalformedCompleteResponseAsRetryableUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req_malformed")
		_, _ = w.Write([]byte(`{"id":`))
	}))
	defer server.Close()

	_, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)},
	)
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindUnknown || providerErr.RequestID != "req_malformed" {
		t.Fatalf("malformed response error = %+v ok=%v err=%v", providerErr, ok, err)
	}
	if !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("malformed success response must be ambiguous, got %T %v", err, err)
	}
}

func TestRespondAllowsOptionalOutputItemID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(
			`{"id":"resp_items","status":"completed","output":` +
				`[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}]}`,
		))
	}))
	defer server.Close()

	response, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)},
	)
	if err != nil {
		t.Fatalf("response without optional item id: %v", err)
	}
	calls := response.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "lookup" {
		t.Fatalf("tool calls = %+v", calls)
	}
}

func TestRespondRejectsDuplicateOutputItemID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(
			`{"id":"resp_items","status":"completed","output":` +
				`[{"id":"fc_1","type":"function_call","call_id":"call_1",` +
				`"name":"lookup","arguments":"{}"},` +
				`{"id":"fc_1","type":"function_call","call_id":"call_2",` +
				`"name":"lookup","arguments":"{}"}]}`,
		))
	}))
	defer server.Close()

	_, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)},
	)
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Code != "malformed_success_response" ||
		!model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("output item identity = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestRespondRejectsMalformedFunctionCall(t *testing.T) {
	tests := []struct {
		name string
		item string
	}{
		{name: "missing call id", item: `{"id":"fc_1","type":"function_call","name":"lookup","arguments":"{}"}`},
		{name: "missing name", item: `{"type":"function_call","call_id":"call_1","arguments":"{}"}`},
		{name: "missing arguments", item: `{"type":"function_call","call_id":"call_1","name":"lookup"}`},
		{name: "null arguments", item: `{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"null"}`},
		{name: "invalid arguments", item: `{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(
					`{"id":"resp_items","status":"completed","output":[` + test.item + `]}`,
				))
			}))
			defer server.Close()

			_, err := testRespondClient(server).Respond(
				context.Background(),
				model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)},
			)
			providerErr, ok := model.ClassifyError(err)
			if !ok || providerErr.Code != "malformed_success_response" ||
				!model.IsAmbiguousProviderOutcome(err) {
				t.Fatalf("malformed function call = %+v ok=%v err=%v", providerErr, ok, err)
			}
		})
	}
}

func TestRespondRejectsUnsupportedOutputShapes(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{
			name:   "item type",
			output: `[{"id":"search_1","type":"future_search_call"}]`,
		},
		{
			name: "message content type",
			output: `[{"id":"msg_1","type":"message",` +
				`"content":[{"type":"future_output","text":"answer"}]}]`,
		},
		{
			name: "reasoning summary type",
			output: `[{"id":"rs_1","type":"reasoning",` +
				`"summary":[{"type":"future_summary","text":"reasoning"}]}]`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(
					`{"id":"resp_unsupported","status":"completed","output":` + test.output + `}`,
				))
			}))
			defer server.Close()

			_, err := testRespondClient(server).Respond(
				context.Background(),
				model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)},
			)
			providerErr, ok := model.ClassifyError(err)
			if !ok || providerErr.Code != "malformed_success_response" ||
				!model.IsAmbiguousProviderOutcome(err) {
				t.Fatalf("unsupported output shape = %+v ok=%v err=%v", providerErr, ok, err)
			}
		})
	}
}

func TestRespondRejectsNULInSuccessfulResponsesPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(
			`{"id":"resp_nul","status":"completed","output":[{"id":"msg_1","type":"message",` +
				`"content":[{"type":"output_text","text":"unsafe\u0000text"}]}]}`,
		))
	}))
	defer server.Close()

	_, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)},
	)
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Code != "malformed_success_response" || !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("NUL response = %+v ok=%v err=%v", providerErr, ok, err)
	}
}
