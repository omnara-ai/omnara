//go:build integration

package executionstore_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestIntegrationInteractionsSerializeCredentialRevocation(t *testing.T) {
	t.Parallel()
	for _, first := range []string{"revocation", "callback"} {
		t.Run(first, func(t *testing.T) {
			t.Parallel()
			f := newIntegrationInteractionFixture(t)
			question := f.questionForOrigin(t, f.a.ID)
			secret, version, err := f.store.Secrets().CreateSecret(f.ctx, secretstore.CreateSecretInput{
				OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerOrg, Name: "shared-callback",
				Actor: userPrincipal(f.user.ID), Material: secrets.SlackAppCredentialsMaterial{
					AccessToken: "xoxb-test", ClientID: "client", ClientSecret: "secret", SigningSecret: "signing",
				},
			})
			require.NoError(t, err)
			secretID := secret.ID
			grant, err := f.store.Secrets().CreateSecretGrant(f.ctx, secretstore.CreateSecretGrantInput{
				OrgID: testOrgID, SecretID: secretID, TargetProjectID: testProjectID, Actor: userPrincipal(f.user.ID),
			})
			require.NoError(t, err)
			configured := integrationstore.ConfigureIntegrationInput{
				OrgID: testOrgID, ProjectID: testProjectID, IntegrationID: f.integration.ID,
				InstalledByUserID: f.user.ID, Provider: f.integration.Provider,
				ProviderTenantID: f.integration.ProviderTenantID, ProviderAccountRef: f.integration.ProviderAccountRef,
				ProviderIdentity: f.integration.ProviderIdentity, OAuthFlowID: uuid.Must(uuid.NewV7()),
				CredentialSecretID: secretID, CredentialVersionID: version.ID,
				ExpectedSetupRevision: f.integration.SetupRevision,
			}
			f.integration, err = f.store.Integrations().ConfigureIntegration(f.ctx, configured)
			require.NoError(t, err)
			input := f.callback(t, question, "U_OTHER")
			blocker := integrationdb.BeginTx(t, f.ctx, f.store.pool)
			q := dbsqlc.New(blocker)
			if first == "revocation" {
				_, err = q.LockSecret(f.ctx, dbsqlc.LockSecretParams{OrgID: testOrgID, ID: secretID})
			} else {
				_, err = q.LockAgentInProject(f.ctx, dbsqlc.LockAgentInProjectParams{
					ProjectID: testProjectID, ID: f.process.AgentID,
				})
			}
			require.NoError(t, err)
			callback := integrationdb.RunAsync(func() (executionstore.AgentInteractionRecord, error) {
				return f.store.Execution().ResolveAgentInteractionFromHandler(f.ctx, input)
			})
			if first == "revocation" {
				integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockSecretForReference", 1)
				_, err = q.DeleteSecretGrant(f.ctx, dbsqlc.DeleteSecretGrantParams{OrgID: testOrgID, ID: grant.ID})
				require.NoError(t, err)
				require.NoError(t, blocker.Commit(f.ctx))
				result := integrationdb.Await(t, callback, "handler callback after credential grant revocation")
				require.ErrorIs(t, result.Err, storeerr.ErrUnauthorized)
				require.Equal(t, executionstore.AgentInteractionStateOpen, f.read(t, question.ID).State)
				return
			}
			integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockAgentInProject", 1)
			revocation := integrationdb.RunAsync(func() (secretstore.SecretGrantRecord, error) {
				return f.store.Secrets().DeleteSecretGrant(f.ctx, secretstore.DeleteSecretGrantInput{
					OrgID: testOrgID, SecretID: secretID, GrantID: grant.ID, Actor: userPrincipal(f.user.ID),
				})
			})
			integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockSecret", 1)
			require.NoError(t, blocker.Commit(f.ctx))
			resolved := integrationdb.AwaitSuccess(t, callback, "callback holding credential access through commit")
			require.Equal(t, executionstore.AgentInteractionStateResolved, resolved.State)
			integrationdb.AwaitSuccess(t, revocation, "revocation after callback commit")
			_, err = f.store.Execution().ResolveAgentInteractionFromHandler(f.ctx, input)
			require.ErrorIs(t, err, storeerr.ErrUnauthorized, "even callback replay requires current credential access")
		})
	}
}
