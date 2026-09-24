package integrationdefinition

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEventRoutingAddressesAndLaunchTriggers(t *testing.T) {
	tests := []struct {
		integrationType Type
		name, account   string
		event           Event
		addresses       []EventAddress
		trigger         string
	}{
		{
			SlackThread,
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
			GitHubPR,
			"github",
			"456",
			Event{Scope: Scope{GitHub: &GitHubScope{RepositoryID: 123, PullRequest: 7}}, Kind: "pull_request_opened"},
			[]EventAddress{{"pull_request", "123#7"}, {"repository", "123"}, {"installation", "456"}},
			"pull_request_opened",
		},
		{
			DiscordThread,
			"discord",
			"999",
			Event{
				Scope:     Scope{Discord: &DiscordScope{GuildID: "123", ChannelID: "456", ThreadID: "789"}},
				Kind:      "message",
				Mentioned: true,
			},
			[]EventAddress{{"thread", "456:789"}, {"channel", "456"}},
			"mention",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition, _ := Lookup(test.integrationType)
			addresses, err := test.event.RoutingAddresses(test.account)
			require.NoError(t, err)
			require.Equal(t, test.addresses, addresses)
			require.True(t, definition.MatchesLauncher(test.event, test.trigger))
			require.False(t, definition.MatchesLauncher(test.event, "arbitrary"))
		})
	}
	event := tests[0].event
	definition, _ := Lookup(SlackThread)
	event.Mentioned = false
	require.False(t, definition.MatchesLauncher(event, "mention"))
	event = tests[2].event
	definition, _ = Lookup(DiscordThread)
	addresses, err := event.RoutingAddresses("different-application-account")
	require.NoError(t, err)
	require.Equal(t, tests[2].addresses, addresses)
	event.Scope.Discord.GuildID = ""
	require.False(t, definition.MatchesLauncher(event, "mention"), "Discord launchers do not support DMs")
	event = tests[1].event
	definition, _ = Lookup(GitHubPR)
	event.Kind = "commit"
	event.Mentioned = true
	require.False(t, definition.MatchesLauncher(event, "mention"), "commit text is not a provider mention event")
}

func TestLauncherMatchesOnlyDeclaredTriggers(t *testing.T) {
	definition, _ := Lookup(GitHubPR)
	event := Event{
		Scope: Scope{GitHub: &GitHubScope{RepositoryID: 123, PullRequest: 7}},
		Kind:  "review_comment", Mentioned: true,
	}
	require.True(t, definition.MatchesLauncher(event, "mention"))
	definition.LaunchTriggers = []string{"pull_request_opened"}
	require.False(t, definition.SupportsLaunchTrigger("mention"))
	require.False(t, definition.MatchesLauncher(event, "mention"))
	event.Kind = "pull_request_opened"
	require.True(t, definition.MatchesLauncher(event, "pull_request_opened"))
	definition.LaunchTriggers = nil
	require.False(t, definition.MatchesLauncher(event, "pull_request_opened"))

	definition, _ = Lookup(SlackThread)
	event.Kind = "discussion_comment"
	require.False(t, definition.MatchesLauncher(event, "mention"), "events must belong to the integration's provider")
}
