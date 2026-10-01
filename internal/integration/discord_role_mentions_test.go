package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscordRoleMentionRoutesAndPreparesSameConversation(t *testing.T) {
	for _, channel := range []string{"300", "400"} {
		for _, both := range []bool{false, true} {
			t.Run(fmt.Sprintf("channel=%s/both=%t", channel, both), func(t *testing.T) {
				f, provider := newDiscordInboxFixture(t)
				f.message.ChannelID, f.message.MentionRoles = channel, []string{"700"}
				f.message.Content = "<@&700> help"
				if !both {
					f.message.Mentions = nil
				}
				raw := discordInboxPayload(t, f.message)
				var routed IntegrationEvent
				calls := 0
				expansion, err := provider.ExpandRouted(t.Context(), f.integrationSetup, raw,
					func(event IntegrationEvent) (bool, error) {
						calls++
						routed = event
						require.True(t, event.Event.Mentioned)
						return true, nil
					})
				require.NoError(t, err)
				require.NotNil(t, expansion.Event)
				require.Equal(t, 1, calls)
				require.Equal(t, routed.Event, expansion.Event.Event)
				require.Equal(t, "discord:message:11:500", expansion.Event.SemanticKey)
				require.Zero(t, f.posts)
				f.mu.Lock()
				f.roles = json.RawMessage(`[{"id":"700","managed":true,"tags":{"bot_id":"99"}}]`)
				f.mu.Unlock()
				require.NoError(t, provider.PrepareConversation(t.Context(), f.integrationSetup, raw,
					expansion.Event.Event.Scope, func(context.Context) error { return nil }),
					"changed roles must not invalidate a frozen conversation")
				if channel == "300" {
					require.Equal(t, 1, f.posts)
					require.Equal(t, "500", expansion.Event.Event.Scope.Discord.ThreadID)
				} else {
					require.Zero(t, f.posts)
					require.Equal(t, "400", expansion.Event.Event.Scope.Discord.ThreadID)
				}
				roleReads := 0
				for _, request := range f.requests {
					if strings.HasSuffix(request, "/roles") {
						roleReads++
					}
				}
				if both {
					require.Zero(t, roleReads, "a direct mention needs no role lookup")
				} else {
					require.Equal(t, 1, roleReads, "preparation must use the frozen decision without another role lookup")
				}
			})
		}
	}
}

func TestDiscordRoleLookupFailureCannotFreezeUnroutedMessage(t *testing.T) {
	f, provider := newDiscordInboxFixture(t)
	f.message.Mentions, f.message.MentionRoles = nil, []string{"700"}
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/roles") {
			return false
		}
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprint(w, `{"retry_after":10}`)
		return true
	}
	raw := discordInboxPayload(t, f.message)
	expansion, err := provider.ExpandRouted(t.Context(), f.integrationSetup, raw,
		func(IntegrationEvent) (bool, error) {
			t.Error("unresolved role mention reached routing")
			return false, nil
		})
	var apiErr *discord.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, discord.RateLimited, apiErr.Code)
	require.Nil(t, expansion.Event)
	require.Equal(t, []string{"GET /api/v10/guilds/100/roles"}, f.requests)
	f.mu.Lock()
	f.override = nil
	f.mu.Unlock()
	expansion, err = provider.ExpandRouted(t.Context(), f.integrationSetup, raw,
		func(event IntegrationEvent) (bool, error) { return event.Event.Mentioned, nil })
	require.NoError(t, err)
	require.NotNil(t, expansion.Event)
	require.True(t, expansion.Event.Event.Mentioned)
}

func TestDiscordRoleLookupRetainsAuthorityAndEmptyFiltering(t *testing.T) {
	for _, scenario := range []string{"revoked", "self", "unrelated empty", "own empty"} {
		t.Run(scenario, func(t *testing.T) {
			f, provider := newDiscordInboxFixture(t)
			f.message.Mentions, f.message.MentionRoles, f.message.Content = nil, []string{"700"}, ""
			switch scenario {
			case "revoked":
				f.afterRead = func() { f.revoked = true }
			case "self":
				f.message.Author.ID = "22"
			case "unrelated empty":
				f.roles = json.RawMessage(`[{"id":"700","managed":true,"tags":{"bot_id":"99"}}]`)
			}
			calls := 0
			expansion, err := provider.ExpandRouted(t.Context(), f.integrationSetup, discordInboxPayload(t, f.message),
				func(event IntegrationEvent) (bool, error) {
					calls++
					require.True(t, event.Event.Mentioned)
					return true, nil
				})
			require.Nil(t, expansion.Event)
			if scenario == "own empty" {
				require.ErrorIs(t, err, ErrIntegrationInboundPermanent)
				require.Equal(t, 1, calls, "recognized mention must not be silently discarded")
			} else {
				require.Zero(t, calls)
				if scenario == "revoked" {
					require.ErrorIs(t, err, storeerr.ErrUnauthorized)
				} else {
					require.NoError(t, err)
				}
				if scenario != "unrelated empty" {
					require.Empty(t, f.requests)
				}
			}
		})
	}
}

func TestDiscordRoleMentionFailureFeedback(t *testing.T) {
	for _, scenario := range []string{"own", "unavailable launch", "unrelated", "lookup failure", "revoked after lookup"} {
		t.Run(scenario, func(t *testing.T) {
			f, provider := newDiscordInboxFixture(t)
			f.message.Mentions, f.message.MentionRoles = nil, []string{"700"}
			if scenario == "unrelated" {
				f.roles = json.RawMessage(`[{"id":"700","name":"Helper","managed":false}]`)
			}
			wantText := inboxFailureMessage
			if scenario == "unavailable launch" {
				wantText = launchUnavailableMessage
			}
			sends := 0
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if strings.HasSuffix(r.URL.Path, "/roles") {
					if scenario == "lookup failure" {
						w.WriteHeader(http.StatusTooManyRequests)
						_, _ = fmt.Fprint(w, `{"retry_after":10}`)
						return true
					}
					if scenario == "revoked after lookup" {
						f.revoked = true
					}
				}
				if r.Method != http.MethodPost {
					return false
				}
				sends++
				assert.Equal(t, "/api/v10/channels/300/messages", r.URL.Path)
				var body struct{ Content, Nonce string }
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				assert.Equal(t, wantText, body.Content)
				nonce, _ := json.Marshal(body.Nonce)
				assert.NoError(t, json.NewEncoder(w).Encode(discord.Message{
					ID: "900", ChannelID: "300", Author: discord.User{ID: "22"}, Nonce: nonce,
				}))
				return true
			}
			raw := discordInboxPayload(t, f.message)
			receipt := feedbackReceipt(f.integrationSetup, raw)
			var err error
			if scenario == "unavailable launch" {
				expansion, expandErr := provider.Expand(t.Context(), f.integrationSetup, raw)
				require.NoError(t, expandErr)
				require.NotNil(t, expansion.Event)
				workflow := &IntegrationLaunchWorkflow{providers: map[string]IntegrationInboxProvider{"discord": provider}}
				workflow.launchUnavailable(t.Context(), IntegrationLaunchContext{
					Integration: f.integrationSetup, Receipt: receipt, Event: *expansion.Event,
				}, ErrIntegrationLaunchUnavailable)
			} else {
				err = provider.NotifyInboxFailure(t.Context(), f.integrationSetup, receipt, inboxFailureMessage)
			}
			switch scenario {
			case "own", "unavailable launch":
				require.NoError(t, err)
				require.Equal(t, 1, sends)
			case "unrelated":
				require.NoError(t, err)
				require.Zero(t, sends)
				require.Equal(t, []string{"GET /api/v10/guilds/100/roles"}, f.requests)
			default:
				require.Error(t, err)
				require.Zero(t, sends)
			}
			require.Zero(t, f.posts, "feedback does not create a thread")
		})
	}
}
