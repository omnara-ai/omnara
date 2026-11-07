//go:build integration

package dbmigrate_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestProjectMachineGrantCreationReceiptMigration(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 68))
	var orgID, projectID, machineID, poolID, machineGrantID, poolGrantID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
INSERT INTO orgs(name, created_at, updated_at)
VALUES ('Receipt migration', statement_timestamp(), statement_timestamp())
RETURNING id`).Scan(&orgID))
	require.NoError(t, pool.QueryRow(ctx, `
INSERT INTO projects(org_id, name, created_at, updated_at)
VALUES ($1, 'Receipt migration', statement_timestamp(), statement_timestamp())
RETURNING id`, orgID).Scan(&projectID))
	require.NoError(t, pool.QueryRow(ctx, `
INSERT INTO machines(
    org_id, source_kind, display_name, provider, lifecycle_state,
    lifecycle_changed_at, created_at, updated_at
)
VALUES ($1, 'byo', 'Migration machine', 'byo', 'active',
        statement_timestamp(), statement_timestamp(), statement_timestamp())
RETURNING id`, orgID).Scan(&machineID))
	require.NoError(t, pool.QueryRow(ctx, `
INSERT INTO machine_pools(
    org_id, name, management_kind, provider, provider_auth_env_var,
    max_total_machines, created_at, updated_at
)
VALUES ($1, 'Migration pool', 'cluster', 'test', 'TEST_PROVIDER_TOKEN',
        2, statement_timestamp(), statement_timestamp())
RETURNING id`, orgID).Scan(&poolID))
	require.NoError(t, pool.QueryRow(ctx, `
INSERT INTO project_machine_grants(
    org_id, project_id, machine_id, description, idempotency_key, created_at, updated_at
)
VALUES ($1, $2, $3, 'original', 'machine-key', statement_timestamp(), statement_timestamp())
RETURNING id`, orgID, projectID, machineID).Scan(&machineGrantID))
	require.NoError(t, pool.QueryRow(ctx, `
INSERT INTO project_machine_pool_grants(
    org_id, project_id, machine_pool_id, description, idempotency_key,
    default_machine_env_overlay, created_at, updated_at
)
VALUES ($1, $2, $3, 'original', 'pool-key', '{"B":"two","A":"one"}',
        statement_timestamp(), statement_timestamp())
RETURNING id`, orgID, projectID, poolID).Scan(&poolGrantID))
	// Internal allocation keys were not public explicit-grant create commands.
	var poolMachineID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
INSERT INTO machines(
    org_id, machine_pool_id, source_kind, display_name, provider, lifecycle_state,
    lifecycle_changed_at, next_reconcile_after, provider_options, created_at, updated_at
)
VALUES ($1, $2, 'pool', 'Internal pool machine', 'test', 'provisioning',
        statement_timestamp(), statement_timestamp(), '{}', statement_timestamp(), statement_timestamp())
RETURNING id`, orgID, poolID).Scan(&poolMachineID))
	_, err := pool.Exec(ctx, `
INSERT INTO project_machine_grants(
    org_id, project_id, machine_id, source_kind, project_machine_pool_grant_id,
    idempotency_key, created_at, updated_at
)
VALUES ($1, $2, $3, 'pool', $4, 'internal-child-key', statement_timestamp(), statement_timestamp())`,
		orgID, projectID, poolMachineID, poolGrantID)
	require.NoError(t, err)
	require.NoError(t, applyProductionPostgresMigrations(ctx, db))
	var publicReceipts int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM project_machine_grant_creations").Scan(&publicReceipts))
	require.Equal(t, 1, publicReceipts, "only explicit public grant keys become receipts")
	store := storage.NewStore(pool)
	machineInput := executionstore.CreateProjectMachineGrantInput{
		OrgID: orgID, ProjectID: projectID, MachineID: machineID,
		Description: "original", IdempotencyKey: "machine-key",
	}
	machineReplay, _, err := store.Execution().CreateProjectMachineGrant(ctx, machineInput)
	require.NoError(t, err)
	require.Equal(t, machineGrantID, machineReplay.ID)
	require.False(t, machineReplay.Created)
	poolInput := executionstore.CreateProjectMachinePoolGrantInput{
		OrgID: orgID, ProjectID: projectID, MachinePoolID: poolID,
		Description: "original", IdempotencyKey: "pool-key",
		DefaultMachineEnvOverlay: []byte(`{ "A": "one", "B": "two" }`),
	}
	poolReplay, err := store.Execution().CreateProjectMachinePoolGrant(ctx, poolInput)
	require.NoError(t, err)
	require.Equal(t, poolGrantID, poolReplay.ID)
	require.False(t, poolReplay.Created)
	var columns int
	require.NoError(t, pool.QueryRow(ctx, `
SELECT count(*) FROM information_schema.columns
WHERE table_schema = 'public'
  AND table_name IN ('project_machine_grants', 'project_machine_pool_grants')
  AND column_name = 'idempotency_key'`).Scan(&columns))
	require.Zero(t, columns)
	for _, kind := range []string{"machine", "machine_pool"} {
		_, err := pool.Exec(ctx, "DELETE FROM project_"+kind+"_grants WHERE project_id=$1", projectID)
		require.NoError(t, err)
		var count int
		require.NoError(t, pool.QueryRow(ctx,
			"SELECT count(*) FROM project_"+kind+"_grant_creations WHERE project_id=$1 AND grant_id IS NULL",
			projectID,
		).Scan(&count))
		require.Equal(t, 1, count)
	}
	_, _, err = store.Execution().CreateProjectMachineGrant(ctx, machineInput)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
	_, err = store.Execution().CreateProjectMachinePoolGrant(ctx, poolInput)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
}
