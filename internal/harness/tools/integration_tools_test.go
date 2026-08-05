package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func TestSlackDestination(t *testing.T) {
	channel, threadTS, err := slack.Destination("dm", "D123")
	if err != nil {
		t.Fatalf("dm destination: %v", err)
	}
	if channel != "D123" || threadTS != "" {
		t.Fatalf("dm destination = %q %q", channel, threadTS)
	}
	channel, threadTS, err = slack.Destination("thread", "C123:111.222")
	if err != nil {
		t.Fatalf("thread destination: %v", err)
	}
	if channel != "C123" || threadTS != "111.222" {
		t.Fatalf("thread destination = %q %q", channel, threadTS)
	}
	if _, _, err := slack.Destination("thread", "C123"); err == nil {
		t.Fatal("expected malformed thread ref to fail")
	}
}

func TestSleepForIntegrationRateLimitRespectsRetryBounds(t *testing.T) {
	t.Run("sleep budget", func(t *testing.T) {
		slept := integrationMessageSendMaxRateLimitSleep
		retry, err := sleepForIntegrationRateLimit(
			context.Background(),
			time.Nanosecond,
			&slept,
			1,
		)
		if err != nil || retry || slept != integrationMessageSendMaxRateLimitSleep {
			t.Fatalf("retry=%v slept=%v err=%v", retry, slept, err)
		}
	})
	t.Run("attempt budget", func(t *testing.T) {
		var slept time.Duration
		retry, err := sleepForIntegrationRateLimit(
			context.Background(),
			0,
			&slept,
			integrationMessageSendAttempts,
		)
		if err != nil || retry || slept != 0 {
			t.Fatalf("retry=%v slept=%v err=%v", retry, slept, err)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var slept time.Duration
		retry, err := sleepForIntegrationRateLimit(ctx, time.Second, &slept, 1)
		if !errors.Is(err, context.Canceled) || retry || slept != 0 {
			t.Fatalf("retry=%v slept=%v err=%v", retry, slept, err)
		}
	})
}

func TestPostIntegrationMessageAddsMarker(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer xoxb-test" {
			t.Fatalf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.333"})
	}))
	defer server.Close()

	agentID := integrationToolTestID("slack-send-agent")
	agentPublicID, err := publicid.Encode(publicid.KindAgent, agentID)
	if err != nil {
		t.Fatalf("encode agent id: %v", err)
	}
	target := slack.MessageTarget{
		TargetRef: "slack-abcd",
		Channel:   "C123",
		ThreadTS:  "111.222",
		BotToken:  "xoxb-test",
	}
	executor := Executor{IntegrationHTTPClient: integrationProviderTestClient(server)}
	result, err := slack.PostMessage(
		context.Background(),
		executor.IntegrationHTTPClient,
		target,
		agentPublicID,
		"call_test",
		"hello",
	)
	if err != nil {
		t.Fatalf("post integration message: %v", err)
	}
	if result.MessageID != "C123:222.333" {
		t.Fatalf("message id = %q", result.MessageID)
	}
	if payload["channel"] != "C123" || payload["thread_ts"] != "111.222" || payload["text"] != "hello" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	metadata, ok := payload["metadata"].(map[string]any)
	if !ok || metadata["event_type"] != slack.MessageMarkerEventType {
		t.Fatalf("missing marker metadata: %#v", payload["metadata"])
	}
	eventPayload, ok := metadata["event_payload"].(map[string]any)
	if !ok || eventPayload["agent_id"] != agentPublicID || eventPayload["provider_call_id"] != "call_test" ||
		eventPayload["target_ref"] != "slack-abcd" {
		t.Fatalf("unexpected marker payload: %#v", eventPayload)
	}
}

func TestReconcileIntegrationMessageFindsMarker(t *testing.T) {
	agentID := integrationToolTestID("slack-readback-agent")
	agentPublicID, err := publicid.Encode(publicid.KindAgent, agentID)
	if err != nil {
		t.Fatalf("encode agent id: %v", err)
	}
	var form url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/conversations.replies" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		form = r.Form
		writeToolTestJSON(w, map[string]any{
			"ok": true,
			"messages": []map[string]any{{
				"ts": "222.333",
				"metadata": map[string]any{
					"event_type": slack.MessageMarkerEventType,
					"event_payload": map[string]any{
						"agent_id":         agentPublicID,
						"provider_call_id": "call_test",
						"target_ref":       "slack-abcd",
					},
				},
			}},
		})
	}))
	defer server.Close()

	target := slack.MessageTarget{TargetRef: "slack-abcd", Channel: "C123", ThreadTS: "111.222", BotToken: "xoxb-test"}
	since := time.Date(2026, 6, 12, 12, 0, 1, 250000000, time.UTC)
	executor := Executor{IntegrationHTTPClient: integrationProviderTestClient(server)}
	messageID, found, result, err := slack.ReconcileMessage(
		context.Background(),
		executor.IntegrationHTTPClient,
		target,
		agentPublicID,
		"call_test",
		since,
	)
	if err != nil {
		t.Fatalf("reconcile integration message: %v", err)
	}
	if result != (slack.APIResult{}) {
		t.Fatalf("readback api result = %+v, want zero", result)
	}
	if !found || messageID != "C123:222.333" {
		t.Fatalf("readback = %q %t", messageID, found)
	}
	if form.Get("channel") != "C123" || form.Get("ts") != "111.222" || form.Get("include_all_metadata") != "true" ||
		form.Get("inclusive") != "true" ||
		form.Get("oldest") != "1781265600.250000" ||
		form.Get("limit") != "100" {
		t.Fatalf("unexpected readback form: %v", form)
	}
}

func TestReconcileIntegrationMessageClassifiesProviderErrors(t *testing.T) {
	agentID := integrationToolTestID("slack-readback-error-agent")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/conversations.history" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		writeToolTestJSON(w, map[string]any{"ok": false, "error": "token_revoked"})
	}))
	defer server.Close()

	executor := Executor{IntegrationHTTPClient: integrationProviderTestClient(server)}
	target := slack.MessageTarget{TargetRef: "slack-abcd", Channel: "D123", BotToken: "xoxb-test"}
	agentPublicID, err := publicid.Encode(publicid.KindAgent, agentID)
	if err != nil {
		t.Fatalf("encode agent id: %v", err)
	}
	messageID, found, result, err := slack.ReconcileMessage(
		context.Background(),
		executor.IntegrationHTTPClient,
		target,
		agentPublicID,
		"call_test",
		time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("reconcile integration message: %v", err)
	}
	if messageID != "" || found {
		t.Fatalf("readback = %q %t, want empty false", messageID, found)
	}
	if !result.PermanentFailure || result.Code != "integration_disabled" {
		t.Fatalf("readback api result = %+v", result)
	}
}

func TestIntegrationProviderRateLimitResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	executor := Executor{IntegrationHTTPClient: integrationProviderTestClient(server)}
	target := slack.MessageTarget{TargetRef: "slack-abcd", Channel: "C123", BotToken: "xoxb-test"}
	result, err := slack.PostMessage(
		context.Background(),
		executor.IntegrationHTTPClient,
		target,
		"agt_testagent",
		"call_test",
		"hello",
	)
	if err != nil {
		t.Fatalf("post integration message: %v", err)
	}
	if !result.RateLimited || result.RetryAfter.String() != "42s" {
		t.Fatalf("rate limit result = %+v", result)
	}
}

func TestPostIntegrationMessageTransportFailureIsStructured(t *testing.T) {
	executor := Executor{
		IntegrationHTTPClient: &http.Client{Transport: errorRoundTripper{err: errors.New("network down")}},
	}
	result, err := slack.PostMessage(
		context.Background(),
		executor.IntegrationHTTPClient,
		slack.MessageTarget{TargetRef: "slack-abcd", Channel: "C123", BotToken: "xoxb-test"},
		"agt_testagent",
		"call_test",
		"hello",
	)
	if err != nil {
		t.Fatalf("post integration message transport failure: %v", err)
	}
	if !result.DeliveryUnknown || result.Message == "" {
		t.Fatalf("transport failure result = %+v", result)
	}
}

func TestIntegrationInteractionPromptPayloadUsesPlainTextAndSharedAction(t *testing.T) {
	interactionID := integrationToolTestID("prompt-interaction")
	agentID := integrationToolTestID("prompt-agent")
	payload, err := integrationInteractionPromptPayload(
		integrationToolTarget{
			Provider:        integrationstore.IntegrationProviderSlack,
			PublicID:        "itgt_testtarget",
			TargetRef:       "slack-abcd",
			ProviderRefKind: "thread",
			ProviderRef:     "C123:111.222",
		},
		agentID,
		executionstore.AgentInteractionRecord{
			ID:              interactionID,
			InteractionKind: "permission",
			State:           executionstore.AgentInteractionStateOpen,
			Request: permissionPromptRequest(
				t,
				"run_command",
				"Permission requested for run_command",
				[]interactionform.ContextItem{{Label: "Command", Value: "echo <@U123>"}},
			),
		},
	)
	if err != nil {
		t.Fatalf("prompt payload: %v", err)
	}
	var decoded struct {
		Channel  string `json:"channel"`
		ThreadTS string `json:"thread_ts"`
		Blocks   []struct {
			Type string `json:"type"`
			Text struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"text"`
			Elements []struct {
				ActionID string `json:"action_id"`
				Value    string `json:"value"`
			} `json:"elements"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode prompt payload: %v", err)
	}
	if decoded.Channel != "C123" || decoded.ThreadTS != "111.222" {
		t.Fatalf("unexpected destination: %+v", decoded)
	}
	if len(decoded.Blocks) != 3 || decoded.Blocks[0].Text.Type != "plain_text" ||
		!strings.Contains(decoded.Blocks[0].Text.Text, "echo <@U123>") {
		t.Fatalf("unexpected prompt text block: %+v", decoded.Blocks)
	}
	if len(decoded.Blocks[2].Elements) != 1 ||
		decoded.Blocks[2].Elements[0].ActionID != slack.PromptAction {
		t.Fatalf("unexpected prompt actions: %+v", decoded.Blocks[2].Elements)
	}
	var value slack.PromptActionValue
	if err := json.Unmarshal([]byte(decoded.Blocks[2].Elements[0].Value), &value); err != nil {
		t.Fatalf("decode prompt action value: %v", err)
	}
	if value.InteractionID == "" || value.AgentID == "" ||
		value.IntegrationTargetID != "itgt_testtarget" {
		t.Fatalf("unexpected prompt action value: %+v", value)
	}
}

func TestIntegrationPermissionPromptIncludesInputSummary(t *testing.T) {
	payload, err := integrationInteractionPromptPayload(
		integrationToolTarget{
			Provider:        integrationstore.IntegrationProviderSlack,
			PublicID:        "itgt_testtarget",
			TargetRef:       "slack-abcd",
			ProviderRefKind: "dm",
			ProviderRef:     "D123",
		},
		integrationToolTestID("prompt-web-agent"),
		executionstore.AgentInteractionRecord{
			ID:              integrationToolTestID("prompt-web-interaction"),
			InteractionKind: "permission",
			State:           executionstore.AgentInteractionStateOpen,
			Request: permissionPromptRequest(
				t,
				"web_search",
				"Permission requested for web_search",
				[]interactionform.ContextItem{{Label: "Query", Value: "FIFA scores today"}},
			),
		},
	)
	if err != nil {
		t.Fatalf("prompt payload: %v", err)
	}
	var decoded struct {
		Blocks []struct {
			Text struct {
				Text string `json:"text"`
			} `json:"text"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode prompt payload: %v", err)
	}
	if len(decoded.Blocks) == 0 ||
		!strings.Contains(decoded.Blocks[0].Text.Text, "FIFA scores today") {
		t.Fatalf("prompt text = %+v", decoded.Blocks)
	}
}

func TestIntegrationQuestionPromptPayloadIncludesQuestionText(t *testing.T) {
	interactionID := integrationToolTestID("prompt-question")
	agentID := integrationToolTestID("prompt-question-agent")
	payload, err := integrationInteractionPromptPayload(
		integrationToolTarget{
			Provider:        integrationstore.IntegrationProviderSlack,
			PublicID:        "itgt_testtarget",
			TargetRef:       "slack-abcd",
			ProviderRefKind: "thread",
			ProviderRef:     "C123:111.222",
		},
		agentID,
		executionstore.AgentInteractionRecord{
			ID:              interactionID,
			InteractionKind: "question",
			State:           executionstore.AgentInteractionStateOpen,
			Request: json.RawMessage(
				`{"title":"Question","questions":[{"prompt":"What now?","options":[{"label":"Ship it"},{"label":"Wait"}]}]}`,
			),
		},
	)
	if err != nil {
		t.Fatalf("prompt payload: %v", err)
	}
	var decoded struct {
		Text   string `json:"text"`
		Blocks []struct {
			Type    string `json:"type"`
			BlockID string `json:"block_id"`
			Text    struct {
				Text string `json:"text"`
			} `json:"text"`
			Element struct {
				Type     string `json:"type"`
				ActionID string `json:"action_id"`
			} `json:"element"`
			Label struct {
				Text string `json:"text"`
			} `json:"label"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode prompt payload: %v", err)
	}
	interactionPublicID, err := publicid.Encode(publicid.KindAgentInteraction, interactionID)
	if err != nil {
		t.Fatalf("encode interaction id: %v", err)
	}
	wantSummary := "Question\n1. What now?\n   1. Ship it\n   2. Wait"
	if decoded.Text != wantSummary || len(decoded.Blocks) != 3 ||
		decoded.Blocks[0].Text.Text != "Question" ||
		decoded.Blocks[0].BlockID != "omnara_interaction_"+interactionPublicID ||
		decoded.Blocks[1].Type != "input" ||
		decoded.Blocks[1].BlockID != "omnara_question_0" ||
		decoded.Blocks[1].Element.ActionID != slack.PromptAnswerAction ||
		decoded.Blocks[1].Label.Text != "What now?" {
		t.Fatalf("unexpected question prompt payload: %+v", decoded)
	}
}

func TestSlackPromptLabelTruncatesLongText(t *testing.T) {
	long := strings.Repeat("x", 3010)
	got := slack.PromptLabel(long)
	if len([]rune(got)) != 3000 || !strings.HasSuffix(got, "...") {
		t.Fatalf("truncated label length=%d suffix=%q", len([]rune(got)), got[len(got)-3:])
	}
}

func permissionPromptRequest(
	t *testing.T,
	toolName string,
	title string,
	contextItems []interactionform.ContextItem,
) json.RawMessage {
	t.Helper()
	authorization, err := toolpermission.NewAuthorization(
		toolName,
		json.RawMessage(`{}`),
	)
	if err != nil {
		t.Fatalf("permission authorization: %v", err)
	}
	descriptor, ok := toolpermission.FindMode(
		toolpermission.CommonModeDescriptors(),
		toolpermission.ModeAlwaysAsk,
	)
	if !ok {
		t.Fatal("always_ask descriptor missing")
	}
	value, err := toolpermission.NewAllowDenyForm(title, contextItems)
	if err != nil {
		t.Fatalf("permission interaction form: %v", err)
	}
	request, err := toolpermission.NewRequest(
		descriptor,
		toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk),
		authorization,
		value,
	)
	if err != nil {
		t.Fatalf("permission request: %v", err)
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal permission request: %v", err)
	}
	return body
}

func writeToolTestJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		panic(err)
	}
}

func integrationToolTestID(seed string) storage.ID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("omnara-integration-tool:"+seed))
}

func integrationProviderTestClient(server *httptest.Server) *http.Client {
	target, err := url.Parse(server.URL)
	if err != nil {
		panic(err)
	}
	base := server.Client()
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &http.Client{
		Transport:     rewriteIntegrationProviderTransport{target: target, base: transport},
		CheckRedirect: base.CheckRedirect,
		Jar:           base.Jar,
		Timeout:       base.Timeout,
	}
}

type rewriteIntegrationProviderTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (t rewriteIntegrationProviderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	clone.URL.Path = strings.TrimPrefix(clone.URL.Path, "/api")
	return t.base.RoundTrip(clone)
}

type errorRoundTripper struct {
	err error
}

func (t errorRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, t.err
}
