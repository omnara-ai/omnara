//go:build integration

package integrationstore_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func connectionFixture(t *testing.T) (
	inboxFixture,
	*secretstore.Store,
	integrationstore.SaveIntegrationConnectionInput,
) {
	t.Helper()
	f := newInboxFixture(t)
	f.store = integrationstore.New(f.pool, executionstore.IntegrationConnectionAccess{})
	f.exec(t, `INSERT INTO org_memberships(org_id,user_id,role,created_at) VALUES($1,$2,'owner',now())`, f.org, f.user)
	wrapper, err := secrets.NewLocalKeyWrapper(
		"connection-test",
		map[string][]byte{"connection-test": []byte("0123456789abcdef0123456789abcdef")},
	)
	require.NoError(t, err)
	secretStore := secretstore.New(f.pool, wrapper, identitystore.New(f.pool, wrapper, nil))
	credential, _, err := secretStore.CreateSecret(f.ctx, secretstore.CreateSecretInput{
		OrgID: f.org, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: f.project,
		Name: "connection-fixture", Actor: identitystore.NewUserPrincipal(f.user),
		Material: secrets.SlackAppCredentialsMaterial{
			AccessToken:   "xoxb-fixture",
			ClientID:      "client",
			ClientSecret:  "client-secret",
			SigningSecret: "signing-secret",
		},
	})
	require.NoError(t, err)
	input := integrationstore.SaveIntegrationConnectionInput{
		OrgID:              f.org,
		ProjectID:          f.project,
		InstalledByUserID:  f.user,
		Provider:           integrationstore.IntegrationProviderSlack,
		CredentialSecretID: credential.ID,
		ProviderTenantID:   "tenant",
		ProviderAccountRef: "router",
		State:              integrationstore.IntegrationConnectionStateActive,
	}
	return f, secretStore, input
}

func TestIntegrationConnectionLifecycleAndOwnership(t *testing.T) {
	t.Parallel()
	f, _, input := connectionFixture(t)
	created, err := f.store.CreateIntegrationConnection(f.ctx, input)
	require.NoError(t, err)
	require.True(t, created.Created)
	require.Equal(t, input.CredentialSecretID, created.CredentialSecretID)
	_, err = f.store.CreateIntegrationConnection(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	second := uuid.New()
	storagefixture.InsertProject(t, f.ctx, f.pool, f.org, second, "other", "other", time.Now())
	foreign := input
	foreign.ProjectID = second
	_, err = f.store.CreateIntegrationConnection(f.ctx, foreign)
	require.ErrorIs(t, err, storeerr.ErrConflict, "hosted account identity is global across projects")
	resolved, err := f.store.GetIntegrationConnectionByProviderAccount(
		f.ctx, input.Provider, input.ProviderTenantID, input.ProviderAccountRef,
	)
	require.NoError(t, err)
	require.Equal(t, created.ID, resolved.ID)
	require.Equal(t, f.project, resolved.ProjectID)
	_, err = f.store.GetIntegrationConnection(f.ctx, second, created.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = f.store.UpdateIntegrationConnection(f.ctx, created.ID, foreign)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	changed := input
	changed.ProviderAccountRef = "different"
	_, err = f.store.UpdateIntegrationConnection(f.ctx, created.ID, changed)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	input.State = integrationstore.IntegrationConnectionStateDisabled
	updated, err := f.store.UpdateIntegrationConnection(f.ctx, created.ID, input)
	require.NoError(t, err)
	require.Equal(t, created.ID, updated.ID)
	require.Equal(t, created.CreatedAt, updated.CreatedAt)
	require.Equal(t, input.State, updated.State)
	// Revoked connection identity is not silently resurrected by an explicit update.
	require.NoError(t, f.store.DeleteIntegrationConnection(f.ctx, f.project, created.ID))
	_, err = f.store.UpdateIntegrationConnection(f.ctx, created.ID, input)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	reconnected, err := f.store.CreateIntegrationConnection(f.ctx, input)
	require.NoError(t, err)
	require.NotEqual(t, created.ID, reconnected.ID)
}

func TestIntegrationConnectionRejectsExternalProvider(t *testing.T) {
	t.Parallel()
	f, _, input := connectionFixture(t)
	input.Provider = "external"
	_, err := f.store.CreateIntegrationConnection(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	_, err = f.pool.Exec(f.ctx, `UPDATE integration_connections SET provider='external' WHERE id=$1`, f.connection)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "23514", pgErr.Code, "the durable provider domain excludes external applications")
}

func TestIntegrationConnectionCredentialAuthorizationAndRotation(t *testing.T) {
	t.Parallel()
	f, secretStore, input := connectionFixture(t)
	other := uuid.New()
	storagefixture.InsertProject(t, f.ctx, f.pool, f.org, other, "credential-owner", "credential-owner", time.Now())
	actor := identitystore.NewUserPrincipal(f.user)
	material := secrets.GitHubAppCredentialsMaterial{
		AppID:         "123",
		PrivateKey:    "test-key-validated-by-caller",
		WebhookSecret: "test-webhook",
	}
	credential, version, err := secretStore.CreateSecret(
		f.ctx,
		secretstore.CreateSecretInput{
			OrgID:          f.org,
			OwnerKind:      secretstore.SecretOwnerProject,
			OwnerProjectID: other,
			Name:           "github",
			Actor:          actor,
			Material:       material,
		},
	)
	require.NoError(t, err)
	input.Provider = integrationstore.IntegrationProviderGitHub
	input.ProviderTenantID, input.ProviderAccountRef = "123", "456"
	input.CredentialSecretID, input.CredentialVersionID, input.CredentialAppID = credential.ID, version.ID, 123
	_, err = f.store.CreateIntegrationConnection(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	grant, err := secretStore.CreateSecretGrant(
		f.ctx,
		secretstore.CreateSecretGrantInput{
			OrgID:           f.org,
			SecretID:        credential.ID,
			TargetProjectID: f.project,
			Actor:           actor,
		},
	)
	require.NoError(t, err)
	saved, err := f.store.CreateIntegrationConnection(f.ctx, input)
	require.NoError(t, err)
	_, newVersion, err := secretStore.CreateSecretVersion(
		f.ctx,
		secretstore.CreateSecretVersionInput{OrgID: f.org, SecretID: credential.ID, Material: material, Actor: actor},
	)
	require.NoError(t, err)
	input.State = integrationstore.IntegrationConnectionStateDisabled
	_, err = f.store.UpdateIntegrationConnection(f.ctx, saved.ID, input)
	require.ErrorIs(
		t,
		err,
		storeerr.ErrConflict,
		"a rotation after payload validation must prevent saving the stale validation",
	)
	current, err := f.store.GetIntegrationConnection(f.ctx, f.project, saved.ID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationConnectionStateActive, current.State)
	input.CredentialVersionID = newVersion.ID
	_, err = f.store.UpdateIntegrationConnection(f.ctx, saved.ID, input)
	require.NoError(t, err)
	_, err = secretStore.DeleteSecretGrant(
		f.ctx,
		secretstore.DeleteSecretGrantInput{OrgID: f.org, SecretID: credential.ID, GrantID: grant.ID, Actor: actor},
	)
	require.NoError(t, err)
	_, err = f.store.UpdateIntegrationConnection(f.ctx, saved.ID, input)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	require.NoError(t, f.store.DeleteIntegrationConnection(f.ctx, f.project, saved.ID))
}

func TestIntegrationConnectionUpdatePreservesConcurrentProviderObservations(t *testing.T) {
	t.Parallel()
	f, _, input := connectionFixture(t)
	saved, err := f.store.CreateIntegrationConnection(f.ctx, input)
	require.NoError(t, err)
	tx, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(f.ctx) }()
	_, err = tx.Exec(
		f.ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('integration_connection_lifecycle:' || $1::uuid::text,0))`,
		saved.ID,
	)
	require.NoError(t, err)
	_, err = tx.Exec(
		f.ctx,
		`UPDATE integration_connections
		 SET provider_identity='{"observed":"new"}',provider_metadata='{"revision":2}' WHERE id=$1`,
		saved.ID,
	)
	require.NoError(t, err)
	input.ProviderIdentity = json.RawMessage(`{"observed":"stale"}`)
	input.ProviderMetadata = json.RawMessage(`{"revision":1}`)
	input.ProviderAgentDisplayName = "New label"
	done := make(chan error, 1)
	go func() { _, err := f.store.UpdateIntegrationConnection(f.ctx, saved.ID, input); done <- err }()
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockIntegrationConnectionLifecycleExclusive", 1)
	require.NoError(t, tx.Commit(f.ctx))
	require.NoError(t, <-done)
	current, err := f.store.GetIntegrationConnection(f.ctx, f.project, saved.ID)
	require.NoError(t, err)
	require.JSONEq(t, `{"observed":"new"}`, string(current.ProviderIdentity))
	require.JSONEq(t, `{"revision":2}`, string(current.ProviderMetadata))
	require.Equal(t, "New label", current.ProviderAgentDisplayName)
}

func TestIntegrationConnectionSlackUpdateCannotRebindCredential(t *testing.T) {
	t.Parallel()
	f, secretStore, input := connectionFixture(t)
	var credentials []uuid.UUID
	for _, name := range []string{"original", "replacement"} {
		credential, _, err := secretStore.CreateSecret(f.ctx, secretstore.CreateSecretInput{
			OrgID: f.org, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: f.project,
			Name: name, Actor: identitystore.NewUserPrincipal(f.user),
			Material: secrets.SlackAppCredentialsMaterial{
				AccessToken:   "xoxb-" + name,
				ClientID:      "client",
				ClientSecret:  "client-secret",
				SigningSecret: "signing-secret",
			},
		})
		require.NoError(t, err)
		credentials = append(credentials, credential.ID)
	}
	input.Provider = integrationstore.IntegrationProviderSlack
	input.CredentialSecretID = credentials[0]
	saved, err := f.store.CreateIntegrationConnection(f.ctx, input)
	require.NoError(t, err)
	changed := input
	changed.CredentialSecretID = credentials[1]
	for _, state := range []integrationstore.IntegrationConnectionState{
		integrationstore.IntegrationConnectionStateActive, integrationstore.IntegrationConnectionStateDisabled,
	} {
		changed.State = state
		_, err = f.store.UpdateIntegrationConnection(f.ctx, saved.ID, changed)
		require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	}
	current, err := f.store.GetIntegrationConnection(f.ctx, f.project, saved.ID)
	require.NoError(t, err)
	require.Equal(t, input.CredentialSecretID, current.CredentialSecretID)
	require.Equal(t, saved.UpdatedAt, current.UpdatedAt)

	// Model OAuth rebinding while a settings save waits on the lifecycle gate.
	// A pre-lock comparison alone would overwrite the new credential with the old.
	tx, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(f.ctx) }()
	_, err = tx.Exec(f.ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('integration_connection_lifecycle:' || $1::uuid::text,0))`,
		saved.ID)
	require.NoError(t, err)
	_, err = tx.Exec(f.ctx,
		`UPDATE integration_connections SET credential_secret_id=$2 WHERE id=$1`, saved.ID, credentials[1])
	require.NoError(t, err)
	input.ProviderAgentDisplayName = "Stale settings save"
	done := make(chan error, 1)
	go func() { _, err := f.store.UpdateIntegrationConnection(f.ctx, saved.ID, input); done <- err }()
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockIntegrationConnectionLifecycleExclusive", 1)
	require.NoError(t, tx.Commit(f.ctx))
	require.ErrorIs(t, <-done, storeerr.ErrInvalidRequest)
	current, err = f.store.GetIntegrationConnection(f.ctx, f.project, saved.ID)
	require.NoError(t, err)
	require.Equal(t, credentials[1], current.CredentialSecretID)
	require.Equal(t, saved.ProviderAgentDisplayName, current.ProviderAgentDisplayName)
}

func TestIntegrationConnectionHostedIdentityAndOAuthReplay(t *testing.T) {
	t.Parallel()
	f, input, setup := slackSetupFixture(t)
	input.ProviderAgentDisplayName = "Known provider name"
	first, _, err := f.store.CompleteSlackAppSetup(f.ctx, input, setup)
	require.NoError(t, err)
	other := uuid.New()
	storagefixture.InsertProject(t, f.ctx, f.pool, f.org, other, "hosted-other", "hosted-other", time.Now())
	foreign := input
	foreign.ProjectID = other
	foreignSetup := setup
	foreignSetup.ProjectID = other
	foreignSetup.Settings.Launcher = nil
	_, _, err = f.store.CompleteSlackAppSetup(f.ctx, foreign, foreignSetup)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	_, _, err = f.store.CompleteSlackAppSetup(f.ctx, input, setup)
	require.ErrorIs(t, err, storeerr.ErrIntegrationOAuthFlowConsumed)
	input.ProviderAccountRef = "second-account"
	_, _, err = f.store.CompleteSlackAppSetup(f.ctx, input, setup)
	require.ErrorIs(t, err, storeerr.ErrIntegrationOAuthFlowConsumed)
	require.NoError(t, f.store.DeleteIntegrationConnection(f.ctx, f.project, first.ID))
	_, _, err = f.store.CompleteSlackAppSetup(f.ctx, input, setup)
	require.ErrorIs(t, err, storeerr.ErrIntegrationOAuthFlowConsumed, "deleted connections still prevent OAuth replay")
}

func TestIntegrationConnectionUpdateCannotRecreateConcurrentDeletion(t *testing.T) {
	t.Parallel()
	f, _, input := connectionFixture(t)
	saved, err := f.store.CreateIntegrationConnection(f.ctx, input)
	require.NoError(t, err)
	tx, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(f.ctx) }()
	_, err = tx.Exec(
		f.ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('integration_connection_lifecycle:' || $1::uuid::text,0))`,
		saved.ID,
	)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { _, err := f.store.UpdateIntegrationConnection(f.ctx, saved.ID, input); done <- err }()
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockIntegrationConnectionLifecycleExclusive", 1)
	_, err = tx.Exec(
		f.ctx,
		`UPDATE integration_connections SET deleted_at=now(),credential_secret_id=NULL WHERE id=$1`,
		saved.ID,
	)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(f.ctx))
	require.ErrorIs(t, <-done, storeerr.ErrNotFound)
	_, err = f.store.GetIntegrationConnectionByProviderAccount(
		f.ctx,
		input.Provider,
		input.ProviderTenantID,
		input.ProviderAccountRef,
	)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
}

func TestIntegrationConnectionDisplayNameReplacementAndProviderRefresh(t *testing.T) {
	t.Parallel()
	f, input, setup := slackSetupFixture(t)
	input.ProviderAgentDisplayName = "Observed name"
	saved, _, err := f.store.CompleteSlackAppSetup(f.ctx, input, setup)
	require.NoError(t, err)
	input.ProviderAgentDisplayName = ""
	input.OAuthFlowID, err = uuid.NewV7()
	require.NoError(t, err)
	refreshed, _, err := f.store.CompleteSlackAppSetup(f.ctx, input, setup)
	require.NoError(t, err)
	require.Equal(t, "Observed name", refreshed.ProviderAgentDisplayName)
	input.OAuthFlowID = uuid.Nil
	edited, err := f.store.UpdateIntegrationConnection(f.ctx, saved.ID, input)
	require.NoError(t, err)
	require.Empty(t, edited.ProviderAgentDisplayName)
}
