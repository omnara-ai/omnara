//go:build integration

package executionstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestMachineOnlineIntervalsClampRegressedLeaseBounds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	machineID := testID("machine-online-regressed-lease")
	tokenID := testID("machine-online-regressed-lease-token")
	runtimeID := testID("machine-online-regressed-lease-runtime")
	if _, err := pool.Exec(ctx, `
INSERT INTO machines(
    id, org_id, source_kind, display_name, provider, lifecycle_state,
    lifecycle_changed_at, env, secret_env, created_at, updated_at
) VALUES (
    $2, $1, 'byo', 'Machine online regressed lease', 'daemon', 'active',
    statement_timestamp(), '{}'::jsonb, '{}'::jsonb,
    statement_timestamp(), statement_timestamp()
)
`, testOrgID, machineID); err != nil {
		t.Fatalf("create regressed-lease machine: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO machine_daemon_tokens(
    id, org_id, machine_id, name, token_hash, created_at
) VALUES ($3, $1, $2, 'daemon', 'regressed-lease-token-hash', statement_timestamp())
`, testOrgID, machineID, tokenID); err != nil {
		t.Fatalf("create regressed-lease daemon token: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO daemon_runtimes(
    id, org_id, machine_id, daemon_token_id, daemon_instance_id,
    daemon_version, state, capacity, metadata, created_at, last_seen_at,
    lease_expires_at, updated_at
) VALUES (
    $4, $1, $2, $3, $5, '1.0.0', 'active', '{}'::jsonb, '{}'::jsonb,
    statement_timestamp(), statement_timestamp(),
    statement_timestamp() + interval '1 hour', statement_timestamp()
)
`, testOrgID, machineID, tokenID, runtimeID, testID("machine-online-regressed-lease-instance")); err != nil {
		t.Fatalf("create regressed-lease daemon runtime: %v", err)
	}

	intervals := loadMachineOnlineIntervals(t, ctx, pool, machineID)
	if len(intervals) != 1 {
		t.Fatalf("initial regressed-lease intervals = %+v, want one", intervals)
	}
	firstStartedAt := intervals[0].StartedAt
	if _, err := pool.Exec(ctx, `
UPDATE daemon_runtimes
SET last_seen_at = $2::timestamptz - interval '2 seconds',
    lease_expires_at = $2::timestamptz - interval '1 second',
    updated_at = statement_timestamp()
WHERE id = $1
`, runtimeID, firstStartedAt); err != nil {
		t.Fatalf("regress active daemon lease: %v", err)
	}
	var effectiveEnd, observedThrough time.Time
	if err := pool.QueryRow(ctx, `
SELECT effective_end_at, observed_through
FROM machine_online_interval_facts
WHERE id = $1
`, intervals[0].ID).Scan(&effectiveEnd, &observedThrough); err != nil {
		t.Fatalf("load regressed online interval fact: %v", err)
	}
	if !effectiveEnd.Equal(firstStartedAt) || !observedThrough.Equal(firstStartedAt) {
		t.Fatalf(
			"regressed interval bounds = effective %s observed %s, want session start %s",
			effectiveEnd,
			observedThrough,
			firstStartedAt,
		)
	}

	var renewedLastSeen time.Time
	if err := pool.QueryRow(ctx, `
UPDATE daemon_runtimes
SET last_seen_at = statement_timestamp(),
    lease_expires_at = statement_timestamp() + interval '1 hour',
    updated_at = statement_timestamp()
WHERE id = $1
RETURNING last_seen_at
`, runtimeID).Scan(&renewedLastSeen); err != nil {
		t.Fatalf("renew regressed daemon lease: %v", err)
	}
	intervals = loadMachineOnlineIntervals(t, ctx, pool, machineID)
	if len(intervals) != 2 || intervals[0].EndedAt == nil ||
		!intervals[0].EndedAt.Equal(firstStartedAt) ||
		intervals[0].EndReason != "daemon_lease_expired" ||
		intervals[1].EndedAt != nil || !intervals[1].StartedAt.Equal(renewedLastSeen) {
		t.Fatalf("renewed regressed-lease intervals = %+v", intervals)
	}

	secondStartedAt := intervals[1].StartedAt
	if _, err := pool.Exec(ctx, `
UPDATE daemon_runtimes
SET last_seen_at = $2::timestamptz - interval '2 seconds',
    lease_expires_at = $2::timestamptz - interval '1 second',
    updated_at = statement_timestamp()
WHERE id = $1
`, runtimeID, secondStartedAt); err != nil {
		t.Fatalf("regress daemon lease before ending: %v", err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE daemon_runtimes
SET state = 'ended',
    state_reason_code = 'daemon_stopped',
    state_reason_message = 'test lease regression',
    ended_at = statement_timestamp(),
    updated_at = statement_timestamp()
WHERE id = $1
`, runtimeID); err != nil {
		t.Fatalf("end daemon runtime with regressed lease: %v", err)
	}
	intervals = loadMachineOnlineIntervals(t, ctx, pool, machineID)
	if len(intervals) != 2 || intervals[1].EndedAt == nil ||
		!intervals[1].EndedAt.Equal(secondStartedAt) ||
		intervals[1].EndReason != "daemon_stopped" {
		t.Fatalf("ended regressed-lease intervals = %+v", intervals)
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

	var effectiveEnd, observedThrough time.Time
	if err := pool.QueryRow(ctx, `
SELECT effective_end_at, observed_through
FROM machine_online_interval_facts
WHERE id = $1
`, intervals[2].ID).Scan(&effectiveEnd, &observedThrough); err != nil {
		t.Fatalf("load capped online interval fact: %v", err)
	}
	if !effectiveEnd.Equal(renewedLeaseExpires) || observedThrough.After(effectiveEnd) ||
		observedThrough.Before(intervals[2].StartedAt) {
		t.Fatalf(
			"interval fact = effective end %s observed through %s; interval = %+v",
			effectiveEnd,
			observedThrough,
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

func TestDaemonLeaseRefreshesPreserveCommittedConfirmationTime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	mustCreateProjectOperatorUser(
		t,
		ctx,
		store,
		"daemon-confirmation-time@example.com",
		"Daemon Confirmation Time",
	)
	machine, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          testOrgID,
			DisplayName:    "Daemon confirmation time",
			IdempotencyKey: "daemon-confirmation-time",
		},
	)
	if err != nil {
		t.Fatalf("create daemon machine: %v", err)
	}
	token, err := store.Execution().CreateBYOMachineDaemonToken(
		ctx,
		executionstore.CreateBYOMachineDaemonTokenInput{
			OrgID:     testOrgID,
			MachineID: machine.ID,
			Name:      "daemon",
			Token:     "daemon-confirmation-time-token",
		},
	)
	if err != nil {
		t.Fatalf("create daemon token: %v", err)
	}
	daemonInstanceID := testID("daemon-confirmation-time-instance")
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

	var committedLastSeen, committedLeaseExpires, committedUpdatedAt time.Time
	if err := pool.QueryRow(ctx, `
UPDATE daemon_runtimes
SET last_seen_at = statement_timestamp() + interval '5 minutes',
    lease_expires_at = statement_timestamp() + interval '10 minutes',
    updated_at = statement_timestamp() + interval '5 minutes'
WHERE org_id = $1 AND machine_id = $2 AND id = $3
RETURNING last_seen_at, lease_expires_at, updated_at
`, testOrgID, machine.ID, runtime.ID).Scan(
		&committedLastSeen,
		&committedLeaseExpires,
		&committedUpdatedAt,
	); err != nil {
		t.Fatalf("seed committed daemon times: %v", err)
	}
	authority := executionstore.DaemonRuntimeAuthority{
		OrgID:           testOrgID,
		MachineID:       machine.ID,
		DaemonRuntimeID: runtime.ID,
		DaemonTokenID:   token.ID,
	}
	heartbeat, err := store.Execution().HeartbeatDaemonRuntime(
		ctx,
		executionstore.DaemonRuntimeLeaseInput{
			Authority:        authority,
			DaemonInstanceID: daemonInstanceID,
			LeaseTimeout:     time.Minute,
		},
	)
	if err != nil {
		t.Fatalf("heartbeat daemon runtime: %v", err)
	}
	if !heartbeat.LastSeenAt.Equal(committedLastSeen) ||
		!heartbeat.LeaseExpiresAt.Equal(committedLeaseExpires) ||
		!heartbeat.UpdatedAt.Equal(committedUpdatedAt) {
		t.Fatalf("heartbeat regressed committed daemon times: %+v", heartbeat)
	}
	refreshed, err := store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            testOrgID,
			MachineID:        machine.ID,
			DaemonTokenID:    token.ID,
			DaemonInstanceID: daemonInstanceID,
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     time.Minute,
		},
	)
	if err != nil {
		t.Fatalf("refresh daemon runtime registration: %v", err)
	}
	if !refreshed.LastSeenAt.Equal(committedLastSeen) ||
		!refreshed.LeaseExpiresAt.Equal(committedLeaseExpires) ||
		!refreshed.UpdatedAt.Equal(committedUpdatedAt) {
		t.Fatalf("registration refresh regressed committed daemon times: %+v", refreshed)
	}

	var openConfirmedThrough time.Time
	if err := pool.QueryRow(ctx, `
SELECT confirmed_through
FROM machine_online_interval_facts
WHERE org_id = $1 AND machine_id = $2 AND daemon_runtime_id = $3 AND ended_at IS NULL
`, testOrgID, machine.ID, runtime.ID).Scan(&openConfirmedThrough); err != nil {
		t.Fatalf("load open interval confirmation: %v", err)
	}
	if !openConfirmedThrough.Equal(committedLastSeen) {
		t.Fatalf(
			"open interval confirmed through %s, want %s",
			openConfirmedThrough,
			committedLastSeen,
		)
	}
	if _, err := store.Execution().EndDaemonRuntime(ctx, authority); err != nil {
		t.Fatalf("end daemon runtime: %v", err)
	}
	var intervalEndedAt, closedConfirmedThrough time.Time
	if err := pool.QueryRow(ctx, `
SELECT ended_at, confirmed_through
FROM machine_online_interval_facts
WHERE org_id = $1 AND machine_id = $2 AND daemon_runtime_id = $3
`, testOrgID, machine.ID, runtime.ID).Scan(&intervalEndedAt, &closedConfirmedThrough); err != nil {
		t.Fatalf("load closed interval confirmation: %v", err)
	}
	if intervalEndedAt.Before(committedLastSeen) || !closedConfirmedThrough.Equal(intervalEndedAt) {
		t.Fatalf(
			"closed interval ended at %s and confirmed through %s, want no earlier than %s",
			intervalEndedAt,
			closedConfirmedThrough,
			committedLastSeen,
		)
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
