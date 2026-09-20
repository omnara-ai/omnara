package appdefinition

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonicalScheduledDestination(t *testing.T) {
	for _, test := range []struct{ provider, raw, want string }{
		{ProviderSlack, ` { "channel_id": "C123" } `, `{"channel_id":"C123"}`},
		{ProviderSlack, `{"channel_id":"G123"}`, `{"channel_id":"G123"}`},
		{ProviderDiscord, `{"channel_id":"123","guild_id":"456"}`, `{"guild_id":"456","channel_id":"123"}`},
		{ProviderDiscord, `{"channel_id":"123"}`, `{"channel_id":"123"}`},
	} {
		t.Run(test.provider+test.raw, func(t *testing.T) {
			actual, err := CanonicalScheduledDestination(test.provider, json.RawMessage(test.raw))
			require.NoError(t, err)
			require.Equal(t, test.want, string(actual))
		})
	}
	for _, test := range []struct{ provider, raw string }{
		{ProviderGitHub, `{"repository_id":1,"pull_request":2}`},
		{ProviderSlack, `{}`}, {ProviderDiscord, `{}`}, {ProviderSlack, `null`},
		{ProviderSlack, `{"channel_id":"D123"}`},
		{ProviderSlack, `{"channel_id":"C123","thread_ts":"1.2"}`},
		{ProviderDiscord, `{"channel_id":"123","thread_id":"456"}`},
		{ProviderSlack, `{"channel_id":"C123","guild_id":"456"}`},
		{ProviderDiscord, `{"channel_id":"bad"}`},
	} {
		t.Run("invalid "+test.provider+test.raw, func(t *testing.T) {
			_, err := CanonicalScheduledDestination(test.provider, json.RawMessage(test.raw))
			require.Error(t, err)
		})
	}
}
