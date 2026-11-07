//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

type grantCreationResult struct {
	ID      uuid.UUID
	Created bool
}

type grantCreationFixture struct {
	pool         *pgxpool.Pool
	store        *Store
	targetID     uuid.UUID
	table        string
	receiptTable string
	lockQuery    string
	reserveQuery string
	create       func(context.Context, uuid.UUID, uuid.UUID, string, string) (grantCreationResult, error)
	lock         func(context.Context, pgx.Tx) error
	revoke       func(context.Context, uuid.UUID) error
	deleteTarget func(context.Context) error
}

func newGrantCreationFixture(t *testing.T, kind string) grantCreationFixture {
	t.Helper()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, storage.WithMachinePoolProviders(mergingMachinePoolProviders{}))
	f := grantCreationFixture{pool: pool, store: store}
	if kind == "machine" {
		machine, err := store.Execution().CreateDaemonMachine(ctx, executionstore.CreateDaemonMachineInput{
			OrgID: testOrgID, DisplayName: "Receipt machine",
		})
		require.NoError(t, err)
		f.targetID = machine.ID
		f.table, f.receiptTable = "project_machine_grants", "project_machine_grant_creations"
		f.lockQuery, f.reserveQuery = "LockMachineForLifecycle", "ReserveProjectMachineGrantCreation"
		f.create = func(
			ctx context.Context, projectID, targetID uuid.UUID, key, description string,
		) (grantCreationResult, error) {
			grant, _, err := store.Execution().CreateProjectMachineGrant(ctx, executionstore.CreateProjectMachineGrantInput{
				OrgID: testOrgID, ProjectID: projectID, MachineID: targetID,
				IdempotencyKey: key, Description: description,
			})
			return grantCreationResult{grant.ID, grant.Created}, err
		}
		f.lock = func(ctx context.Context, tx pgx.Tx) error {
			_, err := dbsqlc.New(tx).LockMachineForLifecycle(ctx, dbsqlc.LockMachineForLifecycleParams{
				OrgID: testOrgID, ID: machine.ID,
			})
			return err
		}
		f.revoke = func(ctx context.Context, grantID uuid.UUID) error {
			_, err := store.Execution().DeleteProjectMachineGrant(ctx, testOrgID, testProjectID, grantID)
			return err
		}
		f.deleteTarget = func(ctx context.Context) error {
			_, err := store.Execution().DeleteMachine(ctx, executionstore.DeleteMachineInput{
				OrgID: testOrgID, MachineID: machine.ID,
			})
			return err
		}
	} else {
		machinePool, err := store.Execution().CreateMachinePool(ctx, completeMachinePoolCreateInputForTest(
			t, ctx, store, executionstore.CreateMachinePoolInput{
				OrgID: testOrgID, Name: "Receipt pool", Provider: "test", MaxTotalMachines: 2,
			},
		))
		require.NoError(t, err)
		f.targetID = machinePool.ID
		f.table, f.receiptTable = "project_machine_pool_grants", "project_machine_pool_grant_creations"
		f.lockQuery, f.reserveQuery = "LockMachinePoolForUpdate", "ReserveProjectMachinePoolGrantCreation"
		f.create = func(
			ctx context.Context, projectID, targetID uuid.UUID, key, description string,
		) (grantCreationResult, error) {
			grant, err := store.Execution().CreateProjectMachinePoolGrant(
				ctx, executionstore.CreateProjectMachinePoolGrantInput{
					OrgID: testOrgID, ProjectID: projectID, MachinePoolID: targetID,
					IdempotencyKey: key, Description: description,
				},
			)
			return grantCreationResult{grant.ID, grant.Created}, err
		}
		f.lock = func(ctx context.Context, tx pgx.Tx) error {
			_, err := dbsqlc.New(tx).LockMachinePoolForUpdate(ctx, dbsqlc.LockMachinePoolForUpdateParams{
				OrgID: testOrgID, ID: machinePool.ID,
			})
			return err
		}
		f.revoke = func(ctx context.Context, grantID uuid.UUID) error {
			_, err := store.Execution().DeleteProjectMachinePoolGrant(ctx, testOrgID, testProjectID, grantID)
			return err
		}
		f.deleteTarget = func(ctx context.Context) error {
			_, err := store.Execution().DeleteMachinePool(ctx, testOrgID, machinePool.ID)
			return err
		}
	}
	return f
}

func (f grantCreationFixture) requireReceipt(t *testing.T, projectID uuid.UUID, key string, grantID *uuid.UUID) {
	t.Helper()
	var actualID *uuid.UUID
	var digest string
	require.NoError(t, f.pool.QueryRow(t.Context(),
		"SELECT grant_id, request_digest FROM "+f.receiptTable+" WHERE project_id=$1 AND idempotency_key=$2",
		projectID, key,
	).Scan(&actualID, &digest))
	require.Equal(t, grantID, actualID)
	require.Regexp(t, "^[0-9a-f]{64}$", digest)
}

func TestProjectMachineGrantCreationConcurrentReplay(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"machine", "pool"} {
		for _, changed := range []bool{false, true} {
			name := kind + "/same_body"
			if changed {
				name = kind + "/changed_body"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				ctx := t.Context()
				f := newGrantCreationFixture(t, kind)
				blocker := integrationdb.BeginTx(t, ctx, f.pool)
				require.NoError(t, f.lock(ctx, blocker))
				first := integrationdb.RunAsync(func() (grantCreationResult, error) {
					return f.create(ctx, testProjectID, f.targetID, "concurrent", "original")
				})
				integrationdb.WaitForNamedLockWaiters(t, ctx, f.pool, f.lockQuery, 1)
				description := "original"
				if changed {
					description = "different"
				}
				second := integrationdb.RunAsync(func() (grantCreationResult, error) {
					return f.create(ctx, testProjectID, f.targetID, "concurrent", description)
				})
				// The losing request must wait on the receipt, before its target lock.
				integrationdb.WaitForNamedLockWaiters(t, ctx, f.pool, f.reserveQuery, 1)
				require.NoError(t, blocker.Commit(ctx))
				created := integrationdb.AwaitSuccess(t, first, "first grant creation")
				replay := integrationdb.Await(t, second, "concurrent grant replay")
				require.True(t, created.Created)
				if changed {
					require.ErrorIs(t, replay.Err, storeerr.ErrIdempotencyConflict)
				} else {
					require.NoError(t, replay.Err)
					require.Equal(t, created.ID, replay.Value.ID)
					require.False(t, replay.Value.Created)
				}
				f.requireReceipt(t, testProjectID, "concurrent", &created.ID)
				var grants, receipts int
				require.NoError(t, f.pool.QueryRow(ctx,
					"SELECT (SELECT count(*) FROM "+f.table+"),(SELECT count(*) FROM "+f.receiptTable+")",
				).Scan(&grants, &receipts))
				require.Equal(t, 1, grants)
				require.Equal(t, 1, receipts)
			})
		}
	}
}

func TestProjectMachineGrantCreationRevocationAndCleanup(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"machine", "pool"} {
		for _, cleanup := range []string{"revoke", "target_delete", "project_delete"} {
			t.Run(kind+"/"+cleanup, func(t *testing.T) {
				t.Parallel()
				ctx := t.Context()
				f := newGrantCreationFixture(t, kind)
				grant, err := f.create(ctx, testProjectID, f.targetID, "consumed", "")
				require.NoError(t, err)
				f.requireReceipt(t, testProjectID, "consumed", &grant.ID)
				switch cleanup {
				case "revoke":
					require.NoError(t, f.revoke(ctx, grant.ID))
				case "target_delete":
					require.NoError(t, f.deleteTarget(ctx))
				case "project_delete":
					user := mustCreateProjectRoleUser(t, ctx, f.store, "grant-receipt-cleanup@example.com", "Receipt cleanup", "admin")
					_, err := f.store.Organizations().DeleteProject(ctx, testOrgID, testProjectID, userPrincipal(user.ID))
					require.NoError(t, err)
				}
				f.requireReceipt(t, testProjectID, "consumed", nil)
				_, err = f.create(ctx, testProjectID, f.targetID, "consumed", "")
				if cleanup == "project_delete" {
					require.ErrorIs(t, err, storeerr.ErrNotFound)
				} else {
					require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
				}
				var count int
				require.NoError(t, f.pool.QueryRow(ctx, "SELECT count(*) FROM "+f.table).Scan(&count))
				require.Zero(t, count)
				if cleanup == "revoke" {
					replacement, err := f.create(ctx, testProjectID, f.targetID, "new-command", "")
					require.NoError(t, err)
					require.True(t, replacement.Created)
					require.NotEqual(t, grant.ID, replacement.ID)
					f.requireReceipt(t, testProjectID, "consumed", nil)
				}
			})
		}
	}
}

func TestProjectMachineGrantCreationReplayDuringRevocation(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"machine", "pool"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newGrantCreationFixture(t, kind)
			grant, err := f.create(ctx, testProjectID, f.targetID, "revoking", "")
			require.NoError(t, err)
			revoke := integrationdb.BeginTx(t, ctx, f.pool)
			require.NoError(t, f.lock(ctx, revoke))
			replay := integrationdb.RunAsync(func() (grantCreationResult, error) {
				return f.create(ctx, testProjectID, f.targetID, "revoking", "")
			})
			integrationdb.WaitForNamedLockWaiters(t, ctx, f.pool, f.lockQuery, 1)
			// Reproduce the lifecycle lock -> grant delete -> receipt FK update order.
			// A replay holding an update lock on the receipt would deadlock here.
			_, err = revoke.Exec(ctx, "DELETE FROM "+f.table+" WHERE id=$1", grant.ID)
			require.NoError(t, err)
			require.NoError(t, revoke.Commit(ctx))
			result := integrationdb.Await(t, replay, "replay racing grant revocation")
			require.ErrorIs(t, result.Err, storeerr.ErrIdempotencyConflict)
			f.requireReceipt(t, testProjectID, "revoking", nil)
		})
	}
}

func TestProjectMachineGrantCreationOldKeyCannotReplaceNewGrant(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"machine", "pool"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newGrantCreationFixture(t, kind)
			original, err := f.create(ctx, testProjectID, f.targetID, "original-key", "same access")
			require.NoError(t, err)
			require.NoError(t, f.revoke(ctx, original.ID))
			replacement, err := f.create(ctx, testProjectID, f.targetID, "replacement-key", "same access")
			require.NoError(t, err)
			require.True(t, replacement.Created)
			require.NotEqual(t, original.ID, replacement.ID)

			_, err = f.create(ctx, testProjectID, f.targetID, "original-key", "same access")
			require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
			replayed, err := f.create(ctx, testProjectID, f.targetID, "replacement-key", "same access")
			require.NoError(t, err)
			require.Equal(t, replacement.ID, replayed.ID)
			require.False(t, replayed.Created)
			f.requireReceipt(t, testProjectID, "original-key", nil)
			f.requireReceipt(t, testProjectID, "replacement-key", &replacement.ID)
			var count int
			require.NoError(t, f.pool.QueryRow(ctx, "SELECT count(*) FROM "+f.table).Scan(&count))
			require.Equal(t, 1, count, "the replacement remains the only active grant")
		})
	}
}

func TestProjectMachineGrantCreationFailedCreateAndKeyScope(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"machine", "pool"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newGrantCreationFixture(t, kind)
			_, err := f.create(ctx, testProjectID, uuid.New(), "retry-valid", "")
			require.Error(t, err)
			var count int
			require.NoError(t, f.pool.QueryRow(ctx, "SELECT count(*) FROM "+f.receiptTable).Scan(&count))
			require.Zero(t, count, "failed create must roll back its reserved key")
			first, err := f.create(ctx, testProjectID, f.targetID, "retry-valid", "")
			require.NoError(t, err)
			require.True(t, first.Created)
			var otherProject uuid.UUID
			require.NoError(t, f.pool.QueryRow(ctx, `
INSERT INTO projects(org_id, name, created_at, updated_at)
VALUES ($1, 'Other receipt scope', statement_timestamp(), statement_timestamp())
RETURNING id`, testOrgID).Scan(&otherProject))
			second, err := f.create(ctx, otherProject, f.targetID, "retry-valid", "")
			require.NoError(t, err)
			require.True(t, second.Created)
			require.NotEqual(t, first.ID, second.ID)
			f.requireReceipt(t, testProjectID, "retry-valid", &first.ID)
			f.requireReceipt(t, otherProject, "retry-valid", &second.ID)
		})
	}
}

func TestProjectMachinePoolGrantCreationReplaysOriginalIntentAfterPatch(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newGrantCreationFixture(t, "pool")
	input := executionstore.CreateProjectMachinePoolGrantInput{
		OrgID: testOrgID, ProjectID: testProjectID, MachinePoolID: f.targetID,
		IdempotencyKey: "original-intent", Description: "original",
		DefaultMachineEnvOverlay: json.RawMessage(`{"B":"two","A":"one"}`),
	}
	created, err := f.store.Execution().CreateProjectMachinePoolGrant(ctx, input)
	require.NoError(t, err)
	updated, err := f.store.Execution().UpdateProjectMachinePoolGrant(
		ctx, executionstore.UpdateProjectMachinePoolGrantInput{
			OrgID: testOrgID, ProjectID: testProjectID, ID: created.ID,
			Description: new("changed"), DefaultMachineEnvOverlay: new(json.RawMessage(`{"C":"three"}`)),
		},
	)
	require.NoError(t, err)
	input.DefaultMachineEnvOverlay = json.RawMessage(`{ "A": "one", "B": "two" }`)
	replay, err := f.store.Execution().CreateProjectMachinePoolGrant(ctx, input)
	require.NoError(t, err)
	require.Equal(t, updated.ID, replay.ID)
	require.Equal(t, updated.Description, replay.Description)
	require.JSONEq(t, string(updated.DefaultMachineEnvOverlay), string(replay.DefaultMachineEnvOverlay))
	require.False(t, replay.Created)
	input.Description = updated.Description
	input.DefaultMachineEnvOverlay = updated.DefaultMachineEnvOverlay
	_, err = f.store.Execution().CreateProjectMachinePoolGrant(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict,
		"a request matching current state still differs from the original command")
}

func TestProjectMachinePoolGrantCreationFingerprintPreservesLargeNumbers(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newGrantCreationFixture(t, "pool")
	input := executionstore.CreateProjectMachinePoolGrantInput{
		OrgID: testOrgID, ProjectID: testProjectID, MachinePoolID: f.targetID, IdempotencyKey: "large-number-intent",
		DefaultMachineProviderOptionsOverlay: json.RawMessage(`{"build":9007199254740992}`),
	}
	created, err := f.store.Execution().CreateProjectMachinePoolGrant(ctx, input)
	require.NoError(t, err)
	replay, err := f.store.Execution().CreateProjectMachinePoolGrant(ctx, input)
	require.NoError(t, err)
	require.Equal(t, created.ID, replay.ID)
	require.False(t, replay.Created)
	// These integers collapse to the same float64, but represent different intents.
	input.DefaultMachineProviderOptionsOverlay = json.RawMessage(`{"build":9007199254740993}`)
	_, err = f.store.Execution().CreateProjectMachinePoolGrant(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
}
