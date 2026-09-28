package integrationdefinition

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRoutingAddressesOnlyContainSupportedConversations(t *testing.T) {
	slack := Event{Scope: Scope{Slack: &SlackScope{ChannelID: "C123", ThreadTS: "1.2"}}, Kind: "message"}
	github := Event{Scope: Scope{GitHub: &GitHubScope{RepositoryID: 123, PullRequest: 7}}, Kind: "discussion_comment"}
	for _, tc := range []struct {
		name  string
		event Event
		want  []EventAddress
	}{
		{"Slack thread", slack, []EventAddress{{"thread", "C123:1.2"}, {"channel", "C123"}}},
		{"Slack channel", Event{Scope: Scope{Slack: &SlackScope{ChannelID: "C123"}}, Kind: "message"},
			[]EventAddress{{"channel", "C123"}}},
		{"Slack DM", Event{Scope: Scope{Slack: &SlackScope{ChannelID: "D123"}}, Kind: "message"},
			[]EventAddress{{"dm", "D123"}}},
		{"GitHub PR", github, []EventAddress{{"pull_request", "123#7"}}},
		{"Discord thread", Event{
			Scope: Scope{Discord: &DiscordScope{ChannelID: "456", ThreadID: "789"}}, Kind: "message",
		}, []EventAddress{{"thread", "789"}, {"channel", "456"}}},
		{"Discord thread only", Event{
			Scope: Scope{Discord: &DiscordScope{ThreadID: "789"}}, Kind: "message",
		}, []EventAddress{{"thread", "789"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addresses, err := tc.event.RoutingAddresses()
			require.NoError(t, err)
			require.Equal(t, tc.want, addresses)
			for _, address := range addresses {
				_, err := ParseConversation(tc.event.Scope.Provider(), address.Kind, address.Ref)
				require.NoError(t, err, "every routing address must be a supported subscription conversation")
			}
		})
	}
	github.Scope.GitHub.RepositoryID = 0
	addresses, err := github.RoutingAddresses()
	require.Error(t, err)
	require.Nil(t, addresses)
}
