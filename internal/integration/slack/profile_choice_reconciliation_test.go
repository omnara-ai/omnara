package slack

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReconcileProfileChoiceRequiresPersistedCreationTime(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = fmt.Fprint(w, `{"ok":true,"messages":[]}`)
	}))
	defer server.Close()
	config := OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()}
	id, _, err := ReconcileProfileChoice(t.Context(), config,
		MessageTarget{Channel: "C123", BotToken: "secret"}, profileChoiceTestID(t), "UBOT", time.Time{})
	require.ErrorContains(t, err, "creation time")
	require.Empty(t, id)
	require.Zero(t, calls.Load(), "missing persisted time must not fall back to an unbounded read")
}

func TestReconcileProfileChoiceMatchesBotActionAndConversation(t *testing.T) {
	choiceID := profileChoiceTestID(t)
	createdAt := time.Unix(301, 0)
	for _, tc := range []struct {
		name   string
		edit   func(*HistoryMessage)
		thread string
		want   bool
	}{
		{name: "thread menu", thread: "1.2", want: true},
		{name: "channel menu", want: true},
		{name: "channel parent", want: true, edit: func(m *HistoryMessage) { m.ThreadTS = m.TS }},
		{name: "different bot", thread: "1.2", edit: func(m *HistoryMessage) { m.User = "UOTHER" }},
		{name: "bot id without exact user", thread: "1.2", edit: func(m *HistoryMessage) { m.User, m.BotID = "", "UBOT" }},
		{name: "wrong choice", thread: "1.2", edit: func(m *HistoryMessage) { m.Blocks[0].Elements[0].ActionID += "-other" }},
		{name: "wrong action type", thread: "1.2", edit: func(m *HistoryMessage) { m.Blocks[0].Elements[0].Type = "button" }},
		{name: "wrong channel", thread: "1.2", edit: func(m *HistoryMessage) { m.Channel = "C_OTHER" }},
		{name: "wrong thread", thread: "1.2", edit: func(m *HistoryMessage) { m.ThreadTS = "4.5" }},
		{name: "thread missing", thread: "1.2", edit: func(m *HistoryMessage) { m.ThreadTS = "" }},
		{name: "channel read cannot match reply", edit: func(m *HistoryMessage) { m.ThreadTS = "1.2" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := HistoryMessage{
				User: "UBOT", Channel: "C123", ThreadTS: tc.thread, TS: "2.3",
				Blocks: []HistoryBlock{{Elements: []actionButton{{
					Type: "static_select", ActionID: ProfileChoiceActionPrefix + choiceID,
				}}}},
			}
			if tc.edit != nil {
				tc.edit(&message)
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				method := "/fixture/conversations.history"
				if tc.thread != "" {
					method = "/fixture/conversations.replies"
				}
				assert.Equal(t, method, r.URL.Path)
				assert.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
				assert.NoError(t, r.ParseForm())
				assert.Equal(t, "C123", r.Form.Get("channel"))
				assert.Equal(t, tc.thread, r.Form.Get("ts"))
				assert.Equal(t, "1.000000", r.Form.Get("oldest"))
				assert.Equal(t, "true", r.Form.Get("inclusive"))
				writeSlackTestJSON(w, map[string]any{"ok": true, "messages": []HistoryMessage{message}})
			}))
			defer server.Close()
			config := OAuthConfig{APIURL: server.URL + "/fixture", HTTPClient: server.Client()}
			id, result, err := ReconcileProfileChoice(t.Context(), config,
				MessageTarget{Channel: "C123", ThreadTS: tc.thread, BotToken: "secret"}, choiceID, "UBOT", createdAt)
			require.NoError(t, err)
			require.Equal(t, APIResult{}, result)
			require.Equal(t, tc.want, id == "2.3")
			require.Equal(t, 1, calls)
		})
	}
}

func TestReconcileProfileChoicePagesAndRejectsIncompleteReads(t *testing.T) {
	choiceID := profileChoiceTestID(t)
	createdAt := time.Unix(301, 0)
	for _, scenario := range []string{
		"second page", "page limit", "missing cursor", "missing messages", "missing timestamp", "rate limited", "revoked",
	} {
		t.Run(scenario, func(t *testing.T) {
			calls, checks := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				assert.NoError(t, r.ParseForm())
				assert.Equal(t, "1.000000", r.Form.Get("oldest"))
				assert.Equal(t, "true", r.Form.Get("inclusive"))
				if calls == 1 {
					assert.Empty(t, r.Form.Get("cursor"))
				} else {
					assert.Equal(t, fmt.Sprint(calls-1), r.Form.Get("cursor"))
				}
				switch scenario {
				case "missing cursor":
					_, _ = fmt.Fprint(w, `{"ok":true,"messages":[],"has_more":true}`)
				case "missing messages":
					_, _ = fmt.Fprint(w, `{"ok":true}`)
				case "rate limited":
					w.Header().Set("Retry-After", "30")
					w.WriteHeader(http.StatusTooManyRequests)
				case "missing timestamp":
					writeSlackTestJSON(w, map[string]any{
						"ok": true,
						"messages": []HistoryMessage{{
							User: "UBOT", ThreadTS: "1.2",
							Blocks: []HistoryBlock{{Elements: []actionButton{{
								Type: "static_select", ActionID: ProfileChoiceActionPrefix + choiceID,
							}}}},
						}},
					})
				default:
					if scenario == "second page" && calls == 2 {
						writeSlackTestJSON(w, map[string]any{
							"ok": true,
							"messages": []HistoryMessage{{
								User: "UBOT", ThreadTS: "1.2", TS: "2.3",
								Blocks: []HistoryBlock{{Elements: []actionButton{{
									Type: "static_select", ActionID: ProfileChoiceActionPrefix + choiceID,
								}}}},
							}},
						})
					} else {
						writeSlackTestJSON(w, map[string]any{
							"ok": true, "messages": []HistoryMessage{},
							"response_metadata": map[string]string{"next_cursor": fmt.Sprint(calls)},
						})
					}
				}
			}))
			defer server.Close()
			client := WithRequestCheck(server.Client(), func(context.Context) error {
				checks++
				if scenario == "revoked" && checks > 1 {
					return context.Canceled
				}
				return nil
			})
			id, result, err := ReconcileProfileChoice(t.Context(), OAuthConfig{APIURL: server.URL, HTTPClient: client},
				MessageTarget{Channel: "C123", ThreadTS: "1.2", BotToken: "secret"}, choiceID, "UBOT", createdAt)
			switch scenario {
			case "second page":
				require.NoError(t, err)
				require.Equal(t, "2.3", id)
				require.Equal(t, APIResult{}, result)
				require.Equal(t, 2, calls)
			case "revoked":
				require.ErrorIs(t, err, context.Canceled)
				require.Equal(t, 1, calls)
			case "rate limited":
				require.NoError(t, err)
				require.True(t, result.RateLimited)
				require.Equal(t, 30*time.Second, result.RetryAfter)
			default:
				require.NoError(t, err)
				require.Empty(t, id)
				require.True(t, result.DeliveryUnknown)
				if scenario == "page limit" {
					require.Equal(t, readbackMaxPages, calls)
				}
			}
			require.LessOrEqual(t, calls, readbackMaxPages)
		})
	}
}
