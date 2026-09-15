//go:build integration

package executionstore_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestIntegrationAppProviderIdentityLookupKeepsExactOwnershipScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	input := integrationstore.CreateIntegrationAppInput{
		OrgID: testOrgID, OwnerProjectID: testProjectID, Provider: testChannelProvider,
		ProviderAppRef: "physical-app", ConnectorKey: testChannelConnector,
		State: integrationstore.IntegrationAppStateActive,
	}
	apps := make([]integrationstore.IntegrationAppRecord, 2)
	errs := make([]error, 2)
	var workers sync.WaitGroup
	for i := range apps {
		workers.Go(func() { apps[i], errs[i] = store.Integrations().CreateIntegrationApp(ctx, input) })
	}
	workers.Wait()
	winner := -1
	for i, err := range errs {
		if err == nil {
			require.Equal(t, -1, winner)
			winner = i
		} else {
			require.ErrorIs(t, err, storeerr.ErrConflict)
		}
	}
	require.NotEqual(t, -1, winner)
	q := dbsqlc.New(pool)
	key := dbsqlc.GetIntegrationAppByProviderRefParams{
		OrgID: testOrgID, OwnerProjectID: &testProjectID,
		Provider: input.Provider, ProviderAppRef: input.ProviderAppRef,
	}
	canonical, err := q.GetIntegrationAppByProviderRef(ctx, key)
	require.NoError(t, err)
	require.Equal(t, apps[winner].ID, canonical.ID, "a racing creator can find the exact conflict owner")
	require.Equal(t, input.ConnectorKey, canonical.ConnectorKey)
	require.Nil(t, canonical.CredentialSecretID, "a physical registration need not invent app credentials")
	input.OwnerProjectID = NilID
	shared, err := store.Integrations().CreateIntegrationApp(ctx, input)
	require.NoError(t, err)
	sharedKey := key
	sharedKey.OwnerProjectID = nil
	sharedRow, err := q.GetIntegrationAppByProviderRef(ctx, sharedKey)
	require.NoError(t, err)
	require.Equal(t, shared.ID, sharedRow.ID)
	require.NotEqual(t, canonical.ID, sharedRow.ID, "NULL owner scope is distinct from project ownership")
	wrongProject := uuid.New()
	for _, wrong := range []dbsqlc.GetIntegrationAppByProviderRefParams{
		{OrgID: uuid.New(), OwnerProjectID: key.OwnerProjectID, Provider: key.Provider, ProviderAppRef: key.ProviderAppRef},
		{OrgID: key.OrgID, OwnerProjectID: &wrongProject, Provider: key.Provider, ProviderAppRef: key.ProviderAppRef},
		{OrgID: key.OrgID, OwnerProjectID: key.OwnerProjectID, Provider: "other", ProviderAppRef: key.ProviderAppRef},
		{OrgID: key.OrgID, OwnerProjectID: key.OwnerProjectID, Provider: key.Provider, ProviderAppRef: "other"},
	} {
		_, err := q.GetIntegrationAppByProviderRef(ctx, wrong)
		require.ErrorIs(t, err, pgx.ErrNoRows, "lookup must not substitute a different scope")
	}
	_, err = pool.Exec(ctx, `UPDATE integration_apps SET state = 'disabled' WHERE id = $1`, canonical.ID)
	require.NoError(t, err)
	disabled, err := q.GetIntegrationAppByProviderRef(ctx, key)
	require.NoError(t, err)
	require.Equal(t, canonical.ID, disabled.ID)
	require.Equal(t, "disabled", disabled.State, "setup must see and reject an existing disabled identity")
	_, err = pool.Exec(ctx, `UPDATE integration_apps SET deleted_at = statement_timestamp() WHERE id = $1`, canonical.ID)
	require.NoError(t, err)
	_, err = q.GetIntegrationAppByProviderRef(ctx, key)
	require.ErrorIs(t, err, pgx.ErrNoRows, "retired identities cannot be reused")
}

func TestInstallationAppSharedLockFencesRetirementAndReturnsCurrentPolicy(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	app, err := store.Integrations().CreateIntegrationApp(ctx, integrationstore.CreateIntegrationAppInput{
		OrgID: testOrgID, OwnerProjectID: testProjectID, Provider: "slack", ProviderAppRef: "A_TEST_INSTALLATION",
		ConnectorKey: channelconnector.BuiltInConnectorKey, InstallationCredentialKind: "slack_app_credentials",
		State: integrationstore.IntegrationAppStateActive,
	})
	require.NoError(t, err)
	key := dbsqlc.LockIntegrationAppForInstallationParams{OrgID: testOrgID, ID: app.ID}
	holder, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = holder.Rollback(ctx) }()
	locked, err := dbsqlc.New(holder).LockIntegrationAppForInstallation(ctx, key)
	require.NoError(t, err)
	require.Equal(t, "active", locked.State)
	require.Equal(t, &testProjectID, locked.OwnerProjectID)
	require.NotNil(t, locked.InstallationCredentialKind)
	require.Equal(t, "slack_app_credentials", *locked.InstallationCredentialKind)
	require.Nil(t, locked.CredentialSecretID)
	var holderPID int32
	require.NoError(t, holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID))
	writer, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = writer.Rollback(ctx) }()
	done := make(chan error, 1)
	go func() {
		_, err := writer.Exec(ctx, `/* installation_app_retire */
			UPDATE integration_apps SET state = 'disabled', deleted_at = statement_timestamp() WHERE id = $1`, app.ID)
		done <- err
	}()
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, pool, "/* installation_app_retire */", holderPID)
	require.NoError(t, holder.Rollback(ctx))
	require.NoError(t, <-done, "retirement waits until installation validation releases the app")
	require.NoError(t, writer.Commit(ctx))
	reader, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = reader.Rollback(ctx) }()
	retired, err := dbsqlc.New(reader).LockIntegrationAppForInstallation(ctx, key)
	require.NoError(t, err)
	require.Equal(t, "disabled", retired.State)
	require.NotNil(t, retired.DeletedAt, "Go must validate the locked lifecycle instead of the earlier app lookup")
	key.OrgID = uuid.New()
	_, err = dbsqlc.New(reader).LockIntegrationAppForInstallation(ctx, key)
	require.ErrorIs(t, err, pgx.ErrNoRows)
}
