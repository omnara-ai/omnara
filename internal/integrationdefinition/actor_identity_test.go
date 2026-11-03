package integrationdefinition

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestActorIdentityUsesPlatformPersonScope(t *testing.T) {
	for _, tc := range []struct {
		kind                     Kind
		tenant, namespace, label string
	}{
		{SlackThread, "T123", "slack:T123", "Slack"},
		{SlackThread, "T456", "slack:T456", "Slack"},
		{DiscordThread, "application-1", "discord", "Discord"},
		{DiscordThread, "application-2", "discord", "Discord"},
		{GitHubPR, "installation-1", "github:github.com", "GitHub"},
		{GitHubPR, "installation-2", "github:github.com", "GitHub"},
	} {
		t.Run(string(tc.kind)+"/"+tc.tenant, func(t *testing.T) {
			definition, ok := Lookup(tc.kind)
			require.True(t, ok)
			namespace, label, err := definition.ActorIdentity(tc.tenant)
			require.NoError(t, err)
			require.Equal(t, tc.namespace, namespace)
			require.Equal(t, tc.label, label)
		})
	}
	futureChannel := Definition{IntegrationKind: "future_slack_channel", Provider: ProviderSlack}
	namespace, label, err := futureChannel.ActorIdentity("T123")
	require.NoError(t, err)
	require.Equal(t, "slack:T123", namespace)
	require.Equal(t, "Slack", label)
	for _, tenant := range []string{"", " \t"} {
		_, _, err := futureChannel.ActorIdentity(tenant)
		require.Error(t, err)
	}
	_, _, err = (Definition{Provider: "unknown"}).ActorIdentity("T123")
	require.Error(t, err)
}
