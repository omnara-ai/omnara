package integrationdb

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	lockWaitTimeout      = 5 * time.Second
	lockWaitPollInterval = 10 * time.Millisecond
)

func WaitForLockWaiters(
	t testing.TB,
	ctx context.Context,
	pool *pgxpool.Pool,
	queryFragment string,
	minimum int,
) {
	t.Helper()
	waitForLockCondition(t, ctx, "lock waiters matching "+queryFragment, func(ctx context.Context) (bool, error) {
		var waiters int
		err := pool.QueryRow(ctx, `
SELECT count(*)::integer
FROM pg_stat_activity
WHERE datname = current_database()
  AND wait_event_type = 'Lock'
  AND query ILIKE '%' || $1 || '%'
`, queryFragment).Scan(&waiters)
		return waiters >= minimum, err
	})
}

func WaitForNamedLockWaiters(
	t testing.TB,
	ctx context.Context,
	pool *pgxpool.Pool,
	queryName string,
	minimum int,
) {
	t.Helper()
	WaitForLockWaiters(t, ctx, pool, "-- name: "+queryName+" ", minimum)
}

func WaitForLockWaitBlockedBy(
	t testing.TB,
	ctx context.Context,
	pool *pgxpool.Pool,
	queryFragment string,
	blockingPID int32,
) time.Time {
	t.Helper()
	var transactionStartedAt time.Time
	waitForLockCondition(t, ctx, "lock wait matching "+queryFragment, func(ctx context.Context) (bool, error) {
		err := pool.QueryRow(ctx, `
SELECT xact_start
FROM pg_stat_activity
WHERE datname = current_database()
  AND wait_event_type = 'Lock'
  AND query ILIKE '%' || $1 || '%'
  AND $2::integer = ANY(pg_blocking_pids(pid))
ORDER BY pid
LIMIT 1
`, queryFragment, blockingPID).Scan(&transactionStartedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return err == nil, err
	})
	return transactionStartedAt
}

func WaitForNamedLockWaitersBlockedByChain(
	t testing.TB,
	ctx context.Context,
	pool *pgxpool.Pool,
	queryName string,
	blockingPID int32,
	minimum int,
) {
	t.Helper()
	waitForLockCondition(t, ctx, "chained lock waiters on "+queryName, func(ctx context.Context) (bool, error) {
		var waiters int
		err := pool.QueryRow(ctx, `
WITH RECURSIVE blocking_chain(waiter_pid, blocker_pid) AS (
  SELECT activity.pid, unnest(pg_blocking_pids(activity.pid))
  FROM pg_stat_activity activity
  WHERE activity.datname = current_database()
  UNION
  SELECT chain.waiter_pid, unnest(pg_blocking_pids(blocker.pid))
  FROM blocking_chain chain
  JOIN pg_stat_activity blocker ON blocker.pid = chain.blocker_pid
)
SELECT count(DISTINCT activity.pid)::integer
FROM pg_stat_activity activity
JOIN blocking_chain chain ON chain.waiter_pid = activity.pid
WHERE activity.datname = current_database()
  AND activity.wait_event_type = 'Lock'
  AND chain.blocker_pid = $1
  AND activity.query ILIKE '%' || $2 || '%'
`, blockingPID, "-- name: "+queryName+" ").Scan(&waiters)
		return waiters >= minimum, err
	})
}

func WaitForApplicationLockWaiter(
	t testing.TB,
	ctx context.Context,
	pool *pgxpool.Pool,
	applicationName string,
) {
	t.Helper()
	waitForLockCondition(t, ctx, "lock waiter for application "+applicationName, func(ctx context.Context) (bool, error) {
		var waiting bool
		err := pool.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM pg_stat_activity
  WHERE datname = current_database()
    AND application_name = $1
    AND wait_event_type = 'Lock'
)`, applicationName).Scan(&waiting)
		return waiting, err
	})
}

func WaitForApplicationNamedLockWaiters(
	t testing.TB,
	ctx context.Context,
	pool *pgxpool.Pool,
	applicationName, queryName string,
	minimum int,
) {
	t.Helper()
	description := "lock waiters on " + queryName + " for application " + applicationName
	waitForLockCondition(t, ctx, description, func(ctx context.Context) (bool, error) {
		var waiters int
		err := pool.QueryRow(ctx, `
SELECT count(*)::integer
FROM pg_stat_activity
WHERE datname = current_database()
  AND application_name = $1
  AND wait_event_type = 'Lock'
  AND query ILIKE '%' || $2 || '%'
`, applicationName, "-- name: "+queryName+" ").Scan(&waiters)
		return waiters >= minimum, err
	})
}

func WaitForGrantedAdvisoryLock(
	t testing.TB,
	ctx context.Context,
	pool *pgxpool.Pool,
	classID, objectID int32,
) {
	t.Helper()
	waitForLockCondition(t, ctx, "granted advisory lock", func(ctx context.Context) (bool, error) {
		var locked bool
		err := pool.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM pg_locks
  WHERE locktype = 'advisory'
    AND classid = $1
    AND objid = $2
    AND granted
)`, classID, objectID).Scan(&locked)
		return locked, err
	})
}

func waitForLockCondition(
	t testing.TB,
	ctx context.Context,
	description string,
	check func(context.Context) (bool, error),
) {
	t.Helper()
	deadline := time.Now().Add(lockWaitTimeout)
	ticker := time.NewTicker(lockWaitPollInterval)
	defer ticker.Stop()
	for {
		satisfied, err := check(ctx)
		if err != nil {
			t.Fatalf("wait for %s: %v", description, err)
		}
		if satisfied {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context ended waiting for %s: %v", description, ctx.Err())
		case <-ticker.C:
		}
	}
}
