package integrationdefinition

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEventInputContextCarriesSenderAndBotMention(t *testing.T) {
	for _, scope := range []Scope{
		{Discord: &DiscordScope{GuildID: "100", ChannelID: "200", ThreadID: "300"}},
		{GitHub: &GitHubScope{RepositoryID: 123, PullRequest: 42}},
	} {
		for _, mentioned := range []bool{false, true} {
			details := InputContext{SenderID: "71", SenderName: "Ada", Mentioned: mentioned, ConversationName: "owner/repo#42"}
			raw, err := AppendEventInputContext("reviews", scope, json.RawMessage(`[{"type":"text","text":"hello"}]`), details)
			require.NoError(t, err)
			var blocks []struct {
				Text     string            `json:"text"`
				Metadata map[string]string `json:"metadata"`
			}
			require.NoError(t, json.Unmarshal(raw, &blocks))
			require.Len(t, blocks, 2)
			require.Equal(t, "hello", blocks[0].Text)
			require.Equal(t, "true", blocks[1].Metadata["omnara_hidden"])
			encoded, err := json.Marshal(details)
			require.NoError(t, err)
			require.Contains(t, blocks[1].Text, string(encoded))
			require.Contains(t, blocks[1].Text, "your integration bot")
			require.Contains(t, blocks[1].Text, "not every message is directed at you")
			var captured struct {
				Event InputContext `json:"event"`
			}
			rawContext := strings.TrimPrefix(blocks[1].Text, "Incoming integration conversation: ")
			require.NoError(t, json.Unmarshal([]byte(rawContext), &captured))
			require.Equal(t, details, captured.Event)
		}
	}
}

func TestScheduledInputContextDoesNotInventSenderOrMention(t *testing.T) {
	raw, err := AppendInputContext("daily", Scope{GitHub: &GitHubScope{RepositoryID: 123, PullRequest: 42}}, json.RawMessage(`[{"type":"text","text":"scheduled"}]`))
	require.NoError(t, err)
	require.NotContains(t, string(raw), "sender_id")
	require.NotContains(t, string(raw), "bot_mentioned")
}
