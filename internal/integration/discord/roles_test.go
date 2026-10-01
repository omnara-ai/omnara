package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveBotMentionUsesOnlyOwnedManagedRole(t *testing.T) {
	for _, tc := range []struct {
		name, roles string
		want        bool
	}{
		{"own role", `[{"id":"777","managed":true,"tags":{"bot_id":"222"}}]`, true},
		{"other bot", `[{"id":"777","managed":true,"tags":{"bot_id":"999"}}]`, false},
		{"application ID", `[{"id":"777","managed":true,"tags":{"bot_id":"111"}}]`, false},
		{"same name assigned role", `[{"id":"777","name":"Helper","managed":false}]`, false},
		{"other managed role", `[{"id":"777","managed":true,"tags":{"integration_id":"222"}}]`, false},
		{"unmanaged bot tag", `[{"id":"777","managed":false,"tags":{"bot_id":"222"}}]`, false},
		{"own role not mentioned", `[{"id":"888","managed":true,"tags":{"bot_id":"222"}}]`, false},
		{"everyone cannot be owned role", `[{"id":"333","managed":true,"tags":{"bot_id":"222"}}]`, false},
		{"no matching role", `[]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, "/api/v10/guilds/333/roles", r.URL.Path)
				assert.Equal(t, "Bot test-token", r.Header.Get("Authorization"))
				_, _ = fmt.Fprint(w, tc.roles)
			})
			dispatch := Dispatch{Type: "MESSAGE_CREATE", Data: json.RawMessage(`{
				"id":"666","guild_id":"333","channel_id":"444","author":{"id":"999"},
				"content":"<@&777>","mention_roles":["777","333"]}`)}
			event, ok, err := NormalizeMessage(dispatch, "222")
			require.NoError(t, err)
			require.True(t, ok)
			resolved, err := client.ResolveBotMention(t.Context(), event)
			require.NoError(t, err)
			require.Equal(t, tc.want, resolved.MentionsBot)
			require.Equal(t, event.Message, resolved.Message)
			require.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestResolveBotMentionSkipsUnneededLookups(t *testing.T) {
	for _, scenario := range []string{"direct and role", "no roles", "everyone", "self", "bot", "DM"} {
		t.Run(scenario, func(t *testing.T) {
			client, _ := testClient(t, func(http.ResponseWriter, *http.Request) {
				t.Error("unneeded role lookup")
			})
			event := MessageEvent{Message: Message{GuildID: "333", MentionRoles: []string{"777"}}}
			switch scenario {
			case "direct and role":
				event.MentionsBot = true
			case "no roles", "everyone":
				event.Message.MentionRoles = nil
				event.Message.Content = "@everyone"
			case "self":
				event.Self = true
			case "bot":
				event.Automated = true
			case "DM":
				event.Message.GuildID = ""
			}
			resolved, err := client.ResolveBotMention(t.Context(), event)
			require.NoError(t, err)
			require.Equal(t, event, resolved)
		})
	}
}

func TestResolveBotMentionRetriesAndPreservesFailures(t *testing.T) {
	for _, scenario := range []string{
		"retry succeeds", "unavailable", "rate limited", "invalid response", "oversized", "revoked during retry",
	} {
		t.Run(scenario, func(t *testing.T) {
			var calls atomic.Int32
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				attempt := calls.Add(1)
				switch scenario {
				case "rate limited":
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = fmt.Fprint(w, `{"retry_after":10}`)
				case "invalid response":
					_, _ = fmt.Fprint(w, `null`)
				case "oversized":
					_, _ = fmt.Fprint(w, strings.Repeat("x", ResponseMaxBytes+1))
				default:
					if scenario != "retry succeeds" || attempt == 1 {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					_, _ = fmt.Fprint(w, `[{"id":"777","managed":true,"tags":{"bot_id":"222"}}]`)
				}
			})
			denied := errors.New("authority revoked")
			client.beforeRequest = func(context.Context) error {
				if scenario == "revoked during retry" && calls.Load() > 0 {
					return denied
				}
				return nil
			}
			event, err := client.ResolveBotMention(t.Context(), MessageEvent{
				Message: Message{GuildID: "333", MentionRoles: []string{"777"}},
			})
			switch scenario {
			case "retry succeeds":
				require.NoError(t, err)
				require.True(t, event.MentionsBot)
				require.EqualValues(t, 2, calls.Load())
			case "unavailable":
				requireAPIError(t, err, TransientFailure)
				require.EqualValues(t, 3, calls.Load())
			case "rate limited":
				require.Equal(t, 10*time.Second, requireAPIError(t, err, RateLimited).RetryAfter)
				require.EqualValues(t, 1, calls.Load())
			case "revoked during retry":
				require.ErrorIs(t, err, denied)
				require.EqualValues(t, 1, calls.Load())
			default:
				requireAPIError(t, err, InvalidResponse)
				require.EqualValues(t, 1, calls.Load())
			}
		})
	}
}
