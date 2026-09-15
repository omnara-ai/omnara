//go:build integration

package executionstore_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func managedInstallationSetupFixture(
	t *testing.T,
) (*Store, integrationstore.UpsertIntegrationInstallInput, integrationstore.ID) {
	t.Helper()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "managed-setup@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "managed-setup-profile")
	app, err := store.Integrations().GetOrCreateIntegrationApp(ctx, integrationstore.CreateIntegrationAppInput{
		OrgID: testOrgID, OwnerProjectID: testProjectID, Provider: testChannelProvider,
		ProviderAppRef: "real-provider-app", ConnectorKey: testChannelConnector,
		InstallationCredentialKind: string(secrets.KindIntegrationCredentials),
		State:                      integrationstore.IntegrationAppStateActive,
	})
	require.NoError(t, err)
	credentials := make([]integrationstore.ID, 2)
	for i, name := range []string{"initial-credential", "replacement-credential"} {
		secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
			OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: testProjectID,
			Name: name, Actor: identitystore.NewUserPrincipal(admin.ID),
			Material: secrets.IntegrationCredentialsMaterial{Values: map[string]string{"token": name}},
		})
		require.NoError(t, err)
		credentials[i] = secret.ID
	}
	return store, integrationstore.UpsertIntegrationInstallInput{
		OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: app.ID,
		InstalledBy: identitystore.NewUserPrincipal(admin.ID), Provider: testChannelProvider,
		IntegrationKind: integrationstore.IntegrationKindManaged, ConnectionMode: "webhook",
		State: integrationstore.IntegrationInstallStateActive, ProviderTenantID: "workspace", ProviderAccountRef: "account",
		CredentialSecretID: credentials[0], OAuthFlowID: uuid.Must(uuid.NewV7()),
		InitialRoute: &integrationstore.CreateIntegrationRouteInput{
			AgentProfileID: profile.ID, DeploymentKey: "default", BehaviorKey: "conversation",
			State: integrationstore.IntegrationRouteStateActive,
		},
	}, credentials[1]
}

func TestManagedInstallationSetupCommitsCredentialsRouteAndOAuthTogether(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store, input, replacement := managedInstallationSetupFixture(t)
	install, err := store.Integrations().UpsertIntegrationInstall(ctx, input)
	require.NoError(t, err)
	require.True(t, install.Created)
	routes, err := store.Integrations().ListActiveIntegrationRoutes(ctx, testProjectID, install.ID)
	require.NoError(t, err)
	require.Len(t, routes, 1)
	require.Equal(t, input.InitialRoute.AgentProfileID, routes[0].AgentProfileID)

	otherProfile := createIntegrationTestProfile(t, ctx, store, "other-setup-profile")
	wrongRoute := *input.InitialRoute
	wrongRoute.AgentProfileID = otherProfile.ID
	replacementInput := input
	replacementInput.InitialRoute = &wrongRoute
	replacementInput.CredentialSecretID, replacementInput.OAuthFlowID = replacement, uuid.Must(uuid.NewV7())
	_, err = store.Integrations().UpsertIntegrationInstall(ctx, replacementInput)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	unchanged, err := store.Integrations().GetIntegrationInstall(ctx, testProjectID, install.ID)
	require.NoError(t, err)
	require.Equal(t, input.CredentialSecretID, unchanged.CredentialSecretID)
	require.Equal(t, input.OAuthFlowID, unchanged.LastOAuthFlowID)
	consumed, err := store.Integrations().IntegrationOAuthFlowConsumed(ctx, replacementInput.OAuthFlowID)
	require.NoError(t, err)
	require.False(t, consumed)

	replacementInput.InitialRoute = input.InitialRoute
	reauthorized, err := store.Integrations().UpsertIntegrationInstall(ctx, replacementInput)
	require.NoError(t, err)
	require.Equal(t, install.ID, reauthorized.ID)
	require.Equal(t, replacement, reauthorized.CredentialSecretID)
	require.Equal(t, replacementInput.OAuthFlowID, reauthorized.LastOAuthFlowID)
	routes, err = store.Integrations().ListActiveIntegrationRoutes(ctx, testProjectID, install.ID)
	require.NoError(t, err)
	require.Len(t, routes, 1, "reauthorization does not duplicate behavior")
}

func TestManagedInstallationConcurrentSetupHasOneConnectionAndBehavior(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store, input, _ := managedInstallationSetupFixture(t)
	inputs := []integrationstore.UpsertIntegrationInstallInput{input, input}
	inputs[0].OAuthFlowID, inputs[1].OAuthFlowID = uuid.Nil, uuid.Nil
	results := make([]integrationstore.IntegrationInstallRecord, 2)
	errs := make([]error, 2)
	var workers sync.WaitGroup
	for i := range inputs {
		workers.Go(func() { results[i], errs[i] = store.Integrations().UpsertIntegrationInstall(ctx, inputs[i]) })
	}
	workers.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, results[0].ID, results[1].ID)
	require.NotEqual(t, results[0].Created, results[1].Created)
	routes, err := store.Integrations().ListActiveIntegrationRoutes(ctx, testProjectID, results[0].ID)
	require.NoError(t, err)
	require.Len(t, routes, 1)
}

func TestManagedInstallationReauthorizationLocksConnectionBeforeApp(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	store, input, replacement := managedInstallationSetupFixture(t)
	install, err := store.Integrations().UpsertIntegrationInstall(ctx, input)
	require.NoError(t, err)
	holder, err := store.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = holder.Rollback(context.WithoutCancel(ctx)) }()
	_, err = holder.Exec(ctx, `SELECT id FROM integration_installs WHERE id=$1 FOR UPDATE`, install.ID)
	require.NoError(t, err)
	input.CredentialSecretID, input.OAuthFlowID = replacement, uuid.Must(uuid.NewV7())
	done := make(chan error, 1)
	go func() { _, err := store.Integrations().UpsertIntegrationInstall(ctx, input); done <- err }()
	integrationdb.WaitForNamedLockWaiters(t, ctx, store.pool, "LockIntegrationInstallByAppProviderAccount", 1)
	_, err = holder.Exec(ctx, `SELECT id FROM integration_apps WHERE id=$1 FOR UPDATE NOWAIT`, install.IntegrationAppID)
	require.NoError(t, err, "a blocked reauthorization must not already hold the parent app lock")
	require.NoError(t, holder.Commit(ctx))
	require.NoError(t, <-done)
}

func TestManagedInstallationRejectsAppDisabledAfterRegistration(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store, input, _ := managedInstallationSetupFixture(t)
	_, err := store.pool.Exec(ctx, `UPDATE integration_apps SET state='disabled' WHERE id=$1`, input.IntegrationAppID)
	require.NoError(t, err)
	_, err = store.Integrations().UpsertIntegrationInstall(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	consumed, err := store.Integrations().IntegrationOAuthFlowConsumed(ctx, input.OAuthFlowID)
	require.NoError(t, err)
	require.False(t, consumed)
	var connections int
	require.NoError(t, store.pool.QueryRow(ctx,
		`SELECT count(*) FROM integration_installs WHERE integration_app_id=$1`, input.IntegrationAppID).Scan(&connections))
	require.Zero(t, connections)
}
