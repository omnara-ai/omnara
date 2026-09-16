//go:build integration

package integrationstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestRuntimeConfigurationUpsertRechecksLifecycleAfterWaiting(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"organization", "project", "installation"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()
			store, setup := discordRuntimeSetupFixture(t)
			setup.DiscordRuntimeShardCount = 0
			install, err := store.Integrations().UpsertIntegrationInstall(t.Context(), setup)
			require.NoError(t, err)
			input := integrationstore.UpsertIntegrationRuntimeUnitInput{
				OrgID: testOrgID, IntegrationAppID: setup.IntegrationAppID,
				UnitKey: "lifecycle-unit", RuntimeKind: "provider_socket", SpecRevision: 1,
				DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
			}
			blocker := integrationdb.BeginTx(t, t.Context(), store.pool)
			q := dbsqlc.New(blocker)
			var statement, waiting string
			var id uuid.UUID
			switch scope {
			case "organization":
				require.NoError(t, q.LockOrganizationLifecycleExclusive(t.Context(),
					dbsqlc.LockOrganizationLifecycleExclusiveParams{OrgID: testOrgID}))
				statement, id = `UPDATE orgs SET deleted_at=now() WHERE id=$1`, testOrgID
				waiting = "LockOrganizationLifecycleShared"
			case "project":
				input.ProjectID, input.IntegrationInstallID = testProjectID, install.ID
				require.NoError(t, q.LockProjectLifecycleExclusive(t.Context(),
					dbsqlc.LockProjectLifecycleExclusiveParams{ProjectID: testProjectID}))
				statement, id = `UPDATE projects SET deleted_at=now() WHERE id=$1`, testProjectID
				waiting = "LockProjectLifecycleShared"
			case "installation":
				input.ProjectID, input.IntegrationInstallID = testProjectID, install.ID
				require.NoError(t, q.LockIntegrationInstallLifecycleExclusive(t.Context(),
					dbsqlc.LockIntegrationInstallLifecycleExclusiveParams{InstallID: install.ID}))
				statement, id = `UPDATE integration_installs SET deleted_at=now() WHERE id=$1`, install.ID
				waiting = "LockIntegrationInstallLifecycleShared"
			}
			pending := integrationdb.RunAsync(func() (integrationstore.IntegrationRuntimeUnitRecord, error) {
				return store.Integrations().UpsertIntegrationRuntimeUnit(t.Context(), input)
			})
			integrationdb.WaitForNamedLockWaiters(t, t.Context(), store.pool, waiting, 1)
			_, err = blocker.Exec(t.Context(), statement, id)
			require.NoError(t, err)
			require.NoError(t, blocker.Commit(t.Context()))
			result := integrationdb.Await(t, pending, "runtime write behind retirement")
			require.ErrorIs(t, result.Err, storeerr.ErrNotFound)
			var count int
			require.NoError(t, store.pool.QueryRow(t.Context(),
				`SELECT count(*) FROM integration_runtime_units WHERE integration_app_id=$1`,
				setup.IntegrationAppID).Scan(&count))
			require.Zero(t, count, "retirement must not leave an orphan runtime")
		})
	}
}

func TestDiscordRuntimeReconnectNeverLocksAppBeforeInstallation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	store, input := discordRuntimeSetupFixture(t)
	install, err := store.Integrations().UpsertIntegrationInstall(ctx, input)
	require.NoError(t, err)
	blocker := integrationdb.BeginTx(t, ctx, store.pool)
	_, err = blocker.Exec(ctx, `SELECT id FROM integration_installs WHERE id=$1 FOR UPDATE`, install.ID)
	require.NoError(t, err)
	input.OAuthFlowID, input.DiscordRuntimeShardCount = uuid.Must(uuid.NewV7()), 3
	pending := integrationdb.RunAsync(func() (integrationstore.IntegrationInstallRecord, error) {
		return store.Integrations().UpsertIntegrationInstall(ctx, input)
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, store.pool, "LockIntegrationInstallByAppProviderAccount", 1)
	_, err = blocker.Exec(ctx, `SELECT id FROM integration_apps WHERE id=$1 FOR UPDATE NOWAIT`, input.IntegrationAppID)
	require.NoError(t, err, "waiting reconnect must not hold or upgrade an app row lock")
	require.NoError(t, blocker.Commit(ctx))
	reconnected := integrationdb.AwaitSuccess(t, pending, "reconnect after installation lock")
	require.Equal(t, install.ID, reconnected.ID)
	discordSetupCounts(t, store, input.IntegrationAppID, 1, 2)
}
