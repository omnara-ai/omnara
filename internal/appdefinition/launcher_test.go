package appdefinition

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonicalLauncherScope(t *testing.T) {
	for _, test := range []struct{ provider, kind, ref, canonical string }{
		{ProviderSlack, "workspace", " T123 ", "T123"},
		{ProviderSlack, "channel", "C123", "C123"},
		{ProviderSlack, "dm", "D123", "D123"},
		{ProviderSlack, "thread", "C123:123.456", "C123:123.456"},
		{ProviderGitHub, "installation", "00123", "123"},
		{ProviderGitHub, "repository", "00123", "123"},
		{ProviderGitHub, "pull_request", "00123#0012", "123#12"},
		{ProviderDiscord, "guild", "123456", "123456"},
		{ProviderDiscord, "channel", "123456", "123456"},
		{ProviderDiscord, "thread", "123456:789012", "123456:789012"},
	} {
		t.Run(test.provider+"/"+test.kind, func(t *testing.T) {
			kind, ref, err := CanonicalLauncherScope(test.provider, test.kind, test.ref)
			require.NoError(t, err)
			require.Equal(t, test.kind, kind)
			require.Equal(t, test.canonical, ref)
			kindAgain, refAgain, err := CanonicalLauncherScope(test.provider, kind, ref)
			require.NoError(t, err)
			require.Equal(t, kind, kindAgain)
			require.Equal(t, ref, refAgain)
		})
	}
	for _, test := range []struct{ provider, kind, ref string }{
		{ProviderSlack, "workspce", "T123"},
		{ProviderSlack, "workspace", "inbox-team"},
		{ProviderSlack, "workspace", "${workspace}"},
		{ProviderSlack, "channel", "D123"},
		{ProviderSlack, "dm", "C123"},
		{ProviderSlack, "thread", "C123:"},
		{ProviderSlack, "thread", "C123:${thread}"},
		{ProviderGitHub, "installation", "0"},
		{ProviderGitHub, "installation", "9223372036854775808"},
		{ProviderGitHub, "repository", "owner/repo"},
		{ProviderGitHub, "repository", "0"},
		{ProviderGitHub, "repository", "-1"},
		{ProviderGitHub, "repository", "9223372036854775808"},
		{ProviderGitHub, "pull_request", "123#0"},
		{ProviderGitHub, "pull_request", "123#12#13"},
		{ProviderGitHub, "pull_request", "owner/repo#12"},
		{ProviderDiscord, "guild", "00123"},
		{ProviderDiscord, "thread", "123:"},
		{ProviderDiscord, "workspace", "123"},
		{"external", "ticket", "CaseSensitive42"},
		{"unknown", "channel", "C123"},
	} {
		t.Run("invalid/"+test.provider+"/"+test.kind+"/"+test.ref, func(t *testing.T) {
			_, _, err := CanonicalLauncherScope(test.provider, test.kind, test.ref)
			require.Error(t, err)
		})
	}
}
