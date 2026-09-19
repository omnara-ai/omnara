package appdefinition

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestEventRoutingAddressesAndLaunchTriggers(t *testing.T) {
	tests := []struct {
		name, account string
		event         Event
		addresses     []EventAddress
		trigger       string
	}{
		{
			"slack",
			"T123",
			Event{
				Scope:     Scope{Slack: &SlackScope{ChannelID: "C123", ThreadTS: "123.456"}},
				Kind:      "message",
				Mentioned: true,
			},
			[]EventAddress{{"thread", "C123:123.456"}, {"channel", "C123"}, {"workspace", "T123"}},
			"mention",
		},
		{
			"github",
			"456",
			Event{Scope: Scope{GitHub: &GitHubScope{RepositoryID: 123, PullRequest: 7}}, Kind: "pull_request_opened"},
			[]EventAddress{{"pull_request", "123#7"}, {"repository", "123"}, {"installation", "456"}},
			"pull_request_opened",
		},
		{
			"discord",
			"999",
			Event{
				Scope:     Scope{Discord: &DiscordScope{GuildID: "123", ChannelID: "456", ThreadID: "789"}},
				Kind:      "message",
				Mentioned: true,
			},
			[]EventAddress{{"thread", "456:789"}, {"channel", "456"}, {"guild", "123"}},
			"mention",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			addresses, err := test.event.RoutingAddresses(test.account)
			require.NoError(t, err)
			require.Equal(t, test.addresses, addresses)
			require.True(t, test.event.MatchesLauncher(test.trigger))
			require.False(t, test.event.MatchesLauncher("arbitrary"))
		})
	}
	event := tests[0].event
	event.Mentioned = false
	require.False(t, event.MatchesLauncher("mention"))
	event = tests[2].event
	addresses, err := event.RoutingAddresses("different-application-account")
	require.NoError(t, err)
	require.Equal(t, EventAddress{"guild", "123"}, addresses[len(addresses)-1])
	event = tests[1].event
	event.Kind = "commit"
	event.Mentioned = true
	require.False(t, event.MatchesLauncher("mention"), "commit text is not a provider mention event")
}
