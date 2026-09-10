//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func startQueuedExpiryProcess(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
) executionstore.ProcessRecord {
	t.Helper()
	toolID := createToolCallForProcessTest(t, ctx, fixture, t.Name(), "run_command")
	return startQueuedExpiryProcessForTool(t, ctx, fixture, toolID)
}

func startQueuedExpiryProcessForTool(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	toolID ID,
) executionstore.ProcessRecord {
	t.Helper()
	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, ToolCallID: toolID, RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "true",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	require.NoError(t, err)
	return process
}

func ageQueuedExpiryProcess(t *testing.T, ctx context.Context, fixture processDaemonFixture, processID ID) {
	t.Helper()
	_, err := fixture.Store.pool.Exec(
		ctx,
		`UPDATE processes SET created_at = statement_timestamp() - ($2::int * interval '1 second') WHERE id = $1`,
		processID, int32((executionstore.ProcessQueueTimeout+time.Minute)/time.Second))
	require.NoError(t, err)
}

func TestProcessOffersSkipExpiredQueueWithoutMaintenance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, t.Name())
	ids := createToolCallBatchForProcessTest(t, ctx, fixture, t.Name(), []processToolCallBatchItem{
		builtInProcessToolCallBatchItem("expired0", "run_command"),
		builtInProcessToolCallBatchItem("expired1", "run_command"),
		builtInProcessToolCallBatchItem("expired2", "run_command"),
		builtInProcessToolCallBatchItem("expired3", "run_command"),
		builtInProcessToolCallBatchItem("fresh", "run_command"),
	})
	var freshID ID
	for index, toolID := range ids {
		process := startQueuedExpiryProcessForTool(t, ctx, fixture, toolID)
		if index < len(ids)-1 {
			ageQueuedExpiryProcess(t, ctx, fixture, process.ID)
		} else {
			freshID = process.ID
		}
	}
	input := executionstore.DaemonWorkInput{Authority: fixture.authority(), Limit: 4}
	offers, err := fixture.Store.Execution().ListDaemonProcessOffers(ctx, input)
	require.NoError(t, err)
	require.Len(t, offers, 1)
	require.Equal(t, freshID, offers[0].Process.ID)
	_, granted, err := fixture.Store.Execution().AcceptDaemonProcess(ctx, executionstore.AcceptDaemonProcessInput{
		Authority: fixture.authority(), ProcessID: freshID,
	})
	require.NoError(t, err)
	require.True(t, granted)
	offers, err = fixture.Store.Execution().ListDaemonProcessOffers(ctx, input)
	require.NoError(t, err)
	require.Empty(t, offers)
}

func TestProcessQueueExpiryReasonPrecedenceOnOfflineMachine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                 string
		wakeFailure, overdue bool
		reason               string
	}{
		{name: "maintenance_fresh", reason: executionstore.ProcessToolReasonMachineUnreachable},
		{name: "maintenance_overdue", overdue: true, reason: executionstore.ProcessToolReasonQueueTimeout},
		{name: "wake_failure_fresh", wakeFailure: true, reason: executionstore.ProcessToolReasonMachineUnreachable},
		{
			name: "wake_failure_overdue", wakeFailure: true, overdue: true,
			reason: executionstore.ProcessToolReasonQueueTimeout,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture := newProcessDaemonFixture(t, ctx, t.Name())
			process := startQueuedExpiryProcess(t, ctx, fixture)
			if tc.overdue {
				ageQueuedExpiryProcess(t, ctx, fixture, process.ID)
			}
			expireDaemonRuntimeForTest(t, ctx, fixture)
			if tc.wakeFailure {
				failed, err := fixture.Store.Execution().FailQueuedProcessAfterWakeFailure(ctx, process)
				require.NoError(t, err)
				require.True(t, failed)
			} else {
				count, err := fixture.Store.Execution().ExpireProcessToolCallsForAllProjects(ctx, 0)
				require.NoError(t, err)
				require.EqualValues(t, 1, count)
			}
			current, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
			require.NoError(t, err)
			require.Equal(t, tc.reason, current.StateReasonCode)
			require.Nil(t, current.ExecutionGrantedAt)
			tool, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, process.ToolCallID)
			require.NoError(t, err)
			assertCompletedToolCallWithResult(t, fixture.Store, fixture.AgentID, tool, tc.reason)
		})
	}
}

func TestTerminalProcessReadRequiresGrantAndAvailableStorage(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		granted bool
		offline bool
		reason  string
	}{
		{name: "unreachable", reason: executionstore.ProcessToolReasonMachineUnreachable},
		{name: "canceled", reason: "agent_canceled_before_grant"},
		{name: "queue_timeout", reason: executionstore.ProcessToolReasonQueueTimeout},
		{name: "storage_exhausted", granted: true, reason: daemonprotocol.ProcessReasonMachineStorageExhausted},
		{name: "granted", granted: true},
		{name: "offline_unreachable", offline: true, reason: executionstore.ProcessToolReasonMachineUnreachable},
		{name: "offline_canceled", offline: true, reason: "agent_canceled_before_grant"},
		{name: "offline_queue_timeout", offline: true, reason: executionstore.ProcessToolReasonQueueTimeout},
		{
			name: "offline_storage_exhausted", offline: true, granted: true,
			reason: daemonprotocol.ProcessReasonMachineStorageExhausted,
		},
		{name: "offline_granted", offline: true, granted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture := newProcessDaemonFixture(t, ctx, t.Name())
			ids := createToolCallBatchForProcessTest(t, ctx, fixture, t.Name(), []processToolCallBatchItem{
				builtInProcessToolCallBatchItem("process", "run_command"),
				builtInProcessToolCallBatchItem("read", "read_process"),
			})
			process := startQueuedExpiryProcessForTool(t, ctx, fixture, ids[0])
			if test.granted {
				_, granted, err := fixture.Store.Execution().AcceptDaemonProcess(ctx, executionstore.AcceptDaemonProcessInput{
					Authority: fixture.authority(), ProcessID: process.ID,
				})
				require.NoError(t, err)
				require.True(t, granted)
			}
			_, err := fixture.Store.pool.Exec(ctx,
				`UPDATE processes SET state = 'failed', state_reason_code = $2 WHERE id = $1`, process.ID, test.reason)
			require.NoError(t, err)
			if test.offline {
				expireDaemonRuntimeForTest(t, ctx, fixture)
			}
			action, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
				ProjectID: testProjectID, AgentID: fixture.AgentID, ToolCallID: ids[1], RuntimeLockID: fixture.Lock.ID,
			}, executionstore.CreateProcessActionInput{
				ProcessID: process.ID, ActionKind: executionstore.ProcessActionKindRead, Payload: json.RawMessage(`{}`),
			})
			if test.granted && test.reason == "" && test.offline {
				require.ErrorIs(t, err, storeerr.ErrNoOnlineDaemonRuntime)
			} else if test.granted && test.reason == "" {
				require.NoError(t, err)
				require.Equal(t, executionstore.ProcessActionStateQueued, action.State)
			} else {
				require.ErrorIs(t, err, storeerr.ErrProcessTerminal)
			}
		})
	}
}

func TestProcessQueueExpiryRecoversSteeringOnOnlineMachine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, t.Name())
	process := startQueuedExpiryProcess(t, ctx, fixture)
	ageQueuedExpiryProcess(t, ctx, fixture, process.ID)
	require.NoError(
		t,
		fixture.Store.Execution().ReleaseAgentRuntimeLock(
			ctx,
			testProjectID,
			fixture.AgentID,
			fixture.Lock.ID))
	steering, _, _, err := fixture.Store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID: testProjectID, AgentID: fixture.AgentID, Actor: mustOmnaraActorParams(t, fixture.UserID),
			ContentBlocks: json.RawMessage(`[{"type":"text","text":"please continue"}]`),
			DeliveryMode:  executionstore.DeliveryModeSteering, IdempotencyKey: t.Name(),
		})
	require.NoError(t, err)
	var inputState string
	require.NoError(
		t,
		fixture.Store.pool.QueryRow(
			ctx,
			`SELECT state FROM agent_inputs WHERE id=$1`,
			steering.ID).Scan(&inputState))
	require.Equal(t, "received", inputState)
	for iteration, want := range []int64{1, 0} {
		expired, expiryErr := fixture.Store.Execution().ExpireProcessToolCallsForAllProjects(
			ctx,
			executionstore.ProcessToolMachineUnreachableGrace)
		require.NoError(t, expiryErr)
		require.Equal(t, want, expired, "sweep %d", iteration)
	}
	current, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.ProcessStateFailed, current.State)
	require.Equal(t, executionstore.ProcessToolReasonQueueTimeout, current.StateReasonCode)
	require.Nil(t, current.ExecutionGrantedAt)
	require.Nil(t, current.SourceStartedAt)
	require.Nil(t, current.SourceEndedAt)
	require.Nil(t, current.ExitCode)
	tool, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, process.ToolCallID)
	require.NoError(t, err)
	assertCompletedToolCallWithResult(
		t,
		fixture.Store,
		fixture.AgentID,
		tool,
		executionstore.ProcessToolReasonQueueTimeout)
	work, found, err := fixture.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.AgentWorkModel, work.Kind)
	require.NoError(
		t,
		fixture.Store.pool.QueryRow(
			ctx,
			`SELECT state FROM agent_inputs WHERE id=$1`,
			steering.ID).Scan(&inputState))
	require.Equal(t, "resolved", inputState)
}

func TestProcessQueueExpiryLeavesFreshGrantedAndCanceledWorkAlone(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"fresh", "starting", "running", "canceled"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture := newProcessDaemonFixture(t, ctx, t.Name())
			process := startQueuedExpiryProcess(t, ctx, fixture)
			if state == "starting" || state == "running" {
				_, found, err := fixture.Store.Execution().AcceptDaemonProcess(
					ctx,
					executionstore.AcceptDaemonProcessInput{Authority: fixture.authority(),
						ProcessID: process.ID})
				require.NoError(t, err)
				require.True(t, found)
			}
			if state == "running" {
				_, err := fixture.Store.pool.Exec(
					ctx,
					`UPDATE processes SET state='running', source_started_at=statement_timestamp() WHERE id=$1`,
					process.ID)
				require.NoError(t, err)
			}
			if state != "fresh" {
				ageQueuedExpiryProcess(t, ctx, fixture, process.ID)
			}
			if state == "canceled" {
				cancelToolCallForTest(t, ctx, fixture.Store, fixture.AgentID, process.ToolCallID)
			}
			before, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
			require.NoError(t, err)
			expired, err := fixture.Store.Execution().ExpireProcessToolCallsForAllProjects(
				ctx,
				executionstore.ProcessToolMachineUnreachableGrace)
			require.NoError(t, err)
			require.Zero(t, expired)
			after, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestProcessQueueExpiryRejectsLateAcceptanceAndPreservesResult(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, t.Name())
	process := startQueuedExpiryProcess(t, ctx, fixture)
	ageQueuedExpiryProcess(t, ctx, fixture, process.ID)
	_, found, err := fixture.Store.Execution().AcceptDaemonProcess(
		ctx,
		executionstore.AcceptDaemonProcessInput{Authority: fixture.authority(),
			ProcessID: process.ID})
	require.NoError(t, err)
	require.False(t, found)
	expired, err := fixture.Store.Execution().ExpireProcessToolCallsForAllProjects(
		ctx,
		executionstore.ProcessToolMachineUnreachableGrace)
	require.NoError(t, err)
	require.EqualValues(t, 1, expired)
	before, found, err := fixture.Store.Execution().GetToolCallResultAuthorityByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		process.ToolCallID)
	require.NoError(t, err)
	require.True(t, found)
	for range 2 {
		report, reportErr := fixture.Store.Execution().CompleteDaemonProcess(
			ctx,
			executionstore.CompleteDaemonProcessInput{
				Authority: fixture.authority(), ProjectID: testProjectID, AgentID: fixture.AgentID, ID: process.ID,
				State: executionstore.ProcessStateFailed, StateReasonCode: daemonprotocol.ProcessReasonMachineStorageExhausted,
				StateReasonMessage: daemonprotocol.ProcessMessageMachineStorageExhausted, StorageExhausted: true,
			})
		require.NoError(t, reportErr)
		require.False(t, report.ToolResultCommitted)
		require.Equal(t, executionstore.ProcessToolReasonQueueTimeout, report.Process.StateReasonCode)
	}
	after, found, err := fixture.Store.Execution().GetToolCallResultAuthorityByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		process.ToolCallID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, before, after)
	registration, err := fixture.Store.Execution().RegisterDaemonRuntimeWithReconciliation(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID: fixture.OrgID, MachineID: fixture.MachineID, DaemonTokenID: fixture.TokenID,
			DaemonInstanceID: fixture.DaemonID, DaemonVersion: "1.0.0", LeaseTimeout: testDaemonRuntimeLeaseTimeout,
			ProcessClaims: []executionstore.ProcessReconciliationClaim{{ProcessID: process.ID,
				SupervisorInstanceID: "prepared-expired",
				Phase:                daemonprotocol.ProcessPhasePrepared,
				SupervisorLive:       true}},
		})
	require.NoError(t, err)
	directive := processReconciliationDirectiveForTest(t, registration.Reconciliation, process.ID)
	require.Equal(t, daemonprotocol.ProcessDispositionClosePreparation, directive.Disposition)
	readTool := createToolCallForProcessActionTest(t, ctx, fixture, t.Name()+"_read")
	_, err = createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, ToolCallID: readTool, RuntimeLockID: fixture.Lock.ID,
	},
		executionstore.CreateProcessActionInput{ProcessID: process.ID,
			ActionKind: executionstore.ProcessActionKindRead,
			Payload:    json.RawMessage(`{}`)})
	require.ErrorIs(t, err, storeerr.ErrProcessTerminal)
}

func TestProcessQueueExpiryAcceptanceRechecksDeadlineAfterLock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, t.Name())
	process := startQueuedExpiryProcess(t, ctx, fixture)
	blocker, err := fixture.Store.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(ctx) }()
	var pid int32
	require.NoError(t, blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
	var deadline time.Time
	require.NoError(
		t,
		blocker.QueryRow(
			ctx,
			`UPDATE processes
SET created_at=statement_timestamp() - ($2::int * interval '1 second') + interval '1 second'
WHERE id=$1
RETURNING created_at + ($2::int * interval '1 second')`,
			process.ID, int32(executionstore.ProcessQueueTimeout/time.Second)).Scan(&deadline))
	type result struct {
		found bool
		err   error
	}
	done := make(chan result, 1)
	go func() {
		_, found, acceptErr := fixture.Store.Execution().AcceptDaemonProcess(
			ctx,
			executionstore.AcceptDaemonProcessInput{Authority: fixture.authority(),
				ProcessID: process.ID})
		done <- result{found, acceptErr}
	}()
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, fixture.Store.pool, "-- name: LockDaemonProcessForAccept", pid)
	waitForDatabaseTime(t, ctx, fixture.Store.pool, deadline.Add(time.Millisecond))
	require.NoError(t, blocker.Commit(ctx))
	accepted := <-done
	require.NoError(t, accepted.err)
	require.False(t, accepted.found)
	expired, err := fixture.Store.Execution().ExpireProcessToolCallsForAllProjects(
		ctx,
		executionstore.ProcessToolMachineUnreachableGrace)
	require.NoError(t, err)
	require.EqualValues(t, 1, expired)
}

func TestProcessQueueExpiryRechecksCancellationAfterMachineLock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, t.Name())
	process := startQueuedExpiryProcess(t, ctx, fixture)
	ageQueuedExpiryProcess(t, ctx, fixture, process.ID)
	blocker, err := fixture.Store.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(ctx) }()
	var pid int32
	require.NoError(t, blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
	_,
		err = dbsqlc.New(blocker).LockMachineForLifecycle(
		ctx,
		dbsqlc.LockMachineForLifecycleParams{OrgID: fixture.OrgID,
			ID: fixture.MachineID})
	require.NoError(t, err)
	type result struct {
		count int64
		err   error
	}
	done := make(chan result, 1)
	go func() {
		count, expiryErr := fixture.Store.Execution().ExpireProcessToolCallsForAllProjects(
			ctx,
			executionstore.ProcessToolMachineUnreachableGrace)
		done <- result{count, expiryErr}
	}()
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, fixture.Store.pool, "-- name: LockMachineForLifecycle", pid)
	cancelToolCallForTest(t, ctx, fixture.Store, fixture.AgentID, process.ToolCallID)
	require.NoError(t, blocker.Commit(ctx))
	expired := <-done
	require.NoError(t, expired.err)
	require.Zero(t, expired.count)
}

func TestProcessQueueExpiryOverridesActiveWakeDeadline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProviderRuntimeStorageFixture(t, ctx, "queue-expiry-wake", true)
	machine := fixture.insertInactiveMachine(t, ctx, "queue-expiry-wake")
	processFixture, processID, toolID := fixture.createQueuedProcess(t, ctx, machine, "queue-expiry-wake")
	disposition, err := fixture.store.Execution().BeginMachineWake(
		ctx,
		testOrgID,
		machine.machineID,
		fixture.machinePool.ID,
		time.Hour)
	require.NoError(t, err)
	require.Equal(t, executionstore.MachineWakeReady, disposition)
	ageQueuedExpiryProcess(t, ctx, processFixture, processID)
	expired, err := fixture.store.Execution().ExpireProcessToolCallsForAllProjects(
		ctx,
		executionstore.ProcessToolMachineUnreachableGrace)
	require.NoError(t, err)
	require.EqualValues(t, 1, expired)
	tool, err := fixture.store.Execution().GetToolCall(ctx, testProjectID, processFixture.AgentID, toolID)
	require.NoError(t, err)
	assertCompletedToolCallWithResult(
		t,
		fixture.store,
		processFixture.AgentID,
		tool,
		executionstore.ProcessToolReasonQueueTimeout)
}

func TestProcessQueueExpiryOnlyExpiresOverdueQueuedWorkOnSelectedMachine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, t.Name())
	ids := createToolCallBatchForProcessTest(t, ctx, fixture, t.Name(), []processToolCallBatchItem{
		builtInProcessToolCallBatchItem("expired", "run_command"),
		builtInProcessToolCallBatchItem("accepted", "run_command"),
		builtInProcessToolCallBatchItem("fresh", "run_command"),
		builtInProcessToolCallBatchItem("read", "read_process"),
	})
	var processes []executionstore.ProcessRecord
	for _, toolID := range ids[:3] {
		process := startQueuedExpiryProcessForTool(t, ctx, fixture, toolID)
		processes = append(processes, process)
	}
	_, found, err := fixture.Store.Execution().AcceptDaemonProcess(ctx, executionstore.AcceptDaemonProcessInput{
		Authority: fixture.authority(),
		ProcessID: processes[1].ID,
	})
	require.NoError(t, err)
	require.True(t, found)
	action, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    ids[3],
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  processes[1].ID,
		ActionKind: executionstore.ProcessActionKindRead,
		Payload:    json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	for _, process := range processes[:2] {
		ageQueuedExpiryProcess(t, ctx, fixture, process.ID)
	}
	expired, err := fixture.Store.Execution().ExpireProcessToolCallsForAllProjects(
		ctx, executionstore.ProcessToolMachineUnreachableGrace,
	)
	require.NoError(t, err)
	require.EqualValues(t, 1, expired)
	for _, toolID := range ids[1:] {
		tool, getErr := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolID)
		require.NoError(t, getErr)
		require.Equal(t, executionstore.ToolCallState("waiting"), tool.State)
	}
	var state string
	require.NoError(t, fixture.Store.pool.QueryRow(
		ctx, `SELECT state FROM process_actions WHERE id=$1`, action.ID,
	).Scan(&state))
	require.Equal(t, "queued", state)
}
