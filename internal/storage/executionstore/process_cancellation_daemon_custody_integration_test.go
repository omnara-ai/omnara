//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestCancelAgentTerminatesAcceptedProcessBeforeRunCommandResolves(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "cancel_accepted_unresolved_process")
	publisher := &recordingPostCommitPublisher{}
	fixture.Store = newIntegrationStore(fixture.Store.pool, WithPostCommitPublisher(publisher))
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "cancel_accepted_unresolved_process", "run_command")

	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 30",
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
	if _, err := fixture.Store.Execution().CancelAgent(
		ctx,
		executionstore.CancelAgentInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			Actor:     mustOmnaraActorParams(t, fixture.UserID),
		},
	); err != nil {
		t.Fatalf("cancel agent: %v", err)
	}
	updated, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get process: %v", err)
	}
	if updated.State != executionstore.ProcessStateUnknown ||
		updated.StateReasonCode != "agent_canceled_after_grant" {
		t.Fatalf("cancel did not close accepted unresolved process: %+v", updated)
	}
	if !publisher.hasProcessTermination(fixture.MachineID, process.ID) {
		t.Fatal("cancel did not request termination for the accepted process")
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get linked tool call: %v", err)
	}
	assertCompletedToolCallWithResult(t, fixture.Store, fixture.AgentID, toolCall, "canceled")
	result, found, err := fixture.Store.Execution().GetToolCallResultAuthorityByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("get canceled tool result: %v", err)
	}
	if !found {
		t.Fatal("canceled tool result not found")
	}
	if result.Outcome != executionstore.ToolResultOutcomeCanceled {
		t.Fatalf("canceled tool result outcome = %q, want canceled", result.Outcome)
	}
	exitCode := 0
	application, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, executionstore.CompleteDaemonProcessInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ID:            process.ID,
		Authority:     fixture.authority(),
		State:         executionstore.ProcessStateExited,
		ExitCode:      &exitCode,
		Result:        json.RawMessage(`{"state":"exited","output":"late\n","cursor":0,"next_cursor":5,"truncated":false,"done":true}`),
		SourceEndedAt: fixture.Now.Add(3 * time.Second),
	})
	if err != nil {
		t.Fatalf("complete daemon process after cancel: %v", err)
	}
	if application.ToolResultCommitted {
		t.Fatalf("late daemon result replaced canceled run_command: %+v", application)
	}
	toolCall, err = fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get linked tool call after daemon completion: %v", err)
	}
	assertCompletedToolCallWithResult(t, fixture.Store, fixture.AgentID, toolCall, "canceled")
}

func TestCancelAgentPreservesProcessAfterRunCommandReturnsHandle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "cancel_preserves_resolved_process")
	publisher := &recordingPostCommitPublisher{}
	fixture.Store = newIntegrationStore(fixture.Store.pool, WithPostCommitPublisher(publisher))
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"cancel_preserves_resolved_process",
		"run_command",
	)
	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 30",
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
		t.Fatalf("accept process found=%v err=%v", found, err)
	}
	started, err := fixture.Store.Execution().MarkProcessStarted(ctx, executionstore.MarkProcessStartedInput{
		ProjectID:       testProjectID,
		AgentID:         fixture.AgentID,
		ID:              process.ID,
		Authority:       fixture.authority(),
		Result:          json.RawMessage(`{"state":"running","done":false}`),
		SourceStartedAt: fixture.Now.Add(2 * time.Second),
	})
	if err != nil || !started.ToolResultCommitted {
		t.Fatalf("commit started process result: application=%+v err=%v", started, err)
	}
	if _, err := fixture.Store.Execution().CancelAgent(ctx, executionstore.CancelAgentInput{
		ProjectID: testProjectID,
		AgentID:   fixture.AgentID,
		Actor:     mustOmnaraActorParams(t, fixture.UserID),
	}); err != nil {
		t.Fatalf("cancel agent: %v", err)
	}
	updated, err := fixture.Store.Execution().GetProcess(
		ctx,
		testProjectID,
		fixture.AgentID,
		process.ID,
	)
	if err != nil {
		t.Fatalf("get process: %v", err)
	}
	if updated.State != executionstore.ProcessStateRunning {
		t.Fatalf("cancel changed independently addressable process: %+v", updated)
	}
	if publisher.hasProcessTermination(fixture.MachineID, process.ID) {
		t.Fatal("cancel requested termination after run_command returned its handle")
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("get run_command: %v", err)
	}
	assertCompletedToolCallWithResult(
		t,
		fixture.Store,
		fixture.AgentID,
		toolCall,
		"next_action",
	)
}

func TestProcessReadinessAndAgentCancellationResolveAtomically(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "process_readiness_cancel_race")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"process_readiness_cancel_race",
		"run_command",
	)
	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 30",
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
		t.Fatalf("accept process found=%v err=%v", found, err)
	}

	agentLock, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin agent lock: %v", err)
	}
	defer func() { _ = agentLock.Rollback(ctx) }()
	if _, err := agentLock.Exec(
		ctx,
		`SELECT 1 FROM agents
		 WHERE project_id = $1 AND id = $2
		 FOR UPDATE`,
		testProjectID,
		fixture.AgentID,
	); err != nil {
		t.Fatalf("hold agent lock: %v", err)
	}

	type startOutcome struct {
		application executionstore.DaemonProcessReportApplication
		err         error
	}
	startDone := make(chan startOutcome, 1)
	go func() {
		application, startErr := fixture.Store.Execution().MarkProcessStarted(
			context.Background(),
			executionstore.MarkProcessStartedInput{
				ProjectID:       testProjectID,
				AgentID:         fixture.AgentID,
				ID:              process.ID,
				Authority:       fixture.authority(),
				SourceStartedAt: fixture.Now.Add(2 * time.Second),
			},
		)
		startDone <- startOutcome{application: application, err: startErr}
	}()
	cancelDone := make(chan error, 1)
	cancelActor := mustOmnaraActorParams(t, fixture.UserID)
	go func() {
		_, cancelErr := fixture.Store.Execution().CancelAgent(
			context.Background(),
			executionstore.CancelAgentInput{
				ProjectID: testProjectID,
				AgentID:   fixture.AgentID,
				Actor:     cancelActor,
			},
		)
		cancelDone <- cancelErr
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.Store.pool, "LockAgentInProject", 2)
	if err := agentLock.Commit(ctx); err != nil {
		t.Fatalf("release agent lock: %v", err)
	}
	start := <-startDone
	if start.err != nil {
		t.Fatalf("process started report: %v", start.err)
	}
	if err := <-cancelDone; err != nil {
		t.Fatalf("cancel agent: %v", err)
	}

	current, err := fixture.Store.Execution().GetProcess(
		ctx,
		testProjectID,
		fixture.AgentID,
		process.ID,
	)
	if err != nil {
		t.Fatalf("get process: %v", err)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("get run_command: %v", err)
	}
	switch current.State {
	case executionstore.ProcessStateRunning:
		if !start.application.ToolResultCommitted {
			t.Fatalf("readiness won without committing its result: %+v", start.application)
		}
		assertCompletedToolCallWithResult(
			t,
			fixture.Store,
			fixture.AgentID,
			toolCall,
			"next_action",
		)
	case executionstore.ProcessStateUnknown:
		if start.application.ToolResultCommitted ||
			current.StateReasonCode != "agent_canceled_after_grant" {
			t.Fatalf(
				"cancellation won with inconsistent report disposition: process=%+v report=%+v",
				current,
				start.application,
			)
		}
		assertCompletedToolCallWithResult(
			t,
			fixture.Store,
			fixture.AgentID,
			toolCall,
			"canceled",
		)
	default:
		t.Fatalf("readiness/cancel race resolved process to %q", current.State)
	}
	var resultCount int
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT count(*)::int
FROM tool_call_results result
JOIN tool_call_read_projection tool_call
  ON tool_call.agent_id = result.agent_id
 AND tool_call.id = result.tool_call_id
WHERE tool_call.project_id = $1 AND result.agent_id = $2 AND result.tool_call_id = $3
`, testProjectID, fixture.AgentID, toolCallID).Scan(&resultCount); err != nil {
		t.Fatalf("count run_command results: %v", err)
	}
	if resultCount != 1 {
		t.Fatalf("readiness/cancel race created %d tool results, want one", resultCount)
	}
}

func TestProcessAcceptAndAgentCancellationResolveAtomically(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "process_accept_cancel_race")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"process_accept_cancel_race",
		"run_command",
	)
	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 30",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}

	processLock, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin process lock: %v", err)
	}
	defer func() { _ = processLock.Rollback(ctx) }()
	if _, err := processLock.Exec(
		ctx,
		`SELECT 1 FROM processes
		 WHERE project_id = $1 AND agent_id = $2 AND id = $3
		 FOR UPDATE`,
		testProjectID,
		fixture.AgentID,
		process.ID,
	); err != nil {
		t.Fatalf("hold process lock: %v", err)
	}

	type acceptOutcome struct {
		found bool
		err   error
	}
	acceptDone := make(chan acceptOutcome, 1)
	go func() {
		_, found, acceptErr := fixture.Store.Execution().AcceptDaemonProcess(
			context.Background(),
			executionstore.AcceptDaemonProcessInput{
				Authority: fixture.authority(),
				ProcessID: process.ID,
			},
		)
		acceptDone <- acceptOutcome{found: found, err: acceptErr}
	}()
	cancelDone := make(chan error, 1)
	cancelActor := mustOmnaraActorParams(t, fixture.UserID)
	go func() {
		_, cancelErr := fixture.Store.Execution().CancelAgent(
			context.Background(),
			executionstore.CancelAgentInput{
				ProjectID: testProjectID,
				AgentID:   fixture.AgentID,
				Actor:     cancelActor,
			},
		)
		cancelDone <- cancelErr
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.Store.pool, "LockDaemonProcessForAccept", 1)
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.Store.pool, "CancelUnresolvedProcessesForAgentTurn", 1)
	if err := processLock.Commit(ctx); err != nil {
		t.Fatalf("release process lock: %v", err)
	}
	accepted := <-acceptDone
	if accepted.err != nil {
		t.Fatalf("accept process: %v", accepted.err)
	}
	if err := <-cancelDone; err != nil {
		t.Fatalf("cancel agent: %v", err)
	}

	current, err := fixture.Store.Execution().GetProcess(
		ctx,
		testProjectID,
		fixture.AgentID,
		process.ID,
	)
	if err != nil {
		t.Fatalf("get process: %v", err)
	}
	switch current.State {
	case executionstore.ProcessStateFailed:
		if accepted.found ||
			current.ExecutionGrantedAt != nil ||
			current.StateReasonCode != "agent_canceled_before_grant" {
			t.Fatalf(
				"pre-grant cancellation resolved inconsistently: process=%+v accepted=%v",
				current,
				accepted.found,
			)
		}
	case executionstore.ProcessStateUnknown:
		if !accepted.found ||
			current.ExecutionGrantedAt == nil ||
			current.StateReasonCode != "agent_canceled_after_grant" {
			t.Fatalf(
				"post-grant cancellation resolved inconsistently: process=%+v accepted=%v",
				current,
				accepted.found,
			)
		}
	default:
		t.Fatalf("accept/cancel race resolved process to %q", current.State)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("get run_command: %v", err)
	}
	assertCompletedToolCallWithResult(
		t,
		fixture.Store,
		fixture.AgentID,
		toolCall,
		"canceled",
	)
}

func TestAcceptedProcessActionCancellationAndDaemonReportResolveAtomically(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	fixture := newProcessDaemonFixture(
		t,
		ctx,
		"cancel_accepted_action_report_race",
	)
	processToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"cancel_accepted_action_report_race_process",
		"run_command",
	)
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
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID,
	); err != nil || !found {
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
		"cancel_accepted_action_report_race_action",
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
		action.ID,
	); err != nil || !found {
		t.Fatalf("accept process action found=%v err=%v", found, err)
	}

	agentLock, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin agent lock: %v", err)
	}
	defer func() { _ = agentLock.Rollback(ctx) }()
	if _, err := agentLock.Exec(
		ctx,
		`SELECT 1 FROM agents
		 WHERE project_id = $1 AND id = $2
		 FOR UPDATE`,
		testProjectID,
		fixture.AgentID,
	); err != nil {
		t.Fatalf("hold agent lock: %v", err)
	}

	cancelDone := make(chan error, 1)
	cancelActor := mustOmnaraActorParams(t, fixture.UserID)
	go func() {
		_, cancelErr := fixture.Store.Execution().CancelAgent(
			context.Background(),
			executionstore.CancelAgentInput{
				ProjectID: testProjectID,
				AgentID:   fixture.AgentID,
				Actor:     cancelActor,
			},
		)
		cancelDone <- cancelErr
	}()
	type reportOutcome struct {
		application executionstore.DaemonProcessActionReportApplication
		err         error
	}
	reportDone := make(chan reportOutcome, 1)
	go func() {
		application, reportErr := fixture.Store.Execution().ApplyDaemonProcessAction(
			context.Background(),
			executionstore.CompleteDaemonProcessActionInput{
				Authority: fixture.authority(),
				ProjectID: testProjectID,
				AgentID:   fixture.AgentID,
				ProcessID: process.ID,
				ID:        action.ID,
			},
		)
		reportDone <- reportOutcome{application: application, err: reportErr}
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.Store.pool, "LockAgentInProject", 2)
	if err := agentLock.Commit(ctx); err != nil {
		t.Fatalf("release agent lock: %v", err)
	}
	if err := <-cancelDone; err != nil {
		t.Fatalf("cancel agent: %v", err)
	}
	report := <-reportDone

	resolved, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		actionToolCallID,
	)
	if err != nil || !found {
		t.Fatalf("get resolved action found=%v err=%v", found, err)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		actionToolCallID,
	)
	if err != nil {
		t.Fatalf("get resolved action tool call: %v", err)
	}
	switch resolved.State {
	case executionstore.ProcessActionStateApplied:
		if report.err != nil || !report.application.ToolResultCommitted {
			t.Fatalf("winning daemon report = %+v err=%v", report.application, report.err)
		}
		assertCompletedProcessActionResult(
			t,
			fixture.Store,
			fixture.AgentID,
			toolCall,
			executionstore.ProcessActionStateApplied,
		)
	case executionstore.ProcessActionStateUnknown:
		if resolved.StateReasonCode != "agent_canceled_after_grant" {
			t.Fatalf("canceled accepted action = %+v", resolved)
		}
		if report.err != nil || report.application.ToolResultCommitted {
			t.Fatalf(
				"late daemon report = %+v err=%v, want cleanup-only",
				report.application,
				report.err,
			)
		}
		assertCompletedToolCallWithResult(
			t,
			fixture.Store,
			fixture.AgentID,
			toolCall,
			"canceled",
		)
	default:
		t.Fatalf("accepted action resolved to %q", resolved.State)
	}
	currentProcess, err := fixture.Store.Execution().GetProcess(
		ctx,
		testProjectID,
		fixture.AgentID,
		process.ID,
	)
	if err != nil {
		t.Fatalf("get process after action cancellation race: %v", err)
	}
	if currentProcess.State != executionstore.ProcessStateRunning {
		t.Fatalf("canceling a process action changed process state: %+v", currentProcess)
	}
	var resultCount, eventCount int
	if err := fixture.Store.pool.QueryRow(ctx, `
		SELECT count(*)::int
		FROM tool_call_results result
		JOIN tool_call_read_projection tool_call
		  ON tool_call.agent_id = result.agent_id
		 AND tool_call.id = result.tool_call_id
		WHERE tool_call.project_id = $1 AND result.agent_id = $2 AND result.tool_call_id = $3
	`, testProjectID, fixture.AgentID, actionToolCallID).Scan(
		&resultCount,
	); err != nil {
		t.Fatalf("count action tool results: %v", err)
	}
	if err := fixture.Store.pool.QueryRow(ctx, `
		SELECT count(*)::int
		FROM agent_events event
		JOIN tool_call_results result
		  ON result.agent_id = event.agent_id
		 AND result.id = event.tool_call_result_id
		JOIN tool_call_read_projection tool_call
		  ON tool_call.agent_id = result.agent_id
		 AND tool_call.id = result.tool_call_id
		WHERE tool_call.project_id = $1
		  AND result.agent_id = $2
		  AND result.tool_call_id = $3
	`, testProjectID, fixture.AgentID, actionToolCallID).Scan(
		&eventCount,
	); err != nil {
		t.Fatalf("count action tool result events: %v", err)
	}
	if resultCount != 1 || eventCount != 1 {
		t.Fatalf(
			"terminal action identities = %d results, %d events; want one of each",
			resultCount,
			eventCount,
		)
	}
}
