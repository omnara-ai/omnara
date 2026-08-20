//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/patch"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

type idlePoolMachineFixture struct {
	processDaemonFixture
	PoolID ID
}

type idlePoolMachinePolicy struct {
	PoolMinutes    *int
	GrantMinutes   *int
	BindingMinutes *int
}

func newIdlePoolMachineFixture(
	t *testing.T,
	ctx context.Context,
	testName string,
	policy idlePoolMachinePolicy,
) idlePoolMachineFixture {
	t.Helper()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithMachinePoolProviders(mergingMachinePoolProviders{}))
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	user := mustCreateProjectOperatorUser(
		t,
		ctx,
		store,
		"idle-"+testName+"@example.com",
		"Idle Machine Tester",
	)
	machinePool := createLaunchTestMachinePool(
		t,
		ctx,
		store,
		"Idle "+testName,
		"test",
		defaultMachineFieldsForTest{DefaultMachineProviderOptions: json.RawMessage(`{"image":"test"}`)},
		1,
		now,
	)
	if policy.PoolMinutes != nil {
		updatedPool, err := store.Execution().UpdateMachinePool(ctx, executionstore.UpdateMachinePoolInput{
			OrgID: testOrgID,
			ID:    machinePool.ID,
			DeleteAfterIdleMinutes: patch.NullableInt{
				Set: true, Value: policy.PoolMinutes,
			},
		})
		if err != nil {
			t.Fatalf("set idle pool policy: %v", err)
		}
		machinePool = updatedPool
	}
	if !sameIntPtr(machinePool.DeleteAfterIdleMinutes, policy.PoolMinutes) {
		t.Fatalf("idle pool policy = %v, want %v", machinePool.DeleteAfterIdleMinutes, policy.PoolMinutes)
	}
	poolGrant, err := store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:                  testOrgID,
			ProjectID:              testProjectID,
			MachinePoolID:          machinePool.ID,
			DeleteAfterIdleMinutes: policy.GrantMinutes,
			IdempotencyKey:         "idle-pool-grant-" + testName,
		},
	)
	if err != nil {
		t.Fatalf("create idle pool grant: %v", err)
	}
	if !sameIntPtr(poolGrant.DeleteAfterIdleMinutes, policy.GrantMinutes) {
		t.Fatalf("idle pool grant policy = %v, want %v", poolGrant.DeleteAfterIdleMinutes, policy.GrantMinutes)
	}
	machineSource := "  - machine_pool_name: " + machinePool.Name + "\n"
	if policy.BindingMinutes != nil {
		machineSource += fmt.Sprintf("    delete_after_idle_minutes: %d\n", *policy.BindingMinutes)
	}
	profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "idle-"+testName, "Idle "+testName, `
instruction: Run idle deletion tests.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
`+machineSource+`tools:
  run_command: {}
  write_process: {}
`, now)
	launch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "idle-pool-agent-" + testName,
	})
	if err != nil {
		t.Fatalf("launch idle pool agent: %v", err)
	}
	binding := launch.MachineBindings[0]
	if !sameIntPtr(binding.DeleteAfterIdleMinutes, policy.BindingMinutes) {
		t.Fatalf("idle machine binding policy = %v, want %v", binding.DeleteAfterIdleMinutes, policy.BindingMinutes)
	}
	claim, claimed, err := store.Execution().ClaimPoolMachineForProvisioning(
		ctx,
		testOrgID,
		binding.MachineID,
	)
	if err != nil || !claimed {
		t.Fatalf("claim idle pool machine provisioning claimed=%v err=%v", claimed, err)
	}
	provisioning, err := store.Execution().BeginPoolMachineProviderProvisioning(
		ctx,
		executionstore.BeginPoolMachineProviderProvisioningInput{
			OrgID:            testOrgID,
			MachineID:        binding.MachineID,
			ProvisionAttempt: claim.Machine.ProvisionAttempts,
			TokenName:        "idle test bootstrap",
		},
	)
	if err != nil {
		t.Fatalf("begin idle pool machine provisioning: %v", err)
	}
	resourceID := "idle-resource-" + testName
	recordPoolMachineProvisioningResourceForTest(
		t,
		ctx,
		store,
		binding.MachineID,
		claim.Machine.ProvisionAttempts,
		resourceID,
	)
	if err := store.Execution().CompletePoolMachineProvisioning(
		ctx,
		testOrgID,
		binding.MachineID,
		resourceID,
		"",
		claim.Machine.ProvisionAttempts,
	); err != nil {
		t.Fatalf("complete idle pool machine provisioning: %v", err)
	}
	if _, err := store.Execution().BootstrapMachineDaemon(ctx, executionstore.MachineDaemonBootstrapInput{
		OrgID:         testOrgID,
		MachineID:     binding.MachineID,
		DaemonTokenID: provisioning.DaemonToken.Record.ID,
	}); err != nil {
		t.Fatalf("bootstrap idle pool machine daemon: %v", err)
	}
	runtime, err := store.Execution().RegisterDaemonRuntime(ctx, executionstore.RegisterDaemonRuntimeInput{
		OrgID:            testOrgID,
		MachineID:        binding.MachineID,
		DaemonTokenID:    provisioning.DaemonToken.Record.ID,
		DaemonInstanceID: testID("idle-daemon-" + testName),
		DaemonVersion:    "1.0.0",
		LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
	})
	if err != nil {
		t.Fatalf("register idle pool machine daemon: %v", err)
	}
	lock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		launch.Agent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire idle pool agent runtime lock: %v", err)
	}
	return idlePoolMachineFixture{
		processDaemonFixture: processDaemonFixture{
			Store:     store,
			OrgID:     testOrgID,
			AgentID:   launch.Agent.ID,
			MachineID: binding.MachineID,
			BindingID: binding.ID,
			TokenID:   provisioning.DaemonToken.Record.ID,
			RuntimeID: runtime.ID,
			DaemonID:  runtime.DaemonInstanceID,
			UserID:    user.ID,
			Lock:      lock,
			Now:       now,
		},
		PoolID: machinePool.ID,
	}
}

func backdateIdleMachineForTest(
	t *testing.T,
	ctx context.Context,
	fixture idlePoolMachineFixture,
	duration time.Duration,
) {
	t.Helper()
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE machines
SET lifecycle_changed_at = statement_timestamp() - $2::bigint * interval '1 second'
WHERE org_id = $1 AND id = $3
`, fixture.OrgID, int64(duration/time.Second), fixture.MachineID); err != nil {
		t.Fatalf("backdate idle machine: %v", err)
	}
}

func idleMachineListedForTest(
	t *testing.T,
	ctx context.Context,
	fixture idlePoolMachineFixture,
) bool {
	t.Helper()
	candidates, err := fixture.Store.Execution().ListExpiredIdlePoolMachines(ctx, 40)
	if err != nil {
		t.Fatalf("list expired idle pool machines: %v", err)
	}
	for _, candidate := range candidates {
		if candidate.OrgID == fixture.OrgID && candidate.MachineID == fixture.MachineID {
			return true
		}
	}
	return false
}

func createQueuedProcessActionForIdleTest(
	t *testing.T,
	ctx context.Context,
	fixture idlePoolMachineFixture,
	testName string,
) (executionstore.ProcessRecord, executionstore.ProcessActionRecord) {
	t.Helper()
	processToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture.processDaemonFixture,
		testName+"-process",
		"run_command",
	)
	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    processToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "cat",
		ShellSelector:         "sh",
	})
	if err != nil {
		t.Fatalf("start idle test process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
	); err != nil || !found {
		t.Fatalf("accept idle test process found=%v err=%v", found, err)
	}
	if _, err := fixture.Store.Execution().MarkProcessStarted(ctx, executionstore.MarkProcessStartedInput{
		ProjectID:       testProjectID,
		AgentID:         fixture.AgentID,
		ID:              process.ID,
		Authority:       fixture.authority(),
		SourceStartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("mark idle test process started: %v", err)
	}
	actionToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture.processDaemonFixture,
		testName+"-action",
		"write_process",
	)
	action, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    actionToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindWrite,
		Payload:    json.RawMessage(`{"data":"hello"}`),
	})
	if err != nil {
		t.Fatalf("create idle test process action: %v", err)
	}
	return process, action
}

func TestAttachedPoolMachineBindingIsExclusive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newIdlePoolMachineFixture(t, ctx, "binding-exclusive", idlePoolMachinePolicy{})
	secondAgentID := mustCreateAgent(t, ctx, fixture.Store, fixture.Now.Add(time.Millisecond))
	var machineGrantID ID
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT id
FROM project_machine_grants
WHERE org_id = $1 AND machine_id = $2 AND source_kind = 'pool'
`, fixture.OrgID, fixture.MachineID).Scan(&machineGrantID); err != nil {
		t.Fatalf("load pool machine grant: %v", err)
	}
	if _, err := executionstore.IntegrationInsertAgentMachineBindingTx(
		ctx,
		fixture.Store.q,
		executionstore.IntegrationInsertAgentMachineBindingInput{
			ProjectID:             testProjectID,
			AgentID:               secondAgentID,
			ProjectMachineGrantID: machineGrantID,
			MachineRef:            "mchr-idl002",
			BindingKind:           executionstore.MachineBindingKindPool,
		},
	); !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("second attached pool binding error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestExpiredIdlePoolMachinePolicyResolution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	zero := 0
	five := 5
	twenty := 20
	tests := []struct {
		name   string
		policy idlePoolMachinePolicy
		want   bool
	}{
		{name: "disabled", want: false},
		{name: "pool", policy: idlePoolMachinePolicy{PoolMinutes: &five}, want: true},
		{name: "grant_override", policy: idlePoolMachinePolicy{PoolMinutes: &five, GrantMinutes: &twenty}, want: false},
		{name: "grant_disabled", policy: idlePoolMachinePolicy{PoolMinutes: &five, GrantMinutes: &zero}, want: false},
		{name: "binding_disabled", policy: idlePoolMachinePolicy{PoolMinutes: &five, GrantMinutes: &five, BindingMinutes: &zero}, want: false},
		{name: "binding_override", policy: idlePoolMachinePolicy{PoolMinutes: &twenty, GrantMinutes: &twenty, BindingMinutes: &five}, want: true},
		{name: "binding_reenables", policy: idlePoolMachinePolicy{PoolMinutes: &five, GrantMinutes: &zero, BindingMinutes: &five}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIdlePoolMachineFixture(t, ctx, "policy-resolution-"+test.name, test.policy)
			backdateIdleMachineForTest(t, ctx, fixture, 10*time.Minute)
			if got := idleMachineListedForTest(t, ctx, fixture); got != test.want {
				t.Fatalf("idle machine listed = %v, want %v", got, test.want)
			}
		})
	}
}

func TestExpiredIdlePoolMachineProcessAndActionActivity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	five := 5
	fixture := newIdlePoolMachineFixture(
		t,
		ctx,
		"process-activity",
		idlePoolMachinePolicy{PoolMinutes: &five},
	)
	backdateIdleMachineForTest(t, ctx, fixture, 20*time.Minute)
	process, action := createQueuedProcessActionForIdleTest(t, ctx, fixture, "idle-process-activity")
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE processes
SET state = 'unknown',
    state_reason_code = 'test_unknown',
    state_changed_at = statement_timestamp() - interval '20 minutes',
    last_activity_at = statement_timestamp() - interval '20 minutes'
WHERE id = $1
`, process.ID); err != nil {
		t.Fatalf("make process nonblocking: %v", err)
	}
	if !idleMachineListedForTest(t, ctx, fixture) {
		if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE process_actions
SET state = 'accepted',
    updated_at = statement_timestamp() - interval '20 minutes'
WHERE id = $1
`, action.ID); err != nil {
			t.Fatalf("accept process action: %v", err)
		}
		if idleMachineListedForTest(t, ctx, fixture) {
			t.Fatal("accepted process action did not block idle deletion")
		}
		if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE process_actions
SET state = 'unknown',
    state_reason_code = 'test_unknown',
    updated_at = statement_timestamp() - interval '20 minutes'
WHERE id = $1
`, action.ID); err != nil {
			t.Fatalf("make process action nonblocking: %v", err)
		}
	} else {
		t.Fatal("queued process action did not block idle deletion")
	}
	if !idleMachineListedForTest(t, ctx, fixture) {
		t.Fatal("unknown process and action should not block idle deletion")
	}
	for _, state := range []string{"queued", "starting", "running"} {
		if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE processes
SET state = $2,
    execution_granted_at = CASE WHEN $2 = 'queued' THEN NULL ELSE statement_timestamp() END,
    source_started_at = CASE WHEN $2 = 'running' THEN statement_timestamp() ELSE NULL END,
    source_ended_at = NULL,
    state_reason_code = NULL,
    state_reason_message = '',
    exit_code = NULL,
    state_changed_at = statement_timestamp() - interval '20 minutes',
    last_activity_at = statement_timestamp() - interval '20 minutes'
WHERE id = $1
`, process.ID, state); err != nil {
			t.Fatalf("make process %s: %v", state, err)
		}
		if idleMachineListedForTest(t, ctx, fixture) {
			t.Fatalf("%s process did not block idle deletion", state)
		}
	}
}

func TestTerminalProcessActionRestartsIdleWindow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	five := 5
	tests := []struct {
		name  string
		state executionstore.ProcessActionState
	}{
		{name: "applied", state: executionstore.ProcessActionStateApplied},
		{name: "failed", state: executionstore.ProcessActionStateFailed},
		{name: "unknown", state: executionstore.ProcessActionStateUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIdlePoolMachineFixture(
				t,
				ctx,
				test.name+"-action",
				idlePoolMachinePolicy{PoolMinutes: &five},
			)
			backdateIdleMachineForTest(t, ctx, fixture, 20*time.Minute)
			process, action := createQueuedProcessActionForIdleTest(
				t,
				ctx,
				fixture,
				"idle-"+test.name+"-action",
			)
			if _, found, err := acceptDaemonProcessActionForTest(
				ctx,
				fixture.Store,
				fixture.OrgID,
				fixture.MachineID,
				fixture.RuntimeID,
				process.ID,
				action.ID,
			); err != nil || !found {
				t.Fatalf("accept idle test action found=%v err=%v", found, err)
			}
			if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE processes SET last_activity_at = statement_timestamp() - interval '20 minutes' WHERE id = $1
`, process.ID); err != nil {
				t.Fatalf("backdate process activity: %v", err)
			}
			var before, updatedAtBefore time.Time
			if err := fixture.Store.pool.QueryRow(ctx, `
SELECT last_activity_at, updated_at FROM processes WHERE id = $1
`, process.ID).Scan(&before, &updatedAtBefore); err != nil {
				t.Fatalf("load process activity before action: %v", err)
			}
			input := executionstore.CompleteDaemonProcessActionInput{
				ProjectID: testProjectID,
				AgentID:   fixture.AgentID,
				ProcessID: process.ID,
				ID:        action.ID,
				Authority: fixture.authority(),
			}
			if test.state != executionstore.ProcessActionStateApplied {
				input.StateReasonCode = "test_" + test.name
			}
			var err error
			switch test.state {
			case executionstore.ProcessActionStateApplied:
				_, err = fixture.Store.Execution().ApplyDaemonProcessAction(ctx, input)
			case executionstore.ProcessActionStateFailed:
				_, err = fixture.Store.Execution().FailDaemonProcessAction(ctx, input)
			case executionstore.ProcessActionStateUnknown:
				_, err = fixture.Store.Execution().MarkDaemonProcessActionUnknown(ctx, input)
			}
			if err != nil {
				t.Fatalf("complete idle test action as %s: %v", test.state, err)
			}
			var after, updatedAtAfter time.Time
			if err := fixture.Store.pool.QueryRow(ctx, `
SELECT last_activity_at, updated_at FROM processes WHERE id = $1
`, process.ID).Scan(&after, &updatedAtAfter); err != nil {
				t.Fatalf("load process activity after action: %v", err)
			}
			if !after.After(before) {
				t.Fatalf("process activity after %s action = %s, want after %s", test.state, after, before)
			}
			if !updatedAtAfter.Equal(updatedAtBefore) {
				t.Fatalf("process updated_at after %s action = %s, want unchanged at %s", test.state, updatedAtAfter, updatedAtBefore)
			}
			if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE processes
SET state = 'unknown', state_reason_code = 'test_unknown', state_changed_at = statement_timestamp()
WHERE id = $1
`, process.ID); err != nil {
				t.Fatalf("make %s-action process nonblocking: %v", test.state, err)
			}
			if idleMachineListedForTest(t, ctx, fixture) {
				t.Fatalf("recent %s action did not restart idle window", test.state)
			}
			if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE processes SET last_activity_at = statement_timestamp() - interval '20 minutes' WHERE id = $1
`, process.ID); err != nil {
				t.Fatalf("expire process activity: %v", err)
			}
			if !idleMachineListedForTest(t, ctx, fixture) {
				t.Fatalf("expired %s action activity kept machine alive", test.state)
			}
		})
	}
}

func TestProcessFailureBeforeExecutionRestartsIdleWindow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	five := 5
	fixture := newIdlePoolMachineFixture(
		t,
		ctx,
		"process-start-failure",
		idlePoolMachinePolicy{PoolMinutes: &five},
	)
	backdateIdleMachineForTest(t, ctx, fixture, 20*time.Minute)
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture.processDaemonFixture,
		"idle-process-start-failure",
		"run_command",
	)
	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "cat",
		ShellSelector:         "sh",
	})
	if err != nil {
		t.Fatalf("start idle test process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
	); err != nil || !found {
		t.Fatalf("accept idle test process found=%v err=%v", found, err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE processes
SET last_activity_at = statement_timestamp() - interval '20 minutes'
WHERE id = $1
`, process.ID); err != nil {
		t.Fatalf("backdate accepted process activity: %v", err)
	}
	if _, err := fixture.Store.Execution().RegisterDaemonRuntimeWithReconciliation(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: fixture.DaemonID,
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
			ProcessClaims: []executionstore.ProcessReconciliationClaim{{
				ProcessID:            process.ID,
				SupervisorInstanceID: "idle-process-start-failure-supervisor",
				Phase:                daemonprotocol.ProcessPhasePrepared,
			}},
		},
	); err != nil {
		t.Fatalf("fail process before execution through reconciliation: %v", err)
	}
	if _, claimed, err := fixture.Store.Execution().ClaimExpiredIdlePoolMachineDeletion(
		ctx,
		fixture.OrgID,
		fixture.MachineID,
	); err != nil || claimed {
		t.Fatalf("claim after recent process start failure claimed=%v err=%v", claimed, err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE processes SET last_activity_at = statement_timestamp() - interval '20 minutes' WHERE id = $1
`, process.ID); err != nil {
		t.Fatalf("expire process start failure activity: %v", err)
	}
	if _, claimed, err := fixture.Store.Execution().ClaimExpiredIdlePoolMachineDeletion(
		ctx,
		fixture.OrgID,
		fixture.MachineID,
	); err != nil || !claimed {
		t.Fatalf("claim after expired process start failure claimed=%v err=%v", claimed, err)
	}
}

func TestExpiredIdlePoolMachineClaimRespectsWakeAttemptLease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	five := 5
	fixture := newIdlePoolMachineFixture(
		t,
		ctx,
		"claim-wake-attempt",
		idlePoolMachinePolicy{PoolMinutes: &five},
	)
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE machines SET sandbox_url = 'https://idle-wake.example.com' WHERE org_id = $1 AND id = $2
`, fixture.OrgID, fixture.MachineID); err != nil {
		t.Fatalf("set idle machine sandbox URL: %v", err)
	}
	if _, err := fixture.Store.Execution().SleepDaemonRuntime(
		ctx,
		executionstore.DaemonRuntimeAuthority{
			OrgID:           fixture.OrgID,
			MachineID:       fixture.MachineID,
			DaemonRuntimeID: fixture.RuntimeID,
			DaemonTokenID:   fixture.TokenID,
		},
	); err != nil {
		t.Fatalf("sleep idle machine runtime: %v", err)
	}
	backdateIdleMachineForTest(t, ctx, fixture, 10*time.Minute)
	if !idleMachineListedForTest(t, ctx, fixture) {
		t.Fatal("idle machine was not initially eligible")
	}
	disposition, err := fixture.Store.Execution().BeginMachineWake(
		ctx,
		fixture.OrgID,
		fixture.MachineID,
		fixture.PoolID,
		5*time.Minute,
	)
	if err != nil || disposition != executionstore.MachineWakeReady {
		t.Fatalf("begin idle machine wake disposition=%v err=%v", disposition, err)
	}
	if idleMachineListedForTest(t, ctx, fixture) {
		t.Fatal("idle machine with active wake attempt was listed")
	}
	var activeWakeExpiresAt time.Time
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT wake_attempt_expires_at FROM machines WHERE org_id = $1 AND id = $2
`, fixture.OrgID, fixture.MachineID).Scan(&activeWakeExpiresAt); err != nil {
		t.Fatalf("read active wake attempt: %v", err)
	}
	if _, claimed, err := fixture.Store.Execution().ClaimExpiredIdlePoolMachineDeletion(
		ctx,
		fixture.OrgID,
		fixture.MachineID,
	); err != nil || claimed {
		t.Fatalf("claim during active wake attempt claimed=%v err=%v", claimed, err)
	}
	var lifecycleState string
	var preservedWakeExpiresAt time.Time
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT lifecycle_state, wake_attempt_expires_at
FROM machines
WHERE org_id = $1 AND id = $2
`, fixture.OrgID, fixture.MachineID).Scan(&lifecycleState, &preservedWakeExpiresAt); err != nil {
		t.Fatalf("read machine after active wake claim: %v", err)
	}
	if lifecycleState != "active" || !preservedWakeExpiresAt.Equal(activeWakeExpiresAt) {
		t.Fatalf(
			"machine after active wake claim state=%q wake_expires_at=%v, want active/%v",
			lifecycleState,
			preservedWakeExpiresAt,
			activeWakeExpiresAt,
		)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE machines
SET wake_attempt_expires_at = statement_timestamp() - interval '1 millisecond'
WHERE org_id = $1 AND id = $2
`, fixture.OrgID, fixture.MachineID); err != nil {
		t.Fatalf("expire idle machine wake attempt: %v", err)
	}
	if !idleMachineListedForTest(t, ctx, fixture) {
		t.Fatal("idle machine with expired wake attempt was not listed")
	}
	claim, claimed, err := fixture.Store.Execution().ClaimExpiredIdlePoolMachineDeletion(
		ctx,
		fixture.OrgID,
		fixture.MachineID,
	)
	if err != nil || !claimed {
		t.Fatalf("claim after expired wake attempt claimed=%v err=%v", claimed, err)
	}
	if claim.Machine.LifecycleState != "deleting" || claim.Machine.LifecycleReasonCode != "idle_timeout" {
		t.Fatalf("claimed idle machine = %+v, want deleting/idle_timeout", claim.Machine)
	}
}

func TestExpiredIdlePoolMachineClaimRechecksPolicyAfterLockWait(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	five := 5
	fixture := newIdlePoolMachineFixture(
		t,
		ctx,
		"claim-policy-recheck",
		idlePoolMachinePolicy{PoolMinutes: &five},
	)
	backdateIdleMachineForTest(t, ctx, fixture, 10*time.Minute)
	if !idleMachineListedForTest(t, ctx, fixture) {
		t.Fatal("idle machine was not initially eligible")
	}
	blockingTx, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin idle claim blocker: %v", err)
	}
	defer func() { _ = blockingTx.Rollback(ctx) }()
	var blockingPID int32
	if err := blockingTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockingPID); err != nil {
		t.Fatalf("get idle claim blocker pid: %v", err)
	}
	if _, err := blockingTx.Exec(ctx, `SELECT id FROM machines WHERE id = $1 FOR UPDATE`, fixture.MachineID); err != nil {
		t.Fatalf("lock idle claim machine: %v", err)
	}
	type claimResult struct {
		claimed bool
		err     error
	}
	done := make(chan claimResult, 1)
	go func() {
		_, claimed, claimErr := fixture.Store.Execution().ClaimExpiredIdlePoolMachineDeletion(
			context.Background(),
			fixture.OrgID,
			fixture.MachineID,
		)
		done <- claimResult{claimed: claimed, err: claimErr}
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		fixture.Store.pool,
		"-- name: LockMachineForLifecycle",
		blockingPID,
	)
	thirty := 30
	if _, err := fixture.Store.Execution().UpdateMachinePool(ctx, executionstore.UpdateMachinePoolInput{
		OrgID: fixture.OrgID,
		ID:    fixture.PoolID,
		DeleteAfterIdleMinutes: patch.NullableInt{
			Set: true, Value: &thirty,
		},
	}); err != nil {
		t.Fatalf("change idle policy while claim waits: %v", err)
	}
	if err := blockingTx.Commit(ctx); err != nil {
		t.Fatalf("commit idle claim blocker: %v", err)
	}
	result := <-done
	if result.err != nil || result.claimed {
		t.Fatalf("idle claim after policy change claimed=%v err=%v", result.claimed, result.err)
	}
}

func TestExpiredIdlePoolMachineClaimRequiresObservation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	five := 5
	fixture := newIdlePoolMachineFixture(
		t,
		ctx,
		"claim-requires-observation",
		idlePoolMachinePolicy{PoolMinutes: &five},
	)
	backdateIdleMachineForTest(t, ctx, fixture, 10*time.Minute)
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE machines SET last_observed_at = NULL WHERE org_id = $1 AND id = $2
`, fixture.OrgID, fixture.MachineID); err != nil {
		t.Fatalf("clear idle machine observation: %v", err)
	}
	if idleMachineListedForTest(t, ctx, fixture) {
		t.Fatal("unobserved idle machine was listed")
	}
	if _, claimed, err := fixture.Store.Execution().ClaimExpiredIdlePoolMachineDeletion(
		ctx,
		fixture.OrgID,
		fixture.MachineID,
	); err != nil || claimed {
		t.Fatalf("claim unobserved idle machine claimed=%v err=%v", claimed, err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE machines SET last_observed_at = statement_timestamp() WHERE org_id = $1 AND id = $2
`, fixture.OrgID, fixture.MachineID); err != nil {
		t.Fatalf("observe idle machine: %v", err)
	}
	claim, claimed, err := fixture.Store.Execution().ClaimExpiredIdlePoolMachineDeletion(
		ctx,
		fixture.OrgID,
		fixture.MachineID,
	)
	if err != nil || !claimed {
		t.Fatalf("claim observed idle machine claimed=%v err=%v", claimed, err)
	}
	if claim.Machine.LifecycleState != "deleting" || claim.Machine.LifecycleReasonCode != "idle_timeout" {
		t.Fatalf("claimed idle machine = %+v, want deleting/idle_timeout", claim.Machine)
	}
}
