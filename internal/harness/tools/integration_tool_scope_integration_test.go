//go:build integration

package tools

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestStandaloneIntegrationToolAccessAndRevocation(t *testing.T) {
	for _, revoke := range []string{"tool", "integration", "credentials", "credential grant"} {
		t.Run(revoke, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(
				t,
				ctx,
				"standalone-integration-tool",
				toolFixtureOptions{withSlackIntegration: true},
			)
			var grant secretstore.SecretGrantRecord
			if revoke == "credential grant" {
				credential, version, err := f.Store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
					OrgID: toolsTestOrgID, OwnerKind: secretstore.SecretOwnerOrg,
					Name: "shared-tool-credentials", Actor: toolsTestUserPrincipal(f.User.ID),
					Material: secrets.SlackAppCredentialsMaterial{
						AccessToken: "xoxb-test", ClientID: "client-id",
						ClientSecret: "client-secret", SigningSecret: "signing-secret",
					},
				})
				require.NoError(t, err)
				grant, err = f.Store.Secrets().CreateSecretGrant(ctx, secretstore.CreateSecretGrantInput{
					OrgID: toolsTestOrgID, SecretID: credential.ID, TargetProjectID: toolsTestProjectID,
					Actor: toolsTestUserPrincipal(f.User.ID),
				})
				require.NoError(t, err)
				f.Install, err = f.Store.Integrations().ConfigureIntegration(ctx, integrationstore.ConfigureIntegrationInput{
					OrgID: toolsTestOrgID, ProjectID: toolsTestProjectID, IntegrationID: f.Install.ID,
					InstalledByUserID: f.User.ID, Provider: f.Install.Provider,
					ProviderTenantID: f.Install.ProviderTenantID, ProviderAccountRef: f.Install.ProviderAccountRef,
					CredentialSecretID: credential.ID, CredentialVersionID: version.ID,
					ExpectedSetupRevision: f.Install.SetupRevision, OAuthFlowID: uuid.Must(uuid.NewV7()),
					ProviderIdentity: f.Install.ProviderIdentity,
				})
				require.NoError(t, err)
			}
			call := f.recordToolCall(t, ctx, "standalone", toolcatalog.IntegrationToolName("chat", "read"), `{}`, f.Now)
			record, err := f.Store.Execution().GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
			require.NoError(t, err)
			executor := Executor{Store: f.Store}
			access, err := executor.resolveIntegrationToolAuthority(ctx, f.turn(), record)
			require.NoError(t, err, "integration authority does not depend on a launcher or subscription")
			_, err = executor.prepareIntegrationToolAccess(ctx, f.turn(), access)
			require.ErrorContains(t, err, "no assigned conversation", "shipped tools keep their conversation restriction")

			access.Authority.Definition.Scope = toolcatalog.IntegrationToolScopeIntegration
			access, err = executor.prepareIntegrationToolAccess(ctx, f.turn(), access)
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
				_, err := f.Store.Integrations().DisconnectIntegration(
					ctx,
					integrationstore.DisconnectIntegrationInput{
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
			case "credential grant":
				_, err := f.Store.Secrets().DeleteSecretGrant(ctx, secretstore.DeleteSecretGrantInput{
					OrgID: toolsTestOrgID, SecretID: grant.SecretID, GrantID: grant.ID,
					Actor: toolsTestUserPrincipal(f.User.ID),
				})
				require.NoError(t, err)
			}
			err = executor.recheckIntegrationToolAccess(ctx, f.turn(), record, access)
			if revoke == "credential grant" {
				require.ErrorIs(t, err, storeerr.ErrNotFound)
				require.NotErrorIs(t, err, ErrToolAuthorizationInvalidated)
				return
			}
			require.ErrorIs(t, err, ErrToolAuthorizationInvalidated)
		})
	}
}
