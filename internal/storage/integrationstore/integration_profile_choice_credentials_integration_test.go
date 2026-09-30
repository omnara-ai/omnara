//go:build integration

package integrationstore_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestIntegrationProfileChoiceHoldsCredentialUntilCommit(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	menu := f.menu(t)
	wrapper, err := secrets.NewLocalKeyWrapper("credential-test", map[string][]byte{
		"credential-test": []byte("0123456789abcdef0123456789abcdef"),
	})
	require.NoError(t, err)
	credentialStore := secretstore.New(f.pool, wrapper, identitystore.New(f.pool, wrapper, nil))
	actor := identitystore.NewUserPrincipal(f.user)
	secret, version, err := credentialStore.CreateSecret(f.ctx, secretstore.CreateSecretInput{
		OrgID: f.org, OwnerKind: secretstore.SecretOwnerOrg, Name: "shared-callback", Actor: actor,
		Material: secrets.SlackAppCredentialsMaterial{
			AccessToken: "xoxb-test", ClientID: "client", ClientSecret: "secret", SigningSecret: "signing",
		},
	})
	require.NoError(t, err)
	secretID := secret.ID
	grant, err := credentialStore.CreateSecretGrant(f.ctx, secretstore.CreateSecretGrantInput{
		OrgID: f.org, SecretID: secretID, TargetProjectID: f.project, Actor: actor,
	})
	require.NoError(t, err)
	f.integration, err = f.store.ConfigureIntegration(f.ctx, integrationstore.ConfigureIntegrationInput{
		OrgID: f.org, ProjectID: f.project, IntegrationID: f.integrationID,
		InstalledByUserID: f.user, Provider: f.integration.Provider,
		ProviderTenantID: f.integration.ProviderTenantID, ProviderAccountRef: f.integration.ProviderAccountRef,
		CredentialSecretID: secretID, CredentialVersionID: version.ID,
		ExpectedSetupRevision: f.integration.SetupRevision, OAuthFlowID: uuid.Must(uuid.NewV7()),
	})
	require.NoError(t, err)
	blocker := integrationdb.BeginTx(t, f.ctx, f.pool)
	require.NoError(t, integrationstore.LockConversationTx(f.ctx, blocker, f.project, f.integrationID, menu.Address))
	input := f.chooseInput(t, menu, "support")
	callback := integrationdb.RunAsync(func() (integrationstore.IntegrationProfileChoiceRecord, error) {
		return f.store.ChooseIntegrationProfile(f.ctx, input)
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockIntegrationConversation", 1)
	revocation := integrationdb.RunAsync(func() (secretstore.SecretGrantRecord, error) {
		return credentialStore.DeleteSecretGrant(f.ctx, secretstore.DeleteSecretGrantInput{
			OrgID: f.org, SecretID: secretID, GrantID: grant.ID, Actor: actor,
		})
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockSecret", 1)
	require.NoError(t, blocker.Commit(f.ctx))
	chosen := integrationdb.AwaitSuccess(t, callback, "profile choice holding credential access through commit")
	require.Equal(t, input.Key, chosen.SelectedKey)
	integrationdb.AwaitSuccess(t, revocation, "revocation after profile choice commit")
	_, err = f.store.ChooseIntegrationProfile(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrUnauthorized, "selected-menu replay still requires credential access")
	require.Equal(t, chosen, f.readChoice(t, menu.ID))
	require.Equal(t, menu.ID, f.decidedReceipt(t, menu.ID).SourceStateID)
}
