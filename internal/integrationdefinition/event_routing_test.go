package integrationdefinition

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRoutingAddressesValidateAccountAndPreserveHierarchy(t *testing.T) {
	slack := Event{Scope: Scope{Slack: &SlackScope{ChannelID: "C123", ThreadTS: "1.2"}}, Kind: "message"}
	github := Event{Scope: Scope{GitHub: &GitHubScope{RepositoryID: 123, PullRequest: 7}}, Kind: "discussion_comment"}
	for _, tc := range []struct {
		name    string
		event   Event
		account string
		want    []EventAddress
	}{
		{"Slack thread", slack, " T123 ", []EventAddress{{"thread", "C123:1.2"}, {"channel", "C123"}, {"workspace", "T123"}}},
		{"Slack channel", Event{Scope: Scope{Slack: &SlackScope{ChannelID: "C123"}}, Kind: "message"},
			"T123", []EventAddress{{"channel", "C123"}, {"workspace", "T123"}}},
		{"Slack DM", Event{Scope: Scope{Slack: &SlackScope{ChannelID: "D123"}}, Kind: "message"},
			"T123", []EventAddress{{"dm", "D123"}, {"workspace", "T123"}}},
		{"GitHub PR", github, " 00456 ",
			[]EventAddress{{"pull_request", "123#7"}, {"repository", "123"}, {"installation", "456"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addresses, err := tc.event.RoutingAddresses(tc.account)
			require.NoError(t, err)
			require.Equal(t, tc.want, addresses)
		})
	}
	for _, account := range []string{"", "workspace", "${workspace}", "C123"} {
		addresses, err := slack.RoutingAddresses(account)
		require.Error(t, err, account)
		require.Nil(t, addresses, "invalid account cannot leave partially usable routes")
	}
	for _, account := range []string{"", "0", "-1", "owner/repository", "9223372036854775808"} {
		addresses, err := github.RoutingAddresses(account)
		require.Error(t, err, account)
		require.Nil(t, addresses)
	}
	github.Scope.GitHub.RepositoryID = 0
	addresses, err := github.RoutingAddresses("456")
	require.Error(t, err)
	require.Nil(t, addresses)
}
