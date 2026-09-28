package integration

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func TestDiscordInteractionScopeUsesThreadIDOnly(t *testing.T) {
	scope, err := DiscordInteractionScope(executionstore.InteractionDestination{
		IntegrationKind: integrationdefinition.DiscordThread,
		Address:         integrationstore.ConversationAddress{Kind: "thread", Ref: "400"},
	})
	require.NoError(t, err)
	require.Equal(t, discord.Scope{ThreadID: "400"}, scope)
}

func TestDiscordRoutedThreadNormalizesAfterSingleChannelRead(t *testing.T) {
	for _, mentioned := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "mention"}[mentioned], func(t *testing.T) {
			f, provider := newDiscordInboxFixture(t)
			f.message.ChannelID = "400"
			if !mentioned {
				f.message.Mentions = nil
			}
			raw := discordInboxPayload(t, f.message)
			want, ok, err := NormalizeDiscordIntegrationEvent(f.integrationSetup, raw, f.channels["400"])
			require.NoError(t, err)
			require.True(t, ok)
			calls := 0
			expansion, err := provider.ExpandRouted(t.Context(), f.integrationSetup, raw,
				func(event IntegrationEvent) (bool, error) {
					calls++
					require.Equal(t, want.Actor, event.Actor)
					require.JSONEq(t, string(want.ContentBlocks), string(event.ContentBlocks))
					require.Equal(t, want.SemanticKey, event.SemanticKey)
					require.Equal(t, want.DeliveryMode, event.DeliveryMode)
					require.Equal(t, want.CancelOpenInteractions, event.CancelOpenInteractions)
					request, err := prepareIntegrationEvent(event, f.integrationSetup)
					require.NoError(t, err, "the provisional event must pass the real route validation")
					require.Equal(t, "400", request.address.Ref)
					if mentioned {
						require.Equal(t, "300", event.Event.Scope.Discord.ChannelID)
						require.Equal(t, []string{"GET /api/v10/channels/400"}, f.requests)
					} else {
						require.Equal(t, &integrationdefinition.DiscordScope{GuildID: "100", ThreadID: "400"}, event.Event.Scope.Discord)
						require.Empty(t, f.requests)
					}
					return true, nil
				})
			require.NoError(t, err)
			require.Equal(t, 1, calls)
			require.NotNil(t, expansion.Event)
			require.Equal(t, want.Event, expansion.Event.Event, "provider facts must replace the provisional scope")
			require.Equal(t, want.DisplayName, expansion.Event.DisplayName)
			require.JSONEq(t, string(want.Metadata), string(expansion.Event.Metadata))
			require.Equal(t, []string{
				"GET /api/v10/channels/400", "GET /api/v10/users/@me", "GET /api/v10/applications/@me",
			}, f.requests)
		})
	}
}

func TestDiscordCheapRoutingRetainsMessageValidation(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*discord.Message)
		fail bool
	}{
		{"invalid author", func(m *discord.Message) { m.Author.ID = "invalid" }, true},
		{"invalid channel", func(m *discord.Message) { m.ChannelID = "invalid" }, true},
		{"invalid guild", func(m *discord.Message) { m.GuildID = "invalid" }, true},
		{"self", func(m *discord.Message) { m.Author.ID = "22" }, false},
		{"bot", func(m *discord.Message) { m.Author.Bot = true }, false},
		{"webhook", func(m *discord.Message) { m.WebhookID = "123" }, false},
		{"DM", func(m *discord.Message) { m.GuildID = "" }, false},
		{"system", func(m *discord.Message) { m.Type = 18 }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, provider := newDiscordInboxFixture(t)
			f.message.Mentions = nil
			test.edit(&f.message)
			expansion, err := provider.ExpandRouted(t.Context(), f.integrationSetup, discordInboxPayload(t, f.message),
				func(IntegrationEvent) (bool, error) {
					t.Error("invalid or filtered message reached route callback")
					return false, nil
				})
			require.Equal(t, test.fail, err != nil, "%v", err)
			require.Nil(t, expansion.Event)
			require.Empty(t, f.requests)
		})
	}
}
