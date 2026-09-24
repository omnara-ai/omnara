//go:build integration

package tools

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestStandaloneIntegrationToolAccessAndRevocation(t *testing.T) {
	for _, revoke := range []string{"tool", "integration", "credentials"} {
		t.Run(revoke, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(
				t,
				ctx,
				"standalone-integration-tool",
				toolFixtureOptions{withSlackIntegration: true},
			)
			call := f.recordToolCall(t, ctx, "standalone", toolcatalog.IntegrationToolName("chat", "read"), `{}`, f.Now)
			record, err := f.Store.Execution().GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
			require.NoError(t, err)
			executor := Executor{Store: f.Store}
			access, err := executor.resolveIntegrationToolAuthority(ctx, f.turn(), record)
			require.NoError(t, err, "integration authority does not depend on a launcher or subscription")
			_, err = executor.prepareIntegrationToolAccess(ctx, f.turn(), record, access)
			require.ErrorContains(t, err, "no assigned conversation", "shipped tools keep their conversation restriction")

			// A local definition exercises standalone execution without exporting a test operation.
			access.Authority.Definition.Scope = toolcatalog.IntegrationToolScopeIntegration
			access, err = executor.prepareIntegrationToolAccess(ctx, f.turn(), record, access)
			require.NoError(t, err)
			require.Empty(t, access.Conversation)
			require.NotEmpty(t, access.Credential)
			require.NoError(t, executor.recheckIntegrationToolAccess(ctx, f.turn(), record, access))
			summary, err := integrationToolPermissionSummary(access, record.Input)
			require.NoError(t, err)
			require.JSONEq(t, `{"arguments":{}}`, string(summary))
			require.Empty(t, integrationToolSubscriptions(t, f))

			switch revoke {
			case "tool":
				source, err := agentconfig.ParseSource(
					agentconfig.SourceFormat(f.AgentConfig.SourceFormat), []byte(f.AgentConfig.Source),
				)
				require.NoError(t, err)
				delete(source.Tools, call.Name)
				changeIntegrationToolConfig(t, ctx, f, source)
				_, err = executor.resolveIntegrationToolAuthority(ctx, f.turn(), record)
				require.ErrorIs(t, err, ErrToolAuthorizationInvalidated)
			case "integration":
				_, err := f.Store.Integrations().DisconnectProjectIntegration(
					ctx,
					integrationstore.DisconnectProjectIntegrationInput{
						ProjectID: f.Agent.ProjectID, IntegrationID: f.Install.ID, ExpectedSetupRevision: &f.Install.SetupRevision,
					},
				)
				require.NoError(t, err)
			case "credentials":
				_, _, err := f.Store.Secrets().CreateSecretVersion(ctx, secretstore.CreateSecretVersionInput{
					OrgID: toolsTestOrgID, SecretID: f.Install.CredentialSecretID,
					Actor: toolsTestUserPrincipal(f.User.ID),
					Material: secrets.SlackAppCredentialsMaterial{
						AccessToken: "rotated-token", ClientID: "client-id",
						ClientSecret: "client-secret", SigningSecret: "signing-secret",
					},
				})
				require.NoError(t, err)
			}
			require.ErrorIs(
				t,
				executor.recheckIntegrationToolAccess(ctx, f.turn(), record, access),
				ErrToolAuthorizationInvalidated,
			)
		})
	}
}
