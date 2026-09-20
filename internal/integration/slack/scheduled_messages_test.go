package slack

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScheduledMessageUsesReceiptMarkerAndReconcilesOwnBot(t *testing.T) {
	const receipt = "occurrence-id"
	var posts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !assert.Equal(t, "Bearer bot-token", r.Header.Get("Authorization")) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/chat.postMessage":
			posts++
			var body map[string]json.RawMessage
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
				http.Error(w, "invalid message body", http.StatusBadRequest)
				return
			}
			assert.JSONEq(t,
				`{"event_type":"omnara_scheduled_thread","event_payload":{"receipt_id":"occurrence-id"}}`,
				string(body["metadata"]),
			)
			assert.Empty(t, body["thread_ts"])
			_, _ = w.Write([]byte(`{"ok":true,"channel":"C123","ts":"100.1"}`))
		case "/conversations.history":
			if !assert.NoError(t, r.ParseForm()) {
				http.Error(w, "invalid history parameters", http.StatusBadRequest)
				return
			}
			assert.Equal(t, "C123", r.Form.Get("channel"))
			assert.Equal(t, "true", r.Form.Get("include_all_metadata"))
			_, _ = w.Write([]byte(`{"ok":true,"messages":[
    {"user":"U_OTHER","ts":"102.1","metadata":{
      "event_type":"omnara_scheduled_thread","event_payload":{"receipt_id":"occurrence-id"}}},
    {"user":"U_BOT","ts":"101.1","metadata":{
      "event_type":"omnara_scheduled_thread","event_payload":{"receipt_id":"another"}}},
    {"user":"U_BOT","ts":"100.1","metadata":{
      "event_type":"omnara_scheduled_thread","event_payload":{"receipt_id":"occurrence-id"}}}
   ]}`))
		default:
			t.Errorf("unexpected provider request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	config := OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()}
	target := MessageTarget{Channel: "C123", BotToken: "bot-token"}
	result, err := PostScheduledMessage(t.Context(), config, target, receipt, "Daily update")
	require.NoError(t, err)
	require.Equal(t, "C123:100.1", result.MessageID)
	id, found, outcome, err := ReconcileScheduledMessage(t.Context(), config, target, receipt, "U_BOT", time.Now())
	require.NoError(t, err)
	require.Equal(t, APIResult{}, outcome)
	require.True(t, found)
	require.Equal(t, result.MessageID, id)
	require.Equal(t, 1, posts, "readback must never repost")
}

func TestScheduledSlackKeepsHTTPServerErrorEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"ok":false,"error":"ratelimited"}`))
	}))
	defer server.Close()
	result, err := PostScheduledMessage(
		t.Context(),
		OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()},
		MessageTarget{Channel: "C123", BotToken: "token"},
		"receipt",
		"opening",
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusInternalServerError, result.StatusCode,
		"a rate-limit error body must not erase the uncertain HTTP 500")
	require.Empty(t, result.MessageID)
}
