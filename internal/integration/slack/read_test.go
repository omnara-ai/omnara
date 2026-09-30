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
	"github.com/stretchr/testify/require"
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
			order := "newest_first"
			if thread != "" {
				order = "oldest_first"
			}
			assert.Equal(t, order, page.Order)
			assert.Len(t, page.Messages, 1)
			assert.Equal(t, 1, requests)
		})
	}
}

func TestReadThreadFollowsCursorToRecentRepliesOnePageAtATime(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assert.NoError(t, r.ParseForm())
		assert.Equal(t, "/conversations.replies", r.URL.Path)
		assert.Equal(t, "111.000", r.Form.Get("ts"))
		assert.Equal(t, "15", r.Form.Get("limit"))
		switch r.Form.Get("cursor") {
		case "":
			_, _ = fmt.Fprint(w, `{
				"ok":true,
				"messages":[{"text":"root","ts":"111.000"},{"text":"older reply","ts":"111.100"}],
				"response_metadata":{"next_cursor":"recent-page"}
			}`)
		case "recent-page":
			_, _ = fmt.Fprint(w, `{
				"ok":true,
				"messages":[{"text":"recent post","ts":"222.000"}],
				"response_metadata":{"next_cursor":""}
			}`)
		default:
			t.Errorf("unexpected cursor %q", r.Form.Get("cursor"))
		}
	}))
	defer server.Close()
	target := MessageTarget{Channel: "C123", ThreadTS: "111.000", BotToken: "secret"}
	page, result, err := ReadMessages(t.Context(), slackTestClient(server), target, "", 0)
	require.NoError(t, err)
	require.Equal(t, APIResult{}, result)
	require.Equal(t, "oldest_first", page.Order)
	require.Equal(t, "recent-page", page.NextCursor)
	require.Equal(t, 1, requests, "read must not crawl the thread automatically")
	require.Len(t, page.Messages, 2)
	require.Equal(t, "root", page.Messages[0].Text)
	page, result, err = ReadMessages(t.Context(), slackTestClient(server), target, page.NextCursor, 0)
	require.NoError(t, err)
	require.Equal(t, APIResult{}, result)
	require.Empty(t, page.NextCursor)
	require.Len(t, page.Messages, 1)
	require.Equal(t, "recent post", page.Messages[0].Text)
	require.Equal(t, 2, requests)
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
