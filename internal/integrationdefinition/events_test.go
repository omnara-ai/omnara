package integrationdefinition

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEventRoutingAddressesAndLaunchTriggers(t *testing.T) {
	tests := []struct {
		integrationKind Kind
		name            string
		event           Event
		addresses       []EventAddress
		trigger         string
	}{
		{
			SlackThread,
			"slack",
			Event{
				Scope:     Scope{Slack: &SlackScope{ChannelID: "C123", ThreadTS: "123.456"}},
				Kind:      "message",
				Mentioned: true,
			},
			[]EventAddress{{"thread", "C123:123.456"}, {"channel", "C123"}},
			"mention",
		},
		{
			GitHubPR,
			"github",
			Event{Scope: Scope{GitHub: &GitHubScope{RepositoryID: 123, PullRequest: 7}}, Kind: "pull_request_opened"},
			[]EventAddress{{"pull_request", "123#7"}},
			"pull_request_opened",
		},
		{
			DiscordThread,
			"discord",
			Event{
				Scope:     Scope{Discord: &DiscordScope{GuildID: "123", ChannelID: "456", ThreadID: "789"}},
				Kind:      "message",
				Mentioned: true,
			},
			[]EventAddress{{"thread", "789"}, {"channel", "456"}},
			"mention",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition, _ := Lookup(test.integrationKind)
			addresses, err := test.event.RoutingAddresses()
			require.NoError(t, err)
			require.Equal(t, test.addresses, addresses)
			require.True(t, definition.MatchesLaunch(testLaunchSettings(definition.IntegrationKind, test.trigger), test.event))
			if definition.IntegrationKind == GitHubPR {
				require.False(t, definition.MatchesLaunch(testLaunchSettings(definition.IntegrationKind, "arbitrary"), test.event))
			}
		})
	}
	event := tests[0].event
	definition, _ := Lookup(SlackThread)
	event.Mentioned = false
	require.False(t, definition.MatchesLaunch(testLaunchSettings(definition.IntegrationKind, "mention"), event))
	event = tests[2].event
	definition, _ = Lookup(DiscordThread)
	addresses, err := event.RoutingAddresses()
	require.NoError(t, err)
	require.Equal(t, tests[2].addresses, addresses)
	event.Scope.Discord.ChannelID = ""
	addresses, err = event.RoutingAddresses()
	require.NoError(t, err)
	require.Equal(t, []EventAddress{{"thread", "789"}}, addresses, "unknown parent must not add a routing address")
	event.Scope.Discord.GuildID = ""
	require.False(t, definition.MatchesLaunch(testLaunchSettings(definition.IntegrationKind, "mention"), event),
		"Discord launchers do not support DMs")
	event = tests[1].event
	definition, _ = Lookup(GitHubPR)
	event.Kind = "commit"
	event.Mentioned = true
	require.False(t, definition.MatchesLaunch(testLaunchSettings(definition.IntegrationKind, "mention"), event),
		"commit text is not a provider mention event")
}

func TestLauncherMayBeAbsent(t *testing.T) {
	definition, _ := Lookup(GitHubPR)
	event := Event{
		Scope: Scope{GitHub: &GitHubScope{RepositoryID: 123, PullRequest: 7}}, Kind: "discussion_comment", Mentioned: true,
	}
	require.True(t, definition.MatchesLaunch(testLaunchSettings(GitHubPR, "mention"), event))
	definition.Launcher = nil
	require.False(t, definition.MatchesLaunch(testLaunchSettings(GitHubPR, "mention"), event))
	definition, _ = Lookup(SlackThread)
	require.False(t, definition.MatchesLaunch(testLaunchSettings(SlackThread, "mention"), event))
}
