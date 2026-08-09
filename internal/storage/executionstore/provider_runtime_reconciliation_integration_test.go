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
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
)

func TestProviderRuntimeCandidateStorageLifecycleAndPagination(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProviderRuntimeStorageFixture(t, ctx, "candidate-lifecycle", true)
	first := fixture.insertInactiveMachine(t, ctx, "first")
	second := fixture.insertInactiveMachine(t, ctx, "second")

	// These rows exercise the discovery exclusions without widening the result.
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
	markedAt, marked, err := fixture.store.Execution().MarkProviderRuntimeMismatch(ctx, candidate)
	if err != nil || !marked || markedAt.IsZero() {
		t.Fatalf("mark provider runtime mismatch = (%s, %t, %v)", markedAt, marked, err)
	}
	if _, marked, err := fixture.store.Execution().MarkProviderRuntimeMismatch(ctx, candidate); err != nil || marked {
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
	if _, marked, err := fixture.store.Execution().MarkProviderRuntimeMismatch(
		ctx,
		secondCandidate,
	); err != nil || !marked {
		t.Fatalf("mark second provider runtime mismatch = (%t, %v)", marked, err)
	}
	fixture.backdateMismatch(t, ctx, candidate.MachineID)
	fixture.backdateMismatch(t, ctx, secondCandidate.MachineID)
	var due []executionstore.ProviderRuntimeCandidate
	dueCursor := executionstore.ListDueProviderRuntimeMismatchesInput{
		ListProviderRuntimeCandidatesInput: executionstore.ListProviderRuntimeCandidatesInput{Limit: 1},
		ConfirmationGrace:                  time.Millisecond,
		InactivityGrace:                    time.Millisecond,
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
		dueCursor.AfterMachineID = page[0].MachineID
		dueCursor.SourceAfterMismatchSince = *page[0].ProviderRuntimeMismatchSince
	}
	if len(due) != 2 {
		t.Fatalf("due mismatch candidates = %+v", due)
	}
	cleared, err := fixture.store.Execution().ClearProviderRuntimeMismatch(ctx, due[0])
	if err != nil || !cleared {
		t.Fatalf("clear mismatch = (%t, %v)", cleared, err)
	}
	if cleared, err := fixture.store.Execution().ClearProviderRuntimeMismatch(ctx, due[0]); err != nil || cleared {
		t.Fatalf("repeat mismatch clear = (%t, %v), want fenced no-op", cleared, err)
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
	if _, marked, err := fixture.store.Execution().MarkProviderRuntimeMismatch(
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

func TestProviderRuntimeMismatchDeletionClaimFencesConcurrentChanges(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(context.Context, *testing.T, providerRuntimeStorageFixture, providerRuntimeMachine)
	}{
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
			name: "pool configuration change",
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
			); err != nil || claimed {
				t.Fatalf("stale provider runtime claim = (%t, %v), want fenced no-op", claimed, err)
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
					RuntimeProtectionEnabled: boolPtrForMachinePoolTest(protected),
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
	inactiveSince := time.Now().UTC().Add(-10 * time.Minute)
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
    $1, $2, $3, $4, $5, 'test-daemon', 'ended', 'daemon_sleep',
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
	}
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
	if _, marked, err := f.store.Execution().MarkProviderRuntimeMismatch(ctx, candidate); err != nil || !marked {
		t.Fatalf("mark provider runtime mismatch = (%t, %v)", marked, err)
	}
	f.backdateMismatch(t, ctx, machineID)
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
