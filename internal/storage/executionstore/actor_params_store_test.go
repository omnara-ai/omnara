package executionstore

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func TestIntegrationActorParamsStableAcrossConfiguredBots(t *testing.T) {
	for _, tc := range []struct {
		kind                                  integrationdefinition.Kind
		provider                              integrationdefinition.Provider
		tenant, otherTenant, namespace, label string
	}{
		{integrationdefinition.SlackThread, integrationdefinition.ProviderSlack, "T123", "T123", "slack:T123", "Slack"},
		{integrationdefinition.DiscordThread, integrationdefinition.ProviderDiscord, "app-1", "app-2", "discord", "Discord"},
		{
			integrationdefinition.GitHubPR, integrationdefinition.ProviderGitHub,
			"install-1", "install-2", "github:github.com", "GitHub",
		},
	} {
		t.Run(string(tc.provider), func(t *testing.T) {
			integration := integrationstore.IntegrationRecord{
				ID: uuid.New(), ProjectID: uuid.New(), IntegrationKind: tc.kind,
				Provider: tc.provider, ProviderTenantID: tc.tenant, ProviderAccountRef: "bot-1",
			}
			actor, err := IntegrationActorParams(integration, "123", nil)
			require.NoError(t, err)
			require.Equal(t, ActorProviderIntegration, actor.Provider)
			require.Equal(t, tc.namespace, actor.ProviderTenantID)
			require.Equal(t, tc.label, actor.Metadata["source_label"])
			integration.ID, integration.ProviderAccountRef = uuid.New(), "bot-2"
			integration.ProviderTenantID = tc.otherTenant
			other, err := IntegrationActorParams(integration, "123", nil)
			require.NoError(t, err)
			require.Equal(t, actor, other)
			require.NoError(t, validateIntegrationInputActor(integration, &actor))
		})
	}
}

func TestValidateIntegrationInputActorRetainsNamespaceConsistency(t *testing.T) {
	integration := integrationstore.IntegrationRecord{
		ID: uuid.New(), IntegrationKind: integrationdefinition.SlackThread,
		Provider: "slack", ProviderTenantID: "T123",
	}
	actor, err := IntegrationActorParams(integration, "U123", nil)
	require.NoError(t, err)
	require.NoError(t, validateIntegrationInputActor(integration, &actor))
	require.Error(t, validateIntegrationInputActor(integration, nil))
	for _, change := range []func(*ActorParams){
		func(a *ActorParams) { a.ProviderTenantID = "slack:T_OTHER" },
		func(a *ActorParams) { a.ProviderTenantID = "discord" },
		func(a *ActorParams) { a.ProviderTenantID = integration.ID.String() },
		func(a *ActorParams) { a.Provider = ActorProviderExternal },
		func(a *ActorParams) { a.ProviderUserID = " \t" },
	} {
		bad := actor
		change(&bad)
		require.Error(t, validateIntegrationInputActor(integration, &bad))
	}
	integration.ProviderTenantID = ""
	_, err = IntegrationActorParams(integration, "U123", nil)
	require.Error(t, err)
	integration.IntegrationKind = "unknown"
	_, err = IntegrationActorParams(integration, "U123", nil)
	require.Error(t, err)
	integration.IntegrationKind, integration.Provider =
		integrationdefinition.DiscordThread, integrationdefinition.ProviderSlack
	_, err = IntegrationActorParams(integration, "U123", nil)
	require.Error(t, err)
}
