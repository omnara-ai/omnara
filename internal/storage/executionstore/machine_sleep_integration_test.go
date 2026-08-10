//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func setMachineSandboxURL(t *testing.T, ctx context.Context, store *Store, machineID ID, sandboxURL string) {
	t.Helper()
	if _, err := store.pool.Exec(
		ctx,
		`UPDATE machines SET sandbox_url = $3 WHERE org_id = $1 AND id = $2`,
		testOrgID,
		machineID,
		sandboxURL,
	); err != nil {
		t.Fatalf("set machine sandbox url: %v", err)
	}
}

func startSleepProcess(
	ctx context.Context,
	fixture processDaemonFixture,
	toolCallID ID,
	input executionstore.CreateProcessInput,
) (executionstore.ProcessRecord, error) {
	return startProcessForTest(
		ctx,
		fixture.Store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    toolCallID,
			RuntimeLockID: fixture.Lock.ID,
		},
		input,
	)
}

func TestSleepDaemonRuntimeLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "sleep_lifecycle")
	setMachineSandboxURL(t, ctx, fixture.Store, fixture.MachineID, "https://sleep-lifecycle.test/")

	record, err := fixture.Store.Execution().SleepDaemonRuntime(ctx, fixture.authority())
	if err != nil {
		t.Fatalf("sleep daemon runtime: %v", err)
	}
	if record.State != "ended" || record.StateReasonCode != "machine_asleep" {
		t.Fatalf("sleep runtime record = %s/%s, want ended/machine_asleep", record.State, record.StateReasonCode)
	}
	var intervalEndedAt time.Time
	var intervalEndReason string
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT ended_at, end_reason_code
FROM machine_online_intervals
WHERE org_id = $1 AND machine_id = $2 AND daemon_runtime_id = $3
`, fixture.OrgID, fixture.MachineID, fixture.RuntimeID).Scan(
		&intervalEndedAt,
		&intervalEndReason,
	); err != nil {
		t.Fatalf("load sleeping machine online interval: %v", err)
	}
	if record.EndedAt == nil || !intervalEndedAt.Equal(*record.EndedAt) ||
		intervalEndReason != "machine_asleep" {
		t.Fatalf("sleeping online interval = %s/%s, runtime = %+v", intervalEndedAt, intervalEndReason, record)
	}
	assertMachineState(t, ctx, fixture.Store, fixture.MachineID, "active", "asleep")

	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "sleep_lifecycle", "run_command")
	process, err := startSleepProcess(ctx, fixture, toolCallID, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "echo awake",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process against asleep machine: %v", err)
	}
	if process.State != executionstore.ProcessStateQueued || process.ExecutionGrantedAt != nil {
		t.Fatalf(
			"asleep-machine process = %s granted=%v, want queued with no execution grant",
			process.State,
			process.ExecutionGrantedAt,
		)
	}

	if _, err := fixture.Store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: fixture.DaemonID,
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		},
	); err != nil {
		t.Fatalf("re-register daemon runtime after wake: %v", err)
	}
	assertMachineState(t, ctx, fixture.Store, fixture.MachineID, "active", "online")

	if _, err := fixture.Store.Execution().EndDaemonRuntime(
		ctx,
		fixture.authorityForRuntime(
			activeDaemonRuntimeID(t, ctx, fixture.Store, fixture.OrgID, fixture.MachineID),
		),
	); err != nil {
		t.Fatalf("end daemon runtime: %v", err)
	}
	assertMachineState(t, ctx, fixture.Store, fixture.MachineID, "active", "offline")
}

func activeDaemonRuntimeID(t *testing.T, ctx context.Context, store *Store, orgID, machineID ID) ID {
	t.Helper()
	var id ID
	if err := store.pool.QueryRow(
		ctx,
		`SELECT id FROM daemon_runtimes WHERE org_id = $1 AND machine_id = $2 AND state = 'active'`,
		orgID,
		machineID,
	).Scan(&id); err != nil {
		t.Fatalf("load active daemon runtime: %v", err)
	}
	return id
}

func TestSleepDaemonRuntimeVetoes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "sleep_vetoes")

	if _, err := fixture.Store.Execution().SleepDaemonRuntime(
		ctx,
		fixture.authority(),
	); !errors.Is(err, storeerr.ErrMachineNotWakeCapable) {
		t.Fatalf("sleep without sandbox url = %v, want storeerr.ErrMachineNotWakeCapable", err)
	}
	assertMachineState(t, ctx, fixture.Store, fixture.MachineID, "active", "online")

	setMachineSandboxURL(t, ctx, fixture.Store, fixture.MachineID, "https://sleep-vetoes.test/")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "sleep_vetoes", "run_command")
	if _, err := startSleepProcess(ctx, fixture, toolCallID, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 3600",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	}); err != nil {
		t.Fatalf("start process: %v", err)
	}
	if _, err := fixture.Store.Execution().SleepDaemonRuntime(
		ctx,
		fixture.authority(),
	); !errors.Is(err, storeerr.ErrMachineSleepPendingWork) {
		t.Fatalf("sleep with queued work = %v, want storeerr.ErrMachineSleepPendingWork", err)
	}
	assertMachineState(t, ctx, fixture.Store, fixture.MachineID, "active", "online")
}

func TestAsleepMachineUnreachableExpiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "sleep_unreachable_expiry")
	setMachineSandboxURL(t, ctx, fixture.Store, fixture.MachineID, "https://sleep-unreachable-expiry.test/")

	if _, err := fixture.Store.Execution().SleepDaemonRuntime(
		ctx,
		fixture.authority(),
	); err != nil {
		t.Fatalf("sleep daemon runtime: %v", err)
	}

	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "sleep_unreachable_expiry", "run_command")
	process, err := startSleepProcess(ctx, fixture, toolCallID, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "echo wake",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process against asleep machine: %v", err)
	}
	if _, err := fixture.Store.Execution().ExpireMachineUnreachableProcessToolCallsForAllProjects(
		ctx,
		0,
	); err != nil {
		t.Fatalf("expire at machine-unreachable grace: %v", err)
	}
	current, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get process: %v", err)
	}
	if current.State != executionstore.ProcessStateFailed || current.StateReasonCode != "machine_unreachable" {
		t.Fatalf("expired process = %s/%s, want failed/machine_unreachable", current.State, current.StateReasonCode)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get tool call: %v", err)
	}
	assertCompletedToolCallWithResult(t, fixture.Store, fixture.AgentID, toolCall, "machine_unreachable")
	assertMachineState(t, ctx, fixture.Store, fixture.MachineID, "active", "asleep")
}

func TestFailQueuedProcessAfterWakeFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "sleep_sync_wake_failure")
	setMachineSandboxURL(t, ctx, fixture.Store, fixture.MachineID, "https://sleep-sync-failure.test/")
	if _, err := fixture.Store.Execution().SleepDaemonRuntime(
		ctx,
		fixture.authority(),
	); err != nil {
		t.Fatalf("sleep daemon runtime: %v", err)
	}
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "sleep_sync_wake_failure", "run_command")
	process, err := startSleepProcess(ctx, fixture, toolCallID, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "echo unreachable",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process against asleep machine: %v", err)
	}
	failed, err := fixture.Store.Execution().FailQueuedProcessAfterWakeFailure(
		ctx,
		process,
	)
	if err != nil {
		t.Fatalf("fail queued process after wake failure: %v", err)
	}
	if !failed {
		t.Fatal("queued process was not failed")
	}
	current, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get failed process: %v", err)
	}
	if current.State != executionstore.ProcessStateFailed ||
		current.StateReasonCode != executionstore.ProcessToolReasonMachineUnreachable {
		t.Fatalf(
			"process = %s/%s, want failed/%s",
			current.State,
			current.StateReasonCode,
			executionstore.ProcessToolReasonMachineUnreachable,
		)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("get completed tool call: %v", err)
	}
	assertCompletedToolCallWithResult(
		t,
		fixture.Store,
		fixture.AgentID,
		toolCall,
		executionstore.ProcessToolReasonMachineUnreachable,
	)
	assertMachineState(t, ctx, fixture.Store, fixture.MachineID, "active", "asleep")
}

func TestFailQueuedProcessAfterWakeFailureSkipsRegisteredMachine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "sleep_sync_wake_race")
	setMachineSandboxURL(t, ctx, fixture.Store, fixture.MachineID, "https://sleep-sync-race.test/")
	if _, err := fixture.Store.Execution().SleepDaemonRuntime(
		ctx,
		fixture.authority(),
	); err != nil {
		t.Fatalf("sleep daemon runtime: %v", err)
	}
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "sleep_sync_wake_race", "run_command")
	process, err := startSleepProcess(ctx, fixture, toolCallID, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "echo reachable",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process against asleep machine: %v", err)
	}
	if _, err := fixture.Store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: fixture.DaemonID,
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		},
	); err != nil {
		t.Fatalf("re-register daemon runtime: %v", err)
	}
	failed, err := fixture.Store.Execution().FailQueuedProcessAfterWakeFailure(
		ctx,
		process,
	)
	if err != nil {
		t.Fatalf("check queued process after registration: %v", err)
	}
	if failed {
		t.Fatal("queued process was failed after daemon registration")
	}
	current, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get queued process: %v", err)
	}
	if current.State != executionstore.ProcessStateQueued {
		t.Fatalf("process state = %s, want queued", current.State)
	}
	assertMachineState(t, ctx, fixture.Store, fixture.MachineID, "active", "online")
}

func TestFailQueuedProcessActionAfterWakeFailureIsScoped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "sleep_action_wake_failure_scope")
	setMachineSandboxURL(t, ctx, fixture.Store, fixture.MachineID, "https://sleep-action-wake-failure.test/")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"sleep_action_wake_failure_scope",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("sleep_action_wake_process", "run_command"),
			builtInProcessToolCallBatchItem("sleep_action_wake_first", "read_process"),
			builtInProcessToolCallBatchItem("sleep_action_wake_second", "read_process"),
		},
	)
	process, err := startSleepProcess(ctx, fixture, toolCallIDs[0], executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "echo done",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
	); err != nil || !found {
		t.Fatalf("accept process found=%t err=%v", found, err)
	}
	if _, err := fixture.Store.Execution().MarkProcessStarted(ctx, executionstore.MarkProcessStartedInput{
		Authority:       fixture.authority(),
		ProjectID:       testProjectID,
		AgentID:         fixture.AgentID,
		ID:              process.ID,
		SourceStartedAt: fixture.Now.Add(time.Second),
	}); err != nil {
		t.Fatalf("mark process started: %v", err)
	}
	exitCode := 0
	completed, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, executionstore.CompleteDaemonProcessInput{
		Authority:     fixture.authority(),
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ID:            process.ID,
		State:         executionstore.ProcessStateExited,
		ExitCode:      &exitCode,
		SourceEndedAt: fixture.Now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("complete process: %v", err)
	}
	if _, err := fixture.Store.Execution().SleepDaemonRuntime(ctx, fixture.authority()); err != nil {
		t.Fatalf("sleep daemon runtime: %v", err)
	}
	actions := make([]executionstore.ProcessActionRecord, 2)
	for index, toolCallID := range toolCallIDs[1:] {
		actions[index], err = createProcessActionForTest(
			ctx,
			fixture.Store,
			executionstore.ExecuteToolCallInput{
				ProjectID:     testProjectID,
				AgentID:       fixture.AgentID,
				ToolCallID:    toolCallID,
				RuntimeLockID: fixture.Lock.ID,
			},
			executionstore.CreateProcessActionInput{
				ProcessID:  process.ID,
				ActionKind: executionstore.ProcessActionKindRead,
				Payload:    json.RawMessage(`{"cursor":0}`),
			},
		)
		if err != nil {
			t.Fatalf("create read action %d: %v", index, err)
		}
	}
	failed, err := fixture.Store.Execution().FailQueuedProcessActionsAfterWakeFailure(
		ctx,
		completed.Process,
		actions[0],
	)
	if err != nil || !failed {
		t.Fatalf("fail first action after wake failure failed=%t err=%v", failed, err)
	}
	for index, expectedState := range []executionstore.ProcessActionState{
		executionstore.ProcessActionStateFailed,
		executionstore.ProcessActionStateQueued,
	} {
		current, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
			ctx,
			testProjectID,
			fixture.AgentID,
			toolCallIDs[index+1],
		)
		if err != nil {
			t.Fatalf("get read action %d: %v", index, err)
		}
		if !found || current.ID != actions[index].ID || current.State != expectedState {
			t.Fatalf("read action %d found=%t action=%+v, want state %s", index, found, current, expectedState)
		}
	}
}

func TestAsleepMachineDeletedReadsOffline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "sleep_deleted")
	setMachineSandboxURL(t, ctx, fixture.Store, fixture.MachineID, "https://sleep-deleted.test/")

	if _, err := fixture.Store.Execution().SleepDaemonRuntime(
		ctx,
		fixture.authority(),
	); err != nil {
		t.Fatalf("sleep daemon runtime: %v", err)
	}
	assertMachineState(t, ctx, fixture.Store, fixture.MachineID, "active", "asleep")

	if _, err := fixture.Store.Execution().DeleteMachine(
		ctx,
		executionstore.DeleteMachineInput{
			OrgID:     fixture.OrgID,
			MachineID: fixture.MachineID,
		},
	); err != nil {
		t.Fatalf("delete machine: %v", err)
	}
	assertMachineState(t, ctx, fixture.Store, fixture.MachineID, "deleted", "offline")

}

func TestExecutableBindingsIncludeAsleepMachines(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "sleep_bindings")
	setMachineSandboxURL(t, ctx, fixture.Store, fixture.MachineID, "https://sleep-bindings.test/")

	bindings, err := fixture.Store.Execution().ListExecutableAgentMachineBindings(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("list executable bindings online: %v", err)
	}
	if len(bindings) != 1 {
		t.Fatalf("online executable bindings = %d, want 1", len(bindings))
	}
	if _, err := fixture.Store.Execution().SleepDaemonRuntime(
		ctx,
		fixture.authority(),
	); err != nil {
		t.Fatalf("sleep daemon runtime: %v", err)
	}
	bindings, err = fixture.Store.Execution().ListExecutableAgentMachineBindings(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("list executable bindings asleep: %v", err)
	}
	if len(bindings) != 1 {
		t.Fatalf("asleep executable bindings = %d, want 1", len(bindings))
	}
	if _, err := fixture.Store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: fixture.DaemonID,
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     testDaemonRuntimeLeaseTimeout,
		},
	); err != nil {
		t.Fatalf("re-register daemon runtime: %v", err)
	}
	if _, err := fixture.Store.Execution().EndDaemonRuntime(
		ctx,
		fixture.authorityForRuntime(
			activeDaemonRuntimeID(t, ctx, fixture.Store, fixture.OrgID, fixture.MachineID),
		),
	); err != nil {
		t.Fatalf("end daemon runtime: %v", err)
	}
	bindings, err = fixture.Store.Execution().ListExecutableAgentMachineBindings(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("list executable bindings offline: %v", err)
	}
	if len(bindings) != 0 {
		t.Fatalf("offline executable bindings = %d, want 0", len(bindings))
	}
}

func TestAsleepUnreachableExpiryIsPerItemFromWorkArrival(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "sleep_expiry_per_item")
	setMachineSandboxURL(t, ctx, fixture.Store, fixture.MachineID, "https://sleep-expiry-per-item.test/")
	if _, err := fixture.Store.Execution().SleepDaemonRuntime(
		ctx,
		fixture.authority(),
	); err != nil {
		t.Fatalf("sleep daemon runtime: %v", err)
	}

	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"sleep_expiry_per_item",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("sleep_expiry_first", "run_command"),
			builtInProcessToolCallBatchItem("sleep_expiry_second", "run_command"),
		},
	)
	firstToolCallID := toolCallIDs[0]
	firstProcess, err := startSleepProcess(ctx, fixture, firstToolCallID, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "echo first",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start first process: %v", err)
	}
	if _, err := fixture.Store.pool.Exec(
		ctx,
		`UPDATE daemon_runtimes
		 SET last_seen_at = statement_timestamp() - interval '3 minutes',
		     ended_at = statement_timestamp() - interval '2 minutes',
		     lease_expires_at = statement_timestamp() - interval '2 minutes',
		     updated_at = statement_timestamp()
		 WHERE org_id = $1 AND machine_id = $2 AND id = $3`,
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
	); err != nil {
		t.Fatalf("age sleeping daemon runtime: %v", err)
	}
	if _, err := fixture.Store.pool.Exec(
		ctx,
		`UPDATE processes
		 SET created_at = statement_timestamp() - interval '1 minute',
		     state_changed_at = statement_timestamp() - interval '1 minute',
		     updated_at = statement_timestamp()
		 WHERE project_id = $1 AND agent_id = $2 AND id = $3`,
		testProjectID,
		fixture.AgentID,
		firstProcess.ID,
	); err != nil {
		t.Fatalf("age first process: %v", err)
	}
	secondToolCallID := toolCallIDs[1]
	secondProcess, err := startSleepProcess(ctx, fixture, secondToolCallID, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "echo second",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start second process: %v", err)
	}
	eligible, err := fixture.Store.q.ListMachineUnreachableQueuedProcessToolCallsForMachine(
		ctx,
		dbsqlc.ListMachineUnreachableQueuedProcessToolCallsForMachineParams{
			OrgID:                          fixture.OrgID,
			MachineID:                      fixture.MachineID,
			MachineUnreachableGraceSeconds: 30,
			LimitCount:                     500,
		},
	)
	if err != nil {
		t.Fatalf("list eligible queued processes: %v", err)
	}
	if len(eligible) != 1 || eligible[0].ID != firstProcess.ID {
		t.Fatalf("eligible queued processes = %v, want only first process", eligible)
	}
	if _, err := fixture.Store.Execution().ExpireMachineUnreachableProcessToolCallsForAllProjects(
		ctx,
		30*time.Second,
	); err != nil {
		t.Fatalf("expire at first-item unreachable grace: %v", err)
	}
	first, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, firstProcess.ID)
	if err != nil {
		t.Fatalf("get first process: %v", err)
	}
	if first.State != executionstore.ProcessStateFailed {
		t.Fatalf("first process = %s, want failed at its unreachable grace", first.State)
	}
	second, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, secondProcess.ID)
	if err != nil {
		t.Fatalf("get second process: %v", err)
	}
	if second.State != "queued" {
		t.Fatalf("second process = %s, want still queued (its own grace has not elapsed)", second.State)
	}
	assertMachineState(t, ctx, fixture.Store, fixture.MachineID, "active", "asleep")
}

func TestSleepRacingStartProcessKeepsWorkWakeable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "sleep_race_start")
	setMachineSandboxURL(t, ctx, fixture.Store, fixture.MachineID, "https://sleep-race-start.test/")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "sleep_race_start", "run_command")

	sleepErrs := make(chan error, 1)
	startErrs := make(chan error, 1)
	go func() {
		_, err := fixture.Store.Execution().SleepDaemonRuntime(ctx, fixture.authority())
		sleepErrs <- err
	}()
	go func() {
		_, err := startSleepProcess(ctx, fixture, toolCallID, executionstore.CreateProcessInput{
			AgentMachineBindingID: fixture.BindingID,
			Command:               "echo race",
			ShellSelector:         "sh",
			Cwd:                   "/work",
		})
		startErrs <- err
	}()
	sleepErr := <-sleepErrs
	if err := <-startErrs; err != nil {
		t.Fatalf("racing start process must always land: %v", err)
	}
	if sleepErr != nil && !errors.Is(sleepErr, storeerr.ErrMachineSleepPendingWork) {
		t.Fatalf("racing sleep = %v, want success or pending-work veto", sleepErr)
	}
	if sleepErr == nil {
		assertMachineState(t, ctx, fixture.Store, fixture.MachineID, "active", "asleep")
	}
}
