//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
)

func TestProviderRuntimeCandidateStorageLifecycleAndPagination(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProviderRuntimeStorageFixture(t, ctx, "candidate-lifecycle", true)
	first := fixture.insertInactiveMachine(t, ctx, "first")
	second := fixture.insertInactiveMachine(t, ctx, "second")

	fixture.insertNeverConnectedMachine(t, ctx, "never-connected")
	fixture.insertInactiveBYOMachine(t, ctx, "byo")
	online := fixture.insertInactiveMachine(t, ctx, "online")
	onlineDaemonInstanceID := uuid.New()
	onlineRegistration, err := fixture.store.Execution().RegisterDaemonRuntimeWithReconciliation(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            testOrgID,
			MachineID:        online.machineID,
			DaemonTokenID:    online.tokenID,
			DaemonInstanceID: onlineDaemonInstanceID,
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     time.Minute,
		},
	)
	if err != nil {
		t.Fatalf("make excluded machine online: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `
UPDATE machines
SET provider_runtime_mismatch_since = statement_timestamp()
WHERE org_id = $1 AND id = $2
`, testOrgID, online.machineID); err != nil {
		t.Fatalf("seed stale provider runtime mismatch before heartbeat: %v", err)
	}
	if _, err := fixture.store.Execution().HeartbeatDaemonRuntime(
		ctx,
		executionstore.DaemonRuntimeLeaseInput{
			Authority: executionstore.DaemonRuntimeAuthority{
				OrgID:           testOrgID,
				MachineID:       online.machineID,
				DaemonRuntimeID: onlineRegistration.Runtime.ID,
				DaemonTokenID:   online.tokenID,
			},
			DaemonInstanceID: onlineDaemonInstanceID,
			LeaseTimeout:     time.Minute,
		},
	); err != nil {
		t.Fatalf("heartbeat online machine with stale provider runtime mismatch: %v", err)
	}
	var onlineMismatchSince *time.Time
	if err := fixture.pool.QueryRow(
		ctx,
		`SELECT provider_runtime_mismatch_since FROM machines WHERE org_id = $1 AND id = $2`,
		testOrgID,
		online.machineID,
	).Scan(&onlineMismatchSince); err != nil {
		t.Fatalf("load provider runtime mismatch after heartbeat: %v", err)
	}
	if onlineMismatchSince != nil {
		t.Fatalf("heartbeat did not clear provider runtime mismatch: %s", *onlineMismatchSince)
	}
	transitional := fixture.insertInactiveMachine(t, ctx, "transitional")
	if _, err := fixture.pool.Exec(ctx, `
UPDATE machines
SET lifecycle_state = 'provisioning',
    lifecycle_changed_at = statement_timestamp(),
    next_reconcile_after = statement_timestamp(),
    updated_at = statement_timestamp()
WHERE org_id = $1 AND id = $2
`, testOrgID, transitional.machineID); err != nil {
		t.Fatalf("make excluded machine transitional: %v", err)
	}
	unprotected := fixture
	unprotected.machinePool = createProviderRuntimeMachinePoolForTest(
		t,
		ctx,
		fixture.store,
		fixture.secretID,
		"candidate-disabled",
		false,
	)
	unprotected.insertInactiveMachine(t, ctx, "disabled")

	var candidates []executionstore.ProviderRuntimeCandidate
	cursor := executionstore.ListProviderRuntimeCandidatesInput{Limit: 1}
	for {
		page, err := fixture.store.Execution().ListProviderRuntimeDiscoveryCandidates(ctx, cursor)
		if err != nil {
			t.Fatalf("list provider runtime candidates: %v", err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) != 1 {
			t.Fatalf("candidate page size = %d, want 1", len(page))
		}
		candidates = append(candidates, page[0])
		cursor.AfterMachineID = page[0].MachineID
	}
	if len(candidates) != 2 {
		t.Fatalf("discovery candidates = %+v, want two eligible machines", candidates)
	}
	wantMachines := map[ID]bool{first.machineID: true, second.machineID: true}
	for _, candidate := range candidates {
		if !wantMachines[candidate.MachineID] {
			t.Fatalf("unexpected discovery candidate: %+v", candidate)
		}
		delete(wantMachines, candidate.MachineID)
		if candidate.ScopeKey == "" || candidate.ProviderAuthVersionID == NilID ||
			candidate.ProviderAuthSecretID != fixture.secretID {
			t.Fatalf("candidate scope identity = %+v", candidate)
		}
		if strings.Contains(string(candidate.ScopeKey), fixture.providerToken) {
			t.Fatal("provider runtime scope key contains credential material")
		}
	}
	if len(wantMachines) != 0 || candidates[0].ScopeKey != candidates[1].ScopeKey {
		t.Fatalf("candidate pagination lost machines or split one scope: %+v", candidates)
	}

	candidate := candidates[0]
	marked, err := fixture.store.Execution().MarkProviderRuntimeMismatch(ctx, candidate)
	if err != nil || !marked {
		t.Fatalf("mark provider runtime mismatch = (%t, %v)", marked, err)
	}
	if marked, err := fixture.store.Execution().MarkProviderRuntimeMismatch(ctx, candidate); err != nil || marked {
		t.Fatalf("repeat mismatch mark = (%t, %v), want idempotent no-op", marked, err)
	}
	if due := listDueProviderRuntimeCandidatesForTest(
		t,
		ctx,
		fixture.store,
		10*time.Minute,
	); len(due) != 0 {
		t.Fatalf("not-yet-due mismatch candidates = %+v", due)
	}
	secondCandidate := candidates[1]
	if secondCandidate.MachineID == candidate.MachineID {
		t.Fatal("candidate pagination returned the same machine twice")
	}
	if marked, err := fixture.store.Execution().MarkProviderRuntimeMismatch(
		ctx,
		secondCandidate,
	); err != nil || !marked {
		t.Fatalf("mark second provider runtime mismatch = (%t, %v)", marked, err)
	}
	fixture.backdateMismatch(t, ctx, candidate.MachineID)
	fixture.backdateMismatch(t, ctx, secondCandidate.MachineID)
	var due []executionstore.ProviderRuntimeCandidate
	dueCursor := executionstore.ListDueProviderRuntimeMismatchesInput{
		Limit:             1,
		ConfirmationGrace: time.Millisecond,
		InactivityGrace:   time.Millisecond,
	}
	for {
		page, err := fixture.store.Execution().ListDueProviderRuntimeMismatches(ctx, dueCursor)
		if err != nil {
			t.Fatalf("page due provider runtime mismatches: %v", err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) != 1 || page[0].ProviderRuntimeMismatchSince == nil {
			t.Fatalf("due mismatch page = %+v, want one complete candidate", page)
		}
		due = append(due, page[0])
		dueCursor.After = executionstore.ProviderRuntimeMismatchCursor{
			MismatchSince: *page[0].ProviderRuntimeMismatchSince,
			MachineID:     page[0].MachineID,
		}
	}
	if len(due) != 2 {
		t.Fatalf("due mismatch candidates = %+v", due)
	}
	result, err := fixture.store.Execution().ApplyProviderRuntimeInactiveObservation(
		ctx,
		due[0],
	)
	if err != nil || !result.Applied || result.WakeAttemptCleared {
		t.Fatalf("clear mismatch = (%+v, %v)", result, err)
	}
	if result, err := fixture.store.Execution().ApplyProviderRuntimeInactiveObservation(
		ctx,
		due[0],
	); err != nil || result.Applied {
		t.Fatalf("repeat mismatch clear = (%+v, %v), want fenced no-op", result, err)
	}
	if _, err := fixture.pool.Exec(ctx, `
UPDATE machines
SET asleep_since = asleep_since + interval '1 second',
    updated_at = statement_timestamp()
WHERE org_id = $1 AND id = $2
`, testOrgID, due[1].MachineID); err != nil {
		t.Fatalf("advance machine inactivity epoch: %v", err)
	}
	if result, err := fixture.store.Execution().ApplyProviderRuntimeInactiveObservation(
		ctx,
		due[1],
	); err != nil || result.Applied {
		t.Fatalf("stale inactive observation = (%+v, %v), want fenced no-op", result, err)
	}
}

func TestProviderRuntimeCandidatesDeriveCrashedDaemonInactivity(t *testing.T) {
	for _, test := range []struct {
		name          string
		activateEnded bool
	}{
		{name: "expired active runtime", activateEnded: true},
		{name: "ended runtime"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture := newProviderRuntimeStorageFixture(t, ctx, "crashed-"+test.name, true)
			machine := fixture.insertInactiveMachine(t, ctx, "crashed-"+test.name)
			if test.activateEnded {
				if _, err := fixture.pool.Exec(ctx, `
UPDATE daemon_runtimes
SET state = 'active',
    state_reason_code = NULL,
    ended_at = NULL,
    updated_at = statement_timestamp()
WHERE org_id = $1 AND machine_id = $2 AND id = $3
`, testOrgID, machine.machineID, machine.runtimeID); err != nil {
					t.Fatalf("restore expired daemon runtime to active: %v", err)
				}
			}
			if _, err := fixture.pool.Exec(ctx, `
UPDATE machines
SET asleep_since = NULL,
    updated_at = statement_timestamp()
WHERE org_id = $1 AND id = $2
`, testOrgID, machine.machineID); err != nil {
				t.Fatalf("clear daemon-reported sleep state: %v", err)
			}
			var expectedInactiveSince time.Time
			if err := fixture.pool.QueryRow(ctx, `
SELECT runtime.effective_end_at
FROM daemon_runtime_connection_facts runtime
WHERE runtime.org_id = $1 AND runtime.machine_id = $2 AND runtime.id = $3
`, testOrgID, machine.machineID, machine.runtimeID).Scan(&expectedInactiveSince); err != nil {
				t.Fatalf("load expected inactive timestamp: %v", err)
			}
			candidate := fixture.discoveryCandidate(t, ctx, machine.machineID)
			if !candidate.InactiveSince.Equal(expectedInactiveSince) {
				t.Fatalf("candidate inactive_since = %s, want %s", candidate.InactiveSince, expectedInactiveSince)
			}
			candidate = fixture.dueCandidate(t, ctx, machine.machineID)
			if _, claimed, err := fixture.store.Execution().ClaimProviderRuntimeMismatchDeletion(
				ctx,
				providerRuntimeClaimInput(candidate),
			); err != nil || !claimed {
				t.Fatalf("claim crashed runtime deletion = (%t, %v), want true/nil", claimed, err)
			}
			var state string
			var endedAt *time.Time
			if err := fixture.pool.QueryRow(ctx, `
SELECT state, ended_at
FROM daemon_runtimes
WHERE org_id = $1 AND machine_id = $2 AND id = $3
`, testOrgID, machine.machineID, machine.runtimeID).Scan(&state, &endedAt); err != nil {
				t.Fatalf("load claimed daemon runtime: %v", err)
			}
			if state != "ended" || endedAt == nil {
				t.Fatalf("claimed daemon runtime = %s/%v, want ended/non-null", state, endedAt)
			}
		})
	}
}

func TestProviderRuntimeMismatchGraceStartsAfterLifecycleLock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProviderRuntimeStorageFixture(t, ctx, "mismatch-lock-time", true)
	machine := fixture.insertInactiveMachine(t, ctx, "mismatch-lock-time")
	candidate := fixture.discoveryCandidate(t, ctx, machine.machineID)

	blockingTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin provider runtime mismatch blocker: %v", err)
	}
	defer func() { _ = blockingTx.Rollback(ctx) }()
	var blockingPID int32
	if err := blockingTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockingPID); err != nil {
		t.Fatalf("get provider runtime mismatch blocker backend: %v", err)
	}
	if _, err := blockingTx.Exec(ctx, `
SELECT id
FROM machines
WHERE org_id = $1 AND id = $2
FOR UPDATE
`, testOrgID, machine.machineID); err != nil {
		t.Fatalf("lock machine before provider runtime mismatch mark: %v", err)
	}

	type markResult struct {
		marked bool
		err    error
	}
	done := make(chan markResult, 1)
	go func() {
		marked, err := fixture.store.Execution().MarkProviderRuntimeMismatch(
			context.Background(),
			candidate,
		)
		done <- markResult{marked: marked, err: err}
	}()
	waitForDatabaseLockWait(
		t,
		ctx,
		fixture.pool,
		"-- name: LockMachineForLifecycle",
		blockingPID,
	)
	if _, err := blockingTx.Exec(ctx, `SELECT pg_sleep(0.2)`); err != nil {
		t.Fatalf("hold provider runtime mismatch lifecycle lock: %v", err)
	}
	var lockReleasedAfter time.Time
	if err := fixture.pool.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&lockReleasedAfter); err != nil {
		t.Fatalf("capture provider runtime mismatch lock boundary: %v", err)
	}
	if err := blockingTx.Commit(ctx); err != nil {
		t.Fatalf("release provider runtime mismatch lifecycle lock: %v", err)
	}
	result := <-done
	if result.err != nil || !result.marked {
		t.Fatalf("mark provider runtime mismatch after lock wait = (%t, %v)", result.marked, result.err)
	}

	var mismatchSince time.Time
	if err := fixture.pool.QueryRow(ctx, `
SELECT provider_runtime_mismatch_since
FROM machines
WHERE org_id = $1 AND id = $2
`, testOrgID, machine.machineID).Scan(&mismatchSince); err != nil {
		t.Fatalf("load provider runtime mismatch timestamp: %v", err)
	}
	if mismatchSince.Before(lockReleasedAfter) {
		t.Fatalf(
			"provider runtime mismatch started at %s before lifecycle lock release boundary %s",
			mismatchSince,
			lockReleasedAfter,
		)
	}
}

func TestMachineWakeIntentFencesRuntimeProtectionDeletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProviderRuntimeStorageFixture(t, ctx, "wake-intent", true)
	machine := fixture.insertInactiveMachine(t, ctx, "wake-intent")

	disposition, err := fixture.store.Execution().BeginMachineWake(
		ctx,
		testOrgID,
		machine.machineID,
		fixture.machinePool.ID,
		time.Minute,
	)
	if err != nil || disposition != executionstore.MachineWakeReady {
		t.Fatalf("begin protected machine wake = (%v, %v), want ready", disposition, err)
	}
	var firstWakeExpiresAt *time.Time
	if err := fixture.pool.QueryRow(
		ctx,
		`SELECT wake_attempt_expires_at FROM machines WHERE org_id = $1 AND id = $2`,
		testOrgID,
		machine.machineID,
	).Scan(&firstWakeExpiresAt); err != nil {
		t.Fatalf("load machine wake intent: %v", err)
	}
	if firstWakeExpiresAt == nil {
		t.Fatal("protected machine wake did not persist intent")
	}

	disposition, err = fixture.store.Execution().BeginMachineWake(
		ctx,
		testOrgID,
		machine.machineID,
		fixture.machinePool.ID,
		time.Minute,
	)
	if err != nil || disposition != executionstore.MachineWakePending {
		t.Fatalf("repeat protected machine wake = (%v, %v), want pending", disposition, err)
	}
	var repeatedWakeExpiresAt *time.Time
	if err := fixture.pool.QueryRow(
		ctx,
		`SELECT wake_attempt_expires_at FROM machines WHERE org_id = $1 AND id = $2`,
		testOrgID,
		machine.machineID,
	).Scan(&repeatedWakeExpiresAt); err != nil {
		t.Fatalf("reload machine wake intent: %v", err)
	}
	if repeatedWakeExpiresAt == nil || !repeatedWakeExpiresAt.Equal(*firstWakeExpiresAt) {
		t.Fatalf("repeat wake changed deadline from %v to %v", firstWakeExpiresAt, repeatedWakeExpiresAt)
	}

	candidate := fixture.discoveryCandidate(t, ctx, machine.machineID)
	if marked, err := fixture.store.Execution().MarkProviderRuntimeMismatch(ctx, candidate); err != nil || !marked {
		t.Fatalf("mark provider runtime mismatch = (%t, %v), want true/nil", marked, err)
	}
	fixture.backdateMismatch(t, ctx, machine.machineID)
	candidate = fixture.discoveryCandidate(t, ctx, machine.machineID)
	for _, due := range listDueProviderRuntimeCandidatesForTest(t, ctx, fixture.store, time.Millisecond) {
		if due.MachineID == machine.machineID {
			t.Fatal("fresh wake appeared in due provider runtime mismatches")
		}
	}
	if _, claimed, err := fixture.store.Execution().ClaimProviderRuntimeMismatchDeletion(
		ctx,
		executionstore.ClaimProviderRuntimeMismatchDeletionInput{
			Candidate:         candidate,
			ConfirmationGrace: time.Millisecond,
			InactivityGrace:   time.Millisecond,
		},
	); err != nil || claimed {
		t.Fatalf("deletion during fresh wake = (%t, %v), want false/nil", claimed, err)
	}

	candidate = fixture.discoveryCandidate(t, ctx, machine.machineID)
	result, err := fixture.store.Execution().ApplyProviderRuntimeInactiveObservation(
		ctx,
		candidate,
	)
	if err != nil || !result.Applied || result.WakeAttemptCleared {
		t.Fatalf("apply inactive observation during wake = (%+v, %v)", result, err)
	}
	var wakeExpiresAt *time.Time
	if err := fixture.pool.QueryRow(
		ctx,
		`SELECT wake_attempt_expires_at FROM machines WHERE org_id = $1 AND id = $2`,
		testOrgID,
		machine.machineID,
	).Scan(&wakeExpiresAt); err != nil {
		t.Fatalf("load wake deadline after fresh inactive observation: %v", err)
	}
	if wakeExpiresAt == nil || !wakeExpiresAt.Equal(*firstWakeExpiresAt) {
		t.Fatalf("fresh inactive observation changed wake deadline from %v to %v", firstWakeExpiresAt, wakeExpiresAt)
	}
	if disposition, err = fixture.store.Execution().BeginMachineWake(
		ctx,
		testOrgID,
		machine.machineID,
		fixture.machinePool.ID,
		time.Minute,
	); err != nil || disposition != executionstore.MachineWakePending {
		t.Fatalf("wake after fresh inactive observation = (%v, %v), want pending", disposition, err)
	}
	if _, err := fixture.pool.Exec(ctx, `
UPDATE machines
SET wake_attempt_expires_at = statement_timestamp() - interval '1 millisecond'
WHERE org_id = $1 AND id = $2
`, testOrgID, machine.machineID); err != nil {
		t.Fatalf("expire machine wake deadline: %v", err)
	}
	if disposition, err = fixture.store.Execution().BeginMachineWake(
		ctx,
		testOrgID,
		machine.machineID,
		fixture.machinePool.ID,
		time.Minute,
	); err != nil || disposition != executionstore.MachineWakeUnresolved {
		t.Fatalf("wake with unresolved expired attempt = (%v, %v), want unresolved", disposition, err)
	}
	candidate = fixture.discoveryCandidate(t, ctx, machine.machineID)
	if !candidate.WakeAttemptExpired {
		t.Fatalf("expired wake candidate = %+v, want PostgreSQL-owned expiry", candidate)
	}
	straddled := candidate
	straddled.WakeAttemptExpired = false
	result, err = fixture.store.Execution().ApplyProviderRuntimeInactiveObservation(
		ctx,
		straddled,
	)
	if err != nil || result.Applied || result.WakeAttemptCleared {
		t.Fatalf("pre-expiry inactive observation after wake expiry = (%+v, %v)", result, err)
	}
	if err := fixture.pool.QueryRow(
		ctx,
		`SELECT wake_attempt_expires_at FROM machines WHERE org_id = $1 AND id = $2`,
		testOrgID,
		machine.machineID,
	).Scan(&wakeExpiresAt); err != nil {
		t.Fatalf("load wake deadline after straddled inactive observation: %v", err)
	}
	if wakeExpiresAt == nil {
		t.Fatal("pre-expiry inactive observation cleared wake after its deadline")
	}
	result, err = fixture.store.Execution().ApplyProviderRuntimeInactiveObservation(
		ctx,
		candidate,
	)
	if err != nil || !result.Applied || !result.WakeAttemptCleared {
		t.Fatalf("apply inactive observation after wake expiry = (%+v, %v)", result, err)
	}
	disposition, err = fixture.store.Execution().BeginMachineWake(
		ctx,
		testOrgID,
		machine.machineID,
		fixture.machinePool.ID,
		time.Minute,
	)
	if err != nil || disposition != executionstore.MachineWakeReady {
		t.Fatalf("wake after authoritative inactive observation = (%v, %v), want ready", disposition, err)
	}
}

func TestExpiredMachineWakeDoesNotFenceRuntimeProtectionDeletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProviderRuntimeStorageFixture(t, ctx, "expired-wake-deletion", true)
	machine := fixture.insertInactiveMachine(t, ctx, "expired-wake-deletion")
	if disposition, err := fixture.store.Execution().BeginMachineWake(
		ctx,
		testOrgID,
		machine.machineID,
		fixture.machinePool.ID,
		time.Minute,
	); err != nil || disposition != executionstore.MachineWakeReady {
		t.Fatalf("begin machine wake = (%v, %v), want ready", disposition, err)
	}
	candidate := fixture.discoveryCandidate(t, ctx, machine.machineID)
	if marked, err := fixture.store.Execution().MarkProviderRuntimeMismatch(ctx, candidate); err != nil || !marked {
		t.Fatalf("mark provider runtime mismatch = (%t, %v), want true/nil", marked, err)
	}
	fixture.backdateMismatch(t, ctx, machine.machineID)
	for _, due := range listDueProviderRuntimeCandidatesForTest(t, ctx, fixture.store, time.Millisecond) {
		if due.MachineID == machine.machineID {
			t.Fatal("fresh wake appeared in due provider runtime mismatches")
		}
	}
	if _, err := fixture.pool.Exec(ctx, `
UPDATE machines
SET wake_attempt_expires_at = statement_timestamp() - interval '1 millisecond'
WHERE org_id = $1 AND id = $2
`, testOrgID, machine.machineID); err != nil {
		t.Fatalf("expire machine wake deadline: %v", err)
	}
	if disposition, err := fixture.store.Execution().BeginMachineWake(
		ctx,
		testOrgID,
		machine.machineID,
		fixture.machinePool.ID,
		time.Minute,
	); err != nil || disposition != executionstore.MachineWakeUnresolved {
		t.Fatalf("repeat expired protected wake = (%v, %v), want unresolved", disposition, err)
	}
	candidate = fixture.dueCandidate(t, ctx, machine.machineID)
	straddled := candidate
	straddled.WakeAttemptExpired = false
	if _, claimed, err := fixture.store.Execution().ClaimProviderRuntimeMismatchDeletion(
		ctx,
		providerRuntimeClaimInput(straddled),
	); err != nil || claimed {
		t.Fatalf("pre-expiry observation claimed after wake expiry = (%t, %v), want false/nil", claimed, err)
	}
	claim, claimed, err := fixture.store.Execution().ClaimProviderRuntimeMismatchDeletion(
		ctx,
		providerRuntimeClaimInput(candidate),
	)
	if err != nil || !claimed {
		t.Fatalf("claim deletion after wake expiry = (%t, %v), want true/nil", claimed, err)
	}
	if claim.Machine.LifecycleState != executionstore.MachineLifecycleStateDeleting ||
		claim.Machine.LifecycleReasonCode != "provider_runtime_mismatch" {
		t.Fatalf("claimed machine = %+v, want deleting/provider_runtime_mismatch", claim.Machine)
	}
}

func TestProviderRuntimeTerminatedDeletionRespectsWakeEpoch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProviderRuntimeStorageFixture(t, ctx, "terminated-wake", true)
	machine := fixture.insertInactiveMachine(t, ctx, "terminated-wake")
	stale := fixture.discoveryCandidate(t, ctx, machine.machineID)
	if disposition, err := fixture.store.Execution().BeginMachineWake(
		ctx,
		testOrgID,
		machine.machineID,
		fixture.machinePool.ID,
		time.Minute,
	); err != nil || disposition != executionstore.MachineWakeReady {
		t.Fatalf("begin machine wake = (%v, %v), want ready", disposition, err)
	}
	if _, claimed, err := fixture.store.Execution().ClaimProviderRuntimeTerminatedDeletion(
		ctx,
		stale,
	); err != nil || claimed {
		t.Fatalf("stale terminated claim = (%t, %v), want false/nil", claimed, err)
	}
	current := fixture.discoveryCandidate(t, ctx, machine.machineID)
	if current.WakeAttemptExpiresAt == nil || current.WakeAttemptExpired {
		t.Fatalf("fresh wake candidate = %+v, want a future wake deadline", current)
	}
	if _, claimed, err := fixture.store.Execution().ClaimProviderRuntimeTerminatedDeletion(
		ctx,
		current,
	); err != nil || claimed {
		t.Fatalf("fresh-wake terminated claim = (%t, %v), want false/nil", claimed, err)
	}
	if _, err := fixture.pool.Exec(ctx, `
UPDATE machines
SET wake_attempt_expires_at = statement_timestamp() - interval '1 millisecond'
WHERE org_id = $1 AND id = $2
`, testOrgID, machine.machineID); err != nil {
		t.Fatalf("expire wake attempt: %v", err)
	}
	current = fixture.discoveryCandidate(t, ctx, machine.machineID)
	if !current.WakeAttemptExpired {
		t.Fatalf("expired wake candidate = %+v, want PostgreSQL-owned expiry", current)
	}
	straddled := current
	straddled.WakeAttemptExpired = false
	if _, claimed, err := fixture.store.Execution().ClaimProviderRuntimeTerminatedDeletion(
		ctx,
		straddled,
	); err != nil || claimed {
		t.Fatalf("pre-expiry observation claimed after wake expiry = (%t, %v), want false/nil", claimed, err)
	}
	claim, claimed, err := fixture.store.Execution().ClaimProviderRuntimeTerminatedDeletion(
		ctx,
		current,
	)
	if err != nil || !claimed {
		t.Fatalf("expired-wake terminated claim = (%t, %v), want true/nil", claimed, err)
	}
	if claim.Machine.LifecycleState != executionstore.MachineLifecycleStateDeleting ||
		claim.Machine.LifecycleReasonCode != "provider_runtime_terminated" {
		t.Fatalf("terminated claim machine = %+v, want deleting/provider_runtime_terminated", claim.Machine)
	}
}

func TestProviderRuntimeTerminatedDeletionRespectsMismatchEpoch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProviderRuntimeStorageFixture(t, ctx, "terminated-mismatch", true)

	t.Run("marker added", func(t *testing.T) {
		machine := fixture.insertInactiveMachine(t, ctx, "marker-added")
		stale := fixture.discoveryCandidate(t, ctx, machine.machineID)
		if marked, err := fixture.store.Execution().MarkProviderRuntimeMismatch(ctx, stale); err != nil || !marked {
			t.Fatalf("mark provider runtime mismatch = (%t, %v), want true/nil", marked, err)
		}
		if _, claimed, err := fixture.store.Execution().ClaimProviderRuntimeTerminatedDeletion(
			ctx,
			stale,
		); err != nil || claimed {
			t.Fatalf("pre-marker terminated claim = (%t, %v), want false/nil", claimed, err)
		}
	})

	t.Run("marker cleared", func(t *testing.T) {
		machine := fixture.insertInactiveMachine(t, ctx, "marker-cleared")
		candidate := fixture.discoveryCandidate(t, ctx, machine.machineID)
		if marked, err := fixture.store.Execution().MarkProviderRuntimeMismatch(ctx, candidate); err != nil || !marked {
			t.Fatalf("mark provider runtime mismatch = (%t, %v), want true/nil", marked, err)
		}
		marked := fixture.discoveryCandidate(t, ctx, machine.machineID)
		result, err := fixture.store.Execution().ApplyProviderRuntimeInactiveObservation(ctx, marked)
		if err != nil || !result.Applied {
			t.Fatalf("clear provider runtime mismatch = (%+v, %v), want applied/nil", result, err)
		}
		if _, claimed, err := fixture.store.Execution().ClaimProviderRuntimeTerminatedDeletion(
			ctx,
			marked,
		); err != nil || claimed {
			t.Fatalf("pre-clear terminated claim = (%t, %v), want false/nil", claimed, err)
		}
	})

	t.Run("marker replaced", func(t *testing.T) {
		machine := fixture.insertInactiveMachine(t, ctx, "marker-replaced")
		candidate := fixture.discoveryCandidate(t, ctx, machine.machineID)
		if marked, err := fixture.store.Execution().MarkProviderRuntimeMismatch(ctx, candidate); err != nil || !marked {
			t.Fatalf("mark provider runtime mismatch = (%t, %v), want true/nil", marked, err)
		}
		stale := fixture.discoveryCandidate(t, ctx, machine.machineID)
		if stale.ProviderRuntimeMismatchSince == nil {
			t.Fatal("marked candidate has no mismatch epoch")
		}
		if _, err := fixture.pool.Exec(ctx, `
UPDATE machines
SET provider_runtime_mismatch_since = $3
WHERE org_id = $1 AND id = $2
`, testOrgID, machine.machineID, stale.ProviderRuntimeMismatchSince.Add(time.Second)); err != nil {
			t.Fatalf("replace provider runtime mismatch epoch: %v", err)
		}
		if _, claimed, err := fixture.store.Execution().ClaimProviderRuntimeTerminatedDeletion(
			ctx,
			stale,
		); err != nil || claimed {
			t.Fatalf("replaced-marker terminated claim = (%t, %v), want false/nil", claimed, err)
		}
	})
}

func TestEnablingRuntimeProtectionPreservesInflightWake(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProviderRuntimeStorageFixture(t, ctx, "enable-during-wake", false)
	machine := fixture.insertInactiveMachine(t, ctx, "enable-during-wake")
	disposition, err := fixture.store.Execution().BeginMachineWake(
		ctx,
		testOrgID,
		machine.machineID,
		fixture.machinePool.ID,
		time.Minute,
	)
	if err != nil || disposition != executionstore.MachineWakeReady {
		t.Fatalf("begin unprotected machine wake = (%v, %v), want ready", disposition, err)
	}
	if _, err := fixture.store.Execution().UpdateMachinePool(
		ctx,
		executionstore.UpdateMachinePoolInput{
			OrgID:                    testOrgID,
			ID:                       fixture.machinePool.ID,
			RuntimeProtectionEnabled: boolPtrForMachinePoolTest(true),
		},
	); err != nil {
		t.Fatalf("enable runtime protection during wake: %v", err)
	}
	disposition, err = fixture.store.Execution().BeginMachineWake(
		ctx,
		testOrgID,
		machine.machineID,
		fixture.machinePool.ID,
		time.Minute,
	)
	if err != nil || disposition != executionstore.MachineWakePending {
		t.Fatalf("wake after enabling protection = (%v, %v), want pending", disposition, err)
	}
}

func TestOrdinaryStaleBootstrapDeletionClearsProviderRuntimeMismatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProviderRuntimeStorageFixture(t, ctx, "stale-bootstrap-marker", true)
	machineID := fixture.insertNeverConnectedMachine(t, ctx, "stale-bootstrap-marker")
	seedProviderRuntimeMismatchForTest(t, ctx, fixture.pool, machineID)
	if _, err := fixture.pool.Exec(ctx, `
UPDATE machines
SET lifecycle_changed_at = statement_timestamp()
      - $1::bigint * interval '1 second',
    updated_at = statement_timestamp()
WHERE org_id = $2 AND id = $3
`, int64(executionstore.StaleMachineBootstrapAge/time.Second)+1, testOrgID, machineID); err != nil {
		t.Fatalf("age never-connected machine: %v", err)
	}
	machine, err := fixture.store.Execution().GetMachine(ctx, testOrgID, machineID)
	if err != nil {
		t.Fatalf("get never-connected machine: %v", err)
	}
	claim, claimed, err := fixture.store.Execution().ClaimPoolMachineDeletion(
		ctx,
		executionstore.MachineDeletingInput{
			OrgID:                    testOrgID,
			MachineID:                machineID,
			LifecycleReasonCode:      "startup_or_daemon_bootstrap_failed",
			LifecycleReasonMessage:   "cleaning up machine because startup script or daemon bootstrap did not complete",
			ExpectedLifecycleVersion: machine.LifecycleVersion,
		},
	)
	if err != nil || !claimed {
		t.Fatalf("claim stale-bootstrap deletion = (%t, %v)", claimed, err)
	}
	if claim.Machine.LifecycleState != "deleting" {
		t.Fatalf("claimed machine = %+v, want deleting", claim.Machine)
	}
	assertProviderRuntimeMismatchClearedForTest(t, ctx, fixture.pool, machineID)
}

func TestProviderRuntimeInactivityGraceBlocksDueSelectionAndClaim(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProviderRuntimeStorageFixture(t, ctx, "fresh-inactivity", true)
	machine := fixture.insertInactiveMachine(t, ctx, "fresh-inactivity")

	candidate := fixture.discoveryCandidate(t, ctx, machine.machineID)
	if marked, err := fixture.store.Execution().MarkProviderRuntimeMismatch(
		ctx,
		candidate,
	); err != nil || !marked {
		t.Fatalf("mark provider runtime mismatch = (%t, %v)", marked, err)
	}
	fixture.backdateMismatch(t, ctx, machine.machineID)
	if _, err := fixture.pool.Exec(ctx, `
UPDATE machines
SET asleep_since = statement_timestamp(),
    updated_at = statement_timestamp()
WHERE org_id = $1 AND id = $2
`, testOrgID, machine.machineID); err != nil {
		t.Fatalf("start a fresh inactivity period: %v", err)
	}

	candidate = fixture.discoveryCandidate(t, ctx, machine.machineID)
	if candidate.ProviderRuntimeMismatchSince == nil {
		t.Fatalf("fresh-inactivity candidate = %+v, want retained old marker", candidate)
	}
	grace := time.Minute
	due, err := fixture.store.Execution().ListDueProviderRuntimeMismatches(
		ctx,
		executionstore.ListDueProviderRuntimeMismatchesInput{
			ConfirmationGrace: time.Millisecond,
			InactivityGrace:   grace,
		},
	)
	if err != nil {
		t.Fatalf("list due candidates with fresh inactivity: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("fresh inactivity became due: %+v", due)
	}
	if _, claimed, err := fixture.store.Execution().ClaimProviderRuntimeMismatchDeletion(
		ctx,
		executionstore.ClaimProviderRuntimeMismatchDeletionInput{
			Candidate:         candidate,
			ConfirmationGrace: time.Millisecond,
			InactivityGrace:   grace,
		},
	); err != nil || claimed {
		t.Fatalf("fresh-inactivity claim = (%t, %v), want false/nil", claimed, err)
	}
}

func TestProviderRuntimeConfirmationGraceBlocksDeletionClaim(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProviderRuntimeStorageFixture(t, ctx, "fresh-mismatch", true)
	machine := fixture.insertInactiveMachine(t, ctx, "fresh-mismatch")
	candidate := fixture.discoveryCandidate(t, ctx, machine.machineID)
	if marked, err := fixture.store.Execution().MarkProviderRuntimeMismatch(
		ctx,
		candidate,
	); err != nil || !marked {
		t.Fatalf("mark provider runtime mismatch = (%t, %v)", marked, err)
	}
	candidate = fixture.discoveryCandidate(t, ctx, machine.machineID)
	if _, claimed, err := fixture.store.Execution().ClaimProviderRuntimeMismatchDeletion(
		ctx,
		executionstore.ClaimProviderRuntimeMismatchDeletionInput{
			Candidate:         candidate,
			ConfirmationGrace: time.Minute,
			InactivityGrace:   time.Millisecond,
		},
	); err != nil || claimed {
		t.Fatalf("fresh-mismatch claim = (%t, %v), want false/nil", claimed, err)
	}
}

func TestProviderRuntimeDeletionRejectsSupersededMismatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProviderRuntimeStorageFixture(t, ctx, "superseded-mismatch", true)
	machine := fixture.insertInactiveMachine(t, ctx, "superseded-mismatch")
	stale := fixture.dueCandidate(t, ctx, machine.machineID)
	if result, err := fixture.store.Execution().ApplyProviderRuntimeInactiveObservation(
		ctx,
		stale,
	); err != nil || !result.Applied {
		t.Fatalf("clear original mismatch = (%+v, %v)", result, err)
	}
	current := fixture.discoveryCandidate(t, ctx, machine.machineID)
	if marked, err := fixture.store.Execution().MarkProviderRuntimeMismatch(
		ctx,
		current,
	); err != nil || !marked {
		t.Fatalf("mark replacement mismatch = (%t, %v)", marked, err)
	}
	if _, claimed, err := fixture.store.Execution().ClaimProviderRuntimeMismatchDeletion(
		ctx,
		providerRuntimeClaimInput(stale),
	); err != nil || claimed {
		t.Fatalf("superseded mismatch claim = (%t, %v), want false/nil", claimed, err)
	}
}

func TestProviderRuntimeMismatchDeletionClaimHandlesConcurrentChanges(t *testing.T) {
	for _, test := range []struct {
		name        string
		wantClaimed bool
		mutate      func(context.Context, *testing.T, providerRuntimeStorageFixture, providerRuntimeMachine)
	}{
		{
			name:        "unrelated pool edit",
			wantClaimed: true,
			mutate: func(ctx context.Context, t *testing.T, fixture providerRuntimeStorageFixture, _ providerRuntimeMachine) {
				t.Helper()
				description := "changed while confirmation was in flight"
				if _, err := fixture.store.Execution().UpdateMachinePool(
					ctx,
					executionstore.UpdateMachinePoolInput{
						OrgID:       testOrgID,
						ID:          fixture.machinePool.ID,
						Description: &description,
					},
				); err != nil {
					t.Fatalf("update machine pool: %v", err)
				}
			},
		},
		{
			name: "daemon reconnect",
			mutate: func(ctx context.Context, t *testing.T, fixture providerRuntimeStorageFixture, machine providerRuntimeMachine) {
				t.Helper()
				if _, err := fixture.store.Execution().RegisterDaemonRuntimeWithReconciliation(
					ctx,
					executionstore.RegisterDaemonRuntimeInput{
						OrgID:            testOrgID,
						MachineID:        machine.machineID,
						DaemonTokenID:    machine.tokenID,
						DaemonInstanceID: uuid.New(),
						DaemonVersion:    "1.0.0",
						LeaseTimeout:     time.Minute,
					},
				); err != nil {
					t.Fatalf("reconnect daemon: %v", err)
				}
			},
		},
		{
			name: "new inactivity period",
			mutate: func(ctx context.Context, t *testing.T, fixture providerRuntimeStorageFixture, machine providerRuntimeMachine) {
				t.Helper()
				if _, err := fixture.pool.Exec(ctx, `
UPDATE machines
SET asleep_since = asleep_since + interval '1 second',
    updated_at = statement_timestamp()
WHERE org_id = $1 AND id = $2
`, testOrgID, machine.machineID); err != nil {
					t.Fatalf("start new inactivity period: %v", err)
				}
			},
		},
		{
			name: "credential rotation",
			mutate: func(ctx context.Context, t *testing.T, fixture providerRuntimeStorageFixture, _ providerRuntimeMachine) {
				t.Helper()
				if _, _, err := fixture.store.Secrets().CreateSecretVersion(
					ctx,
					secretstore.CreateSecretVersionInput{
						OrgID:    testOrgID,
						SecretID: fixture.secretID,
						Material: secrets.GenericMaterial{Value: "rotated-provider-token"},
						Actor:    userPrincipal(fixture.adminID),
					},
				); err != nil {
					t.Fatalf("rotate provider credential: %v", err)
				}
			},
		},
		{
			name: "provider configuration change",
			mutate: func(ctx context.Context, t *testing.T, fixture providerRuntimeStorageFixture, _ providerRuntimeMachine) {
				t.Helper()
				if _, err := fixture.store.Execution().UpdateMachinePool(
					ctx,
					executionstore.UpdateMachinePoolInput{
						OrgID:          testOrgID,
						ID:             fixture.machinePool.ID,
						ProviderConfig: json.RawMessage(`{"scope":"changed"}`),
					},
				); err != nil {
					t.Fatalf("update provider configuration: %v", err)
				}
			},
		},
		{
			name: "protection disabled",
			mutate: func(ctx context.Context, t *testing.T, fixture providerRuntimeStorageFixture, _ providerRuntimeMachine) {
				t.Helper()
				if _, err := fixture.store.Execution().UpdateMachinePool(
					ctx,
					executionstore.UpdateMachinePoolInput{
						OrgID:                    testOrgID,
						ID:                       fixture.machinePool.ID,
						RuntimeProtectionEnabled: boolPtrForMachinePoolTest(false),
					},
				); err != nil {
					t.Fatalf("disable runtime protection: %v", err)
				}
			},
		},
		{
			name: "pool deleted",
			mutate: func(ctx context.Context, t *testing.T, fixture providerRuntimeStorageFixture, _ providerRuntimeMachine) {
				t.Helper()
				if _, err := fixture.store.Execution().DeleteMachinePool(
					ctx,
					testOrgID,
					fixture.machinePool.ID,
				); err != nil {
					t.Fatalf("delete machine pool: %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture := newProviderRuntimeStorageFixture(t, ctx, "claim-"+test.name, true)
			machine := fixture.insertInactiveMachine(t, ctx, "target")
			candidate := fixture.dueCandidate(t, ctx, machine.machineID)
			test.mutate(ctx, t, fixture, machine)

			if _, claimed, err := fixture.store.Execution().ClaimProviderRuntimeMismatchDeletion(
				ctx,
				providerRuntimeClaimInput(candidate),
			); err != nil || claimed != test.wantClaimed {
				t.Fatalf(
					"provider runtime claim = (%t, %v), want claimed=%t",
					claimed,
					err,
					test.wantClaimed,
				)
			}
		})
	}
}

func TestProviderRuntimeMismatchDeletionClaimIsSingleWriter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProviderRuntimeStorageFixture(t, ctx, "claim-single-writer", true)
	machine := fixture.insertInactiveMachine(t, ctx, "target")
	candidate := fixture.dueCandidate(t, ctx, machine.machineID)

	type result struct {
		claimed bool
		err     error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			_, claimed, err := fixture.store.Execution().ClaimProviderRuntimeMismatchDeletion(
				context.Background(),
				providerRuntimeClaimInput(candidate),
			)
			results <- result{claimed: claimed, err: err}
		}()
	}
	claimed := 0
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent provider runtime claim: %v", result.err)
		}
		if result.claimed {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("successful concurrent claims = %d, want 1", claimed)
	}
	deleting, err := fixture.store.Execution().GetMachine(ctx, testOrgID, machine.machineID)
	if err != nil {
		t.Fatalf("get claimed mismatch machine: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `
UPDATE machines
SET next_reconcile_after = statement_timestamp() - interval '1 second',
    updated_at = statement_timestamp()
WHERE org_id = $1 AND id = $2
`, testOrgID, machine.machineID); err != nil {
		t.Fatalf("expire mismatch deletion claim lease: %v", err)
	}
	if _, retried, err := fixture.store.Execution().ClaimPoolMachineDeletion(
		ctx,
		executionstore.MachineDeletingInput{
			OrgID:                    testOrgID,
			MachineID:                machine.machineID,
			LifecycleReasonCode:      "deleting_retry",
			LifecycleReasonMessage:   "retrying machine deletion",
			ExpectedLifecycleVersion: deleting.LifecycleVersion,
		},
	); err != nil || !retried {
		t.Fatalf("recover abandoned mismatch deletion claim = (%t, %v)", retried, err)
	}
	if _, err := fixture.store.Execution().UpdateMachinePool(
		ctx,
		executionstore.UpdateMachinePoolInput{
			OrgID:                    testOrgID,
			ID:                       fixture.machinePool.ID,
			RuntimeProtectionEnabled: boolPtrForMachinePoolTest(false),
		},
	); err != nil {
		t.Fatalf("disable protection after forced deletion claim: %v", err)
	}
	var mismatchSince *time.Time
	if err := fixture.pool.QueryRow(
		ctx,
		`SELECT provider_runtime_mismatch_since FROM machines WHERE org_id = $1 AND id = $2`,
		testOrgID,
		machine.machineID,
	).Scan(&mismatchSince); err != nil {
		t.Fatalf("load retained forced-deletion marker: %v", err)
	}
	if mismatchSince == nil {
		t.Fatal("pool protection toggle erased the forced-deletion incident marker")
	}
	if due := listDueProviderRuntimeCandidatesForTest(t, ctx, fixture.store, time.Millisecond); len(due) != 0 {
		t.Fatalf("claimed deleting machine remained in due index query: %+v", due)
	}
}

func TestMachineWakeDeadlineProtectsQueuedWork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProviderRuntimeStorageFixture(t, ctx, "wake-deadline-work", true)
	machine := fixture.insertInactiveMachine(t, ctx, "wake-deadline-work")
	processFixture, processID, toolCallID := fixture.createQueuedProcess(
		t,
		ctx,
		machine,
		"wake-deadline-work",
	)
	process, err := fixture.store.Execution().GetProcess(
		ctx,
		testProjectID,
		processFixture.AgentID,
		processID,
	)
	if err != nil {
		t.Fatalf("get queued process: %v", err)
	}
	if disposition, err := fixture.store.Execution().BeginMachineWake(
		ctx,
		testOrgID,
		machine.machineID,
		fixture.machinePool.ID,
		time.Minute,
	); err != nil || disposition != executionstore.MachineWakeReady {
		t.Fatalf("begin machine wake = (%v, %v), want ready", disposition, err)
	}
	workQuery := dbsqlc.New(fixture.pool)
	queuedDuringWake, err := workQuery.ListMachineUnreachableQueuedProcessToolCallsForMachine(
		ctx,
		dbsqlc.ListMachineUnreachableQueuedProcessToolCallsForMachineParams{
			OrgID:                          testOrgID,
			MachineID:                      machine.machineID,
			MachineUnreachableGraceSeconds: 0,
			LimitCount:                     1,
		},
	)
	if err != nil || len(queuedDuringWake) != 0 {
		t.Fatalf("queued work listed during wake = (%d, %v), want 0/nil", len(queuedDuringWake), err)
	}
	if failed, err := fixture.store.Execution().FailQueuedProcessAfterWakeFailure(
		ctx,
		process,
	); err != nil || failed {
		t.Fatalf("direct expiry during wake = (%t, %v), want false/nil", failed, err)
	}
	if expired, err := fixture.store.Execution().ExpireMachineUnreachableProcessToolCallsForAllProjects(
		ctx,
		0,
	); err != nil || expired != 0 {
		t.Fatalf("maintenance expiry during wake = (%d, %v), want 0/nil", expired, err)
	}
	if _, err := fixture.pool.Exec(ctx, `
UPDATE machines
SET wake_attempt_expires_at = statement_timestamp() - interval '1 millisecond'
WHERE org_id = $1 AND id = $2
`, testOrgID, machine.machineID); err != nil {
		t.Fatalf("expire wake deadline: %v", err)
	}
	queuedAfterWake, err := workQuery.ListMachineUnreachableQueuedProcessToolCallsForMachine(
		ctx,
		dbsqlc.ListMachineUnreachableQueuedProcessToolCallsForMachineParams{
			OrgID:                          testOrgID,
			MachineID:                      machine.machineID,
			MachineUnreachableGraceSeconds: 0,
			LimitCount:                     1,
		},
	)
	if err != nil || len(queuedAfterWake) != 1 {
		t.Fatalf("queued work listed after wake = (%d, %v), want 1/nil", len(queuedAfterWake), err)
	}
	if expired, err := fixture.store.Execution().ExpireMachineUnreachableProcessToolCallsForAllProjects(
		ctx,
		0,
	); err != nil || expired != 1 {
		t.Fatalf("maintenance expiry after wake = (%d, %v), want 1/nil", expired, err)
	}
	process, err = fixture.store.Execution().GetProcess(
		ctx,
		testProjectID,
		processFixture.AgentID,
		processID,
	)
	if err != nil {
		t.Fatalf("get expired process: %v", err)
	}
	if process.State != executionstore.ProcessStateFailed ||
		process.StateReasonCode != executionstore.ProcessToolReasonMachineUnreachable {
		t.Fatalf("expired process = %s/%s, want failed/machine_unreachable", process.State, process.StateReasonCode)
	}
	toolCall, err := fixture.store.Execution().GetToolCall(
		ctx,
		testProjectID,
		processFixture.AgentID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("get expired tool call: %v", err)
	}
	assertCompletedToolCallWithResult(
		t,
		fixture.store,
		processFixture.AgentID,
		toolCall,
		executionstore.ProcessToolReasonMachineUnreachable,
	)
}

func TestRuntimeProtectionAndUnreachableExpiryConverge(t *testing.T) {
	for _, runtimeProtectionFirst := range []bool{false, true} {
		name := "unreachable_first"
		if runtimeProtectionFirst {
			name = "runtime_protection_first"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture := newProviderRuntimeStorageFixture(t, ctx, "work-outcome-"+name, true)
			machine := fixture.insertInactiveMachine(t, ctx, "work-outcome-"+name)
			processFixture, processID, toolCallID := fixture.createQueuedProcess(
				t,
				ctx,
				machine,
				"work-outcome-"+name,
			)
			candidate := fixture.dueCandidate(t, ctx, machine.machineID)

			claimRuntimeProtection := func() {
				t.Helper()
				claim, claimed, err := fixture.store.Execution().ClaimProviderRuntimeMismatchDeletion(
					ctx,
					providerRuntimeClaimInput(candidate),
				)
				if err != nil || !claimed {
					t.Fatalf("claim provider runtime deletion = (%t, %v)", claimed, err)
				}
				if claim.Machine.LifecycleState != executionstore.MachineLifecycleStateDeleting ||
					claim.Machine.LifecycleReasonCode != "provider_runtime_mismatch" {
					t.Fatalf("claimed machine = %+v, want deleting/provider_runtime_mismatch", claim.Machine)
				}
			}
			expireUnreachable := func(want int64) {
				t.Helper()
				expired, err := fixture.store.Execution().ExpireMachineUnreachableProcessToolCallsForAllProjects(
					ctx,
					0,
				)
				if err != nil || expired != want {
					t.Fatalf("expire machine-unreachable work = (%d, %v), want %d/nil", expired, err, want)
				}
			}

			if runtimeProtectionFirst {
				claimRuntimeProtection()
				expireUnreachable(0)
			} else {
				expireUnreachable(1)
				claimRuntimeProtection()
			}

			process, err := fixture.store.Execution().GetProcess(
				ctx,
				testProjectID,
				processFixture.AgentID,
				processID,
			)
			if err != nil {
				t.Fatalf("get terminal process: %v", err)
			}
			if process.State != executionstore.ProcessStateFailed ||
				process.StateReasonCode != executionstore.ProcessToolReasonMachineUnreachable {
				t.Fatalf(
					"terminal process = %s/%s, want failed/%s",
					process.State,
					process.StateReasonCode,
					executionstore.ProcessToolReasonMachineUnreachable,
				)
			}
			toolCall, err := fixture.store.Execution().GetToolCall(
				ctx,
				testProjectID,
				processFixture.AgentID,
				toolCallID,
			)
			if err != nil {
				t.Fatalf("get terminal tool call: %v", err)
			}
			assertCompletedToolCallWithResult(
				t,
				fixture.store,
				processFixture.AgentID,
				toolCall,
				executionstore.ProcessToolReasonMachineUnreachable,
			)
			var resultCount int
			if err := fixture.pool.QueryRow(
				ctx,
				`SELECT count(*) FROM tool_call_results WHERE tool_call_id = $1`,
				toolCallID,
			).Scan(&resultCount); err != nil {
				t.Fatalf("count terminal tool results: %v", err)
			}
			if resultCount != 1 {
				t.Fatalf("terminal tool results = %d, want 1", resultCount)
			}
		})
	}
}

type providerRuntimeStorageFixture struct {
	pool          *pgxpool.Pool
	store         *Store
	machinePool   executionstore.MachinePoolRecord
	secretID      ID
	adminID       ID
	providerToken string
}

type providerRuntimeMachine struct {
	machineID ID
	tokenID   ID
	runtimeID ID
}

func newProviderRuntimeStorageFixture(
	t *testing.T,
	ctx context.Context,
	seed string,
	protected bool,
) providerRuntimeStorageFixture {
	t.Helper()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	admin := createSecretTestUser(t, ctx, store, "runtime protection "+seed, "admin")
	providerToken := "provider-token-" + uuid.NewString()
	secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "runtime-protection-" + uuid.NewString(),
		Material:  secrets.GenericMaterial{Value: providerToken},
		Actor:     userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("create provider credential: %v", err)
	}
	machinePool := createProviderRuntimeMachinePoolForTest(
		t,
		ctx,
		store,
		secret.ID,
		seed,
		protected,
	)
	return providerRuntimeStorageFixture{
		pool:          pool,
		store:         store,
		machinePool:   machinePool,
		secretID:      secret.ID,
		adminID:       admin.ID,
		providerToken: providerToken,
	}
}

func createProviderRuntimeMachinePoolForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	secretID ID,
	seed string,
	protected bool,
) executionstore.MachinePoolRecord {
	t.Helper()
	machinePool, err := store.Execution().CreateMachinePool(
		ctx,
		completeMachinePoolCreateInputForTest(
			t,
			ctx,
			store,
			machinePoolInputWithDefaultMachineForTest(
				executionstore.CreateMachinePoolInput{
					OrgID:                    testOrgID,
					Name:                     "Runtime protection " + seed,
					Provider:                 "test.provider",
					ProviderConfig:           json.RawMessage(`{"scope":"test"}`),
					ProviderAuthSecretID:     secretID,
					RuntimeProtectionEnabled: protected,
					MaxTotalMachines:         100,
				},
				defaultMachineFieldsForTest{
					DefaultMachineCPU:             1,
					DefaultMachineMemoryMB:        1024,
					DefaultMachineEnv:             json.RawMessage(`{}`),
					DefaultMachineProviderOptions: json.RawMessage(`{}`),
				},
			),
		),
	)
	if err != nil {
		t.Fatalf("create runtime protection pool: %v", err)
	}
	return machinePool
}

func (f providerRuntimeStorageFixture) insertInactiveMachine(
	t *testing.T,
	ctx context.Context,
	seed string,
) providerRuntimeMachine {
	t.Helper()
	machineID := uuid.New()
	tokenID := uuid.New()
	runtimeID := uuid.New()
	daemonInstanceID := uuid.New()
	var inactiveSince time.Time
	if err := f.pool.QueryRow(
		ctx,
		`SELECT statement_timestamp() - interval '10 minutes'`,
	).Scan(&inactiveSince); err != nil {
		t.Fatalf("read database-owned inactive timestamp: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `
INSERT INTO machines(
    id, org_id, machine_pool_id, source_kind, display_name, provider,
    lifecycle_state, lifecycle_changed_at, provider_resource_id,
    provider_provision_attempted_at, cpu, memory_mb, cwd, env, secret_env,
    provider_options, metadata, created_at, updated_at
) VALUES (
    $1, $2, $3, 'pool', $4, $5,
    'active', $6, $7, $6, 1, 1024, '', '{}'::jsonb, '{}'::jsonb,
    '{}'::jsonb, '{}'::jsonb, $6, $6
)
`,
		machineID,
		testOrgID,
		f.machinePool.ID,
		"runtime-machine-"+seed,
		f.machinePool.Provider,
		inactiveSince.Add(-time.Minute),
		"runtime-resource-"+uuid.NewString(),
	); err != nil {
		t.Fatalf("insert runtime protection machine: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `
INSERT INTO machine_daemon_tokens(
    id, org_id, machine_id, name, token_hash, created_at
) VALUES ($1, $2, $3, 'runtime-protection', $4, $5)
`, tokenID, testOrgID, machineID, "runtime-token-"+uuid.NewString(), inactiveSince.Add(-time.Minute)); err != nil {
		t.Fatalf("insert runtime protection daemon token: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `
INSERT INTO daemon_runtimes(
    id, org_id, machine_id, daemon_token_id, daemon_instance_id,
    daemon_version, state, state_reason_code, created_at, last_seen_at,
    lease_expires_at, ended_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, '1.0.0', 'ended', 'daemon_sleep',
    $6, $6, $7, $7, $7
)
`,
		runtimeID,
		testOrgID,
		machineID,
		tokenID,
		daemonInstanceID,
		inactiveSince.Add(-time.Minute),
		inactiveSince,
	); err != nil {
		t.Fatalf("insert inactive daemon runtime: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `
UPDATE machines
SET current_daemon_runtime_id = $1,
    asleep_since = $2,
    last_observed_at = $2,
    updated_at = $2
WHERE org_id = $3 AND id = $4
`, runtimeID, inactiveSince, testOrgID, machineID); err != nil {
		t.Fatalf("attach inactive daemon runtime: %v", err)
	}
	return providerRuntimeMachine{
		machineID: machineID,
		tokenID:   tokenID,
		runtimeID: runtimeID,
	}
}

func (f providerRuntimeStorageFixture) createQueuedProcess(
	t *testing.T,
	ctx context.Context,
	machine providerRuntimeMachine,
	seed string,
) (processDaemonFixture, ID, ID) {
	t.Helper()
	processFixture := f.createProcessFixture(t, ctx, machine, seed)
	toolCallID := createToolCallForProcessTest(t, ctx, processFixture, seed, "run_command")
	process, err := startProcessForTest(
		ctx,
		f.store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       processFixture.AgentID,
			ToolCallID:    toolCallID,
			RuntimeLockID: processFixture.Lock.ID,
		},
		executionstore.CreateProcessInput{
			AgentMachineBindingID: processFixture.BindingID,
			Command:               "sleep 3600",
			ShellSelector:         "sh",
			Cwd:                   "/work",
		},
	)
	if err != nil {
		t.Fatalf("start queued process: %v", err)
	}
	return processFixture, process.ID, toolCallID
}

func (f providerRuntimeStorageFixture) createProcessFixture(
	t *testing.T,
	ctx context.Context,
	machine providerRuntimeMachine,
	seed string,
) processDaemonFixture {
	t.Helper()
	poolGrant, err := f.store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachinePoolID:  f.machinePool.ID,
			IdempotencyKey: "runtime-protection-work-" + uuid.NewString(),
		},
	)
	if err != nil {
		t.Fatalf("create project pool grant: %v", err)
	}
	machineGrant, err := f.store.q.UpsertProjectMachineGrant(
		ctx,
		dbsqlc.UpsertProjectMachineGrantParams{
			OrgID:                     testOrgID,
			ProjectID:                 testProjectID,
			MachineID:                 machine.machineID,
			SourceKind:                string(executionstore.ProjectMachineGrantSourceKindPool),
			ProjectMachinePoolGrantID: &poolGrant.ID,
			Description:               poolGrant.Description,
			Metadata:                  json.RawMessage(`{}`),
		},
	)
	if err != nil {
		t.Fatalf("create generated machine grant: %v", err)
	}
	user := mustCreateProjectOperatorUser(
		t,
		ctx,
		f.store,
		"runtime-protection-"+uuid.NewString()+"@example.com",
		"Runtime Protection Tester",
	)
	agentID := mustCreateAgent(t, ctx, f.store, time.Now().UTC())
	binding, err := executionstore.IntegrationInsertAgentMachineBindingTx(
		ctx,
		f.store.q,
		executionstore.IntegrationInsertAgentMachineBindingInput{
			ProjectID:             testProjectID,
			AgentID:               agentID,
			ProjectMachineGrantID: machineGrant.ID,
			MachineRef:            testMachineRef(seed),
			BindingKind:           executionstore.MachineBindingKindPool,
			Cwd:                   "/work",
		},
	)
	if err != nil {
		t.Fatalf("bind agent to pool machine: %v", err)
	}
	lock, err := f.store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		agentID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire agent runtime lock: %v", err)
	}
	processFixture := processDaemonFixture{
		Store:     f.store,
		OrgID:     testOrgID,
		AgentID:   agentID,
		MachineID: machine.machineID,
		BindingID: binding.ID,
		TokenID:   machine.tokenID,
		UserID:    user.ID,
		Lock:      lock,
		GrantID:   machineGrant.ID,
		Now:       time.Now().UTC(),
	}
	return processFixture
}

func (f providerRuntimeStorageFixture) insertNeverConnectedMachine(
	t *testing.T,
	ctx context.Context,
	seed string,
) ID {
	t.Helper()
	machineID := uuid.New()
	now := time.Now().UTC()
	if _, err := f.pool.Exec(ctx, `
INSERT INTO machines(
    id, org_id, machine_pool_id, source_kind, display_name, provider,
    lifecycle_state, lifecycle_changed_at, provider_resource_id,
    provider_provision_attempted_at, cpu, memory_mb, cwd, env, secret_env,
    provider_options, metadata, created_at, updated_at
) VALUES (
    $1, $2, $3, 'pool', $4, $5,
    'active', $6, $7, $6, 1, 1024, '', '{}'::jsonb, '{}'::jsonb,
    '{}'::jsonb, '{}'::jsonb, $6, $6
)
`,
		machineID,
		testOrgID,
		f.machinePool.ID,
		"never-connected-"+seed,
		f.machinePool.Provider,
		now,
		"never-connected-resource-"+uuid.NewString(),
	); err != nil {
		t.Fatalf("insert never-connected pool machine: %v", err)
	}
	return machineID
}

func (f providerRuntimeStorageFixture) insertInactiveBYOMachine(
	t *testing.T,
	ctx context.Context,
	seed string,
) ID {
	t.Helper()
	machine, err := f.store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          testOrgID,
			DisplayName:    "provider runtime byo " + seed,
			IdempotencyKey: "provider-runtime-byo-" + seed,
		},
	)
	if err != nil {
		t.Fatalf("create BYO runtime exclusion machine: %v", err)
	}
	token, err := f.store.Execution().CreateBYOMachineDaemonToken(
		ctx,
		executionstore.CreateBYOMachineDaemonTokenInput{
			OrgID:     testOrgID,
			MachineID: machine.ID,
			Name:      "provider-runtime-byo",
			Token:     "provider-runtime-byo-token-" + uuid.NewString(),
		},
	)
	if err != nil {
		t.Fatalf("create BYO runtime exclusion token: %v", err)
	}
	runtime, err := f.store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            testOrgID,
			MachineID:        machine.ID,
			DaemonTokenID:    token.ID,
			DaemonInstanceID: uuid.New(),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     time.Minute,
		},
	)
	if err != nil {
		t.Fatalf("register BYO runtime exclusion daemon: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `
UPDATE daemon_runtimes
SET last_seen_at = statement_timestamp() - interval '6 minutes',
    lease_expires_at = statement_timestamp() - interval '5 minutes',
    updated_at = statement_timestamp()
WHERE org_id = $1 AND machine_id = $2 AND id = $3
`, testOrgID, machine.ID, runtime.ID); err != nil {
		t.Fatalf("expire BYO runtime exclusion daemon lease: %v", err)
	}
	return machine.ID
}

func (f providerRuntimeStorageFixture) backdateMismatch(
	t *testing.T,
	ctx context.Context,
	machineID ID,
) {
	t.Helper()
	tag, err := f.pool.Exec(ctx, `
UPDATE machines
SET provider_runtime_mismatch_since = statement_timestamp() - interval '5 minutes'
WHERE org_id = $1 AND id = $2 AND provider_runtime_mismatch_since IS NOT NULL
`, testOrgID, machineID)
	if err != nil {
		t.Fatalf("backdate provider runtime mismatch: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("backdate provider runtime mismatch rows = %d, want 1", tag.RowsAffected())
	}
}

func (f providerRuntimeStorageFixture) dueCandidate(
	t *testing.T,
	ctx context.Context,
	machineID ID,
) executionstore.ProviderRuntimeCandidate {
	t.Helper()
	candidate := f.discoveryCandidate(t, ctx, machineID)
	if candidate.ProviderRuntimeMismatchSince == nil {
		if marked, err := f.store.Execution().MarkProviderRuntimeMismatch(ctx, candidate); err != nil || !marked {
			t.Fatalf("mark provider runtime mismatch = (%t, %v)", marked, err)
		}
		f.backdateMismatch(t, ctx, machineID)
	}
	for _, current := range listDueProviderRuntimeCandidatesForTest(t, ctx, f.store, time.Millisecond) {
		if current.MachineID == machineID {
			return current
		}
	}
	t.Fatalf("machine %s was not a due provider runtime candidate", machineID)
	return executionstore.ProviderRuntimeCandidate{}
}

func (f providerRuntimeStorageFixture) discoveryCandidate(
	t *testing.T,
	ctx context.Context,
	machineID ID,
) executionstore.ProviderRuntimeCandidate {
	t.Helper()
	candidates, err := f.store.Execution().ListProviderRuntimeDiscoveryCandidates(
		ctx,
		executionstore.ListProviderRuntimeCandidatesInput{},
	)
	if err != nil {
		t.Fatalf("list provider runtime discovery candidate: %v", err)
	}
	var candidate executionstore.ProviderRuntimeCandidate
	for _, current := range candidates {
		if current.MachineID == machineID {
			candidate = current
			break
		}
	}
	if candidate.MachineID == NilID {
		t.Fatalf("machine %s was not a provider runtime discovery candidate", machineID)
	}
	return candidate
}

func listDueProviderRuntimeCandidatesForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	grace time.Duration,
) []executionstore.ProviderRuntimeCandidate {
	t.Helper()
	candidates, err := store.Execution().ListDueProviderRuntimeMismatches(
		ctx,
		executionstore.ListDueProviderRuntimeMismatchesInput{
			ConfirmationGrace: grace,
			InactivityGrace:   grace,
		},
	)
	if err != nil {
		t.Fatalf("list due provider runtime mismatches: %v", err)
	}
	return candidates
}

func providerRuntimeClaimInput(
	candidate executionstore.ProviderRuntimeCandidate,
) executionstore.ClaimProviderRuntimeMismatchDeletionInput {
	return executionstore.ClaimProviderRuntimeMismatchDeletionInput{
		Candidate:         candidate,
		ConfirmationGrace: time.Millisecond,
		InactivityGrace:   time.Millisecond,
	}
}

func seedProviderRuntimeMismatchForTest(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	machineIDs ...ID,
) {
	t.Helper()
	for _, machineID := range machineIDs {
		tag, err := pool.Exec(ctx, `
UPDATE machines
SET provider_runtime_mismatch_since = statement_timestamp(),
    updated_at = statement_timestamp()
WHERE org_id = $1 AND id = $2
`, testOrgID, machineID)
		if err != nil {
			t.Fatalf("seed provider runtime mismatch for %s: %v", machineID, err)
		}
		if tag.RowsAffected() != 1 {
			t.Fatalf("seed provider runtime mismatch rows for %s = %d, want 1", machineID, tag.RowsAffected())
		}
	}
}

func assertProviderRuntimeMismatchClearedForTest(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	machineIDs ...ID,
) {
	t.Helper()
	for _, machineID := range machineIDs {
		var mismatchSince *time.Time
		if err := pool.QueryRow(
			ctx,
			`SELECT provider_runtime_mismatch_since FROM machines WHERE org_id = $1 AND id = $2`,
			testOrgID,
			machineID,
		).Scan(&mismatchSince); err != nil {
			t.Fatalf("load provider runtime mismatch for %s: %v", machineID, err)
		}
		if mismatchSince != nil {
			t.Fatalf("provider runtime mismatch for %s was not cleared: %s", machineID, *mismatchSince)
		}
	}
}
