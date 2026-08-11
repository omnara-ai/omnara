//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

type runtimeLockLeaseFixture struct {
	Pool    *pgxpool.Pool
	Store   *Store
	AgentID ID
}

func newRuntimeLockLeaseFixture(t *testing.T, ctx context.Context, seed string) runtimeLockLeaseFixture {
	t.Helper()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	configID := mustCreateAgentConfig(t, ctx, store, testProjectID, "runtime-lock-lease-"+seed, now)
	agent, err := store.Execution().CreateAgentFixture(
		ctx,
		executionstore.AgentFixtureInput{ProjectID: testProjectID, CurrentConfigID: configID},
	)
	if err != nil {
		t.Fatalf("create runtime-lock lease agent: %v", err)
	}
	return runtimeLockLeaseFixture{Pool: pool, Store: store, AgentID: agent.ID}
}

func (f runtimeLockLeaseFixture) acquire(
	t *testing.T,
	ctx context.Context,
	workerProcessID ID,
	leaseDuration time.Duration,
) executionstore.AgentRuntimeLockRecord {
	t.Helper()
	return f.acquireForAgent(t, ctx, f.AgentID, workerProcessID, leaseDuration)
}

func (f runtimeLockLeaseFixture) acquireForAgent(
	t *testing.T,
	ctx context.Context,
	agentID, workerProcessID ID,
	leaseDuration time.Duration,
) executionstore.AgentRuntimeLockRecord {
	t.Helper()
	lock, err := f.Store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		agentID,
		workerProcessID,
		leaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire agent runtime lock: %v", err)
	}
	return lock
}

func TestAgentRuntimeLockAcquisitionStoresDatabaseOwnedLease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newRuntimeLockLeaseFixture(t, ctx, "acquisition")
	leaseDuration := 37 * time.Second

	var databaseBefore time.Time
	if err := fixture.Pool.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&databaseBefore); err != nil {
		t.Fatalf("read database time before acquisition: %v", err)
	}
	lock := fixture.acquire(t, ctx, testWorkerProcessID, leaseDuration)
	var databaseAfter time.Time
	if err := fixture.Pool.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&databaseAfter); err != nil {
		t.Fatalf("read database time after acquisition: %v", err)
	}

	if !lock.StartedAt.Equal(lock.RenewedAt) {
		t.Fatalf("acquisition timestamps differ: started=%s renewed=%s", lock.StartedAt, lock.RenewedAt)
	}
	if lock.StartedAt.Before(databaseBefore) || lock.StartedAt.After(databaseAfter) {
		t.Fatalf(
			"acquisition timestamp %s outside database window [%s, %s]",
			lock.StartedAt,
			databaseBefore,
			databaseAfter,
		)
	}
	if got := lock.LeaseExpiresAt.Sub(lock.RenewedAt); got != leaseDuration {
		t.Fatalf("stored lease duration = %s, want %s", got, leaseDuration)
	}
	if lock.WorkerProcessID != testWorkerProcessID {
		t.Fatalf("worker process id = %s, want %s", lock.WorkerProcessID, testWorkerProcessID)
	}
}

func TestAgentRuntimeLockMutationsRejectWrongProject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newRuntimeLockLeaseFixture(t, ctx, "wrong-project-mutations")
	wrongProjectID := seedAdditionalProjectForTest(
		t,
		ctx,
		fixture.Pool,
		"runtime_lock_wrong_scope",
	)
	lock := fixture.acquire(t, ctx, testWorkerProcessID, time.Minute)

	if _, err := fixture.Store.Execution().RenewAgentRuntimeLock(
		ctx,
		wrongProjectID,
		fixture.AgentID,
		lock.ID,
		time.Minute,
	); !errors.Is(err, storeerr.ErrRuntimeLockInactive) {
		t.Fatalf("wrong-project renewal error = %v, want runtime lock inactive", err)
	}
	if _, err := dbsqlc.New(fixture.Pool).RequestAgentRuntimeCancel(
		ctx,
		dbsqlc.RequestAgentRuntimeCancelParams{
			ProjectID: wrongProjectID,
			AgentID:   fixture.AgentID,
		},
	); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("wrong-project cancellation error = %v, want no rows", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		wrongProjectID,
		fixture.AgentID,
		lock.ID,
	); err == nil {
		t.Fatal("wrong-project release unexpectedly succeeded")
	}

	var cancelRequested bool
	if err := fixture.Pool.QueryRow(
		ctx,
		`SELECT cancel_requested_at IS NOT NULL FROM agent_runtime_locks WHERE id = $1`,
		lock.ID,
	).Scan(&cancelRequested); err != nil {
		t.Fatalf("load runtime lock after wrong-project mutations: %v", err)
	}
	if cancelRequested {
		t.Fatal("wrong-project mutation requested runtime cancellation")
	}
	if _, err := fixture.Store.Execution().RenewAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		lock.ID,
		time.Minute,
	); err != nil {
		t.Fatalf("correct-project renewal after rejected mutations: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		lock.ID,
	); err != nil {
		t.Fatalf("correct-project release after rejected mutations: %v", err)
	}
}

func TestAgentRuntimeLockReaperReconcilesIdleAgentWithoutWakeup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newRuntimeLockLeaseFixture(t, ctx, "reap-wakeup")
	lock := fixture.acquire(t, ctx, testWorkerProcessID, time.Minute)
	expireAgentRuntimeLockForTest(t, ctx, fixture.Store, lock.ID)
	if err := fixture.Store.Execution().DeleteAgentWakeup(ctx, testProjectID, fixture.AgentID); err != nil {
		t.Fatalf("clear agent wakeup before reap: %v", err)
	}

	reaped, err := fixture.Store.Execution().ReapExpiredAgentRuntimeLocks(ctx, 100)
	if err != nil {
		t.Fatalf("reap expired runtime lock: %v", err)
	}
	if reaped != 1 {
		t.Fatalf("reaped runtime locks = %d, want 1", reaped)
	}
	var wakeupCount int
	if err := fixture.Pool.QueryRow(
		ctx,
		`SELECT count(*)
FROM agent_wakeups wake
JOIN agents agent ON agent.id = wake.agent_id
WHERE agent.project_id = $1 AND wake.agent_id = $2`,
		testProjectID,
		fixture.AgentID,
	).Scan(&wakeupCount); err != nil {
		t.Fatalf("count wakeups after runtime-lock reap: %v", err)
	}
	if wakeupCount != 0 {
		t.Fatalf("wakeups after idle runtime-lock reap = %d, want 0", wakeupCount)
	}
}

func TestAgentRuntimeLockReaperContinuesAfterCandidateFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newRuntimeLockLeaseFixture(t, ctx, "reap-error-isolation")
	failedLock := fixture.acquire(t, ctx, testWorkerProcessID, time.Minute)
	laterAgentID := mustCreateAgent(t, ctx, fixture.Store, time.Now().UTC())
	laterLock := fixture.acquireForAgent(
		t,
		ctx,
		laterAgentID,
		testID("runtime_lock_reap_later_worker"),
		time.Minute,
	)
	for _, agentID := range []ID{fixture.AgentID, laterAgentID} {
		if err := fixture.Store.Execution().DeleteAgentWakeup(ctx, testProjectID, agentID); err != nil {
			t.Fatalf("clear agent wakeup before reap: %v", err)
		}
	}
	if _, err := fixture.Pool.Exec(
		ctx,
		`UPDATE agent_runtime_locks
SET started_at = statement_timestamp() - interval '4 minutes',
    renewed_at = statement_timestamp() - interval '3 minutes',
    lease_expires_at = statement_timestamp() - CASE
      WHEN id = $1 THEN interval '2 minutes'
      ELSE interval '1 minute'
    END
WHERE id IN ($1, $2)`,
		failedLock.ID,
		laterLock.ID,
	); err != nil {
		t.Fatalf("expire runtime locks in deterministic order: %v", err)
	}
	if _, err := fixture.Pool.Exec(ctx, `
CREATE TABLE runtime_lock_reap_test_commit_failures (agent_id uuid PRIMARY KEY);
CREATE FUNCTION fail_runtime_lock_reap_commit()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM runtime_lock_reap_test_commit_failures failure
        WHERE failure.agent_id = OLD.agent_id
    ) THEN
        RAISE EXCEPTION 'injected runtime lock reap commit failure';
    END IF;
    RETURN OLD;
END
$function$;
CREATE CONSTRAINT TRIGGER runtime_lock_reap_commit_failure
AFTER DELETE ON agent_runtime_locks
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION fail_runtime_lock_reap_commit();
`); err != nil {
		t.Fatalf("install runtime-lock reap commit failure: %v", err)
	}
	if _, err := fixture.Pool.Exec(
		ctx,
		`INSERT INTO runtime_lock_reap_test_commit_failures(agent_id) VALUES ($1)`,
		fixture.AgentID,
	); err != nil {
		t.Fatalf("seed runtime-lock reap commit failure: %v", err)
	}

	reaped, reapErr := fixture.Store.Execution().ReapExpiredAgentRuntimeLocks(ctx, 100)
	if reapErr == nil || !strings.Contains(reapErr.Error(), "injected runtime lock reap commit failure") {
		t.Fatalf("runtime-lock reap error = %v, want injected commit failure", reapErr)
	}
	if reaped != 1 {
		t.Fatalf("reaped runtime locks = %d, want later candidate to succeed", reaped)
	}
	var failedExists, laterExists bool
	if err := fixture.Pool.QueryRow(
		ctx,
		`SELECT EXISTS (SELECT 1 FROM agent_runtime_locks WHERE id = $1),
                EXISTS (SELECT 1 FROM agent_runtime_locks WHERE id = $2)`,
		failedLock.ID,
		laterLock.ID,
	).Scan(&failedExists, &laterExists); err != nil {
		t.Fatalf("load runtime locks after isolated failure: %v", err)
	}
	if !failedExists || laterExists {
		t.Fatalf("runtime locks after isolated failure: failed=%v later=%v, want true/false", failedExists, laterExists)
	}
}

func TestAgentRuntimeLockRenewalAdvancesLeaseTogether(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newRuntimeLockLeaseFixture(t, ctx, "renewal")
	lock := fixture.acquire(t, ctx, testWorkerProcessID, time.Minute)
	if _, err := fixture.Pool.Exec(
		ctx,
		`UPDATE agent_runtime_locks
SET started_at = statement_timestamp() - interval '5 minutes',
    renewed_at = statement_timestamp() - interval '4 minutes',
    lease_expires_at = statement_timestamp() - interval '3 minutes'
WHERE id = $1`,
		lock.ID,
	); err != nil {
		t.Fatalf("age runtime-lock lease: %v", err)
	}
	var agedRenewedAt, agedLeaseExpiresAt time.Time
	if err := fixture.Pool.QueryRow(
		ctx,
		`SELECT renewed_at, lease_expires_at FROM agent_runtime_locks WHERE id = $1`,
		lock.ID,
	).Scan(&agedRenewedAt, &agedLeaseExpiresAt); err != nil {
		t.Fatalf("load aged runtime lock: %v", err)
	}

	leaseDuration := 41 * time.Second
	renewal, err := fixture.Store.Execution().RenewAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		lock.ID,
		leaseDuration,
	)
	if err != nil {
		t.Fatalf("renew agent runtime lock: %v", err)
	}
	renewedLock := renewal.RuntimeLock
	if !renewedLock.RenewedAt.After(agedRenewedAt) {
		t.Fatalf("renewal did not advance: before=%s after=%s", agedRenewedAt, renewedLock.RenewedAt)
	}
	if !renewedLock.LeaseExpiresAt.After(agedLeaseExpiresAt) {
		t.Fatalf("lease deadline did not advance: before=%s after=%s", agedLeaseExpiresAt, renewedLock.LeaseExpiresAt)
	}
	if got := renewedLock.LeaseExpiresAt.Sub(renewedLock.RenewedAt); got != leaseDuration {
		t.Fatalf("renewed lease duration = %s, want %s", got, leaseDuration)
	}
}

func TestAgentRuntimeLockReaperUsesStoredDeadline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newRuntimeLockLeaseFixture(t, ctx, "stored-deadline")
	lock := fixture.acquire(t, ctx, testWorkerProcessID, executionstore.MaximumAgentRuntimeLockLeaseDuration)

	reaped, err := fixture.Store.Execution().ReapExpiredAgentRuntimeLocks(ctx, 100)
	if err != nil {
		t.Fatalf("reap before stored deadline: %v", err)
	}
	if reaped != 0 {
		t.Fatalf("reaped %d runtime locks before stored deadline, want 0", reaped)
	}
	if err := fixture.Store.Execution().EnsureRuntimeLockActive(
		ctx,
		testProjectID,
		fixture.AgentID,
		lock.ID,
	); err != nil {
		t.Fatalf("runtime lock inactive before stored deadline: %v", err)
	}

	expireAgentRuntimeLockForTest(t, ctx, fixture.Store, lock.ID)
	reaped, err = fixture.Store.Execution().ReapExpiredAgentRuntimeLocks(ctx, 100)
	if err != nil {
		t.Fatalf("reap after stored deadline: %v", err)
	}
	if reaped != 1 {
		t.Fatalf("reaped %d runtime locks after stored deadline, want 1", reaped)
	}
	if err := fixture.Store.Execution().EnsureRuntimeLockActive(
		ctx,
		testProjectID,
		fixture.AgentID,
		lock.ID,
	); !errors.Is(err, storeerr.ErrRuntimeLockInactive) {
		t.Fatalf("old runtime lock active after reap: %v", err)
	}
}

func TestAgentRuntimeLockAcquisitionWaitGrantsFullLeaseAfterAgentUnlock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newRuntimeLockLeaseFixture(t, ctx, "acquisition-lock-wait")
	lockTx, err := fixture.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin agent lock holder: %v", err)
	}
	defer func() { _ = lockTx.Rollback(ctx) }()
	if _, err := lockTx.Exec(
		ctx,
		`SELECT id FROM agents WHERE project_id = $1 AND id = $2 FOR UPDATE`,
		testProjectID,
		fixture.AgentID,
	); err != nil {
		t.Fatalf("lock agent row: %v", err)
	}

	leaseDuration := 43 * time.Second
	type acquireResult struct {
		lock executionstore.AgentRuntimeLockRecord
		err  error
	}
	acquired := make(chan acquireResult, 1)
	go func() {
		lock, acquireErr := fixture.Store.Execution().AcquireAgentRuntimeLock(
			context.Background(),
			testProjectID,
			fixture.AgentID,
			testWorkerProcessID,
			leaseDuration,
		)
		acquired <- acquireResult{lock: lock, err: acquireErr}
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.Pool, "LockAgentInProject", 1)
	var releasedAfter time.Time
	if err := fixture.Pool.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&releasedAfter); err != nil {
		t.Fatalf("read database time before agent unlock: %v", err)
	}
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatalf("release agent row: %v", err)
	}

	result := <-acquired
	if result.err != nil {
		t.Fatalf("acquire after agent unlock: %v", result.err)
	}
	if result.lock.RenewedAt.Before(releasedAfter) {
		t.Fatalf("lease started before row-lock wait ended: renewed=%s marker=%s", result.lock.RenewedAt, releasedAfter)
	}
	if got := result.lock.LeaseExpiresAt.Sub(result.lock.RenewedAt); got != leaseDuration {
		t.Fatalf("post-wait lease duration = %s, want full %s", got, leaseDuration)
	}
}

func TestAgentRuntimeLockRenewalWaitGrantsFullLeaseAfterRuntimeUnlock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newRuntimeLockLeaseFixture(t, ctx, "renewal-lock-wait")
	leaseDuration := 47 * time.Second
	lock := fixture.acquire(t, ctx, testWorkerProcessID, leaseDuration)
	lockTx, err := fixture.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin runtime lock holder: %v", err)
	}
	defer func() { _ = lockTx.Rollback(ctx) }()
	if _, err := lockTx.Exec(
		ctx,
		`SELECT id FROM agent_runtime_locks WHERE id = $1 FOR UPDATE`,
		lock.ID,
	); err != nil {
		t.Fatalf("lock runtime row: %v", err)
	}

	type renewalResult struct {
		renewal executionstore.AgentRuntimeLockRenewal
		err     error
	}
	renewed := make(chan renewalResult, 1)
	go func() {
		renewedLock, renewErr := fixture.Store.Execution().RenewAgentRuntimeLock(
			context.Background(),
			testProjectID,
			fixture.AgentID,
			lock.ID,
			leaseDuration,
		)
		renewed <- renewalResult{renewal: renewedLock, err: renewErr}
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.Pool, "LockAgentRuntimeLockForRenewal", 1)
	var releasedAfter time.Time
	if err := fixture.Pool.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&releasedAfter); err != nil {
		t.Fatalf("read database time before runtime unlock: %v", err)
	}
	unlockStartedAt := time.Now()
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatalf("release runtime row: %v", err)
	}

	result := <-renewed
	if result.err != nil {
		t.Fatalf("renew after runtime unlock: %v", result.err)
	}
	if result.renewal.LocalLeaseBudgetStartedAt.Before(unlockStartedAt) {
		t.Fatalf(
			"local lease budget started before row-lock wait ended: started=%s marker=%s",
			result.renewal.LocalLeaseBudgetStartedAt,
			unlockStartedAt,
		)
	}
	renewedLock := result.renewal.RuntimeLock
	if renewedLock.RenewedAt.Before(releasedAfter) {
		t.Fatalf("lease renewed before row-lock wait ended: renewed=%s marker=%s", renewedLock.RenewedAt, releasedAfter)
	}
	if got := renewedLock.LeaseExpiresAt.Sub(renewedLock.RenewedAt); got != leaseDuration {
		t.Fatalf("post-wait renewed lease duration = %s, want full %s", got, leaseDuration)
	}
}

func TestAgentRuntimeLockReaperSkipsContendedAgent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newRuntimeLockLeaseFixture(t, ctx, "reap-skip-contended-agent")
	lock := fixture.acquire(t, ctx, testWorkerProcessID, time.Minute)
	expireAgentRuntimeLockForTest(t, ctx, fixture.Store, lock.ID)

	agentTx, err := fixture.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin agent lock holder: %v", err)
	}
	defer func() { _ = agentTx.Rollback(ctx) }()
	if _, err := agentTx.Exec(
		ctx,
		`SELECT id FROM agents WHERE project_id = $1 AND id = $2 FOR UPDATE`,
		testProjectID,
		fixture.AgentID,
	); err != nil {
		t.Fatalf("lock agent row: %v", err)
	}

	reapCtx, cancelReap := context.WithTimeout(ctx, 2*time.Second)
	reaped, err := fixture.Store.Execution().ReapExpiredAgentRuntimeLocks(reapCtx, 100)
	cancelReap()
	if err != nil {
		t.Fatalf("reap with contended agent: %v", err)
	}
	if reaped != 0 {
		t.Fatalf("reaped %d contended runtime locks, want 0", reaped)
	}
	var lockExists bool
	if err := fixture.Pool.QueryRow(
		ctx,
		`SELECT EXISTS (SELECT 1 FROM agent_runtime_locks WHERE id = $1)`,
		lock.ID,
	).Scan(&lockExists); err != nil {
		t.Fatalf("check contended runtime lock: %v", err)
	}
	if !lockExists {
		t.Fatal("contended runtime lock disappeared")
	}
	if err := agentTx.Commit(ctx); err != nil {
		t.Fatalf("release agent row: %v", err)
	}

	reaped, err = fixture.Store.Execution().ReapExpiredAgentRuntimeLocks(ctx, 100)
	if err != nil {
		t.Fatalf("reap after agent contention cleared: %v", err)
	}
	if reaped != 1 {
		t.Fatalf("reaped %d runtime locks after contention cleared, want 1", reaped)
	}
}

func TestAgentRuntimeLockReaperSkipsContendedRuntimeAndContinues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newRuntimeLockLeaseFixture(t, ctx, "reap-skip-contended-runtime")
	firstLock := fixture.acquire(t, ctx, testWorkerProcessID, time.Minute)
	secondAgentID := mustCreateAgent(t, ctx, fixture.Store, time.Now().UTC())
	secondLock := fixture.acquireForAgent(
		t,
		ctx,
		secondAgentID,
		testID("runtime_lock_reap_second_worker"),
		time.Minute,
	)
	if _, err := fixture.Pool.Exec(
		ctx,
		`UPDATE agent_runtime_locks
SET started_at = statement_timestamp() - interval '4 minutes',
    renewed_at = statement_timestamp() - interval '3 minutes',
    lease_expires_at = statement_timestamp() - CASE
      WHEN id = $1 THEN interval '2 minutes'
      ELSE interval '1 minute'
    END
WHERE id IN ($1, $2)`,
		firstLock.ID,
		secondLock.ID,
	); err != nil {
		t.Fatalf("expire runtime locks in deterministic order: %v", err)
	}

	runtimeTx, err := fixture.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin runtime-lock holder: %v", err)
	}
	defer func() { _ = runtimeTx.Rollback(ctx) }()
	if _, err := runtimeTx.Exec(
		ctx,
		`SELECT id FROM agent_runtime_locks WHERE id = $1 FOR UPDATE`,
		firstLock.ID,
	); err != nil {
		t.Fatalf("lock first runtime row: %v", err)
	}

	reapCtx, cancelReap := context.WithTimeout(ctx, 2*time.Second)
	reaped, err := fixture.Store.Execution().ReapExpiredAgentRuntimeLocks(reapCtx, 100)
	cancelReap()
	if err != nil {
		t.Fatalf("reap with contended runtime: %v", err)
	}
	if reaped != 1 {
		t.Fatalf("reaped %d runtime locks, want unlocked candidate only", reaped)
	}
	var firstExists, secondExists bool
	if err := fixture.Pool.QueryRow(
		ctx,
		`SELECT EXISTS (SELECT 1 FROM agent_runtime_locks WHERE id = $1),
                EXISTS (SELECT 1 FROM agent_runtime_locks WHERE id = $2)`,
		firstLock.ID,
		secondLock.ID,
	).Scan(&firstExists, &secondExists); err != nil {
		t.Fatalf("load runtime locks after contended reap: %v", err)
	}
	if !firstExists || secondExists {
		t.Fatalf("runtime locks after contended reap: first=%v second=%v, want true/false", firstExists, secondExists)
	}
	if err := runtimeTx.Commit(ctx); err != nil {
		t.Fatalf("release first runtime row: %v", err)
	}
	reaped, err = fixture.Store.Execution().ReapExpiredAgentRuntimeLocks(ctx, 100)
	if err != nil {
		t.Fatalf("reap after runtime contention cleared: %v", err)
	}
	if reaped != 1 {
		t.Fatalf("reaped %d runtime locks after contention cleared, want 1", reaped)
	}
}

func TestAgentRuntimeLockRenewalWinningReaperRacePreservesLease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newRuntimeLockLeaseFixture(t, ctx, "renewal-wins")
	lock := fixture.acquire(t, ctx, testWorkerProcessID, time.Minute)
	expireAgentRuntimeLockForTest(t, ctx, fixture.Store, lock.ID)

	renewalTx, err := fixture.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin renewal transaction: %v", err)
	}
	defer func() { _ = renewalTx.Rollback(ctx) }()
	renewal, err := executionstore.IntegrationRenewAgentRuntimeLockTx(
		ctx,
		dbsqlc.New(renewalTx),
		testProjectID,
		fixture.AgentID,
		lock.ID,
		time.Minute,
	)
	if err != nil {
		t.Fatalf("renew runtime lock in transaction: %v", err)
	}
	if !renewal.RuntimeLock.LeaseExpiresAt.After(renewal.RuntimeLock.RenewedAt) {
		t.Fatalf("renewal stored invalid lease: %+v", renewal)
	}

	reapCtx, cancelReap := context.WithTimeout(ctx, 2*time.Second)
	reaped, reapErr := fixture.Store.Execution().ReapExpiredAgentRuntimeLocks(reapCtx, 100)
	cancelReap()
	if reapErr != nil {
		t.Fatalf("reaper while renewal owns runtime row: %v", reapErr)
	}
	if reaped != 0 {
		t.Fatalf("reaper deleted %d locks while renewal owns runtime row, want 0", reaped)
	}
	if err := renewalTx.Commit(ctx); err != nil {
		t.Fatalf("commit winning renewal: %v", err)
	}
	if err := fixture.Store.Execution().EnsureRuntimeLockActive(
		ctx,
		testProjectID,
		fixture.AgentID,
		lock.ID,
	); err != nil {
		t.Fatalf("renewal-winning runtime lock was deleted: %v", err)
	}
}

func TestAgentRuntimeLockReaperWinningRaceFencesOldWorker(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newRuntimeLockLeaseFixture(t, ctx, "reaper-wins")
	oldWorkerID := testID("runtime_lock_reaper_old_worker")
	lock := fixture.acquire(t, ctx, oldWorkerID, time.Minute)
	expireAgentRuntimeLockForTest(t, ctx, fixture.Store, lock.ID)
	if err := fixture.Store.Execution().EnsureRuntimeLockActive(
		ctx,
		testProjectID,
		fixture.AgentID,
		lock.ID,
	); !errors.Is(err, storeerr.ErrRuntimeLockInactive) {
		t.Fatalf("expired runtime lock active before reap: %v", err)
	}
	oldOwnedWrite := toolCallFixtureInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		SourceEventID:      testID("runtime_lock_reaper_old_source_event"),
		ModelCallContextID: testID("runtime_lock_reaper_old_model_context"),
		RuntimeLockID:      lock.ID,
		ProviderCallID:     "runtime-lock-reaper-old-worker",
		Name:               "read_process",
		Input:              json.RawMessage(`{}`),
	}
	if _, err := insertToolCallForTest(ctx, fixture.Store, oldOwnedWrite); !errors.Is(err, storeerr.ErrRuntimeLockInactive) {
		t.Fatalf("expired runtime-lock write before reap = %v, want inactive runtime lock", err)
	}

	reapTx, err := fixture.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("begin reaper transaction: %v", err)
	}
	defer func() { _ = reapTx.Rollback(ctx) }()
	txNotifications := notifications.NewTxNotifications()
	reaped, err := executionstore.IntegrationReapExpiredAgentRuntimeLockTx(
		ctx,
		txNotifications,
		reapTx,
		testProjectID,
		fixture.AgentID,
		lock.ID,
		executionstore.ModelCallRetryBackoff,
	)
	if err != nil {
		t.Fatalf("reap expired runtime lock in transaction: %v", err)
	}
	if !reaped {
		t.Fatal("expired runtime lock was not reaped")
	}

	renewalDone := make(chan error, 1)
	go func() {
		_, renewalErr := fixture.Store.Execution().RenewAgentRuntimeLock(
			context.Background(),
			testProjectID,
			fixture.AgentID,
			lock.ID,
			time.Minute,
		)
		renewalDone <- renewalErr
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.Pool, "LockAgentRuntimeLockForRenewal", 1)
	if err := fixture.Store.Execution().IntegrationCommitTxWithNotifications(
		ctx,
		reapTx,
		txNotifications,
		"commit winning reaper",
	); err != nil {
		t.Fatalf("commit winning reaper: %v", err)
	}
	if err := <-renewalDone; !errors.Is(err, storeerr.ErrRuntimeLockInactive) {
		t.Fatalf("old worker renewal after reap = %v, want inactive runtime lock", err)
	}

	replacementWorkerID := testID("runtime_lock_reaper_replacement_worker")
	replacement := fixture.acquire(t, ctx, replacementWorkerID, time.Minute)
	if replacement.ID == lock.ID {
		t.Fatalf("replacement reused old runtime-lock ID: old=%+v replacement=%+v", lock, replacement)
	}
	if replacement.WorkerProcessID != replacementWorkerID {
		t.Fatalf("replacement routing id = %s, want %s", replacement.WorkerProcessID, replacementWorkerID)
	}
	if _, err := insertToolCallForTest(ctx, fixture.Store, oldOwnedWrite); !errors.Is(err, storeerr.ErrRuntimeLockInactive) {
		t.Fatalf("old worker tool-call write after replacement = %v, want inactive runtime lock", err)
	}
}

func TestConcurrentRuntimeLockReapersApplyRecoveryOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newRuntimeLockLeaseFixture(t, ctx, "concurrent-reapers")
	lock := fixture.acquire(t, ctx, testWorkerProcessID, time.Minute)
	expireAgentRuntimeLockForTest(t, ctx, fixture.Store, lock.ID)
	if err := fixture.Store.Execution().DeleteAgentWakeup(ctx, testProjectID, fixture.AgentID); err != nil {
		t.Fatalf("clear agent wakeup before reap: %v", err)
	}

	reapTx, err := fixture.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin winning reaper: %v", err)
	}
	defer func() { _ = reapTx.Rollback(ctx) }()
	txNotifications := notifications.NewTxNotifications()
	won, err := executionstore.IntegrationReapExpiredAgentRuntimeLockTx(
		ctx,
		txNotifications,
		reapTx,
		testProjectID,
		fixture.AgentID,
		lock.ID,
		executionstore.ModelCallRetryBackoff,
	)
	if err != nil {
		t.Fatalf("reap expired runtime in winning transaction: %v", err)
	}
	if !won {
		t.Fatal("winning reaper did not apply recovery")
	}

	reapCtx, cancelReap := context.WithTimeout(ctx, 2*time.Second)
	contending, contendErr := fixture.Store.Execution().ReapExpiredAgentRuntimeLocks(reapCtx, 100)
	cancelReap()
	if contendErr != nil || contending != 0 {
		t.Fatalf("contending reaper count=%d error=%v, want 0 and nil", contending, contendErr)
	}
	if err := fixture.Store.Execution().IntegrationCommitTxWithNotifications(
		ctx,
		reapTx,
		txNotifications,
		"commit winning reaper",
	); err != nil {
		t.Fatalf("commit winning reaper: %v", err)
	}
	var runtimeExists bool
	if err := fixture.Pool.QueryRow(
		ctx,
		`SELECT EXISTS (SELECT 1 FROM agent_runtime_locks WHERE id = $1)`,
		lock.ID,
	).Scan(&runtimeExists); err != nil {
		t.Fatalf("load runtime after concurrent reapers: %v", err)
	}
	if runtimeExists {
		t.Fatal("winning reaper did not delete runtime lock")
	}
}
