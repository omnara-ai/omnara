package slack

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestReadMessagesUsesBoundedConversationCursor(t *testing.T) {
	for _, thread := range []string{"", "111.222"} {
		t.Run(thread, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				assert.NoError(t, r.ParseForm())
				assert.Equal(t, "C123", r.Form.Get("channel"))
				assert.Equal(t, thread, r.Form.Get("ts"))
				assert.Equal(t, "15", r.Form.Get("limit"))
				assert.Equal(t, "https://untrusted.example/cursor", r.Form.Get("cursor"))
				method := "/conversations.history"
				if thread != "" {
					method = "/conversations.replies"
				}
				assert.Equal(t, method, r.URL.Path)
				_, _ = fmt.Fprint(
					w,
					`{"ok":true,"messages":[{"user":"U123","text":"hello","ts":"111.333"}],"response_metadata":{"next_cursor":"next"}}`,
				)
			}))
			defer server.Close()
			page, status, err := ReadMessages(
				t.Context(),
				slackTestClient(server),
				MessageTarget{Channel: "C123", ThreadTS: thread, BotToken: "secret"},
				"https://untrusted.example/cursor",
				0,
			)
			assert.NoError(t, err)
			assert.Equal(t, APIResult{}, status)
			assert.Equal(t, "next", page.NextCursor)
			assert.Len(t, page.Messages, 1)
			assert.Equal(t, 1, requests)
		})
	}
}

func TestRequestCheckStopsReadbackAtRevocation(t *testing.T) {
	requests, checks := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = fmt.Fprint(w, `{"ok":true,"messages":[],"response_metadata":{"next_cursor":"next"}}`)
	}))
	defer server.Close()
	revoked := errors.New("authority revoked")
	base := slackTestClient(server)
	client := WithRequestCheck(base, func(context.Context) error {
		checks++
		if checks > 1 {
			return revoked
		}
		return nil
	})
	_, _, _, err := ReconcileMessage(
		t.Context(),
		client,
		MessageTarget{Channel: "C123", BotToken: "secret"},
		"agent",
		"call",
		time.Now(),
	)
	assert.ErrorIs(t, err, revoked)
	assert.Equal(t, 1, requests, "revocation must prevent the second page from reaching Slack")
	assert.Equal(t, 2, checks)
	_, _, err = ReadMessages(t.Context(), base, MessageTarget{Channel: "C123", BotToken: "secret"}, "", 1)
	assert.NoError(t, err, "the shared client must remain unmodified")
	assert.Equal(t, 2, requests)
}
