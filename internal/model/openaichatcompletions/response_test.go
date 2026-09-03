package openaichatcompletions

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

func TestRespondSendsStoredBytesAndParsesToolCalls(t *testing.T) {
	var sent string
	body := `{"id":"chatcmpl_1","model":"gpt-served","choices":[{"index":0,"message":` +
		`{"role":"assistant","content":"done","tool_calls":[{"id":"call_1","type":"function",` +
		`"function":{"name":"run_command","arguments":"{\"command\":\"cat a.txt\"}"}}]},` +
		`"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":100,"completion_tokens":20,` +
		`"prompt_tokens_details":{"cached_tokens":40},` +
		`"completion_tokens_details":{"reasoning_tokens":5}}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("missing auth header")
		}
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q, want /chat/completions", r.URL.Path)
		}
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Fatalf("Accept = %q, want text/event-stream", r.Header.Get("Accept"))
		}
		requestBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		sent = string(requestBody)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	client := testRespondClient(server)
	stored := json.RawMessage(
		`{"model":"gpt-test","messages":"exact","stream":true,"stream_options":{"include_usage":true}}`,
	)
	resp, err := client.Respond(context.Background(), model.Request{ProviderRequest: stored})
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	var sentBody map[string]any
	if err := json.Unmarshal([]byte(sent), &sentBody); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if sentBody["model"] != "gpt-test" || sentBody["messages"] != "exact" || sentBody["stream"] != true {
		t.Fatalf("streaming wire body = %v, want exact prepared fields", sentBody)
	}
	streamOptions, ok := sentBody["stream_options"].(map[string]any)
	if !ok || streamOptions["include_usage"] != true {
		t.Fatalf("streaming wire options = %v", sentBody["stream_options"])
	}
	if sent != string(stored) {
		t.Fatalf("sent bytes = %s, want exact prepared bytes %s", sent, stored)
	}
	toolCalls := resp.ToolCalls()
	if resp.ID != "chatcmpl_1" ||
		resp.Text() != "done" ||
		len(toolCalls) != 1 ||
		toolCalls[0].ID != "call_1" ||
		toolCalls[0].Name != "run_command" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if string(toolCalls[0].Input) != `{"command":"cat a.txt"}` {
		t.Fatalf("tool input = %s", toolCalls[0].Input)
	}
	if resp.ServedProviderModelSlug != "gpt-served" {
		t.Fatalf("served provider model slug = %q, want provider response model slug", resp.ServedProviderModelSlug)
	}
	if resp.StopReason != model.StopReasonToolUse {
		t.Fatalf("stop reason = %q, want tool_use", resp.StopReason)
	}
	if resp.Usage.InputTokens != 100 ||
		resp.Usage.UncachedInputTokens != 60 ||
		resp.Usage.OutputTokens != 20 ||
		resp.Usage.CacheReadTokens != 40 ||
		resp.Usage.CacheWriteTokens != 0 ||
		resp.Usage.ReasoningTokens != 5 {
		t.Fatalf("usage not normalized: %+v", resp.Usage)
	}
	replayItem := resp.ProviderReplay
	if !json.Valid(replayItem) || !strings.Contains(string(replayItem), `"id":"call_1"`) {
		t.Fatalf("expected provider replay envelope to preserve raw tool call: %s", resp.ProviderReplay)
	}
}

func TestRespondRejectsMalformedToolArgumentsAtModelBoundary(t *testing.T) {
	for _, test := range []struct {
		name      string
		arguments string
	}{
		{name: "missing"},
		{name: "empty", arguments: `,"arguments":""`},
		{name: "null", arguments: `,"arguments":null`},
		{name: "string null", arguments: `,"arguments":"null"`},
		{name: "malformed", arguments: `,"arguments":"{"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := `{"id":"chatcmpl_1","model":"gpt-served","choices":[{"index":0,"message":` +
				`{"role":"assistant","tool_calls":[{"id":"call_bad","type":"function",` +
				`"function":{"name":"run_command"` + test.arguments + `}}]},"finish_reason":"tool_calls"}]}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			_, err := testRespondClient(server).Respond(
				context.Background(),
				model.Request{ProviderRequest: json.RawMessage(`{"model":"gpt-test"}`)},
			)
			if err == nil || !strings.Contains(err.Error(), "tool input must be a JSON object") {
				t.Fatalf("malformed tool arguments error = %v", err)
			}
		})
	}
}

func TestParseResponseStoresRequestValidAssistantReplay(t *testing.T) {
	body := json.RawMessage(`{"id":"chatcmpl_1","model":"gpt-served","choices":[{"index":0,` +
		`"message":{"role":"assistant","content":null,` +
		`"annotations":[{"type":"url_citation"}],"provider_debug":"drop-me",` +
		`"tool_calls":[{"id":"call_1","type":"function",` +
		`"function":{"name":"parameterless_tool","arguments":"{}",` +
		`"description":"drop-me","parameters":{}}}]},` +
		`"finish_reason":"tool_calls"}]}`)
	resp, err := (protocol{}).ParseResponse(context.Background(), route.Response{
		StatusCode: http.StatusOK,
		Body:       body,
	})
	if err != nil {
		t.Fatalf("parse response: %v", err)
	}
	var replay map[string]json.RawMessage
	if err := json.Unmarshal(resp.ProviderReplay, &replay); err != nil {
		t.Fatalf("decode replay: %v", err)
	}
	if len(replay) != 3 ||
		string(replay["role"]) != `"assistant"` ||
		string(replay["content"]) != "null" {
		t.Fatalf("replay contains response-only or fabricated fields: %s", resp.ProviderReplay)
	}
	var calls []struct {
		Function map[string]json.RawMessage `json:"function"`
	}
	if err := json.Unmarshal(replay["tool_calls"], &calls); err != nil {
		t.Fatalf("decode replay tool calls: %v", err)
	}
	if len(calls) != 1 ||
		len(calls[0].Function) != 2 ||
		string(calls[0].Function["name"]) != `"parameterless_tool"` ||
		string(calls[0].Function["arguments"]) != `"{}"` {
		t.Fatalf("parameterless tool input was not normalized: %s", replay["tool_calls"])
	}
}

func TestParseResponseRecordsOpenRouterReportedCost(t *testing.T) {
	body := json.RawMessage(`{"id":"chatcmpl_cost","model":"openai/gpt-5","choices":[{"index":0,` +
		`"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":2,"cost":1.2500e-5}}`)

	openRouter := protocol{client: Client{APIVariant: modelprotocol.APIVariantOpenRouter}}
	response, err := openRouter.ParseResponse(context.Background(), route.Response{
		StatusCode: http.StatusOK,
		Body:       body,
	})
	if err != nil {
		t.Fatalf("parse OpenRouter response: %v", err)
	}
	if response.ProviderReportedCostUSD != "0.0000125" {
		t.Fatalf("provider-reported cost = %q, want 0.0000125", response.ProviderReportedCostUSD)
	}

	response, err = (protocol{}).ParseResponse(context.Background(), route.Response{
		StatusCode: http.StatusOK,
		Body:       body,
	})
	if err != nil {
		t.Fatalf("parse default response: %v", err)
	}
	if response.ProviderReportedCostUSD != "" {
		t.Fatalf("default OpenAI-compatible cost = %q, want unknown", response.ProviderReportedCostUSD)
	}
}

func TestParseResponseDistinguishesFreeAndUnavailableOpenRouterCost(t *testing.T) {
	p := protocol{client: Client{APIVariant: modelprotocol.APIVariantOpenRouter}}
	for _, test := range []struct {
		name string
		cost string
		want string
	}{
		{name: "free", cost: `,"cost":0`, want: "0"},
		{name: "missing"},
		{name: "null", cost: `,"cost":null`},
		{name: "invalid negative", cost: `,"cost":-0.01`},
		{name: "unrecognized shape", cost: `,"cost":{"total":0.01}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := json.RawMessage(`{"id":"chatcmpl_cost","model":"openai/gpt-5","choices":[{"index":0,` +
				`"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],` +
				`"usage":{"prompt_tokens":1,"completion_tokens":1` + test.cost + `}}`)
			response, err := p.ParseResponse(context.Background(), route.Response{
				StatusCode: http.StatusOK,
				Body:       body,
			})
			if err != nil {
				t.Fatalf("parse response: %v", err)
			}
			if string(response.ProviderReportedCostUSD) != test.want {
				t.Fatalf("provider-reported cost = %q, want %q", response.ProviderReportedCostUSD, test.want)
			}
		})
	}
}

func TestParseResponseReportsOpenRouterAccountingIssues(t *testing.T) {
	for _, test := range []struct {
		name              string
		usage             string
		wantCost          modelenvelope.ProviderReportedCostUSD
		wantLevel         string
		wantReasonField   string
		wantReason        string
		forbiddenLogValue string
	}{
		{
			name:              "negative cost",
			usage:             `"cost":-0.01`,
			wantLevel:         "warn",
			wantReasonField:   "model_response.provider_reported_cost_usd.unavailable_reason",
			wantReason:        "invalid_provider_value",
			forbiddenLogValue: "-0.01",
		},
		{
			name:              "unrecognized cost shape",
			usage:             `"cost":{"total":0.01}`,
			wantLevel:         "warn",
			wantReasonField:   "model_response.provider_reported_cost_usd.unavailable_reason",
			wantReason:        "invalid_provider_value",
			forbiddenLogValue: `{"total":0.01}`,
		},
		{
			name:            "missing BYOK identity preserves known account cost",
			usage:           `"cost":0.0000125,"cost_details":[]`,
			wantCost:        "0.0000125",
			wantLevel:       "info",
			wantReasonField: "model_response.provider_reported_cost_usd.accounting_limitation",
			wantReason:      "byok_state_missing",
		},
		{
			name:            "malformed BYOK identity preserves known account cost",
			usage:           `"cost":0.0000125,"is_byok":"unknown","cost_details":[]`,
			wantCost:        "0.0000125",
			wantLevel:       "warn",
			wantReasonField: "model_response.provider_reported_cost_usd.accounting_limitation",
			wantReason:      "invalid_byok_state",
		},
		{
			name:            "missing BYOK upstream cost",
			usage:           `"cost":0,"is_byok":true`,
			wantLevel:       "warn",
			wantReasonField: "model_response.provider_reported_cost_usd.unavailable_reason",
			wantReason:      "byok_cost_component_missing",
		},
		{
			name:            "invalid BYOK cost details",
			usage:           `"cost":0,"is_byok":true,"cost_details":[]`,
			wantLevel:       "warn",
			wantReasonField: "model_response.provider_reported_cost_usd.unavailable_reason",
			wantReason:      "invalid_provider_value",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			base := logpkg.WithLogger(
				context.Background(),
				slog.New(logpkg.NewJSONHandler(&logs, nil)),
			)
			event := logpkg.NewEvent(base, "model.call")
			ctx := logpkg.WithEvent(base, event)
			p := protocol{client: Client{APIVariant: modelprotocol.APIVariantOpenRouter}}
			body := json.RawMessage(`{"id":"chatcmpl_cost","model":"openai/gpt-5","choices":[{"index":0,` +
				`"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],` +
				`"usage":{"prompt_tokens":1,"completion_tokens":1,` + test.usage + `}}`)

			response, err := p.ParseResponse(ctx, route.Response{StatusCode: http.StatusOK, Body: body})
			if err != nil {
				t.Fatalf("parse response with OpenRouter accounting issue: %v", err)
			}
			if response.Text() != "done" || response.ProviderReportedCostUSD != test.wantCost {
				t.Fatalf(
					"response text=%q cost=%q, want done and %q",
					response.Text(),
					response.ProviderReportedCostUSD,
					test.wantCost,
				)
			}
			event.Done(context.Background())

			var record map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record); err != nil {
				t.Fatalf("decode model-call event: %v", err)
			}
			if record["level"] != test.wantLevel || record[test.wantReasonField] != test.wantReason {
				t.Fatalf("provider accounting event = %+v", record)
			}
			if test.forbiddenLogValue != "" && strings.Contains(logs.String(), test.forbiddenLogValue) {
				t.Fatalf("model-call event included raw provider cost: %s", logs.String())
			}
		})
	}
}

func TestOpenRouterAccountingDiagnosticsPreserveModelCallErrorLevel(t *testing.T) {
	var logs bytes.Buffer
	base := logpkg.WithLogger(
		context.Background(),
		slog.New(logpkg.NewJSONHandler(&logs, nil)),
	)
	event := logpkg.NewEvent(base, "model.call")
	ctx := logpkg.WithEvent(base, event)
	p := protocol{client: Client{APIVariant: modelprotocol.APIVariantOpenRouter}}
	body := json.RawMessage(`{"id":"chatcmpl_cost","choices":[],"usage":{"cost":0.0000125}}`)

	_, err := p.ParseResponse(ctx, route.Response{StatusCode: http.StatusOK, Body: body})
	if err == nil {
		t.Fatal("response without a choice was accepted")
	}
	logpkg.Error(ctx, err)
	event.Done(context.Background())

	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record); err != nil {
		t.Fatalf("decode model-call event: %v", err)
	}
	if record["level"] != "error" ||
		record["model_response.provider_reported_cost_usd.accounting_limitation"] != "byok_state_missing" {
		t.Fatalf("model-call event = %+v", record)
	}
}

func TestParseResponseIgnoresUnrecognizedDefaultVariantCost(t *testing.T) {
	body := json.RawMessage(`{"id":"chatcmpl_cost","model":"gpt-test","choices":[{"index":0,` +
		`"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":1,"completion_tokens":1,"cost":{"total":0.01}}}`)
	response, err := (protocol{}).ParseResponse(context.Background(), route.Response{
		StatusCode: http.StatusOK,
		Body:       body,
	})
	if err != nil {
		t.Fatalf("parse response with unrecognized cost extension: %v", err)
	}
	if response.ProviderReportedCostUSD != "" {
		t.Fatalf("default OpenAI-compatible cost = %q, want unknown", response.ProviderReportedCostUSD)
	}
}

func TestChatReplayRejectsNonObjectToolInput(t *testing.T) {
	_, ok := chatReplaySemantics(chatResponseMessage{
		Role: chatRoleAssistant,
		ToolCalls: []json.RawMessage{
			json.RawMessage(
				`{"id":"call_1","type":"function","function":{"name":"parameterless_tool","arguments":"null"}}`,
			),
		},
	})
	if ok {
		t.Fatal("replay with non-object tool input was accepted")
	}
}

func TestRespondPreservesOpenRouterReasoningDetailsInAssistantReplay(t *testing.T) {
	body := `{"id":"chatcmpl_reasoning","model":"anthropic/claude-sonnet-4","choices":[` +
		`{"index":0,"message":{"role":"assistant","reasoning_details":[` +
		`{"type":"reasoning.summary","summary":"Checked the command plan",` +
		`"id":"rs_1","format":"anthropic-claude-v1","index":0},` +
		`{"type":"reasoning.encrypted","data":"enc_1",` +
		`"id":"rs_2","format":"anthropic-claude-v1","index":1}],` +
		`"tool_calls":[{"id":"call_1","type":"function",` +
		`"function":{"name":"run_command","arguments":"{\"command\":\"pwd\"}"}}]},` +
		`"finish_reason":"tool_calls"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	client := Client{EndpointPath: testEndpointPath,
		Auth:              route.BearerToken{Token: "test-key"},
		BaseURL:           server.URL,
		ProviderModelSlug: "anthropic/claude-sonnet-4",
		HTTPClient:        server.Client(),
		APIVariant:        modelprotocol.APIVariantOpenRouter,
	}
	resp, err := client.Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"model":"anthropic/claude-sonnet-4"}`)},
	)
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if len(resp.Content) != 2 ||
		resp.Content[0].Type != model.ResponsePartTypeReasoning ||
		resp.Content[0].Text != "Checked the command plan" ||
		resp.Content[1].Type != model.ResponsePartTypeToolCall {
		t.Fatalf("expected reasoning summary then tool call content, got %+v", resp.Content)
	}
	toolCalls := resp.ToolCalls()
	if len(toolCalls) != 1 {
		t.Fatalf("expected one tool call, got %+v", toolCalls)
	}
	replayItem := resp.ProviderReplay
	if !json.Valid(replayItem) {
		t.Fatalf("expected assistant_message replay envelope, got %s", resp.ProviderReplay)
	}
	if !strings.Contains(string(replayItem), `"reasoning_details"`) ||
		!strings.Contains(string(replayItem), `"data":"enc_1"`) {
		t.Fatalf("reasoning_details not preserved in assistant replay: %s", replayItem)
	}

	replay := testProviderReplay(
		"anthropic/claude-sonnet-4",
		modelprotocol.APIFormatOpenAIChatCompletions,
		modelprotocol.APIVariantOpenRouter,
		resp.ProviderReplay,
	)
	replayMessage := withToolCallLinks(chatReplayMessage("mcc_1", replay), "call_1")
	replayMessage.Content = json.RawMessage(
		`[{"type":"reasoning","text":"Checked the command plan"},{"type":"tool_call","tool_call_id":"call_1"}]`,
	)
	prepared, err := (Client{
		ModelProviderConfigID: testModelProviderConfigID,
		EndpointPath:          testEndpointPath,
		ProviderModelSlug:     "anthropic/claude-sonnet-4",
		APIVariant:            modelprotocol.APIVariantOpenRouter,
	}).Prepare(
		context.Background(),
		model.PrepareInput{Context: modelcontext.Bundle{
			Messages: []modelcontext.Message{replayMessage},
			ToolResults: []modelcontext.ToolResultRef{{
				ToolCallID:         "call_1",
				ModelCallContextID: "mcc_1",
				ProviderCallID:     "call_1",
				Name:               "run_command",
				Input:              json.RawMessage(`{"command":"pwd"}`),
				ContentParts:       json.RawMessage(`[{"type":"text","text":"/workspace"}]`),
			}}},
		},
	)
	if err != nil {
		t.Fatalf("prepare replay: %v", err)
	}
	var payload struct {
		Messages []struct {
			Role             string            `json:"role"`
			ReasoningDetails []json.RawMessage `json:"reasoning_details"`
			ToolCalls        []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(prepared.Body, &payload); err != nil {
		t.Fatalf("decode prepared payload: %v", err)
	}
	if len(payload.Messages) != 3 ||
		payload.Messages[1].Role != string(chatRoleAssistant) ||
		len(payload.Messages[1].ReasoningDetails) != 2 ||
		len(payload.Messages[1].ToolCalls) != 1 ||
		payload.Messages[1].ToolCalls[0].ID != "call_1" {
		t.Fatalf("assistant reasoning replay not rebuilt in next request: %s", prepared.Body)
	}
}

func TestRespondSurfacesReasoningBeforeVisibleText(t *testing.T) {
	body := `{"id":"chatcmpl_reasoning_text","model":"anthropic/claude-sonnet-4",` +
		`"choices":[{"index":0,"message":{"role":"assistant","reasoning_details":[` +
		`{"type":"reasoning.summary","summary":"Checked the answer",` +
		`"id":"rs_1","format":"anthropic-claude-v1","index":0}],` +
		`"content":"visible answer"},"finish_reason":"stop"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	client := testRespondClient(server)
	resp, err := client.Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"model":"gpt-test"}`)},
	)
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if len(resp.Content) != 2 ||
		resp.Content[0].Type != model.ResponsePartTypeReasoning ||
		resp.Content[0].Text != "Checked the answer" ||
		resp.Content[1].Type != model.ResponsePartTypeText ||
		resp.Content[1].Text != "visible answer" {
		t.Fatalf("expected reasoning summary then visible text, got %+v", resp.Content)
	}
	replayItem := resp.ProviderReplay
	if !json.Valid(replayItem) || !strings.Contains(string(replayItem), `"reasoning_details"`) {
		t.Fatalf("text-only response lost reasoning replay: %s", resp.ProviderReplay)
	}

	message := chatReplayMessage("mcc_reasoning", testProviderReplay(
		"gpt-test",
		modelprotocol.APIFormatOpenAIChatCompletions,
		modelprotocol.APIVariantDefault,
		resp.ProviderReplay,
	))
	message.Content = json.RawMessage(
		`[{"type":"reasoning","text":"Checked the answer"},{"type":"text","text":"visible answer"}]`,
	)
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: modelcontext.Bundle{Messages: []modelcontext.Message{message}},
	})
	if err != nil {
		t.Fatalf("prepare replay: %v", err)
	}
	if !strings.Contains(string(prepared.Body), `"reasoning_details"`) ||
		!strings.Contains(string(prepared.Body), `"content":"visible answer"`) {
		t.Fatalf("text-only reasoning replay not restored with assistant text: %s", prepared.Body)
	}
}

func TestRespondTreatsMalformedCompleteChatShapeAsRetryableUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req_malformed_chat")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	_, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"model":"gpt-test"}`)},
	)
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindUnknown || providerErr.RequestID != "req_malformed_chat" {
		t.Fatalf("malformed complete response = %+v ok=%v err=%v", providerErr, ok, err)
	}
	if !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("malformed success response must be ambiguous: %T %v", err, err)
	}
}

func TestRespondRejectsAlternativeChatChoices(t *testing.T) {
	tests := []struct {
		name    string
		choices string
	}{
		{
			name: "multiple",
			choices: `[{"index":0,"message":{"role":"assistant","content":"first"},"finish_reason":"stop"},` +
				`{"index":1,"message":{"role":"assistant","content":"second"},"finish_reason":"stop"}]`,
		},
		{
			name:    "nonzero index",
			choices: `[{"index":1,"message":{"role":"assistant","content":"second"},"finish_reason":"stop"}]`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(
					`{"id":"chatcmpl_alternatives","model":"gpt-test","choices":` + test.choices + `}`,
				))
			}))
			defer server.Close()

			response, err := testRespondClient(server).Respond(
				context.Background(),
				model.Request{ProviderRequest: json.RawMessage(`{"model":"gpt-test"}`)},
			)
			providerErr, ok := model.ClassifyError(err)
			if !ok || providerErr.Code != "malformed_success_response" ||
				!model.IsAmbiguousProviderOutcome(err) {
				t.Fatalf("alternative choices = %+v ok=%v err=%v", providerErr, ok, err)
			}
			if response.ID != "chatcmpl_alternatives" || response.ServedProviderModelSlug != "gpt-test" {
				t.Fatalf("safe response evidence = %+v", response)
			}
		})
	}
}

func TestRespondRejectsMalformedSuccessfulChatContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "object", content: `{"text":"answer"}`},
		{name: "empty array", content: `[]`},
		{name: "unknown array part", content: `[{"type":"future_text","text":"answer"}]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(
					`{"id":"chatcmpl_content","model":"gpt-test","choices":[{"index":0,` +
						`"message":{"role":"assistant","content":` + test.content + `},"finish_reason":"stop"}],` +
						`"usage":{"prompt_tokens":7,"completion_tokens":3}}`,
				))
			}))
			defer server.Close()

			response, err := testRespondClient(server).Respond(
				context.Background(),
				model.Request{ProviderRequest: json.RawMessage(`{"model":"gpt-test"}`)},
			)
			providerErr, ok := model.ClassifyError(err)
			if !ok || providerErr.Code != "malformed_success_response" ||
				!model.IsAmbiguousProviderOutcome(err) {
				t.Fatalf("malformed content = %+v ok=%v err=%v", providerErr, ok, err)
			}
			if response.ID != "chatcmpl_content" || response.ServedProviderModelSlug != "gpt-test" ||
				response.Usage.InputTokens != 7 || response.Usage.OutputTokens != 3 {
				t.Fatalf("safe response evidence = %+v", response)
			}
		})
	}
}

func TestRespondRejectsNULInSuccessfulChatResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(
			`{"id":"chatcmpl_nul","choices":[{"index":0,"message":{"role":"assistant",` +
				`"content":"unsafe\u0000text"},"finish_reason":"stop"}]}`,
		))
	}))
	defer server.Close()

	_, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"model":"gpt-test"}`)},
	)
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Code != "malformed_success_response" || !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("NUL response = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestRespondTreatsLengthFinishAsSuccessfulMaxTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(
			`{"id":"chatcmpl_partial","choices":[{"index":0,"message":{"role":"assistant",` +
				`"reasoning":"still thinking","content":"partial","tool_calls":[{"id":"call_partial",` +
				`"type":"function","function":{"name":"run_command","arguments":"{"}}]},` +
				`"finish_reason":"length"}]}`,
		))
	}))
	defer server.Close()
	resp, err := testRespondClient(server).Respond(
		context.Background(),
		model.Request{ProviderRequest: json.RawMessage(`{"model":"gpt-test"}`)},
	)
	if err != nil {
		t.Fatalf("length response: %v", err)
	}
	if resp.StopReason != model.StopReasonMaxTokens || resp.Text() != "partial" ||
		resp.HasToolCalls() || len(resp.ProviderReplay) != 0 || len(resp.Content) != 2 ||
		resp.Content[0].Type != model.ResponsePartTypeReasoning ||
		resp.Content[0].Text != "still thinking" {
		t.Fatalf("length response = %+v", resp)
	}
}

func TestUsageFromResponseNormalizesCacheWriteTokensWithinPromptTokens(t *testing.T) {
	usage := usageFromResponse(chatUsage{
		PromptTokens:     1000,
		CompletionTokens: 60,
		PromptTokensDetails: chatTokenDetails{
			CachedTokens:     100,
			CacheWriteTokens: 800,
		},
	})
	if usage.InputTokens != 1000 ||
		usage.UncachedInputTokens != 100 ||
		usage.CacheReadTokens != 100 ||
		usage.CacheWriteTokens != 800 ||
		usage.OutputTokens != 60 {
		t.Fatalf("usage = %+v, want cache details normalized as prompt token subsets", usage)
	}
	envelope, err := model.NewResponseEnvelopeForStorage(
		"gpt-test",
		modelprotocol.APIFormatOpenAIChatCompletions,
		modelprotocol.APIVariantDefault,
		model.Response{ID: "chatcmpl_usage", Usage: usage},
	)
	if err != nil {
		t.Fatalf("response envelope: %v", err)
	}
	if envelope.Normalized.Usage != usage {
		t.Fatalf("stored usage = %+v, want %+v", envelope.Normalized.Usage, usage)
	}

	if got := usageFromResponse(chatUsage{
		PromptTokens: 10,
		PromptTokensDetails: chatTokenDetails{
			CachedTokens:     2,
			CacheWriteTokens: 9,
		},
	}); got != (model.Usage{}) {
		t.Fatalf("impossible cache-write distribution produced usage: %+v", got)
	}
}

func TestParseResponseRecordsOpenRouterServedProvider(t *testing.T) {
	documented := json.RawMessage(`{"id":"chatcmpl_provider","model":"moonshotai/kimi-k3",` +
		`"openrouter_metadata":{"requested":"moonshotai/kimi-k3","strategy":"direct","endpoints":{"total":2,` +
		`"available":[{"provider":"Together","model":"moonshotai/kimi-k3","selected":false},` +
		`{"provider":"Moonshot AI","model":"moonshotai/kimi-k3","selected":true}]}},` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":2}}`)
	fallback := json.RawMessage(`{"id":"chatcmpl_provider","model":"moonshotai/kimi-k3","provider":"Moonshot AI",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":2}}`)

	openRouter := protocol{client: Client{APIVariant: modelprotocol.APIVariantOpenRouter}}
	for name, body := range map[string]json.RawMessage{"router metadata": documented, "top-level provider": fallback} {
		response, err := openRouter.ParseResponse(context.Background(), route.Response{
			StatusCode: http.StatusOK,
			Body:       body,
		})
		if err != nil {
			t.Fatalf("parse OpenRouter response (%s): %v", name, err)
		}
		if response.ProviderMetadata.OpenRouter.Provider != "Moonshot AI" {
			t.Fatalf("%s: provider metadata = %+v, want Moonshot AI", name, response.ProviderMetadata)
		}
	}

	response, err := (protocol{}).ParseResponse(context.Background(), route.Response{
		StatusCode: http.StatusOK,
		Body:       documented,
	})
	if err != nil {
		t.Fatalf("parse default response: %v", err)
	}
	if response.ProviderMetadata != (modelenvelope.ProviderMetadata{}) {
		t.Fatalf("default variant provider metadata = %+v, want none", response.ProviderMetadata)
	}
}

func TestUsageFromResponseReadsProviderCacheSpellings(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{
			name: "openai details",
			body: `{"prompt_tokens":283,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":256}}`,
		},
		{
			name: "deepseek",
			body: `{"prompt_tokens":283,"completion_tokens":2,"prompt_cache_hit_tokens":256,"prompt_cache_miss_tokens":27}`,
		},
		{name: "moonshot", body: `{"prompt_tokens":283,"completion_tokens":2,"cached_tokens":256}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var usage chatUsage
			if err := json.Unmarshal([]byte(tc.body), &usage); err != nil {
				t.Fatalf("decode usage: %v", err)
			}
			got := usageFromResponse(usage)
			if got.InputTokens != 283 || got.CacheReadTokens != 256 || got.UncachedInputTokens != 27 {
				t.Fatalf("usage = %+v, want 283 input with 256 cached", got)
			}
		})
	}
}
