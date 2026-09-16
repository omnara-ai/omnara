package httpapi

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func TestChannelInstallationCredentialProjection(t *testing.T) {
	t.Parallel()
	payload := secrets.Payload{
		secrets.KeyAccessToken: "bot-token", secrets.KeyClientID: "client-id",
		secrets.KeyClientSecret: "client-secret", secrets.KeySigningSecret: "signing-secret",
	}
	for _, test := range []struct {
		name, connector, provider, expected string
	}{
		{"builtin Slack", channelconnector.BuiltInConnectorKey, "slack", `{"access_token":"bot-token"}`},
		{"custom Slack", "custom", "slack",
			`{"access_token":"bot-token","client_id":"client-id","client_secret":"client-secret","signing_secret":"signing-secret"}`},
		{"different provider", channelconnector.BuiltInConnectorKey, "discord",
			`{"access_token":"bot-token","client_id":"client-id","client_secret":"client-secret","signing_secret":"signing-secret"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			encoded, err := channelInstallationCredentialPayloadJSON(integrationstore.IntegrationAppRecord{
				ConnectorKey: test.connector, Provider: test.provider,
			}, payload)
			require.NoError(t, err)
			require.JSONEq(t, test.expected, string(encoded))
			require.Equal(t, "signing-secret", payload[secrets.KeySigningSecret], "projection must not mutate stored payload")
		})
	}
}

func TestFirstPartyAppCredentialsExcludeOAuthSetupSecrets(t *testing.T) {
	t.Parallel()
	payload := secrets.Payload{
		"client_secret": "setup-only", "private_key": "app-key", "webhook_secret": "signature-key", "bot_token": "bot-token",
	}
	for _, test := range []struct{ provider, expected string }{
		{"github", `{"private_key":"app-key","webhook_secret":"signature-key"}`},
		{"discord", `{"bot_token":"bot-token"}`},
	} {
		t.Run(test.provider, func(t *testing.T) {
			t.Parallel()
			encoded, err := channelAppCredentialPayloadJSON(integrationstore.IntegrationAppRecord{
				ConnectorKey: channelconnector.BuiltInConnectorKey, Provider: test.provider,
			}, payload)
			require.NoError(t, err)
			require.JSONEq(t, test.expected, string(encoded))
			require.NotContains(t, string(encoded), "setup-only")
			require.Equal(t, "setup-only", payload["client_secret"])
		})
	}
}
