//go:build integration

package storage

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestConcurrentPoolTeardownDestroysSharedDeletedOrgSecret(t *testing.T) {
	t.Parallel()
	f := newSecretConsumerFixture(t, "secret")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	machines := []uuid.UUID{uuid.New(), uuid.New()}
	for _, machineID := range machines {
		poolID := uuid.New()
		_, err := f.store.pool.Exec(ctx, `INSERT INTO machine_pools(
  id, org_id, name, management_kind, provider, default_machine_memory_mb,
  max_total_machines, max_total_memory_mb, max_machine_memory_mb,
  provider_auth_secret_id, deleted_at, created_at, updated_at)
VALUES ($1, $2, $1::uuid::text, 'tenant', 'test', 1024, 1, 1024, 1024, $3, statement_timestamp(),
statement_timestamp(), statement_timestamp())`,
			poolID, testOrgID, f.secret.ID)
		require.NoError(t, err)
		_, err = f.store.pool.Exec(ctx, `INSERT INTO machines(
  id, org_id, machine_pool_id, source_kind, display_name, provider, lifecycle_state,
  lifecycle_changed_at, memory_mb, provider_options, next_reconcile_after, delete_attempts, delete_reason_code,
  created_at, updated_at)
VALUES ($1, $2, $3, 'pool', 'Teardown Machine', 'test', 'deleting', statement_timestamp(),
  1024, '{}'::jsonb, statement_timestamp(), 1, 'test_cleanup', statement_timestamp(), statement_timestamp())`,
			machineID, testOrgID, poolID)
		require.NoError(t, err)
	}
	_, err := f.store.pool.Exec(ctx, "UPDATE orgs SET deleted_at = statement_timestamp() WHERE id = $1", testOrgID)
	require.NoError(t, err)
	_, err = f.store.pool.Exec(ctx,
		"UPDATE secrets SET deleted_at = statement_timestamp(), current_version_id = NULL WHERE id = $1", f.secret.ID)
	require.NoError(t, err)

	// Hold the first completion after its reference check but before commit. Its
	// cleared pool reference is still invisible to the second transaction.
	firstAtCommit := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	config := f.store.pool.Config()
	config.ConnConfig.Tracer = &integrationdb.QueryTrace{OnQuery: func(sql string) {
		if sql == "commit" {
			close(firstAtCommit)
			select {
			case <-releaseFirst:
			case <-ctx.Done():
			}
		}
	}}
	firstPool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	defer func() { release(); firstPool.Close() }()
	first := integrationdb.RunAsyncError(func() error {
		return newIntegrationStore(firstPool).Execution().CompletePoolMachineDeletion(ctx, testOrgID, machines[0], 1)
	})
	select {
	case <-firstAtCommit:
	case <-ctx.Done():
		t.Fatal("first pool completion did not reach commit")
	}

	secondAtCleanup := make(chan struct{}, 1)
	config = f.store.pool.Config()
	config.ConnConfig.Tracer = &integrationdb.QueryTrace{OnQuery: func(sql string) {
		if strings.HasPrefix(sql, "-- name: LockResourceCreation ") {
			select {
			case secondAtCleanup <- struct{}{}:
			default:
			}
		}
	}}
	secondPool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	defer func() { release(); secondPool.Close() }()
	second := integrationdb.RunAsyncError(func() error {
		return newIntegrationStore(secondPool).Execution().CompletePoolMachineDeletion(ctx, testOrgID, machines[1], 1)
	})
	var secondErr error
	var secondFinished bool
	select {
	case <-secondAtCleanup:
		// The cleanup lock must wait for the first commit before the second
		// transaction obtains a fresh reference snapshot.
		integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, "LockResourceCreation", 1)
	case secondErr = <-second:
		// Without serialization both transactions can commit after observing
		// the other's reference, permanently stranding the ciphertext.
		secondFinished = true
	case <-ctx.Done():
		t.Fatal("second pool completion did not reach cleanup")
	}
	release()
	require.NoError(t, integrationdb.Await(t, first, "first pool completion"))
	if !secondFinished {
		secondErr = integrationdb.Await(t, second, "second pool completion")
	}
	require.NoError(t, secondErr)
	var versions, references int
	require.NoError(t, f.store.pool.QueryRow(ctx, `SELECT
  (SELECT count(*) FROM secret_versions WHERE secret_id = $1),
  (SELECT count(*) FROM machine_pools WHERE provider_auth_secret_id = $1)`, f.secret.ID).Scan(&versions, &references))
	require.Zero(t, references)
	require.Zero(t, versions, "the final shared-secret consumer must destroy the retained ciphertext")
}

func TestPoolTeardownCompletionSerializesWithOrganizationDeletion(t *testing.T) {
	for _, firstOperation := range []string{"organization", "completion"} {
		t.Run(firstOperation, func(t *testing.T) {
			f := newSecretConsumerFixture(t, "secret")
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			poolID, machineID := uuid.New(), uuid.New()
			_, err := f.store.pool.Exec(ctx, `INSERT INTO machine_pools(
  id, org_id, name, management_kind, provider, default_machine_memory_mb,
  max_total_machines, max_total_memory_mb, max_machine_memory_mb,
  provider_auth_secret_id, deleted_at, created_at, updated_at)
VALUES ($1, $2, $1::uuid::text, 'tenant', 'test', 1024, 1, 1024, 1024, $3, statement_timestamp(),
statement_timestamp(), statement_timestamp())`, poolID, testOrgID, f.secret.ID)
			require.NoError(t, err)
			_, err = f.store.pool.Exec(ctx, `INSERT INTO machines(
  id, org_id, machine_pool_id, source_kind, display_name, provider, lifecycle_state,
  lifecycle_changed_at, memory_mb, provider_options, next_reconcile_after, delete_attempts, delete_reason_code,
  created_at, updated_at)
VALUES ($1, $2, $3, 'pool', 'Teardown Machine', 'test', 'deleting', statement_timestamp(),
  1024, '{}'::jsonb, statement_timestamp(), 1, 'test_cleanup', statement_timestamp(), statement_timestamp())`,
				machineID, testOrgID, poolID)
			require.NoError(t, err)

			// Pause after the first reference check while its writes are still
			// uncommitted. The pool was already deleted, so organization deletion
			// must serialize through the remaining machine, not the pool row.
			firstAtCommit := make(chan struct{})
			releaseFirst := make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
			config := f.store.pool.Config()
			firstTrace := &integrationdb.QueryTrace{OnQuery: func(sql string) {
				if sql == "commit" {
					close(firstAtCommit)
					select {
					case <-releaseFirst:
					case <-ctx.Done():
					}
				}
			}}
			config.ConnConfig.Tracer = firstTrace
			firstPool, err := pgxpool.NewWithConfig(ctx, config)
			require.NoError(t, err)
			defer func() { release(); firstPool.Close() }()
			run := func(store *Store, operation string) error {
				if operation == "completion" {
					return store.Execution().CompletePoolMachineDeletion(ctx, testOrgID, machineID, 1)
				}
				_, deleteErr := store.Organizations().DeleteOrganizationOnceForIntegration(
					ctx, testOrgID, userPrincipal(f.admin.ID),
				)
				return deleteErr
			}
			first := integrationdb.RunAsyncError(func() error { return run(newIntegrationStore(firstPool), firstOperation) })
			select {
			case <-firstAtCommit:
			case firstErr := <-first:
				t.Fatalf("first operation returned before commit: %v", firstErr)
			case <-ctx.Done():
				t.Fatal("first operation did not reach commit")
			}
			secondOperation, waitQuery := "completion", "LockPoolMachineDeletionAttemptForLifecycle"
			if firstOperation == "completion" {
				secondOperation, waitQuery = "organization", "LockMachineForLifecycle"
			}
			secondTrace := &integrationdb.QueryTrace{}
			config = f.store.pool.Config()
			config.ConnConfig.Tracer = secondTrace
			secondPool, err := pgxpool.NewWithConfig(ctx, config)
			require.NoError(t, err)
			defer func() { release(); secondPool.Close() }()
			second := integrationdb.RunAsyncError(func() error { return run(newIntegrationStore(secondPool), secondOperation) })
			integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, waitQuery, 1)
			release()
			require.NoError(t, integrationdb.Await(t, first, firstOperation))
			require.NoError(t, integrationdb.Await(t, second, secondOperation))
			require.EqualValues(t, 1, firstTrace.Begins.Load(), "first operation must not need a deadlock retry")
			require.EqualValues(t, 1, secondTrace.Begins.Load(), "second operation must not need a deadlock retry")
			var versions, references int
			require.NoError(t, f.store.pool.QueryRow(ctx, `SELECT
  (SELECT count(*) FROM secret_versions WHERE secret_id = $1),
  (SELECT count(*) FROM machine_pools WHERE provider_auth_secret_id = $1)`, f.secret.ID).Scan(&versions, &references))
			require.Zero(t, references)
			require.Zero(t, versions, "organization deletion and final completion must not strand ciphertext")
		})
	}
}
