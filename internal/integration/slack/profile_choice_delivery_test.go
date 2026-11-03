package slack

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPostProfileChoiceUsesConfiguredEndpointAndConfirmedReceipt(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, response string
		status         int
		unknown        bool
		permanent      bool
		rateLimited    bool
	}{
		{name: "confirmed", response: `{"ok":true,"channel":"C123","ts":"123.789"}`},
		{name: "missing timestamp", response: `{"ok":true,"channel":"C123"}`, unknown: true},
		{name: "wrong channel", response: `{"ok":true,"channel":"C_OTHER","ts":"123.789"}`, unknown: true},
		{name: "invalid JSON", response: `{`, unknown: true},
		{name: "provider error", response: `{"ok":false,"error":"token_revoked"}`, permanent: true},
		{name: "rate limited", response: `{}`, status: http.StatusTooManyRequests, rateLimited: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			choiceID, calls := profileChoiceTestID(t), 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/fixture/chat.postMessage" ||
					r.Header.Get("Authorization") != "Bearer xoxb-fixture" ||
					r.Header.Get("Content-Type") != "application/json" {
					t.Error("incorrect profile menu request endpoint or headers")
				}
				var payload struct {
					Channel  string          `json:"channel"`
					ThreadTS string          `json:"thread_ts"`
					Text     string          `json:"text"`
					Blocks   json.RawMessage `json:"blocks"`
				}
				if json.NewDecoder(r.Body).Decode(&payload) != nil || payload.Channel != "C123" ||
					payload.ThreadTS != "123.456" || !strings.Contains(payload.Text, "Available until noon.") ||
					!strings.Contains(string(payload.Blocks), ProfileChoiceActionPrefix+choiceID) ||
					!strings.Contains(string(payload.Blocks), `"static_select"`) {
					t.Error("incorrect profile menu payload")
				}
				w.Header().Set("Retry-After", "2")
				if test.status != 0 {
					w.WriteHeader(test.status)
				}
				fmt.Fprint(w, test.response)
			}))
			defer server.Close()
			id, result, err := PostProfileChoice(t.Context(),
				OAuthConfig{APIURL: server.URL + "/fixture/", HTTPClient: server.Client()},
				MessageTarget{Channel: "C123", ThreadTS: "123.456", BotToken: "xoxb-fixture"}, choiceID,
				[]ProfileChoiceOption{{Key: "first", Name: "First"}, {Key: "second", Name: "Second"}},
				"Available until noon.")
			require.NoError(t, err)
			require.Equal(t, 1, calls, "send must not retry or read history")
			require.Equal(t, test.unknown, result.DeliveryUnknown)
			require.Equal(t, test.permanent, result.PermanentFailure)
			require.Equal(t, test.rateLimited, result.RateLimited)
			if test.name == "confirmed" {
				require.Equal(t, "123.789", id)
				require.Equal(t, APIResult{}, result)
			} else {
				require.Empty(t, id)
			}
			if test.rateLimited {
				require.Equal(t, 2*time.Second, result.RetryAfter)
			}
		})
	}
}

func TestUpdateProfileChoiceClearsMenuAtConfiguredEndpoint(t *testing.T) {
	t.Parallel()
	for _, confirmedID := range []string{"123.789", "different", ""} {
		t.Run("receipt="+confirmedID, func(t *testing.T) {
			t.Parallel()
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/fixture/chat.update" ||
					r.Header.Get("Authorization") != "Bearer xoxb-fixture" {
					t.Error("incorrect profile menu update endpoint or headers")
				}
				var payload struct {
					Channel string          `json:"channel"`
					TS      string          `json:"ts"`
					Text    string          `json:"text"`
					Blocks  json.RawMessage `json:"blocks"`
				}
				if json.NewDecoder(r.Body).Decode(&payload) != nil || payload.Channel != "C123" ||
					payload.TS != "123.789" || payload.Text != "Profile selected." || string(payload.Blocks) != "[]" {
					t.Error("update must target the confirmed menu and clear its blocks")
				}
				writeSlackTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": confirmedID})
			}))
			defer server.Close()
			config := OAuthConfig{APIURL: server.URL + "/fixture", HTTPClient: server.Client()}
			target := MessageTarget{Channel: "C123", BotToken: "xoxb-fixture"}
			result, err := UpdateProfileChoice(t.Context(), config, target, "123.789", "Profile selected.")
			require.NoError(t, err)
			require.Equal(t, APIResult{DeliveryUnknown: confirmedID != "123.789"}, result)
			require.Equal(t, 1, calls, "update must not retry or read history")
			_, err = UpdateProfileChoice(t.Context(), config, target, "", "Stale menu.")
			require.Error(t, err)
			require.Equal(t, 1, calls, "missing receipt must not send an update")
		})
	}
}
