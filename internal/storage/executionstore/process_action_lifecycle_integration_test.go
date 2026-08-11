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
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestQueuedProcessActionFailureSkipsCompletedToolCall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "queued_action_failure_skips_completed_tool")
	processToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"queued_action_failure_skips_completed_tool_process",
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
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID); err != nil ||
		!found {
		t.Fatalf("accept process found=%v err=%v", found, err)
	}
	if _, err := fixture.Store.Execution().MarkProcessStarted(
		ctx,
		executionstore.MarkProcessStartedInput{
			ProjectID:       testProjectID,
			AgentID:         fixture.AgentID,
			ID:              process.ID,
			Authority:       fixture.authority(),
			SourceStartedAt: fixture.Now.Add(2 * time.Second),
		},
	); err != nil {
		t.Fatalf("mark process started: %v", err)
	}
	actionToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"queued_action_failure_skips_completed_tool",
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
		Payload:    json.RawMessage(`{"data":"hello\n"}`),
	})
	if err != nil {
		t.Fatalf("create process action: %v", err)
	}
	cancelToolCallForTest(
		t,
		ctx,
		fixture.Store,
		fixture.AgentID,
		actionToolCallID)

	rows, err := fixture.Store.q.MarkQueuedProcessActionsFailedForProcess(
		ctx,
		dbsqlc.MarkQueuedProcessActionsFailedForProcessParams{
			OrgID:              fixture.OrgID,
			ProcessID:          process.ID,
			StateReasonCode:    sqlcTextFromEmpty(executionstore.ProcessToolReasonMachineUnreachable),
			StateReasonMessage: "",
		},
	)
	if err != nil {
		t.Fatalf("mark queued action with completed tool call: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("marked queued actions = %d, want none after tool call completed", len(rows))
	}
	current, found, err := fixture.Store.Execution().GetProcessActionByToolCall(ctx, testProjectID, fixture.AgentID, actionToolCallID)
	if err != nil {
		t.Fatalf("get process action: %v", err)
	}
	if !found || current.ID != action.ID || current.State != executionstore.ProcessActionStateQueued {
		t.Fatalf("action after completed-tool failure attempt found=%v action=%+v, want queued", found, current)
	}
}

func TestAcceptDaemonProcessActionRechecksLeaseAfterActionLockWait(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "accept_action_post_lock_lease")
	processToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"accept_action_post_lock_lease_process",
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
		t.Fatalf("accept process found=%v err=%v", found, err)
	}
	if _, err := fixture.Store.Execution().MarkProcessStarted(ctx, executionstore.MarkProcessStartedInput{
		ProjectID:       testProjectID,
		AgentID:         fixture.AgentID,
		ID:              process.ID,
		Authority:       fixture.authority(),
		SourceStartedAt: fixture.Now.Add(time.Second),
	}); err != nil {
		t.Fatalf("mark process started: %v", err)
	}
	actionToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"accept_action_post_lock_lease_action",
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
		t.Fatalf("create process action: %v", err)
	}

	blockingTx, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin action accept blocker: %v", err)
	}
	defer func() { _ = blockingTx.Rollback(ctx) }()
	var blockingPID int32
	if err := blockingTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockingPID); err != nil {
		t.Fatalf("get action accept blocker backend: %v", err)
	}
	if _, err := blockingTx.Exec(ctx, `
SELECT id
FROM process_actions
WHERE org_id = $1 AND process_id = $2 AND id = $3
FOR UPDATE
`, fixture.OrgID, process.ID, action.ID); err != nil {
		t.Fatalf("lock process action before daemon accept: %v", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE daemon_runtimes
SET last_seen_at = statement_timestamp(),
    lease_expires_at = statement_timestamp() + interval '250 milliseconds',
    updated_at = statement_timestamp()
WHERE org_id = $1 AND machine_id = $2 AND id = $3
`, fixture.OrgID, fixture.MachineID, fixture.RuntimeID); err != nil {
		t.Fatalf("shorten daemon runtime lease: %v", err)
	}

	type acceptResult struct {
		found bool
		err   error
	}
	done := make(chan acceptResult, 1)
	go func() {
		_, found, acceptErr := fixture.Store.Execution().AcceptDaemonProcessAction(
			context.Background(),
			executionstore.AcceptDaemonProcessActionInput{
				Authority: fixture.authority(),
				ProcessID: process.ID,
				ID:        action.ID,
			},
		)
		done <- acceptResult{found: found, err: acceptErr}
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		fixture.Store.pool,
		"-- name: LockDaemonProcessActionForAccept",
		blockingPID,
	)
	if _, err := blockingTx.Exec(ctx, `SELECT pg_sleep(0.3)`); err != nil {
		t.Fatalf("wait for daemon runtime lease expiry: %v", err)
	}
	if err := blockingTx.Commit(ctx); err != nil {
		t.Fatalf("release action accept blocker: %v", err)
	}
	result := <-done
	if result.err != nil {
		t.Fatalf("accept daemon process action after lease expiry: %v", result.err)
	}
	if result.found {
		t.Fatal("process action was accepted after its runtime lease expired during the action lock wait")
	}
	current, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		actionToolCallID,
	)
	if err != nil || !found {
		t.Fatalf("get process action found=%v err=%v", found, err)
	}
	if current.State != executionstore.ProcessActionStateQueued {
		t.Fatalf("process action after rejected accept = %+v, want queued", current)
	}
}

func TestCreateProcessActionRechecksRuntimeLeaseAfterProcessLockWait(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "create_action_post_lock_lease")
	processToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"create_action_post_lock_lease_process",
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
		t.Fatalf("accept process found=%v err=%v", found, err)
	}
	if _, err := fixture.Store.Execution().MarkProcessStarted(ctx, executionstore.MarkProcessStartedInput{
		ProjectID:       testProjectID,
		AgentID:         fixture.AgentID,
		ID:              process.ID,
		Authority:       fixture.authority(),
		SourceStartedAt: fixture.Now.Add(time.Second),
	}); err != nil {
		t.Fatalf("mark process started: %v", err)
	}
	actionToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"create_action_post_lock_lease_action",
		"write_process",
	)

	blockingTx, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin process action creation blocker: %v", err)
	}
	defer func() { _ = blockingTx.Rollback(ctx) }()
	var blockingPID int32
	if err := blockingTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockingPID); err != nil {
		t.Fatalf("get process action creation blocker backend: %v", err)
	}
	if _, err := blockingTx.Exec(ctx, `
SELECT id
FROM processes
WHERE project_id = $1 AND agent_id = $2 AND id = $3
FOR UPDATE
`, testProjectID, fixture.AgentID, process.ID); err != nil {
		t.Fatalf("lock process before action creation: %v", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE agent_runtime_locks
SET renewed_at = statement_timestamp(),
    lease_expires_at = statement_timestamp() + interval '250 milliseconds'
WHERE agent_id = $1 AND id = $2
`, fixture.AgentID, fixture.Lock.ID); err != nil {
		t.Fatalf("shorten agent runtime lease: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, createErr := createProcessActionForTest(
			context.Background(),
			fixture.Store,
			executionstore.ExecuteToolCallInput{
				ProjectID:     testProjectID,
				AgentID:       fixture.AgentID,
				ToolCallID:    actionToolCallID,
				RuntimeLockID: fixture.Lock.ID,
			},
			executionstore.CreateProcessActionInput{
				ProcessID:  process.ID,
				ActionKind: executionstore.ProcessActionKindWrite,
				Payload:    json.RawMessage(`{"data":"hello"}`),
			},
		)
		done <- createErr
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		fixture.Store.pool,
		"-- name: LockProcessForActionCreation",
		blockingPID,
	)
	if _, err := blockingTx.Exec(ctx, `SELECT pg_sleep(0.3)`); err != nil {
		t.Fatalf("wait for agent runtime lease expiry: %v", err)
	}
	if err := blockingTx.Commit(ctx); err != nil {
		t.Fatalf("release process action creation blocker: %v", err)
	}
	if err := <-done; !errors.Is(err, storeerr.ErrRuntimeLockInactive) {
		t.Fatalf("create process action after runtime lease expiry error = %v, want inactive runtime lock", err)
	}
	if _, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		actionToolCallID,
	); err != nil || found {
		t.Fatalf("process action after runtime lease expiry found=%v err=%v", found, err)
	}
}

func TestAcceptedProcessActionUnknownSkipsCompletedToolCall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "accepted_action_unknown_skips_completed_tool")
	processToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"accepted_action_unknown_skips_completed_tool_process",
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
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID); err != nil ||
		!found {
		t.Fatalf("accept process found=%v err=%v", found, err)
	}
	if _, err := fixture.Store.Execution().MarkProcessStarted(
		ctx,
		executionstore.MarkProcessStartedInput{
			ProjectID:       testProjectID,
			AgentID:         fixture.AgentID,
			ID:              process.ID,
			Authority:       fixture.authority(),
			SourceStartedAt: fixture.Now.Add(2 * time.Second),
		},
	); err != nil {
		t.Fatalf("mark process started: %v", err)
	}
	actionToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"accepted_action_unknown_skips_completed_tool",
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
		Payload:    json.RawMessage(`{"data":"hello\n"}`),
	})
	if err != nil {
		t.Fatalf("create process action: %v", err)
	}
	if _, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		action.ID); err != nil ||
		!found {
		t.Fatalf("accept process action found=%v err=%v", found, err)
	}
	cancelToolCallForTest(
		t,
		ctx,
		fixture.Store,
		fixture.AgentID,
		actionToolCallID)

	processRows, err := fixture.Store.q.ResolveAcceptedProcessActionsWithoutEvidence(
		ctx,
		dbsqlc.ResolveAcceptedProcessActionsWithoutEvidenceParams{
			OrgID:              fixture.OrgID,
			ProcessID:          process.ID,
			StateReasonCode:    sqlcTextFromEmpty(executionstore.ProcessToolReasonMachineUnreachable),
			StateReasonMessage: "",
		},
	)
	if err != nil {
		t.Fatalf("mark accepted action unknown for process: %v", err)
	}
	if len(processRows) != 0 {
		t.Fatalf("process unknown rows = %d, want none after tool call completed", len(processRows))
	}
	current, found, err := fixture.Store.Execution().GetProcessActionByToolCall(ctx, testProjectID, fixture.AgentID, actionToolCallID)
	if err != nil {
		t.Fatalf("get process action: %v", err)
	}
	if !found || current.ID != action.ID || current.State != executionstore.ProcessActionStateAccepted {
		t.Fatalf("action after completed-tool unknown attempt found=%v action=%+v, want accepted", found, current)
	}
}

func TestDaemonProcessFinishedPreservesOutstandingReads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "process_finished_actions")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"process_finished_actions",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("process_finished_actions_process", "run_command"),
			builtInProcessToolCallBatchItem("process_finished_actions_queued", "read_process"),
			builtInProcessToolCallBatchItem("process_finished_actions_accepted", "read_process"),
		},
	)
	processToolCallID, queuedToolCallID, acceptedToolCallID := toolCallIDs[0], toolCallIDs[1], toolCallIDs[2]

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    processToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "cat",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		NilID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	if _, err := fixture.Store.Execution().MarkProcessStarted(
		ctx,
		executionstore.MarkProcessStartedInput{
			ProjectID:       testProjectID,
			AgentID:         fixture.AgentID,
			ID:              process.ID,
			Authority:       fixture.authority(),
			SourceStartedAt: fixture.Now.Add(1500 * time.Millisecond),
		},
	); err != nil {
		t.Fatalf("mark process started: %v", err)
	}
	acceptedAction, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    acceptedToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindRead,
		Payload:    json.RawMessage(`{"cursor":0,"wait_ms":1000}`),
	})
	if err != nil {
		t.Fatalf("create accepted action: %v", err)
	}
	accepted, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		NilID)

	if err != nil {
		t.Fatalf("accept action: %v", err)
	} else if !found {
		t.Fatal("expected action accept")
	}
	if accepted.ID != acceptedAction.ID {
		t.Fatalf("accepted action id = %s, want %s", accepted.ID, acceptedAction.ID)
	}
	queuedAction, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    queuedToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindRead,
		Payload:    json.RawMessage(`{"cursor":0}`),
	})
	if err != nil {
		t.Fatalf("create queued action: %v", err)
	}
	exitCode := 0
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(
		ctx,
		executionstore.CompleteDaemonProcessInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ID:            process.ID,
			Authority:     fixture.authority(),
			State:         executionstore.ProcessStateExited,
			ExitCode:      &exitCode,
			Result:        json.RawMessage(`{"output":"done","cursor":0,"next_cursor":4,"truncated":false}`),
			SourceEndedAt: fixture.Now.Add(5 * time.Second),
		},
	); err != nil {
		t.Fatalf("complete daemon process: %v", err)
	}
	queuedUpdated, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		queuedToolCallID,
	)
	if err != nil {
		t.Fatalf("get queued action: %v", err)
	}
	if !found || queuedUpdated.ID != queuedAction.ID ||
		queuedUpdated.State != executionstore.ProcessActionStateQueued ||
		queuedUpdated.StateReasonCode != "" {
		t.Fatalf("queued action after process terminal = found %v %+v", found, queuedUpdated)
	}
	acceptedUpdated, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		acceptedToolCallID,
	)
	if err != nil {
		t.Fatalf("get accepted action: %v", err)
	}
	if !found || acceptedUpdated.ID != acceptedAction.ID ||
		acceptedUpdated.State != executionstore.ProcessActionStateAccepted ||
		acceptedUpdated.StateReasonCode != "" {
		t.Fatalf("accepted action after process terminal = found %v %+v", found, acceptedUpdated)
	}
	queuedToolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, queuedToolCallID)
	if err != nil {
		t.Fatalf("get queued action tool call: %v", err)
	}
	if queuedToolCall.State != executionstore.ToolCallStateWaiting ||
		queuedToolCall.CompletedAt != nil {
		t.Fatalf("queued read tool call = %+v", queuedToolCall)
	}
	acceptedToolCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		acceptedToolCallID,
	)
	if err != nil {
		t.Fatalf("get accepted action tool call: %v", err)
	}
	if acceptedToolCall.State != executionstore.ToolCallStateWaiting ||
		acceptedToolCall.CompletedAt != nil {
		t.Fatalf("accepted read tool call = %+v", acceptedToolCall)
	}
}

func TestDaemonHeartbeatDoesNotDiscardAcceptedWrite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "heartbeat_keeps_accepted_write")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"heartbeat_keeps_accepted_write",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("heartbeat_keeps_accepted_write_process", "run_command"),
			builtInProcessToolCallBatchItem("heartbeat_keeps_accepted_write_action", "write_process"),
		},
	)
	processToolCallID, closeInputToolCallID := toolCallIDs[0], toolCallIDs[1]

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    processToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "cat",
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
		process.ID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	if _, err := fixture.Store.Execution().MarkProcessStarted(
		ctx,
		executionstore.MarkProcessStartedInput{
			ProjectID:       testProjectID,
			AgentID:         fixture.AgentID,
			ID:              process.ID,
			Authority:       fixture.authority(),
			SourceStartedAt: fixture.Now.Add(1500 * time.Millisecond),
		},
	); err != nil {
		t.Fatalf("mark process started: %v", err)
	}
	action, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    closeInputToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindWrite,
		Payload:    json.RawMessage(`{"close_stdin":true}`),
	})
	if err != nil {
		t.Fatalf("create close input action: %v", err)
	}
	if _, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		NilID); err != nil {
		t.Fatalf("accept close input action: %v", err)
	} else if !found {
		t.Fatal("expected close input action accept")
	}
	exitCode := 0
	endedAt := fixture.Now.Add(3 * time.Second)
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(
		ctx,
		executionstore.CompleteDaemonProcessInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ID:            process.ID,
			Authority:     fixture.authority(),
			State:         executionstore.ProcessStateExited,
			ExitCode:      &exitCode,
			Result:        json.RawMessage(`{"output":"","cursor":0,"next_cursor":0,"truncated":false}`),
			SourceEndedAt: endedAt,
		},
	); err != nil {
		t.Fatalf("complete daemon process: %v", err)
	}
	if _, err := fixture.Store.Execution().HeartbeatDaemonRuntime(
		ctx,
		executionstore.DaemonRuntimeLeaseInput{
			Authority:        fixture.authority(),
			DaemonInstanceID: fixture.DaemonID,
			LeaseTimeout:     time.Hour,
		},
	); err != nil {
		t.Fatalf("heartbeat daemon runtime: %v", err)
	}
	updated, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		closeInputToolCallID,
	)
	if err != nil {
		t.Fatalf("get completed close input action: %v", err)
	}
	if !found || updated.ID != action.ID || updated.State != executionstore.ProcessActionStateAccepted ||
		updated.StateReasonCode != "" {
		t.Fatalf("write action after process completion = found %v %+v", found, updated)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, closeInputToolCallID)
	if err != nil {
		t.Fatalf("get completed close input tool call: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateWaiting || toolCall.CompletedAt != nil {
		t.Fatalf("accepted write tool call = %+v", toolCall)
	}
}

func TestTerminalProcessDoesNotReofferAcceptedMutation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "terminal_process_keeps_accepted_mutation")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"terminal_process_keeps_accepted_mutation",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("terminal_process_keeps_accepted_mutation_process", "run_command"),
			builtInProcessToolCallBatchItem("terminal_process_keeps_accepted_mutation_close", "write_process"),
		},
	)
	processToolCallID, closeInputToolCallID := toolCallIDs[0], toolCallIDs[1]

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    processToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "cat",
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
		process.ID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	if _, err := fixture.Store.Execution().MarkProcessStarted(
		ctx,
		executionstore.MarkProcessStartedInput{
			ProjectID:       testProjectID,
			AgentID:         fixture.AgentID,
			ID:              process.ID,
			Authority:       fixture.authority(),
			SourceStartedAt: fixture.Now.Add(1500 * time.Millisecond),
		},
	); err != nil {
		t.Fatalf("mark process started: %v", err)
	}
	action, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    closeInputToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindWrite,
		Payload:    json.RawMessage(`{"close_stdin":true}`),
	})
	if err != nil {
		t.Fatalf("create close input action: %v", err)
	}
	if _, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		NilID); err != nil {
		t.Fatalf("accept close input action: %v", err)
	} else if !found {
		t.Fatal("expected close input action accept")
	}
	exitCode := 0
	endedAt := fixture.Now.Add(3 * time.Second)
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(
		ctx,
		executionstore.CompleteDaemonProcessInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ID:            process.ID,
			Authority:     fixture.authority(),
			State:         executionstore.ProcessStateExited,
			ExitCode:      &exitCode,
			Result:        json.RawMessage(`{"output":"","cursor":0,"next_cursor":0,"truncated":false}`),
			SourceEndedAt: endedAt,
		},
	); err != nil {
		t.Fatalf("complete daemon process: %v", err)
	}
	if _, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		NilID); err != nil {
		t.Fatalf("accept after process completion: %v", err)
	} else if found {
		t.Fatal("terminal process should not accept a new mutating action")
	}
	pending, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		closeInputToolCallID,
	)
	if err != nil {
		t.Fatalf("get resolved close input action: %v", err)
	}
	if !found || pending.ID != action.ID ||
		pending.State != executionstore.ProcessActionStateAccepted ||
		pending.StateReasonCode != "" {
		t.Fatalf("write action after process completion = found %v %+v", found, pending)
	}
	updated, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		closeInputToolCallID,
	)
	if err != nil {
		t.Fatalf("re-read resolved close input action: %v", err)
	}
	if !found || updated.ID != action.ID || updated.State != executionstore.ProcessActionStateAccepted ||
		updated.StateReasonCode != "" {
		t.Fatalf("accepted write action changed = found %v %+v", found, updated)
	}
}

func TestProcessCompletionKeepsTerminalReadsAvailable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "terminal_reads_available")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"terminal_reads_available",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("terminal_reads_available_process", "run_command"),
			builtInProcessToolCallBatchItem("terminal_reads_available_accepted_read", "read_process"),
			builtInProcessToolCallBatchItem("terminal_reads_available_read", "read_process"),
			builtInProcessToolCallBatchItem("terminal_reads_available_late_read", "read_process"),
		},
	)
	processToolCallID, acceptedReadToolCallID := toolCallIDs[0], toolCallIDs[1]
	readToolCallID, lateReadToolCallID := toolCallIDs[2], toolCallIDs[3]

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    processToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "cat",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		NilID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	if _, err := fixture.Store.Execution().MarkProcessStarted(
		ctx,
		executionstore.MarkProcessStartedInput{
			ProjectID:       testProjectID,
			AgentID:         fixture.AgentID,
			ID:              process.ID,
			Authority:       fixture.authority(),
			SourceStartedAt: fixture.Now.Add(1500 * time.Millisecond),
		},
	); err != nil {
		t.Fatalf("mark process started: %v", err)
	}
	exitCode := 0
	endedAt := fixture.Now.Add(5 * time.Second)
	acceptedReadTransaction := executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    acceptedReadToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}
	acceptedReadInput := executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindRead,
		Payload:    json.RawMessage(`{"cursor":64}`),
	}
	acceptedAction, err := createProcessActionForTest(
		ctx,
		fixture.Store,
		acceptedReadTransaction,
		acceptedReadInput,
	)
	if err != nil {
		t.Fatalf("create accepted read action: %v", err)
	}
	acceptedAction, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		acceptedAction.ID)

	if err != nil {
		t.Fatalf("accept read action: %v", err)
	}
	if !found || acceptedAction.State != executionstore.ProcessActionStateAccepted {
		t.Fatalf("accepted read action = found %v %+v", found, acceptedAction)
	}
	readTransaction := executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    readToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}
	readInput := executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindRead,
		Payload:    json.RawMessage(`{"cursor":0}`),
	}
	action, err := createProcessActionForTest(ctx, fixture.Store, readTransaction, readInput)
	if err != nil {
		t.Fatalf("create queued read action: %v", err)
	}
	replayed, err := createProcessActionForTest(ctx, fixture.Store, readTransaction, readInput)
	if err != nil {
		t.Fatalf("replay queued read action: %v", err)
	}
	if replayed.ID != action.ID || replayed.State != executionstore.ProcessActionStateQueued {
		t.Fatalf("replayed read action = %+v, want queued action %s", replayed, action.ID)
	}
	replayedAccepted, err := createProcessActionForTest(
		ctx,
		fixture.Store,
		acceptedReadTransaction,
		acceptedReadInput,
	)
	if err != nil {
		t.Fatalf("replay accepted read action: %v", err)
	}
	if replayedAccepted.ID != acceptedAction.ID || replayedAccepted.State != executionstore.ProcessActionStateAccepted {
		t.Fatalf(
			"replayed accepted read action = %+v, want accepted action %s",
			replayedAccepted,
			acceptedAction.ID,
		)
	}
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(
		ctx,
		executionstore.CompleteDaemonProcessInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ID:            process.ID,
			Authority:     fixture.authority(),
			State:         executionstore.ProcessStateExited,
			ExitCode:      &exitCode,
			Result:        json.RawMessage(`{"output":"done","cursor":0,"next_cursor":4,"truncated":false}`),
			SourceEndedAt: endedAt,
		},
	); err != nil {
		t.Fatalf("complete daemon process: %v", err)
	}
	acceptedUpdated, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		acceptedReadToolCallID,
	)
	if err != nil {
		t.Fatalf("get accepted observation after process completion: %v", err)
	}
	if !found || acceptedUpdated.ID != acceptedAction.ID ||
		acceptedUpdated.State != executionstore.ProcessActionStateAccepted ||
		acceptedUpdated.StateReasonCode != "" {
		t.Fatalf("accepted observation after process completion = found %v %+v", found, acceptedUpdated)
	}
	updated, found, err := fixture.Store.Execution().GetProcessActionByToolCall(ctx, testProjectID, fixture.AgentID, readToolCallID)
	if err != nil {
		t.Fatalf("get queued observation after process completion: %v", err)
	}
	if !found || updated.ID != action.ID || updated.State != executionstore.ProcessActionStateQueued ||
		updated.StateReasonCode != "" {
		t.Fatalf("queued observation after process completion = found %v %+v", found, updated)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, readToolCallID)
	if err != nil {
		t.Fatalf("get queued observation tool call: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateWaiting || toolCall.CompletedAt != nil {
		t.Fatalf("queued terminal read tool call = %+v", toolCall)
	}
	lateRead, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    lateReadToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindRead,
		Payload:    json.RawMessage(`{"cursor":0}`),
	})
	if err != nil {
		t.Fatalf("create late terminal read action: %v", err)
	}
	if lateRead.State != executionstore.ProcessActionStateQueued || lateRead.Seq != 3 {
		t.Fatalf("late terminal read = %+v", lateRead)
	}
}

func TestDaemonHeartbeatKeepsQueuedTerminalRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "heartbeat_keeps_terminal_read")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"heartbeat_keeps_terminal_read",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("heartbeat_keeps_terminal_read_process", "run_command"),
			builtInProcessToolCallBatchItem("heartbeat_keeps_terminal_read_read", "read_process"),
		},
	)
	processToolCallID, readToolCallID := toolCallIDs[0], toolCallIDs[1]

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    processToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "cat",
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
		process.ID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	if _, err := fixture.Store.Execution().MarkProcessStarted(
		ctx,
		executionstore.MarkProcessStartedInput{
			ProjectID:       testProjectID,
			AgentID:         fixture.AgentID,
			ID:              process.ID,
			Authority:       fixture.authority(),
			SourceStartedAt: fixture.Now.Add(1500 * time.Millisecond),
		},
	); err != nil {
		t.Fatalf("mark process started: %v", err)
	}
	exitCode := 0
	endedAt := fixture.Now.Add(4 * time.Second)
	action, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    readToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindRead,
		Payload:    json.RawMessage(`{"cursor":0}`),
	})
	if err != nil {
		t.Fatalf("create queued read action: %v", err)
	}
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(
		ctx,
		executionstore.CompleteDaemonProcessInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ID:            process.ID,
			Authority:     fixture.authority(),
			State:         executionstore.ProcessStateExited,
			ExitCode:      &exitCode,
			Result:        json.RawMessage(`{"output":"done","cursor":0,"next_cursor":4,"truncated":false}`),
			SourceEndedAt: endedAt,
		},
	); err != nil {
		t.Fatalf("complete daemon process: %v", err)
	}
	if _, err := fixture.Store.Execution().HeartbeatDaemonRuntime(
		ctx,
		executionstore.DaemonRuntimeLeaseInput{
			Authority:        fixture.authority(),
			DaemonInstanceID: fixture.DaemonID,
			LeaseTimeout:     time.Hour,
		},
	); err != nil {
		t.Fatalf("heartbeat daemon runtime: %v", err)
	}
	updated, found, err := fixture.Store.Execution().GetProcessActionByToolCall(ctx, testProjectID, fixture.AgentID, readToolCallID)
	if err != nil {
		t.Fatalf("get terminal read action: %v", err)
	}
	if !found || updated.ID != action.ID || updated.State != executionstore.ProcessActionStateQueued ||
		updated.StateReasonCode != "" {
		t.Fatalf("queued read action after process completion = found %v %+v", found, updated)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, readToolCallID)
	if err != nil {
		t.Fatalf("get terminal read tool call: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateWaiting || toolCall.CompletedAt != nil {
		t.Fatalf("queued terminal read tool call = %+v", toolCall)
	}
}

func TestAcceptedWriteRemainsOwnedAfterParentTerminalizes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "terminal_process_keeps_accepted_write")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"terminal_process_keeps_accepted_write",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("terminal_process_keeps_accepted_write_process", "run_command"),
			builtInProcessToolCallBatchItem("terminal_process_keeps_accepted_write_close", "write_process"),
		},
	)
	processToolCallID, closeInputToolCallID := toolCallIDs[0], toolCallIDs[1]

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    processToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "cat",
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
		process.ID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	if _, err := fixture.Store.Execution().MarkProcessStarted(
		ctx,
		executionstore.MarkProcessStartedInput{
			ProjectID:       testProjectID,
			AgentID:         fixture.AgentID,
			ID:              process.ID,
			Authority:       fixture.authority(),
			SourceStartedAt: fixture.Now.Add(1500 * time.Millisecond),
		},
	); err != nil {
		t.Fatalf("mark process started: %v", err)
	}
	action, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    closeInputToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindWrite,
		Payload:    json.RawMessage(`{"close_stdin":true}`),
	})
	if err != nil {
		t.Fatalf("create close input action: %v", err)
	}
	if _, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		NilID); err != nil {
		t.Fatalf("accept close input action: %v", err)
	} else if !found {
		t.Fatal("expected close input action accept")
	}
	exitCode := 0
	endedAt := fixture.Now.Add(3 * time.Second)
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(
		ctx,
		executionstore.CompleteDaemonProcessInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ID:            process.ID,
			Authority:     fixture.authority(),
			State:         executionstore.ProcessStateExited,
			ExitCode:      &exitCode,
			Result:        json.RawMessage(`{"output":"","cursor":0,"next_cursor":0,"truncated":false}`),
			SourceEndedAt: endedAt,
		},
	); err != nil {
		t.Fatalf("complete daemon process: %v", err)
	}
	updated, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		closeInputToolCallID,
	)
	if err != nil {
		t.Fatalf("get resolved close input action: %v", err)
	}
	if !found || updated.ID != action.ID || updated.State != executionstore.ProcessActionStateAccepted ||
		updated.StateReasonCode != "" {
		t.Fatalf("write action after process completion = found %v %+v", found, updated)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, closeInputToolCallID)
	if err != nil {
		t.Fatalf("get resolved close input tool call: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateWaiting || toolCall.CompletedAt != nil {
		t.Fatalf("accepted write tool call = %+v", toolCall)
	}
}

func TestAcceptedReadRemainsOwnedAfterParentTerminalizes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "terminal_process_keeps_accepted_read")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"terminal_process_keeps_accepted_read",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("terminal_process_keeps_accepted_read_process", "run_command"),
			builtInProcessToolCallBatchItem("terminal_process_keeps_accepted_read_read", "read_process"),
		},
	)
	processToolCallID, readToolCallID := toolCallIDs[0], toolCallIDs[1]

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    processToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "cat",
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
		process.ID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	if _, err := fixture.Store.Execution().MarkProcessStarted(
		ctx,
		executionstore.MarkProcessStartedInput{
			ProjectID:       testProjectID,
			AgentID:         fixture.AgentID,
			ID:              process.ID,
			Authority:       fixture.authority(),
			SourceStartedAt: fixture.Now.Add(1500 * time.Millisecond),
		},
	); err != nil {
		t.Fatalf("mark process started: %v", err)
	}
	exitCode := 0
	endedAt := fixture.Now.Add(4 * time.Second)
	action, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    readToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindRead,
		Payload:    json.RawMessage(`{"cursor":0}`),
	})
	if err != nil {
		t.Fatalf("create read action: %v", err)
	}
	accepted, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		NilID)

	if err != nil {
		t.Fatalf("accept read action: %v", err)
	}
	if !found || accepted.ID != action.ID || accepted.State != executionstore.ProcessActionStateAccepted {
		t.Fatalf("read action accept = found %v %+v", found, accepted)
	}
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(
		ctx,
		executionstore.CompleteDaemonProcessInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ID:            process.ID,
			Authority:     fixture.authority(),
			State:         executionstore.ProcessStateExited,
			ExitCode:      &exitCode,
			Result:        json.RawMessage(`{"output":"done","cursor":0,"next_cursor":4,"truncated":false}`),
			SourceEndedAt: endedAt,
		},
	); err != nil {
		t.Fatalf("complete daemon process: %v", err)
	}
	updated, found, err := fixture.Store.Execution().GetProcessActionByToolCall(ctx, testProjectID, fixture.AgentID, readToolCallID)
	if err != nil {
		t.Fatalf("get resolved read action: %v", err)
	}
	if !found || updated.ID != action.ID || updated.State != executionstore.ProcessActionStateAccepted ||
		updated.StateReasonCode != "" {
		t.Fatalf("read action after process completion = found %v %+v", found, updated)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, readToolCallID)
	if err != nil {
		t.Fatalf("get resolved read tool call: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateWaiting || toolCall.CompletedAt != nil {
		t.Fatalf("accepted read tool call = %+v", toolCall)
	}
}

func TestInterruptRemainsFIFOBehindQueuedWrite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "interrupt_fifo_queued_action")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"interrupt_fifo_queued_action",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("interrupt_fifo_queued_process", "run_command"),
			builtInProcessToolCallBatchItem("interrupt_fifo_queued_write", "write_process"),
			builtInProcessToolCallBatchItem("interrupt_fifo_queued_interrupt", "stop_process"),
		},
	)
	processToolCallID, writeToolCallID, interruptToolCallID := toolCallIDs[0], toolCallIDs[1], toolCallIDs[2]

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    processToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "cat",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	markProcessStartedForTest(t, ctx, fixture, process, fixture.Now.Add(1500*time.Millisecond))
	writeAction, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    writeToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindWrite,
		Payload:    json.RawMessage(`{"data":"queued\n"}`),
	})
	if err != nil {
		t.Fatalf("create queued write action: %v", err)
	}
	interruptAction, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    interruptToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindInterrupt,
		Payload:    json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("create interrupt action: %v", err)
	}
	if interruptAction.State != executionstore.ProcessActionStateQueued {
		t.Fatalf("interrupt action = %+v, want queued", interruptAction)
	}
	updatedWrite, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		writeToolCallID,
	)
	if err != nil {
		t.Fatalf("get queued write action: %v", err)
	}
	if !found ||
		updatedWrite.ID != writeAction.ID ||
		updatedWrite.State != executionstore.ProcessActionStateQueued ||
		updatedWrite.StateReasonCode != "" {
		t.Fatalf("queued write action = found %v %+v", found, updatedWrite)
	}
	writeToolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, writeToolCallID)
	if err != nil {
		t.Fatalf("get write tool call: %v", err)
	}
	if writeToolCall.State != executionstore.ToolCallStateWaiting ||
		writeToolCall.CompletedAt != nil {
		t.Fatalf("queued write tool call = %+v", writeToolCall)
	}
}

func TestTerminateActionRejectsLaterMutationsButAllowsReads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "terminate_blocks_later_actions")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"terminate_blocks_later_actions",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("terminate_blocks_later_actions_process", "run_command"),
			builtInProcessToolCallBatchItem("terminate_blocks_later_actions_terminate", "read_process"),
			builtInProcessToolCallBatchItem("terminate_blocks_later_actions_write", "read_process"),
			builtInProcessToolCallBatchItem("terminate_blocks_later_actions_read", "read_process"),
		},
	)
	processToolCallID, terminateToolCallID := toolCallIDs[0], toolCallIDs[1]
	writeToolCallID, readToolCallID := toolCallIDs[2], toolCallIDs[3]

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    processToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "cat",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	markProcessStartedForTest(t, ctx, fixture, process, fixture.Now.Add(1500*time.Millisecond))
	if _, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    terminateToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindTerminate,
		Payload:    json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("create terminate action: %v", err)
	}
	_, err = createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    writeToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindWrite,
		Payload:    json.RawMessage(`{"data":"late\n"}`),
	})
	if !errors.Is(err, storeerr.ErrProcessTerminating) {
		t.Fatalf("late write after terminate error = %v, want ErrProcessTerminating", err)
	}
	if _, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    readToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindRead,
		Payload:    json.RawMessage(`{"cursor":0}`),
	}); err != nil {
		t.Fatalf("read after terminate action should still queue for observation: %v", err)
	}
}

func TestDaemonProcessActionsGrantOneFIFOActionAtATime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "action_offer_prefix_batch")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"action_offer_prefix_batch",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("action_offer_prefix_batch_process", "run_command"),
			builtInProcessToolCallBatchItem("action_offer_prefix_batch_0", "read_process"),
			builtInProcessToolCallBatchItem("action_offer_prefix_batch_1", "read_process"),
			builtInProcessToolCallBatchItem("action_offer_prefix_batch_2", "read_process"),
		},
	)
	processToolCallID := toolCallIDs[0]

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    processToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "cat",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	markProcessStartedForTest(t, ctx, fixture, process, fixture.Now.Add(1500*time.Millisecond))

	var actions []executionstore.ProcessActionRecord
	for i, kind := range []executionstore.ProcessActionKind{executionstore.ProcessActionKindWrite, executionstore.ProcessActionKindWrite, executionstore.ProcessActionKindWrite} {
		payload := json.RawMessage(`{}`)
		if kind == executionstore.ProcessActionKindWrite {
			payload = json.RawMessage(`{"data":"chunk\n"}`)
		}
		action, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    toolCallIDs[i+1],
			RuntimeLockID: fixture.Lock.ID,
		}, executionstore.CreateProcessActionInput{
			ProcessID:  process.ID,
			ActionKind: kind,
			Payload:    payload,
		})
		if err != nil {
			t.Fatalf("create action %d: %v", i, err)
		}
		actions = append(actions, action)
	}

	offers, err := fixture.Store.Execution().ListDaemonProcessActionOffers(
		ctx,
		executionstore.DaemonWorkInput{
			Authority: fixture.authority(),
			Limit:     10,
		},
	)
	if err != nil {
		t.Fatalf("list action offers: %v", err)
	}
	if len(offers) != 1 ||
		offers[0].ID != actions[0].ID ||
		offers[0].Seq != 1 {
		t.Fatalf("initial action offers = %+v, want only %s", offers, actions[0].ID)
	}
	if accepted, found, err := fixture.Store.Execution().AcceptDaemonProcessAction(
		ctx,
		executionstore.AcceptDaemonProcessActionInput{
			Authority: fixture.authority(),
			ProcessID: process.ID,
			ID:        actions[1].ID,
		},
	); err != nil {
		t.Fatalf("accept out-of-order action: %v", err)
	} else if found {
		t.Fatalf("out-of-order accept found %+v, want not found", accepted)
	}
	for i, action := range actions {
		offers, err = fixture.Store.Execution().ListDaemonProcessActionOffers(
			ctx,
			executionstore.DaemonWorkInput{
				Authority: fixture.authority(),
				Limit:     10,
			},
		)
		if err != nil {
			t.Fatalf("list action offers before action %d: %v", i, err)
		}
		if len(offers) != 1 || offers[0].ID != action.ID {
			t.Fatalf("offers before action %d = %+v", i, offers)
		}
		accepted, found, err := fixture.Store.Execution().AcceptDaemonProcessAction(
			ctx,
			executionstore.AcceptDaemonProcessActionInput{
				Authority: fixture.authority(),
				ProcessID: process.ID,
				ID:        action.ID,
			},
		)
		if err != nil {
			t.Fatalf("accept action %d: %v", i, err)
		}
		if !found ||
			accepted.Action.ID != action.ID ||
			accepted.Action.State != executionstore.ProcessActionStateAccepted {
			t.Fatalf("accepted action %d = found %v %+v", i, found, accepted)
		}
		blocked, err := fixture.Store.Execution().ListDaemonProcessActionOffers(
			ctx,
			executionstore.DaemonWorkInput{
				Authority: fixture.authority(),
				Limit:     10,
			},
		)
		if err != nil {
			t.Fatalf("list offers while action %d accepted: %v", i, err)
		}
		if len(blocked) != 0 {
			t.Fatalf("offers while action %d is accepted = %+v", i, blocked)
		}
		if _, err := fixture.Store.Execution().ApplyDaemonProcessAction(
			ctx,
			executionstore.CompleteDaemonProcessActionInput{
				ProjectID: testProjectID,
				AgentID:   fixture.AgentID,
				ProcessID: process.ID,
				ID:        action.ID,
				Authority: fixture.authority(),
				Result:    json.RawMessage(`{}`),
			},
		); err != nil {
			t.Fatalf("complete action %d: %v", i, err)
		}
	}
	queued, err := createProcessActionForTest(
		ctx,
		fixture.Store,
		executionstore.ExecuteToolCallInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			ToolCallID: createToolCallForProcessActionTest(
				t,
				ctx,
				fixture,
				"action_offer_after_accepted_prefix",
			),
			RuntimeLockID: fixture.Lock.ID,
		},
		executionstore.CreateProcessActionInput{
			ProcessID:  process.ID,
			ActionKind: executionstore.ProcessActionKindRead,
			Payload:    json.RawMessage(`{"cursor":0}`),
		},
	)
	if err != nil {
		t.Fatalf("create action after accepted prefix: %v", err)
	}
	offers, err = fixture.Store.Execution().ListDaemonProcessActionOffers(
		ctx,
		executionstore.DaemonWorkInput{
			Authority: fixture.authority(),
			Limit:     1,
		},
	)
	if err != nil {
		t.Fatalf("list action offers after accepted prefix: %v", err)
	}
	if len(offers) != 1 || offers[0].ID != queued.ID {
		t.Fatalf(
			"offers after accepted prefix = %+v, want queued action %s",
			offers,
			queued.ID,
		)
	}
}

func TestAcceptedActionKeepsLaterControlActionsFIFO(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "action_fifo_control")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"action_fifo_control",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("action_fifo_control_process", "run_command"),
			builtInProcessToolCallBatchItem("action_fifo_control_write_1", "write_process"),
			builtInProcessToolCallBatchItem("action_fifo_control_write_2", "write_process"),
			builtInProcessToolCallBatchItem("action_fifo_control_interrupt", "stop_process"),
		},
	)
	processToolCallID := toolCallIDs[0]

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    processToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "cat",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	markProcessStartedForTest(t, ctx, fixture, process, fixture.Now.Add(1500*time.Millisecond))

	firstWrite, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallIDs[1],
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindWrite,
		Payload:    json.RawMessage(`{"data":"first\n"}`),
	})
	if err != nil {
		t.Fatalf("create first write: %v", err)
	}
	if _, found, err := fixture.Store.Execution().AcceptDaemonProcessAction(
		ctx,
		executionstore.AcceptDaemonProcessActionInput{
			Authority: fixture.authority(),
			ProcessID: process.ID,
			ID:        firstWrite.ID,
		},
	); err != nil {
		t.Fatalf("accept first write: %v", err)
	} else if !found {
		t.Fatal("expected first write accept")
	}
	secondWrite, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallIDs[2],
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindWrite,
		Payload:    json.RawMessage(`{"data":"second\n"}`),
	})
	if err != nil {
		t.Fatalf("create second write: %v", err)
	}
	interrupt, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallIDs[3],
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindInterrupt,
		Payload:    json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("create interrupt: %v", err)
	}

	blockedOffers, err := fixture.Store.Execution().ListDaemonProcessActionOffers(
		ctx,
		executionstore.DaemonWorkInput{
			Authority: fixture.authority(),
			Limit:     10,
		},
	)
	if err != nil {
		t.Fatalf("list offers while first write is accepted: %v", err)
	}
	if len(blockedOffers) != 0 {
		t.Fatalf("offers while first write is accepted = %+v", blockedOffers)
	}
	if accepted, found, err := fixture.Store.Execution().AcceptDaemonProcessAction(
		ctx,
		executionstore.AcceptDaemonProcessActionInput{
			Authority: fixture.authority(),
			ProcessID: process.ID,
			ID:        interrupt.ID,
		},
	); err != nil {
		t.Fatalf("accept interrupt out of order: %v", err)
	} else if found {
		t.Fatalf("accepted interrupt out of order: %+v", accepted)
	}
	if _, err := fixture.Store.Execution().ApplyDaemonProcessAction(
		ctx,
		executionstore.CompleteDaemonProcessActionInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			ProcessID: process.ID,
			ID:        firstWrite.ID,
			Authority: fixture.authority(),
			Result:    json.RawMessage(`{}`),
		},
	); err != nil {
		t.Fatalf("report first write: %v", err)
	}
	offers, err := fixture.Store.Execution().ListDaemonProcessActionOffers(
		ctx,
		executionstore.DaemonWorkInput{
			Authority: fixture.authority(),
			Limit:     10,
		},
	)
	if err != nil {
		t.Fatalf("list offers after first write: %v", err)
	}
	if len(offers) != 1 || offers[0].ID != secondWrite.ID {
		t.Fatalf("offers after first write = %+v, want second write", offers)
	}
	if _, found, err := fixture.Store.Execution().AcceptDaemonProcessAction(
		ctx,
		executionstore.AcceptDaemonProcessActionInput{
			Authority: fixture.authority(),
			ProcessID: process.ID,
			ID:        secondWrite.ID,
		},
	); err != nil || !found {
		t.Fatalf("accept second write found=%t err=%v", found, err)
	}
	if _, err := fixture.Store.Execution().ApplyDaemonProcessAction(
		ctx,
		executionstore.CompleteDaemonProcessActionInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			ProcessID: process.ID,
			ID:        secondWrite.ID,
			Authority: fixture.authority(),
			Result:    json.RawMessage(`{}`),
		},
	); err != nil {
		t.Fatalf("report second write: %v", err)
	}
	offers, err = fixture.Store.Execution().ListDaemonProcessActionOffers(
		ctx,
		executionstore.DaemonWorkInput{
			Authority: fixture.authority(),
			Limit:     10,
		},
	)
	if err != nil {
		t.Fatalf("list offers after second write: %v", err)
	}
	if len(offers) != 1 || offers[0].ID != interrupt.ID {
		t.Fatalf("offers after second write = %+v, want interrupt", offers)
	}
}

func TestCompleteProcessPreservesOutstandingReads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "complete_process_actions")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"complete_process_actions",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("complete_process_actions_process", "run_command"),
			builtInProcessToolCallBatchItem("complete_process_actions_queued", "read_process"),
			builtInProcessToolCallBatchItem("complete_process_actions_accepted", "read_process"),
		},
	)
	processToolCallID, queuedToolCallID, acceptedToolCallID := toolCallIDs[0], toolCallIDs[1], toolCallIDs[2]

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    processToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "cat",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		NilID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	markProcessStartedForTest(t, ctx, fixture, process, fixture.Now.Add(1500*time.Millisecond))
	acceptedAction, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    acceptedToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindRead,
		Payload:    json.RawMessage(`{"cursor":0,"wait_ms":1000}`),
	})
	if err != nil {
		t.Fatalf("create accepted action: %v", err)
	}
	if _, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		NilID); err != nil {
		t.Fatalf("accept action: %v", err)
	} else if !found {
		t.Fatal("expected action accept")
	}
	queuedAction, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    queuedToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindRead,
		Payload:    json.RawMessage(`{"cursor":0}`),
	})
	if err != nil {
		t.Fatalf("create queued action: %v", err)
	}
	exitCode := 0
	if _, err := fixture.Store.Execution().CompleteProcess(
		ctx,
		executionstore.CompleteProcessInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ID:            process.ID,
			RuntimeLockID: fixture.Lock.ID,
			State:         executionstore.ProcessStateExited,
			ExitCode:      &exitCode,
			SourceEndedAt: fixture.Now.Add(5 * time.Second),
		},
	); err != nil {
		t.Fatalf("complete process: %v", err)
	}
	queuedUpdated, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		queuedToolCallID,
	)
	if err != nil {
		t.Fatalf("get queued action: %v", err)
	}
	if !found || queuedUpdated.ID != queuedAction.ID || queuedUpdated.State != executionstore.ProcessActionStateQueued ||
		queuedUpdated.StateReasonCode != "" {
		t.Fatalf("queued action after complete process = found %v %+v", found, queuedUpdated)
	}
	acceptedUpdated, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		acceptedToolCallID,
	)
	if err != nil {
		t.Fatalf("get accepted action: %v", err)
	}
	if !found || acceptedUpdated.ID != acceptedAction.ID ||
		acceptedUpdated.State != executionstore.ProcessActionStateAccepted ||
		acceptedUpdated.StateReasonCode != "" {
		t.Fatalf("accepted action after complete process = found %v %+v", found, acceptedUpdated)
	}
	queuedToolCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		queuedToolCallID,
	)
	if err != nil {
		t.Fatalf("get queued action tool call: %v", err)
	}
	if queuedToolCall.State != executionstore.ToolCallStateWaiting ||
		queuedToolCall.CompletedAt != nil {
		t.Fatalf("queued read tool call = %+v", queuedToolCall)
	}
	acceptedToolCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		acceptedToolCallID,
	)
	if err != nil {
		t.Fatalf("get accepted action tool call: %v", err)
	}
	if acceptedToolCall.State != executionstore.ToolCallStateWaiting ||
		acceptedToolCall.CompletedAt != nil {
		t.Fatalf("accepted read tool call = %+v", acceptedToolCall)
	}
}

func TestCompleteProcessPreservesAcceptedTerminateEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "complete_process_terminate_action")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"complete_process_terminate_action",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("complete_process_terminate_action_process", "run_command"),
			builtInProcessToolCallBatchItem("complete_process_terminate_action_terminate", "stop_process"),
		},
	)
	processToolCallID, terminateToolCallID := toolCallIDs[0], toolCallIDs[1]

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    processToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "cat",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		NilID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	markProcessStartedForTest(t, ctx, fixture, process, fixture.Now.Add(1500*time.Millisecond))
	action, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    terminateToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindTerminate,
		Payload:    json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("create terminate action: %v", err)
	}
	if _, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		NilID); err != nil {
		t.Fatalf("accept terminate action: %v", err)
	} else if !found {
		t.Fatal("expected terminate action accept")
	}
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(
		ctx,
		executionstore.CompleteDaemonProcessInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ID:            process.ID,
			Authority:     fixture.authority(),
			State:         executionstore.ProcessStateKilled,
			SourceEndedAt: fixture.Now.Add(4 * time.Second),
		},
	); err != nil {
		t.Fatalf("complete daemon process: %v", err)
	}
	updated, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		terminateToolCallID,
	)
	if err != nil {
		t.Fatalf("get terminate action: %v", err)
	}
	if !found || updated.ID != action.ID ||
		updated.State != executionstore.ProcessActionStateAccepted ||
		updated.StateReasonCode != "" {
		t.Fatalf("accepted terminate after complete process = found %v %+v", found, updated)
	}
	reportInput := executionstore.CompleteDaemonProcessActionInput{
		ProjectID:       testProjectID,
		AgentID:         fixture.AgentID,
		ProcessID:       process.ID,
		ID:              action.ID,
		Authority:       fixture.authority(),
		StateReasonCode: "already_stopped",
	}
	application, err := fixture.Store.Execution().ApplyDaemonProcessAction(
		ctx,
		reportInput,
	)
	if err != nil || !application.ToolResultCommitted {
		t.Fatalf(
			"late terminate report = %+v err=%v, want committed evidence",
			application,
			err,
		)
	}
	if application.Action.StateReasonCode != "already_stopped" {
		t.Fatalf("late terminate application = %+v", application)
	}
	replayed, err := fixture.Store.Execution().ApplyDaemonProcessAction(ctx, reportInput)
	if err != nil || !replayed.ToolResultCommitted ||
		replayed.Action.StateReasonCode != "already_stopped" {
		t.Fatalf(
			"replayed late terminate report = %+v err=%v",
			replayed,
			err,
		)
	}
	resolved, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		terminateToolCallID,
	)
	if err != nil {
		t.Fatalf("get resolved terminate action: %v", err)
	}
	if !found || resolved.ID != action.ID ||
		resolved.State != executionstore.ProcessActionStateApplied ||
		resolved.StateReasonCode != "already_stopped" {
		t.Fatalf("terminate action after late report = found %v %+v", found, resolved)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, terminateToolCallID)
	if err != nil {
		t.Fatalf("get terminate tool call: %v", err)
	}
	assertCompletedProcessActionResult(
		t,
		fixture.Store,
		fixture.AgentID,
		toolCall,
		executionstore.ProcessActionStateApplied,
	)
	assertCompletedToolCallWithResult(
		t,
		fixture.Store,
		fixture.AgentID,
		toolCall,
		"already_stopped",
	)
}

func TestQueuedTerminateFailsWhenProcessBecomesUnknown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "queued_terminate_process_unknown")
	process := startRunningProcessForReadTest(
		t,
		ctx,
		fixture,
		"queued_terminate_process_unknown",
		nil,
	)
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"queued_terminate_process_unknown_stop",
		"stop_process",
	)
	action, err := createProcessActionForTest(
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
			ActionKind: executionstore.ProcessActionKindTerminate,
			Payload:    json.RawMessage(`{}`),
		},
	)
	if err != nil {
		t.Fatalf("create queued terminate: %v", err)
	}
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(
		ctx,
		executionstore.CompleteDaemonProcessInput{
			ProjectID:          testProjectID,
			AgentID:            fixture.AgentID,
			ID:                 process.ID,
			Authority:          fixture.authority(),
			State:              executionstore.ProcessStateUnknown,
			StateReasonCode:    "containment_unproven",
			StateReasonMessage: "the process may still exist",
			SourceEndedAt:      fixture.Now.Add(4 * time.Second),
		},
	); err != nil {
		t.Fatalf("mark process unknown: %v", err)
	}
	updated, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("get queued terminate: %v", err)
	}
	if !found ||
		updated.ID != action.ID ||
		updated.State != executionstore.ProcessActionStateFailed ||
		updated.StateReasonCode != "process_state_unknown" {
		t.Fatalf("queued terminate after unknown process = found %t %+v", found, updated)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("get terminate tool call: %v", err)
	}
	assertCompletedProcessActionResult(
		t,
		fixture.Store,
		fixture.AgentID,
		toolCall,
		executionstore.ProcessActionStateFailed,
	)
	assertCompletedToolCallWithResult(
		t,
		fixture.Store,
		fixture.AgentID,
		toolCall,
		"process_state_unknown",
	)

	interruptToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"interrupt_unknown_process",
		"stop_process",
	)
	_, err = createProcessActionForTest(
		ctx,
		fixture.Store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    interruptToolCallID,
			RuntimeLockID: fixture.Lock.ID,
		},
		executionstore.CreateProcessActionInput{
			ProcessID:  process.ID,
			ActionKind: executionstore.ProcessActionKindInterrupt,
			Payload:    json.RawMessage(`{}`),
		},
	)
	if !errors.Is(err, storeerr.ErrProcessStateUnknown) {
		t.Fatalf(
			"interrupt unknown process error = %v, want ErrProcessStateUnknown",
			err,
		)
	}
	if _, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		interruptToolCallID,
	); err != nil || found {
		t.Fatalf("interrupt unknown process action found=%t err=%v", found, err)
	}
}

func TestUnknownTerminateRemainsMutationBarrier(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "unknown_terminate_barrier")
	process := startRunningProcessForReadTest(
		t,
		ctx,
		fixture,
		"unknown_terminate_barrier",
		nil,
	)
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"unknown_terminate_barrier_actions",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("unknown_terminate_barrier_stop", "stop_process"),
			builtInProcessToolCallBatchItem("unknown_terminate_barrier_write", "write_process"),
			builtInProcessToolCallBatchItem("unknown_terminate_barrier_interrupt", "stop_process"),
			builtInProcessToolCallBatchItem("unknown_terminate_barrier_terminate", "stop_process"),
			builtInProcessToolCallBatchItem("unknown_terminate_barrier_read", "read_process"),
		},
	)
	toolCallIDsByLabel := map[string]ID{
		"unknown_terminate_barrier_stop":      toolCallIDs[0],
		"unknown_terminate_barrier_write":     toolCallIDs[1],
		"unknown_terminate_barrier_interrupt": toolCallIDs[2],
		"unknown_terminate_barrier_terminate": toolCallIDs[3],
		"unknown_terminate_barrier_read":      toolCallIDs[4],
	}
	createAction := func(
		label string,
		kind executionstore.ProcessActionKind,
		at time.Time,
	) (executionstore.ProcessActionRecord, error) {
		return createProcessActionForTest(
			ctx,
			fixture.Store,
			executionstore.ExecuteToolCallInput{
				ProjectID:     testProjectID,
				AgentID:       fixture.AgentID,
				ToolCallID:    toolCallIDsByLabel[label],
				RuntimeLockID: fixture.Lock.ID,
			},
			executionstore.CreateProcessActionInput{
				ProcessID:  process.ID,
				ActionKind: kind,
				Payload:    json.RawMessage(`{}`),
			},
		)
	}

	terminate, err := createAction(
		"unknown_terminate_barrier_stop",
		executionstore.ProcessActionKindTerminate,
		fixture.Now.Add(3*time.Second),
	)
	if err != nil {
		t.Fatalf("create terminate: %v", err)
	}
	if _, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		terminate.ID); err != nil || !found {
		t.Fatalf("accept terminate found=%t err=%v", found, err)
	}
	application, err := fixture.Store.Execution().MarkDaemonProcessActionUnknown(
		ctx,
		executionstore.CompleteDaemonProcessActionInput{
			ProjectID:          testProjectID,
			AgentID:            fixture.AgentID,
			ProcessID:          process.ID,
			ID:                 terminate.ID,
			Authority:          fixture.authority(),
			StateReasonCode:    "containment_unproven",
			StateReasonMessage: "the process may still exist",
		},
	)
	if err != nil || application.Action.State != executionstore.ProcessActionStateUnknown {
		t.Fatalf("mark terminate unknown = %+v err=%v", application, err)
	}

	for index, test := range []struct {
		name string
		kind executionstore.ProcessActionKind
	}{
		{name: "write", kind: executionstore.ProcessActionKindWrite},
		{name: "interrupt", kind: executionstore.ProcessActionKindInterrupt},
		{name: "terminate", kind: executionstore.ProcessActionKindTerminate},
	} {
		_, err := createAction(
			"unknown_terminate_barrier_"+test.name,
			test.kind,
			fixture.Now.Add(time.Duration(6+index)*time.Second),
		)
		if !errors.Is(err, storeerr.ErrProcessTerminating) {
			t.Fatalf("%s after unknown terminate error = %v", test.name, err)
		}
	}
	read, err := createAction(
		"unknown_terminate_barrier_read",
		executionstore.ProcessActionKindRead,
		fixture.Now.Add(9*time.Second),
	)
	if err != nil || read.State != executionstore.ProcessActionStateQueued {
		t.Fatalf("read after unknown terminate = %+v err=%v", read, err)
	}
}

func TestQueuedTerminateSucceedsWhenProcessAlreadyStopped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "queued_terminate_process_killed")
	process := startRunningProcessForReadTest(
		t,
		ctx,
		fixture,
		"queued_terminate_process_killed",
		nil,
	)
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"queued_terminate_process_killed_stop",
		"stop_process",
	)
	action, err := createProcessActionForTest(
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
			ActionKind: executionstore.ProcessActionKindTerminate,
			Payload:    json.RawMessage(`{}`),
		},
	)
	if err != nil {
		t.Fatalf("create queued terminate: %v", err)
	}
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(
		ctx,
		executionstore.CompleteDaemonProcessInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ID:            process.ID,
			Authority:     fixture.authority(),
			State:         executionstore.ProcessStateKilled,
			SourceEndedAt: fixture.Now.Add(4 * time.Second),
		},
	); err != nil {
		t.Fatalf("mark process killed: %v", err)
	}
	updated, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("get queued terminate: %v", err)
	}
	if !found ||
		updated.ID != action.ID ||
		updated.State != executionstore.ProcessActionStateApplied ||
		updated.StateReasonCode != "already_stopped" ||
		updated.StateReasonMessage != "" {
		t.Fatalf("queued terminate after killed process = found %t %+v", found, updated)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("get terminate tool call: %v", err)
	}
	assertCompletedProcessActionResult(
		t,
		fixture.Store,
		fixture.AgentID,
		toolCall,
		executionstore.ProcessActionStateApplied,
	)
	assertCompletedToolCallWithResult(
		t,
		fixture.Store,
		fixture.AgentID,
		toolCall,
		"already_stopped",
	)

	repeatedToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"queued_terminate_process_killed_stop_again",
		"stop_process",
	)
	_, err = createProcessActionForTest(
		ctx,
		fixture.Store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    repeatedToolCallID,
			RuntimeLockID: fixture.Lock.ID,
		},
		executionstore.CreateProcessActionInput{
			ProcessID:  process.ID,
			ActionKind: executionstore.ProcessActionKindTerminate,
			Payload:    json.RawMessage(`{}`),
		},
	)
	if !errors.Is(err, storeerr.ErrProcessAlreadyStopped) {
		t.Fatalf("repeated terminate error = %v, want ErrProcessAlreadyStopped", err)
	}
}

func TestCompleteProcessLeavesAppliedTerminateActionApplied(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "complete_process_applied_terminate_action")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"complete_process_applied_terminate_action",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("complete_process_applied_terminate_action_process", "run_command"),
			builtInProcessToolCallBatchItem("complete_process_applied_terminate_action_terminate", "stop_process"),
		},
	)
	processToolCallID, terminateToolCallID := toolCallIDs[0], toolCallIDs[1]

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    processToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "cat",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		NilID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	markProcessStartedForTest(t, ctx, fixture, process, fixture.Now.Add(1500*time.Millisecond))
	action, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    terminateToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindTerminate,
		Payload:    json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("create terminate action: %v", err)
	}
	if _, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		NilID); err != nil {
		t.Fatalf("accept terminate action: %v", err)
	} else if !found {
		t.Fatal("expected terminate action accept")
	}
	if _, err := fixture.Store.Execution().ApplyDaemonProcessAction(
		ctx,
		executionstore.CompleteDaemonProcessActionInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			ProcessID: process.ID,
			ID:        action.ID,
			Authority: fixture.authority(),
		},
	); err != nil {
		t.Fatalf("apply terminate action: %v", err)
	}
	if _, err := fixture.Store.Execution().CompleteProcess(
		ctx,
		executionstore.CompleteProcessInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ID:            process.ID,
			RuntimeLockID: fixture.Lock.ID,
			State:         executionstore.ProcessStateKilled,
			SourceEndedAt: fixture.Now.Add(4 * time.Second),
		},
	); err != nil {
		t.Fatalf("complete process: %v", err)
	}
	updated, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		terminateToolCallID,
	)
	if err != nil {
		t.Fatalf("get terminate action: %v", err)
	}
	if !found || updated.ID != action.ID || updated.State != executionstore.ProcessActionStateApplied {
		t.Fatalf("applied terminate after complete process = found %v %+v", found, updated)
	}
}

func TestDuplicateDaemonProcessActionReportReplaysTerminalState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		state         executionstore.ProcessActionState
		reasonCode    string
		reasonMessage string
		wantError     string
	}{
		{name: "applied", state: executionstore.ProcessActionStateApplied},
		{
			name:          "failed_message",
			state:         executionstore.ProcessActionStateFailed,
			reasonCode:    "read_failed",
			reasonMessage: "read pipe closed",
			wantError:     "read pipe closed",
		},
		{
			name:          "unknown",
			state:         executionstore.ProcessActionStateUnknown,
			reasonCode:    "action_outcome_unknown",
			reasonMessage: "action crossed its effect boundary",
			wantError:     "action crossed its effect boundary",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newProcessDaemonFixture(
				t,
				ctx,
				"action_report_replay_"+tt.name,
			)
			toolCallIDs := createToolCallBatchForProcessTest(
				t,
				ctx,
				fixture,
				"action_report_replay_"+tt.name,
				[]processToolCallBatchItem{
					builtInProcessToolCallBatchItem("action_report_replay_process_"+tt.name, "run_command"),
					builtInProcessToolCallBatchItem("action_report_replay_action_"+tt.name, "read_process"),
				},
			)
			processToolCallID, actionToolCallID := toolCallIDs[0], toolCallIDs[1]

			process, err := startProcessForTest(
				ctx,
				fixture.Store,
				executionstore.ExecuteToolCallInput{
					ProjectID:     testProjectID,
					AgentID:       fixture.AgentID,
					ToolCallID:    processToolCallID,
					RuntimeLockID: fixture.Lock.ID,
				},
				executionstore.CreateProcessInput{
					AgentMachineBindingID: fixture.BindingID,
					Command:               "cat",
					ShellSelector:         "sh",
					Cwd:                   "/work",
				},
			)
			if err != nil {
				t.Fatalf("start process: %v", err)
			}
			if _, found, err := acceptDaemonProcessForTest(
				ctx,
				fixture.Store,
				testOrgID,
				fixture.MachineID,
				fixture.RuntimeID,
				NilID); err != nil {
				t.Fatalf("accept process: %v", err)
			} else if !found {
				t.Fatal("expected process accept")
			}
			markProcessStartedForTest(
				t,
				ctx,
				fixture,
				process,
				fixture.Now.Add(1500*time.Millisecond),
			)
			action, err := createProcessActionForTest(
				ctx,
				fixture.Store,
				executionstore.ExecuteToolCallInput{
					ProjectID:     testProjectID,
					AgentID:       fixture.AgentID,
					ToolCallID:    actionToolCallID,
					RuntimeLockID: fixture.Lock.ID,
				},
				executionstore.CreateProcessActionInput{
					ProcessID:  process.ID,
					ActionKind: executionstore.ProcessActionKindWrite,
					Payload:    json.RawMessage(`{"data":"replay"}`),
				},
			)
			if err != nil {
				t.Fatalf("create process action: %v", err)
			}
			if _, found, err := acceptDaemonProcessActionForTest(
				ctx,
				fixture.Store,
				testOrgID,
				fixture.MachineID,
				fixture.RuntimeID,
				process.ID,
				action.ID); err != nil {
				t.Fatalf("accept action: %v", err)
			} else if !found {
				t.Fatal("expected action accept")
			}
			if _, found, err := acceptDaemonProcessActionForTest(
				ctx,
				fixture.Store,
				testOrgID,
				fixture.MachineID,
				fixture.RuntimeID,
				process.ID,
				action.ID); err != nil {
				t.Fatalf("repeat action accept: %v", err)
			} else if found {
				t.Fatal("accepted action should not accept a second time")
			}

			input := executionstore.CompleteDaemonProcessActionInput{
				ProjectID:          testProjectID,
				AgentID:            fixture.AgentID,
				ProcessID:          process.ID,
				ID:                 action.ID,
				Authority:          fixture.authority(),
				StateReasonCode:    tt.reasonCode,
				StateReasonMessage: tt.reasonMessage,
			}
			first, err := fixture.Store.Execution().IntegrationCompleteDaemonProcessAction(
				ctx,
				input,
				tt.state,
			)
			if err != nil {
				t.Fatalf("complete process action: %v", err)
			}
			if !first.ToolResultCommitted || first.Action.State != tt.state {
				t.Fatalf("first process action report was not committed: %+v", first)
			}
			toolCall, err := fixture.Store.Execution().GetToolCall(
				ctx,
				testProjectID,
				fixture.AgentID,
				actionToolCallID,
			)
			if err != nil {
				t.Fatalf("get completed action tool call: %v", err)
			}
			var resultParts []struct {
				Type  string `json:"type"`
				Value struct {
					Error string `json:"error"`
				} `json:"value"`
			}
			if err := json.Unmarshal(toolCall.ResultContentParts, &resultParts); err != nil {
				t.Fatalf("decode action tool result: %v", err)
			}
			if len(resultParts) != 1 ||
				resultParts[0].Type != "structured_data" ||
				resultParts[0].Value.Error != tt.wantError {
				t.Fatalf(
					"action tool result = %s, want message %q",
					toolCall.ResultContentParts,
					tt.wantError,
				)
			}

			second, err := fixture.Store.Execution().IntegrationCompleteDaemonProcessAction(
				ctx,
				input,
				tt.state,
			)
			if err != nil {
				t.Fatalf("duplicate process action should replay: %v", err)
			}
			if !second.ToolResultCommitted ||
				second.Action.ID != first.Action.ID ||
				second.Action.State != tt.state {
				t.Fatalf("unexpected replay action: %+v", second)
			}

			input.Result = json.RawMessage(
				`{"output":"different"}`,
			)
			conflict, err := fixture.Store.Execution().IntegrationCompleteDaemonProcessAction(
				ctx,
				input,
				tt.state,
			)
			if err != nil || conflict.ToolResultCommitted {
				t.Fatalf(
					"conflicting duplicate process action = %+v err=%v, want cleanup-only",
					conflict,
					err,
				)
			}
		})
	}
}

func TestDaemonProcessActionReportRetainsEvidenceWhenAnotherToolResultWon(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(
		t,
		ctx,
		"action_completion_requires_own_result",
	)
	processToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"action_completion_requires_own_result_process",
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
		process.ID); err != nil || !found {
		t.Fatalf("accept process found=%v err=%v", found, err)
	}
	markProcessStartedForTest(
		t,
		ctx,
		fixture,
		process,
		fixture.Now.Add(2*time.Second),
	)

	actionToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"action_completion_requires_own_result_action",
		"write_process",
	)
	action, err := createProcessActionForTest(
		ctx,
		fixture.Store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    actionToolCallID,
			RuntimeLockID: fixture.Lock.ID,
		},
		executionstore.CreateProcessActionInput{
			ProcessID:  process.ID,
			ActionKind: executionstore.ProcessActionKindWrite,
			Payload:    json.RawMessage(`{"data":"hello\n"}`),
		},
	)
	if err != nil {
		t.Fatalf("create process action: %v", err)
	}
	if _, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		action.ID); err != nil || !found {
		t.Fatalf("accept process action found=%v err=%v", found, err)
	}
	forceToolCallResultForTest(
		t,
		ctx,
		fixture.Store,
		testProjectID,
		fixture.AgentID,
		actionToolCallID,
		executionstore.ToolResultOutcomeFailed,
		json.RawMessage(`{"code":"externally_resolved"}`))

	application, err := fixture.Store.Execution().ApplyDaemonProcessAction(
		ctx,
		executionstore.CompleteDaemonProcessActionInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			ProcessID: process.ID,
			ID:        action.ID,
			Authority: fixture.authority(),
		},
	)
	if err != nil {
		t.Fatalf("apply process action after another result won: %v", err)
	}
	if application.ToolResultCommitted {
		t.Fatalf("action report incorrectly claimed the existing result: %+v", application)
	}
	current, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		actionToolCallID,
	)
	if err != nil || !found {
		t.Fatalf("get process action found=%v err=%v", found, err)
	}
	if current.State != executionstore.ProcessActionStateApplied {
		t.Fatalf("action state = %s, want applied", current.State)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		actionToolCallID,
	)
	if err != nil {
		t.Fatalf("get externally resolved tool call: %v", err)
	}
	assertCompletedToolCallWithResult(
		t,
		fixture.Store,
		fixture.AgentID,
		toolCall,
		"externally_resolved",
	)
}

func TestProcessActionAcceptRequiresCurrentMachineRuntimeAndActiveGrant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "action_accept")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"action_accept",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("action_accept_process", "read_process"),
			builtInProcessToolCallBatchItem("action_accept_write", "read_process"),
			builtInProcessToolCallBatchItem("action_accept_replay", "read_process"),
			builtInProcessToolCallBatchItem("action_accept_after_revoke", "read_process"),
		},
	)
	processToolCallID, actionToolCallID := toolCallIDs[0], toolCallIDs[1]
	replayToolCallID, afterRevokeToolCallID := toolCallIDs[2], toolCallIDs[3]

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    processToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "cat",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if _, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    actionToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindWrite,
		Payload:    json.RawMessage(`{"data":"hello\n"}`),
	}); !errors.Is(err, storeerr.ErrRuntimeLockInactive) {
		t.Fatalf("create process action before process start error = %v, want ErrRuntimeLockInactive", err)
	}
	if _, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		NilID); err != nil {
		t.Fatalf("accept action before process accept: %v", err)
	} else if found {
		t.Fatal("action should not accept before the process is granted and running")
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		NilID); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	markProcessStartedForTest(t, ctx, fixture, process, fixture.Now.Add(3500*time.Millisecond))
	action, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    actionToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindWrite,
		Payload:    json.RawMessage(`{"data":"hello\n"}`),
	})
	if err != nil {
		t.Fatalf("create process action after process start: %v", err)
	}
	if _, err := fixture.Store.Execution().DeleteProjectMachineGrant(
		ctx,
		testOrgID,
		testProjectID,
		fixture.GrantID,
	); err != nil {
		t.Fatalf("revoke grant: %v", err)
	}
	if _, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    replayToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindWrite,
		Payload:    json.RawMessage(`{"data":"hello\n"}`),
	}); err == nil {
		t.Fatal(
			"new process action should fail after project machine grant revocation while an earlier action is still queued",
		)
	}
	if _, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    afterRevokeToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindWrite,
		Payload:    json.RawMessage(`{"data":"after revoke\n"}`),
	}); err == nil {
		t.Fatal("create process action should fail after project machine grant revocation")
	}
	if _, found, err := acceptDaemonProcessActionForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		NilID); err != nil {
		t.Fatalf("accept action after revoke: %v", err)
	} else if found {
		t.Fatalf("action %s should not accept after project machine grant revocation", action.ID)
	}
}

func TestAcceptDaemonProcessActionRejectsKnownActionAfterProjectMachineGrantRevocation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "action_direct_accept_after_grant_revoke")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"action_direct_accept_after_grant_revoke",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("action_direct_accept_after_grant_revoke_process", "run_command"),
			builtInProcessToolCallBatchItem("action_direct_accept_after_grant_revoke_write", "read_process"),
		},
	)
	processToolCallID, actionToolCallID := toolCallIDs[0], toolCallIDs[1]
	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    processToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "cat",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID); err != nil ||
		!found {
		t.Fatalf("accept process found=%v err=%v", found, err)
	}
	markProcessStartedForTest(t, ctx, fixture, process, fixture.Now.Add(1500*time.Millisecond))
	action, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    actionToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindWrite,
		Payload:    json.RawMessage(`{"data":"hello\n"}`),
	})
	if err != nil {
		t.Fatalf("create process action: %v", err)
	}
	if _, err := fixture.Store.Execution().DeleteProjectMachineGrant(
		ctx,
		testOrgID,
		testProjectID,
		fixture.GrantID,
	); err != nil {
		t.Fatalf("revoke grant: %v", err)
	}
	if _, found, err := fixture.Store.Execution().AcceptDaemonProcessAction(
		ctx,
		executionstore.AcceptDaemonProcessActionInput{
			Authority: fixture.authority(),
			ProcessID: process.ID,
			ID:        action.ID,
		},
	); err != nil {
		t.Fatalf("direct accept action after grant revoke: %v", err)
	} else if found {
		t.Fatal("direct action accept should not succeed after project machine grant revocation")
	}
}

func TestProcessActionReplayRejectsDifferentTargetProcess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "action_replay_target")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"action_replay_target",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("action_replay_process_one", "read_process"),
			builtInProcessToolCallBatchItem("action_replay_process_two", "read_process"),
			builtInProcessToolCallBatchItem("action_replay_write", "read_process"),
		},
	)
	processOneToolCallID, processTwoToolCallID := toolCallIDs[0], toolCallIDs[1]
	actionToolCallID := toolCallIDs[2]

	processOne, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    processOneToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "cat",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start first process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		processOne.ID); err != nil ||
		!found {
		t.Fatalf("accept first process found=%v err=%v", found, err)
	}
	markProcessStartedForTest(t, ctx, fixture, processOne, fixture.Now.Add(750*time.Millisecond))
	processTwo, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    processTwoToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "cat",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start second process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		processTwo.ID); err != nil ||
		!found {
		t.Fatalf("accept second process found=%v err=%v", found, err)
	}
	markProcessStartedForTest(t, ctx, fixture, processTwo, fixture.Now.Add(1750*time.Millisecond))
	payload := json.RawMessage(`{"data":"hello\n"}`)
	if _, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    actionToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  processOne.ID,
		ActionKind: executionstore.ProcessActionKindWrite,
		Payload:    payload,
	}); err != nil {
		t.Fatalf("create first action: %v", err)
	}
	if _, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    actionToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  processTwo.ID,
		ActionKind: executionstore.ProcessActionKindWrite,
		Payload:    payload,
	}); !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("same tool call replay against a different process error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestProcessAndActionReplayByToolCall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "replay_by_tool_call")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"replay_by_tool_call",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("replay_new_runtime_process", "read_process"),
			builtInProcessToolCallBatchItem("replay_new_runtime_action", "read_process"),
		},
	)
	processToolCallID, actionToolCallID := toolCallIDs[0], toolCallIDs[1]

	processTransaction := executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    processToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}
	processInput := executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		IOMode:                "pipe",
		Command:               "cat",
		ShellSelector:         "sh",
	}
	process, err := startProcessForTest(ctx, fixture.Store, processTransaction, processInput)
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	binding := getAgentMachineBindingForTest(t, ctx, fixture.Store, testProjectID, fixture.AgentID, fixture.BindingID)
	if _, err := fixture.Store.q.UpdateAttachedAgentMachineBindingConfig(
		ctx,
		dbsqlc.UpdateAttachedAgentMachineBindingConfigParams{
			Description:      binding.Description,
			Cwd:              "/changed",
			EnvOverlay:       binding.EnvOverlay,
			SecretEnvOverlay: binding.SecretEnvOverlay,
			ProjectID:        testProjectID,
			AgentID:          fixture.AgentID,
			ID:               fixture.BindingID,
		},
	); err != nil {
		t.Fatalf("update binding defaults before replay: %v", err)
	}
	replayedProcess, err := startProcessForTest(ctx, fixture.Store, processTransaction, processInput)
	if err != nil {
		t.Fatalf("replay process by tool call: %v", err)
	}
	if replayedProcess.ID != process.ID {
		t.Fatalf("replayed process id = %s, want %s", replayedProcess.ID, process.ID)
	}
	if replayedProcess.Cwd != "/work" {
		t.Fatalf("replayed process cwd = %q, want original snapshot /work", replayedProcess.Cwd)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID); err != nil ||
		!found {
		t.Fatalf("accept replay process found=%v err=%v", found, err)
	}
	markProcessStartedForTest(t, ctx, fixture, process, fixture.Now.Add(2300*time.Millisecond))
	otherMachine, err := fixture.Store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          testOrgID,
			DisplayName:    "Replay target machine",
			IdempotencyKey: "idem-machine-replay-target",
		},
	)
	if err != nil {
		t.Fatalf("create replay target machine: %v", err)
	}
	otherGrant, _, err := fixture.Store.Execution().CreateProjectMachineGrant(
		ctx,
		executionstore.CreateProjectMachineGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachineID:      otherMachine.ID,
			IdempotencyKey: "idem-grant-replay-target",
		},
	)
	if err != nil {
		t.Fatalf("create replay target grant: %v", err)
	}
	otherBinding, err := executionstore.IntegrationInsertAgentMachineBindingTx(
		ctx,
		fixture.Store.q,
		executionstore.IntegrationInsertAgentMachineBindingInput{
			ProjectID:             testProjectID,
			AgentID:               fixture.AgentID,
			ProjectMachineGrantID: otherGrant.ID,
			MachineRef:            "mchr-repl42",
			BindingKind:           "explicit",
			Cwd:                   "/work",
		},
	)
	if err != nil {
		t.Fatalf("bind replay target machine: %v", err)
	}
	conflictingProcessInput := processInput
	conflictingProcessInput.AgentMachineBindingID = otherBinding.ID
	if _, err := startProcessForTest(ctx, fixture.Store, processTransaction, conflictingProcessInput); !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("replay process against different binding error = %v, want ErrIdempotencyConflict", err)
	}

	actionTransaction := executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    actionToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}
	actionInput := executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindWrite,
		Payload:    json.RawMessage(`{"data":"hello\n"}`),
	}
	action, err := createProcessActionForTest(ctx, fixture.Store, actionTransaction, actionInput)
	if err != nil {
		t.Fatalf("create process action: %v", err)
	}
	replayedAction, err := createProcessActionForTest(ctx, fixture.Store, actionTransaction, actionInput)
	if err != nil {
		t.Fatalf("replay action by tool call: %v", err)
	}
	if replayedAction.ID != action.ID {
		t.Fatalf("replayed action id = %s, want %s", replayedAction.ID, action.ID)
	}
}

func TestCreateProcessActionMissingProcessReturnsNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "missing_process_action")
	actionToolCallID := createToolCallForProcessActionTest(t, ctx, fixture, "missing_process_action")

	_, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    actionToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  testID("missing-process-action-target"),
		ActionKind: executionstore.ProcessActionKindRead,
		Payload:    json.RawMessage(`{"cursor":0,"max_bytes":4096,"wait_ms":0}`),
	})
	if !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("missing process action error = %v, want ErrNotFound", err)
	}
}
