//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestQueuedProcessFailureSkipsCompletedToolCall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "queued_process_failure_skips_completed_tool")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"queued_process_failure_skips_completed_tool",
		"run_command",
	)
	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 3600",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	cancelToolCallForTest(
		t,
		ctx,
		fixture.Store,
		fixture.AgentID,
		toolCallID)

	if _, err := fixture.Store.q.MarkQueuedProcessFailedByMachine(ctx, dbsqlc.MarkQueuedProcessFailedByMachineParams{
		ProjectID:       testProjectID,
		AgentID:         fixture.AgentID,
		ID:              process.ID,
		OrgID:           fixture.OrgID,
		MachineID:       fixture.MachineID,
		StateReasonCode: sqlcTextFromEmpty(executionstore.ProcessToolReasonMachineUnreachable),
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("mark queued process with completed tool call err=%v, want no rows", err)
	}
	current, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get process: %v", err)
	}
	if current.State != executionstore.ProcessStateQueued || current.SourceEndedAt != nil {
		t.Fatalf("process after completed-tool failure attempt = %+v, want queued", current)
	}
}

func TestDaemonReportsRejectTerminalProcessThatWasNeverGranted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "ungranted_terminal_report")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"ungranted_terminal_report",
		"run_command",
	)
	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "echo should-not-run",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	appendStopEventForProcessTest(t, ctx, fixture)

	terminal, err := fixture.Store.Execution().GetProcess(
		ctx,
		testProjectID,
		fixture.AgentID,
		process.ID,
	)
	if err != nil {
		t.Fatalf("get canceled process: %v", err)
	}
	if terminal.State != executionstore.ProcessStateFailed ||
		terminal.ExecutionGrantedAt != nil ||
		terminal.StateReasonCode != "agent_canceled_before_grant" {
		t.Fatalf(
			"canceled process = %+v, want terminal without an execution grant",
			terminal,
		)
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
	); !errors.Is(err, storeerr.ErrProcessExecutionNotGranted) {
		t.Fatalf(
			"started report for never-granted process error = %v, want ErrProcessExecutionNotGranted",
			err,
		)
	}
	exitCode := 0
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(
		ctx,
		executionstore.CompleteDaemonProcessInput{
			ProjectID:       testProjectID,
			AgentID:         fixture.AgentID,
			ID:              process.ID,
			Authority:       fixture.authority(),
			State:           executionstore.ProcessStateExited,
			ExitCode:        &exitCode,
			SourceStartedAt: fixture.Now.Add(2 * time.Second),
			SourceEndedAt:   fixture.Now.Add(3 * time.Second),
		},
	); !errors.Is(err, storeerr.ErrProcessExecutionNotGranted) {
		t.Fatalf(
			"terminal report for never-granted process error = %v, want ErrProcessExecutionNotGranted",
			err,
		)
	}

	unchanged, err := fixture.Store.Execution().GetProcess(
		ctx,
		testProjectID,
		fixture.AgentID,
		process.ID,
	)
	if err != nil {
		t.Fatalf("get process after rejected reports: %v", err)
	}
	if unchanged.State != terminal.State ||
		unchanged.StateReasonCode != terminal.StateReasonCode ||
		unchanged.ExecutionGrantedAt != nil {
		t.Fatalf("rejected reports changed process: before=%+v after=%+v", terminal, unchanged)
	}
}

func TestDaemonProcessFailureBeforeStartHasNoSourceTimes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "failed_before_start_source_times")
	toolCallID := createToolCallForProcessActionTest(
		t,
		ctx,
		fixture,
		"failed_before_start_source_times",
	)
	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "invalid launch",
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
		NilID,
	); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}
	input := executionstore.CompleteDaemonProcessInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ID:                 process.ID,
		Authority:          fixture.authority(),
		State:              executionstore.ProcessStateFailed,
		StateReasonCode:    "start_failed",
		StateReasonMessage: "command did not start",
	}
	application, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, input)
	if err != nil {
		t.Fatalf("complete failed process: %v", err)
	}
	if application.Process.State != executionstore.ProcessStateFailed ||
		application.Process.SourceStartedAt != nil ||
		application.Process.SourceEndedAt != nil ||
		application.Process.StateChangedAt.IsZero() {
		t.Fatalf("failed-before-start process = %+v", application.Process)
	}
	replay, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, input)
	if err != nil {
		t.Fatalf("replay failed process completion: %v", err)
	}
	if replay.Process.ID != process.ID || replay.Process.SourceEndedAt != nil {
		t.Fatalf("failed-before-start replay = %+v", replay.Process)
	}
}

func TestProcessCompletionDoesNotOverwriteCompletedObservationToolCall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "completed_observation_skips_completed_tool")
	processToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"completed_observation_skips_completed_tool_process",
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
		"completed_observation_skips_completed_tool",
		"read_process",
	)
	action, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    actionToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindRead,
		Payload:    json.RawMessage(`{"cursor":0}`),
	})
	if err != nil {
		t.Fatalf("create read action: %v", err)
	}
	endedAt := fixture.Now.Add(4 * time.Second)
	cancelToolCallForTest(
		t,
		ctx,
		fixture.Store,
		fixture.AgentID,
		actionToolCallID)

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
			SourceEndedAt: endedAt,
		},
	); err != nil {
		t.Fatalf("complete process with completed observation tool call: %v", err)
	}
	current, found, err := fixture.Store.Execution().GetProcessActionByToolCall(ctx, testProjectID, fixture.AgentID, actionToolCallID)
	if err != nil {
		t.Fatalf("get read action: %v", err)
	}
	if !found || current.ID != action.ID || current.State != executionstore.ProcessActionStateQueued {
		t.Fatalf("read action after process completion found=%v action=%+v, want queued", found, current)
	}
}

func TestDuplicateDaemonProcessFinishedReportReplaysTerminalState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "process_finished_replay")
	toolCallID := createToolCallForProcessActionTest(t, ctx, fixture, "process_finished_replay")

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "echo hi",
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
	exitCode := 0
	input := executionstore.CompleteDaemonProcessInput{
		ProjectID:       testProjectID,
		AgentID:         fixture.AgentID,
		ID:              process.ID,
		Authority:       fixture.authority(),
		State:           executionstore.ProcessStateExited,
		ExitCode:        &exitCode,
		Result:          json.RawMessage(`{"output":"hi\n","cursor":0,"next_cursor":3,"truncated":false}`),
		SourceStartedAt: fixture.Now.Add(time.Second),
		SourceEndedAt:   fixture.Now.Add(2 * time.Second),
	}
	first, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, input)
	if err != nil {
		t.Fatalf("complete daemon process: %v", err)
	}
	if !first.ToolResultCommitted {
		t.Fatalf("first terminal report was not committed: %+v", first)
	}
	second, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, input)
	if err != nil {
		t.Fatalf("duplicate complete daemon process should replay: %v", err)
	}
	if !second.ToolResultCommitted ||
		second.Process.ID != first.Process.ID ||
		second.Process.State != executionstore.ProcessStateExited ||
		second.Process.ExitCode == nil ||
		*second.Process.ExitCode != exitCode {
		t.Fatalf("unexpected replay record: %+v", second)
	}
	input.Result = json.RawMessage(`{"output":"different\n"}`)
	conflict, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, input)
	if err != nil || conflict.ToolResultCommitted {
		t.Fatalf(
			"conflicting duplicate complete daemon process = %+v err=%v, want cleanup-only",
			conflict,
			err,
		)
	}
}

func TestCompleteDaemonProcessRejectsReversedSourceTimes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "process_finished_fast_command")
	toolCallID := createToolCallForProcessActionTest(t, ctx, fixture, "process_finished_fast_command")

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "echo fast",
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
	exitCode := 0
	endedAt := fixture.Now.Add(1500 * time.Millisecond)
	_, err = fixture.Store.Execution().CompleteDaemonProcess(
		ctx,
		executionstore.CompleteDaemonProcessInput{
			ProjectID:       testProjectID,
			AgentID:         fixture.AgentID,
			ID:              process.ID,
			Authority:       fixture.authority(),
			State:           executionstore.ProcessStateExited,
			ExitCode:        &exitCode,
			Result:          json.RawMessage(`{"output":"fast\n","cursor":0,"next_cursor":5,"truncated":false}`),
			SourceStartedAt: fixture.Now.Add(2 * time.Second),
			SourceEndedAt:   endedAt,
		},
	)
	if err == nil {
		t.Fatal("complete daemon process with reversed source times succeeded")
	}
}

func TestDaemonProcessFinishedPreservesAcceptedWriteEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "process_finished_late_write")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"process_finished_late_write",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("process_finished_late_write_process", "run_command"),
			builtInProcessToolCallBatchItem("process_finished_late_write_action", "write_process"),
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
		action.ID); err != nil {
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
		t.Fatalf("complete daemon process before action report: %v", err)
	}
	resolved, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		closeInputToolCallID,
	)
	if err != nil {
		t.Fatalf("get resolved close input action: %v", err)
	}
	if !found || resolved.ID != action.ID ||
		resolved.State != executionstore.ProcessActionStateAccepted ||
		resolved.StateReasonCode != "" {
		t.Fatalf("write action after process terminal = found %v %+v", found, resolved)
	}
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
	if err != nil || !application.ToolResultCommitted {
		t.Fatalf(
			"late write action report = %+v err=%v, want committed evidence",
			application,
			err,
		)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, closeInputToolCallID)
	if err != nil {
		t.Fatalf("get close input tool call: %v", err)
	}
	assertCompletedProcessActionResult(
		t,
		fixture.Store,
		fixture.AgentID,
		toolCall,
		executionstore.ProcessActionStateApplied,
	)
}

func TestDaemonProcessFinishedPreservesAcceptedTerminateEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "process_finished_late_terminate")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"process_finished_late_terminate",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("process_finished_late_terminate_process", "run_command"),
			builtInProcessToolCallBatchItem("process_finished_late_terminate_action", "stop_process"),
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
		Command:               "sleep 60",
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
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
		action.ID); err != nil {
		t.Fatalf("accept terminate action: %v", err)
	} else if !found {
		t.Fatal("expected terminate action accept")
	}
	endedAt := fixture.Now.Add(3 * time.Second)
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(
		ctx,
		executionstore.CompleteDaemonProcessInput{
			ProjectID:          testProjectID,
			AgentID:            fixture.AgentID,
			ID:                 process.ID,
			Authority:          fixture.authority(),
			State:              executionstore.ProcessStateKilled,
			StateReasonCode:    "terminate_requested",
			StateReasonMessage: "terminate-mode stop requested",
			Result:             json.RawMessage(`{"output":"","cursor":0,"next_cursor":0,"truncated":false}`),
			SourceEndedAt:      endedAt,
		},
	); err != nil {
		t.Fatalf("complete daemon process before terminate report: %v", err)
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
		resolved.State != executionstore.ProcessActionStateAccepted ||
		resolved.StateReasonCode != "" {
		t.Fatalf("terminate action after process terminal = found %v %+v", found, resolved)
	}
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
	if err != nil || !application.ToolResultCommitted {
		t.Fatalf(
			"late terminate action report = %+v err=%v, want committed evidence",
			application,
			err,
		)
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
}
