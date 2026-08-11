//go:build integration

package executionstore_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/pressly/goose/v3"
)

func TestMachineOnlineIntervalTrackingStartsAtMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDB(*pool.Config().ConnConfig.Copy())
	t.Cleanup(func() { _ = db.Close() })
	migrator, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		os.DirFS("../../../migrations"),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := migrator.UpTo(ctx, 11); err != nil {
		t.Fatalf("migrate through version 11: %v", err)
	}

	machineID := testID("machine-online-tracking-start")
	tokenID := testID("machine-online-tracking-start-token")
	runtimeID := testID("machine-online-tracking-start-runtime")
	mustExec := func(label, query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}
	mustExec("create existing org", `
INSERT INTO orgs(id, name, created_at, updated_at)
VALUES ($1, 'Machine online tracking start', statement_timestamp(), statement_timestamp())
`, testOrgID)
	mustExec("create existing machine", `
INSERT INTO machines(
    id, org_id, source_kind, display_name, provider, lifecycle_state,
    lifecycle_changed_at, env, secret_env, created_at, updated_at
) VALUES (
    $2, $1, 'byo', 'Machine online tracking start', 'daemon', 'active',
    statement_timestamp(), '{}'::jsonb, '{}'::jsonb,
    statement_timestamp(), statement_timestamp()
)
`, testOrgID, machineID)
	mustExec("create existing daemon token", `
INSERT INTO machine_daemon_tokens(
    id, org_id, machine_id, name, token_hash, created_at
) VALUES (
    $3, $1, $2, 'tracking-start daemon', 'machine-online-tracking-start-token-hash',
    statement_timestamp()
)
`, testOrgID, machineID, tokenID)
	mustExec("create existing daemon runtime", `
INSERT INTO daemon_runtimes(
    id, org_id, machine_id, daemon_token_id, daemon_instance_id,
    daemon_version, state, capacity, metadata, created_at, last_seen_at,
    lease_expires_at, updated_at
) VALUES (
    $4, $1, $2, $3, $5, '1.0.0', 'active', '{}'::jsonb, '{}'::jsonb,
    statement_timestamp() - interval '1 hour',
    statement_timestamp() - interval '1 hour',
    statement_timestamp() + interval '1 hour', statement_timestamp()
)
`, testOrgID, machineID, tokenID, runtimeID, testID("machine-online-tracking-start-instance"))
	mustExec(
		"select existing daemon runtime",
		`UPDATE machines SET current_daemon_runtime_id = $2 WHERE id = $1`,
		machineID,
		runtimeID,
	)

	var trackingStartedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&trackingStartedAt); err != nil {
		t.Fatalf("capture tracking start lower bound: %v", err)
	}
	if _, err := migrator.UpTo(ctx, 12); err != nil {
		t.Fatalf("migrate through version 12: %v", err)
	}
	var trackingFinishedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&trackingFinishedAt); err != nil {
		t.Fatalf("capture tracking start upper bound: %v", err)
	}

	var startedAt, confirmedThrough time.Time
	if err := pool.QueryRow(ctx, `
SELECT interval.started_at, fact.confirmed_through
FROM machine_online_intervals interval
JOIN machine_online_interval_facts fact ON fact.id = interval.id
WHERE interval.org_id = $1 AND interval.machine_id = $2 AND interval.daemon_runtime_id = $3
`, testOrgID, machineID, runtimeID).Scan(&startedAt, &confirmedThrough); err != nil {
		t.Fatalf("load initial online interval: %v", err)
	}
	if startedAt.Before(trackingStartedAt) || startedAt.After(trackingFinishedAt) {
		t.Fatalf(
			"initial interval started at %s, migration ran from %s through %s",
			startedAt,
			trackingStartedAt,
			trackingFinishedAt,
		)
	}
	if !confirmedThrough.Equal(startedAt) {
		t.Fatalf("initial interval confirmed through %s, want tracking start %s", confirmedThrough, startedAt)
	}
}

func TestMachineOnlineIntervalsTrackAndCapDaemonLeaseSessions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	mustCreateProjectOperatorUser(t, ctx, store, "machine-intervals@example.com", "Machine Intervals")
	machine, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          testOrgID,
			DisplayName:    "Machine Interval Test",
			IdempotencyKey: "machine-interval-test",
		},
	)
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}
	token, err := store.Execution().CreateBYOMachineDaemonToken(
		ctx,
		executionstore.CreateBYOMachineDaemonTokenInput{
			OrgID:     testOrgID,
			MachineID: machine.ID,
			Name:      "machine-interval-daemon",
			Token:     "machine-interval-token",
		},
	)
	if err != nil {
		t.Fatalf("create daemon token: %v", err)
	}
	daemonInstanceID := testID("machine-interval-daemon")
	runtime, err := store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            testOrgID,
			MachineID:        machine.ID,
			DaemonTokenID:    token.ID,
			DaemonInstanceID: daemonInstanceID,
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     time.Hour,
		},
	)
	if err != nil {
		t.Fatalf("register daemon runtime: %v", err)
	}

	intervals := loadMachineOnlineIntervals(t, ctx, pool, machine.ID)
	if len(intervals) != 1 || intervals[0].DaemonRuntimeID != runtime.ID ||
		intervals[0].StartedAt.After(runtime.LastSeenAt) || intervals[0].EndedAt != nil {
		t.Fatalf("initial online intervals = %+v", intervals)
	}

	_, err = store.Execution().HeartbeatDaemonRuntime(
		ctx,
		executionstore.DaemonRuntimeLeaseInput{
			Authority: executionstore.DaemonRuntimeAuthority{
				OrgID:           testOrgID,
				MachineID:       machine.ID,
				DaemonRuntimeID: runtime.ID,
				DaemonTokenID:   token.ID,
			},
			DaemonInstanceID: daemonInstanceID,
			LeaseTimeout:     time.Hour,
		},
	)
	if err != nil {
		t.Fatalf("heartbeat daemon runtime: %v", err)
	}
	intervals = loadMachineOnlineIntervals(t, ctx, pool, machine.ID)
	if len(intervals) != 1 || intervals[0].EndedAt != nil {
		t.Fatalf("ordinary heartbeat changed intervals: %+v", intervals)
	}
	refreshed, err := store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            testOrgID,
			MachineID:        machine.ID,
			DaemonTokenID:    token.ID,
			DaemonInstanceID: daemonInstanceID,
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     time.Hour,
		},
	)
	if err != nil {
		t.Fatalf("refresh daemon runtime registration: %v", err)
	}
	if refreshed.ID != runtime.ID || len(loadMachineOnlineIntervals(t, ctx, pool, machine.ID)) != 1 {
		t.Fatalf("registration refresh created another online interval: %+v", refreshed)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO machine_online_intervals(
    org_id, machine_id, daemon_runtime_id, started_at
) VALUES ($1, $2, $3, statement_timestamp())
`, testOrgID, machine.ID, runtime.ID); err == nil {
		t.Fatal("second open machine interval was accepted")
	}
	if _, err := pool.Exec(ctx, `
UPDATE machine_online_intervals
SET ended_at = started_at - interval '1 second', end_reason_code = 'invalid'
WHERE org_id = $1 AND machine_id = $2 AND ended_at IS NULL
`, testOrgID, machine.ID); err == nil {
		t.Fatal("online interval accepted an end before its start")
	}

	ended, err := store.Execution().EndDaemonRuntime(
		ctx,
		executionstore.DaemonRuntimeAuthority{
			OrgID:           testOrgID,
			MachineID:       machine.ID,
			DaemonRuntimeID: runtime.ID,
			DaemonTokenID:   token.ID,
		},
	)
	if err != nil {
		t.Fatalf("end daemon runtime: %v", err)
	}
	intervals = loadMachineOnlineIntervals(t, ctx, pool, machine.ID)
	if len(intervals) != 1 || intervals[0].EndedAt == nil || ended.EndedAt == nil ||
		!intervals[0].EndedAt.Equal(*ended.EndedAt) {
		t.Fatalf("closed online interval = %+v, runtime = %+v", intervals, ended)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE machine_online_intervals SET ended_at = ended_at + interval '1 second' WHERE id = $1`,
		intervals[0].ID,
	); err == nil {
		t.Fatal("closed online interval accepted a rewrite")
	}
	if _, err := pool.Exec(
		ctx,
		`DELETE FROM machine_online_intervals WHERE id = $1`,
		intervals[0].ID,
	); err == nil {
		t.Fatal("closed online interval accepted a delete")
	}

	var revivedLastSeen time.Time
	if err := pool.QueryRow(ctx, `
UPDATE daemon_runtimes
SET state = 'active',
    state_reason_code = NULL,
    state_reason_message = '',
    last_seen_at = statement_timestamp(),
    lease_expires_at = statement_timestamp() + interval '1 hour',
    ended_at = NULL,
    updated_at = statement_timestamp()
WHERE org_id = $1 AND machine_id = $2 AND id = $3
RETURNING last_seen_at
`, testOrgID, machine.ID, runtime.ID).Scan(&revivedLastSeen); err != nil {
		t.Fatalf("revive daemon runtime: %v", err)
	}
	intervals = loadMachineOnlineIntervals(t, ctx, pool, machine.ID)
	if len(intervals) != 2 || intervals[1].EndedAt != nil ||
		!intervals[1].StartedAt.Equal(revivedLastSeen) {
		t.Fatalf("revived online intervals = %+v", intervals)
	}
	expireDaemonRuntimeLeaseForTest(t, ctx, store, testOrgID, machine.ID, runtime.ID)
	var revivedLeaseExpires time.Time
	if err := pool.QueryRow(
		ctx,
		`SELECT lease_expires_at FROM daemon_runtimes WHERE id = $1`,
		runtime.ID,
	).Scan(&revivedLeaseExpires); err != nil {
		t.Fatalf("load expired revived daemon runtime lease: %v", err)
	}

	renewed, err := store.Execution().HeartbeatDaemonRuntime(
		ctx,
		executionstore.DaemonRuntimeLeaseInput{
			Authority: executionstore.DaemonRuntimeAuthority{
				OrgID:           testOrgID,
				MachineID:       machine.ID,
				DaemonRuntimeID: runtime.ID,
				DaemonTokenID:   token.ID,
			},
			DaemonInstanceID: daemonInstanceID,
			LeaseTimeout:     time.Hour,
		},
	)
	if err != nil {
		t.Fatalf("renew daemon runtime after lease lapse: %v", err)
	}
	renewedLastSeen, renewedLeaseExpires := renewed.LastSeenAt, renewed.LeaseExpiresAt
	intervals = loadMachineOnlineIntervals(t, ctx, pool, machine.ID)
	if len(intervals) != 3 || intervals[1].EndedAt == nil ||
		!intervals[1].EndedAt.Equal(revivedLeaseExpires) ||
		intervals[1].EndReason != "daemon_lease_expired" ||
		intervals[2].EndedAt != nil ||
		!intervals[2].StartedAt.Equal(renewedLastSeen) {
		t.Fatalf("post-lapse online intervals = %+v", intervals)
	}

	var effectiveEnd, observedThrough, confirmedThrough time.Time
	if err := pool.QueryRow(ctx, `
SELECT effective_end_at, observed_through, confirmed_through
FROM machine_online_interval_facts
WHERE id = $1
`, intervals[2].ID).Scan(&effectiveEnd, &observedThrough, &confirmedThrough); err != nil {
		t.Fatalf("load capped online interval fact: %v", err)
	}
	if !effectiveEnd.Equal(renewedLeaseExpires) || observedThrough.After(effectiveEnd) ||
		observedThrough.Before(intervals[2].StartedAt) ||
		!confirmedThrough.Equal(renewedLastSeen) {
		t.Fatalf(
			"interval fact = effective end %s observed through %s confirmed through %s; interval = %+v",
			effectiveEnd,
			observedThrough,
			confirmedThrough,
			intervals[2],
		)
	}

	replacement, err := store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            testOrgID,
			MachineID:        machine.ID,
			DaemonTokenID:    token.ID,
			DaemonInstanceID: testID("machine-interval-replacement-daemon"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     time.Hour,
		},
	)
	if err != nil {
		t.Fatalf("register replacement daemon runtime: %v", err)
	}
	intervals = loadMachineOnlineIntervals(t, ctx, pool, machine.ID)
	if len(intervals) != 4 || intervals[2].EndedAt == nil ||
		intervals[2].EndReason != "daemon_runtime_replaced" ||
		intervals[3].DaemonRuntimeID != replacement.ID || intervals[3].EndedAt != nil {
		t.Fatalf("replacement daemon online intervals = %+v", intervals)
	}
	if err := pool.QueryRow(ctx, `
SELECT confirmed_through
FROM machine_online_interval_facts
WHERE id = $1
`, intervals[2].ID).Scan(&confirmedThrough); err != nil {
		t.Fatalf("load closed online interval confirmation: %v", err)
	}
	if !confirmedThrough.Equal(*intervals[2].EndedAt) {
		t.Fatalf(
			"closed interval confirmed through %s, want %s",
			confirmedThrough,
			*intervals[2].EndedAt,
		)
	}
	if _, err := store.Execution().DeleteMachine(
		ctx,
		executionstore.DeleteMachineInput{OrgID: testOrgID, MachineID: machine.ID},
	); err != nil {
		t.Fatalf("delete machine with active online interval: %v", err)
	}
	intervals = loadMachineOnlineIntervals(t, ctx, pool, machine.ID)
	if len(intervals) != 4 || intervals[3].EndedAt == nil ||
		intervals[3].EndReason != "machine_deleted" {
		t.Fatalf("deleted-machine online intervals = %+v", intervals)
	}
}

type machineOnlineIntervalTestRecord struct {
	ID              ID
	DaemonRuntimeID ID
	StartedAt       time.Time
	EndedAt         *time.Time
	EndReason       string
}

func loadMachineOnlineIntervals(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	machineID ID,
) []machineOnlineIntervalTestRecord {
	t.Helper()
	rows, err := pool.Query(ctx, `
SELECT id, daemon_runtime_id, started_at, ended_at, coalesce(end_reason_code, '')
FROM machine_online_intervals
WHERE org_id = $1 AND machine_id = $2
ORDER BY started_at, id
`, testOrgID, machineID)
	if err != nil {
		t.Fatalf("query machine online intervals: %v", err)
	}
	defer rows.Close()
	var intervals []machineOnlineIntervalTestRecord
	for rows.Next() {
		var interval machineOnlineIntervalTestRecord
		if err := rows.Scan(
			&interval.ID,
			&interval.DaemonRuntimeID,
			&interval.StartedAt,
			&interval.EndedAt,
			&interval.EndReason,
		); err != nil {
			t.Fatalf("scan machine online interval: %v", err)
		}
		intervals = append(intervals, interval)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate machine online intervals: %v", err)
	}
	return intervals
}
