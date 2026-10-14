package integration

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/stretchr/testify/require"
)

func TestDiscordInboxDropsUnsupportedOnlyNonMentionsBeforeRouting(t *testing.T) {
	for _, test := range []struct{ name, field, value string }{
		{"empty", "", ""},
		{"sticker", "sticker_items", `[{"id":"600","name":"wave","format_type":1}]`},
		{"poll", "poll", `{"question":{"text":"Choose"}}`},
		{"forward", "message_snapshots", `[{"message":{"content":"forwarded text"}}]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, provider := newDiscordInboxFixture(t)
			f.message.ChannelID, f.message.Content, f.message.Mentions = "400", "", nil
			encoded, err := json.Marshal(f.message)
			require.NoError(t, err)
			var message map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(encoded, &message))
			if test.field != "" {
				message[test.field] = json.RawMessage(test.value)
			}
			encoded, err = json.Marshal(message)
			require.NoError(t, err)
			raw, err := json.Marshal(discord.Dispatch{Type: "MESSAGE_CREATE", Sequence: 17, Data: encoded})
			require.NoError(t, err)
			_, ok, err := normalizeDiscordIntegrationEvent(f.integrationSetup, raw, f.channels["400"])
			require.NoError(t, err)
			require.False(t, ok)
			expansion, err := provider.ExpandRouted(t.Context(), f.integrationSetup, raw,
				func(IntegrationEvent) (bool, error) {
					t.Error("content-less non-mention reached routing")
					return true, nil
				})
			require.NoError(t, err)
			require.Nil(t, expansion.Event)
			require.Empty(t, expansion.Files)
			require.Empty(t, f.requests)
		})
	}
}

func TestDiscordInboxEmptyFilterKeepsTextFilesAndNativeMentions(t *testing.T) {
	for _, test := range []struct {
		name, content string
		mention, file bool
	}{
		{"text", "reply", false, false},
		{"attachment only", "", false, true},
		{"mention only", "<@22>", true, false},
		{"missing mention content", "", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, provider := newDiscordInboxFixture(t)
			f.message.Content = test.content
			if !test.mention {
				f.message.ChannelID, f.message.Mentions = "400", nil
			}
			if test.file {
				f.attach("600", []byte("attachment"), "text/plain")
			}
			raw := discordInboxPayload(t, f.message)
			_, ok, err := normalizeDiscordIntegrationEvent(f.integrationSetup, raw, f.channels[f.message.ChannelID])
			require.NoError(t, err)
			require.True(t, ok)
			routed := false
			expansion, err := provider.ExpandRouted(t.Context(), f.integrationSetup, raw,
				func(event IntegrationEvent) (bool, error) {
					routed = true
					_, err := prepareIntegrationEvent(event, f.integrationSetup)
					require.NoError(t, err)
					return true, nil
				})
			require.True(t, routed)
			if test.mention && test.content == "" {
				require.ErrorIs(t, err, ErrIntegrationInboundPermanent)
				require.ErrorContains(t, err, "no supported content")
				require.Nil(t, expansion.Event)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, expansion.Event)
			require.Equal(t, test.mention, expansion.Event.Event.Mentioned)
			if test.file {
				require.Len(t, expansion.Files, 1)
			} else {
				require.JSONEq(t, `[{"type":"text","text":"`+test.content+`"}]`, string(expansion.Event.ContentBlocks))
			}
		})
	}
}
