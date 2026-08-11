//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestDeleteMachineEndsDaemonRuntime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	mustCreateProjectOperatorUser(t, ctx, store, "archive-machine@example.com", "Archive Machine Tester")
	createdMachine, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          testOrgID,
			DisplayName:    "Archive Machine Machine",
			IdempotencyKey: "idem-archive-machine",
		},
	)
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}
	_, machine, err := store.Execution().CreateProjectMachineGrant(
		ctx,
		executionstore.CreateProjectMachineGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachineID:      createdMachine.ID,
			IdempotencyKey: "idem-pmg-archive-machine",
		},
	)
	if err != nil {
		t.Fatalf("create project machine grant: %v", err)
	}
	token, err := store.Execution().CreateBYOMachineDaemonToken(
		ctx,
		executionstore.CreateBYOMachineDaemonTokenInput{
			OrgID:     testOrgID,
			MachineID: createdMachine.ID,
			Name:      "daemon",
			Token:     "token-archive-machine",
		},
	)
	if err != nil {
		t.Fatalf("create daemon token: %v", err)
	}
	if _, err := store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            createdMachine.OrgID,
			MachineID:        createdMachine.ID,
			DaemonTokenID:    token.ID,
			DaemonInstanceID: testID("daemon-delete-machine"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     time.Hour,
		},
	); err != nil {
		t.Fatalf("register daemon runtime: %v", err)
	}
	assertMachineState(t, ctx, store, machine.ID, executionstore.MachineLifecycleStateActive, executionstore.MachineConnectionStateOnline)
	if _, err := store.Execution().DeleteMachine(
		ctx,
		executionstore.DeleteMachineInput{
			OrgID:     testOrgID,
			MachineID: createdMachine.ID,
		},
	); err != nil {
		t.Fatalf("delete machine: %v", err)
	}
	assertMachineState(t, ctx, store, machine.ID, executionstore.MachineLifecycleStateDeleted, executionstore.MachineConnectionStateOffline)
}

func TestDeleteMachineFailsQueuedProcessBeforeExecutionGrant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "delete_machine_queued_process")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "delete_machine_queued_process", "run_command")
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
	if _, err := fixture.Store.Execution().DeleteMachine(
		ctx,
		executionstore.DeleteMachineInput{
			OrgID:     fixture.OrgID,
			MachineID: fixture.MachineID,
		},
	); err != nil {
		t.Fatalf("delete machine: %v", err)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get tool call: %v", err)
	}
	assertCompletedToolCallWithResult(t, fixture.Store, fixture.AgentID, toolCall, "machine_deleted")
	current, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get process: %v", err)
	}
	if current.State != executionstore.ProcessStateFailed ||
		current.StateReasonCode != "machine_deleted" ||
		current.SourceEndedAt != nil ||
		current.ExecutionGrantedAt != nil {
		t.Fatalf("deleted-machine queued process = %+v, want failed/machine_deleted", current)
	}
}

func TestStartProcessFailsAfterMachineDeleted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "delete_machine_rejects_new_process")
	if _, err := fixture.Store.Execution().DeleteMachine(
		ctx,
		executionstore.DeleteMachineInput{
			OrgID:     fixture.OrgID,
			MachineID: fixture.MachineID,
		},
	); err != nil {
		t.Fatalf("delete machine: %v", err)
	}
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "delete_machine_rejects_new_process", "run_command")
	if _, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 3600",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	}); !errors.Is(err, storeerr.ErrMachineNotReachable) {
		t.Fatalf("start process after machine delete err=%v, want ErrMachineNotReachable", err)
	}
	if _, found, err := fixture.Store.Execution().GetProcessByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	); err != nil ||
		found {
		t.Fatalf("process by tool call after delete found=%v err=%v, want no process", found, err)
	}
}

func TestStartProcessReplaySucceedsAfterMachineDeleted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "delete_machine_replays_existing_process")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "delete_machine_replays_existing_process", "run_command")
	transaction := executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}
	input := executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 3600",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	}
	process, err := startProcessForTest(ctx, fixture.Store, transaction, input)
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if _, err := fixture.Store.Execution().DeleteMachine(
		ctx,
		executionstore.DeleteMachineInput{
			OrgID:     fixture.OrgID,
			MachineID: fixture.MachineID,
		},
	); err != nil {
		t.Fatalf("delete machine: %v", err)
	}
	replay, err := startProcessForTest(ctx, fixture.Store, transaction, input)
	if err != nil {
		t.Fatalf("replay start process after machine delete: %v", err)
	}
	if replay.ID != process.ID {
		t.Fatalf("replay process id = %s, want existing %s", replay.ID, process.ID)
	}
}

func TestDeleteMachineUnknownsGrantedProcessAfterRuntimeEnds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "delete_machine_ended_runtime_process")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "delete_machine_ended_runtime_process", "run_command")
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
	grant, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID)

	if err != nil || !found {
		t.Fatalf("accept process found=%v err=%v", found, err)
	}
	if grant.Process.ExecutionGrantedAt == nil {
		t.Fatal("accepted process is missing execution grant time")
	}
	expireDaemonRuntimeForTest(t, ctx, fixture)
	if records, err := fixture.Store.Execution().EndExpiredDaemonRuntimes(
		ctx,
		10); err != nil ||
		len(records) != 1 {
		t.Fatalf("end expired daemon runtime records=%d err=%v", len(records), err)
	}
	if _, err := fixture.Store.Execution().DeleteMachine(
		ctx,
		executionstore.DeleteMachineInput{
			OrgID:     fixture.OrgID,
			MachineID: fixture.MachineID,
		},
	); err != nil {
		t.Fatalf("delete machine: %v", err)
	}
	current, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get process: %v", err)
	}
	if current.State != executionstore.ProcessStateUnknown ||
		current.StateReasonCode != "machine_deleted" ||
		current.SourceEndedAt != nil ||
		current.ExecutionGrantedAt == nil ||
		!current.ExecutionGrantedAt.Equal(*grant.Process.ExecutionGrantedAt) {
		t.Fatalf("deleted-machine ended-runtime process = %+v, want unknown/machine_deleted", current)
	}
}

func TestDeleteMachineResolvesTerminalProcessActions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(
		t,
		ctx,
		"delete_machine_terminal_actions",
	)
	cases := []struct {
		name      string
		toolName  string
		kind      executionstore.ProcessActionKind
		accepted  bool
		wantState executionstore.ProcessActionState
	}{
		{
			name:      "queued_read",
			toolName:  "read_process",
			kind:      executionstore.ProcessActionKindRead,
			wantState: executionstore.ProcessActionStateFailed,
		},
		{
			name:      "accepted_read",
			toolName:  "read_process",
			kind:      executionstore.ProcessActionKindRead,
			accepted:  true,
			wantState: executionstore.ProcessActionStateFailed,
		},
		{
			name:      "accepted_write",
			toolName:  "write_process",
			kind:      executionstore.ProcessActionKindWrite,
			accepted:  true,
			wantState: executionstore.ProcessActionStateUnknown,
		},
	}
	type seededAction struct {
		processID  ID
		actionID   ID
		toolCallID ID
		wantState  executionstore.ProcessActionState
	}
	inputs := make([]terminalProcessActionTestInput, len(cases))
	for index, testCase := range cases {
		inputs[index] = terminalProcessActionTestInput{
			Name:     "delete_machine_terminal_" + testCase.name,
			ToolName: testCase.toolName,
			Kind:     testCase.kind,
			Accepted: testCase.accepted,
		}
	}
	results := createTerminalProcessActionsForLifecycleTest(t, ctx, fixture, inputs)
	seeded := make([]seededAction, 0, len(cases))
	for index, testCase := range cases {
		result := results[index]
		seeded = append(seeded, seededAction{
			processID:  result.Process.ID,
			actionID:   result.Action.ID,
			toolCallID: result.ToolCallID,
			wantState:  testCase.wantState,
		})
	}
	if _, err := fixture.Store.Execution().DeleteMachine(
		ctx,
		executionstore.DeleteMachineInput{
			OrgID:     fixture.OrgID,
			MachineID: fixture.MachineID,
		},
	); err != nil {
		t.Fatalf("delete machine: %v", err)
	}
	for _, expected := range seeded {
		action, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
			ctx,
			testProjectID,
			fixture.AgentID,
			expected.toolCallID,
		)
		if err != nil {
			t.Fatalf("get terminal process action: %v", err)
		}
		if !found ||
			action.ID != expected.actionID ||
			action.State != expected.wantState ||
			action.StateReasonCode != "machine_deleted" {
			t.Fatalf(
				"terminal process action after machine deletion = found %t %+v",
				found,
				action,
			)
		}
		toolCall, err := fixture.Store.Execution().GetToolCall(
			ctx,
			testProjectID,
			fixture.AgentID,
			expected.toolCallID,
		)
		if err != nil {
			t.Fatalf("get terminal process action tool call: %v", err)
		}
		assertCompletedToolCallWithResult(
			t,
			fixture.Store,
			fixture.AgentID,
			toolCall,
			"machine_deleted",
		)
		process, err := fixture.Store.Execution().GetProcess(
			ctx,
			testProjectID,
			fixture.AgentID,
			expected.processID,
		)
		if err != nil {
			t.Fatalf("get terminal process: %v", err)
		}
		if process.State != executionstore.ProcessStateExited {
			t.Fatalf("machine deletion rewrote terminal process: %+v", process)
		}
	}
}

func TestMachineUnreachableProcessToolCallUnblocksWithoutTerminalizingProcess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "machine_unreachable_process_tool")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "machine_unreachable_process_tool", "run_command")
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
	expireDaemonRuntimeForTest(t, ctx, fixture)
	if records, err := fixture.Store.Execution().EndExpiredDaemonRuntimes(
		ctx,
		10); err != nil ||
		len(records) != 1 {
		t.Fatalf("end expired daemon runtime records=%d err=%v", len(records), err)
	}
	if expired, err := fixture.Store.Execution().ExpireMachineUnreachableProcessToolCallsForAllProjects(
		ctx, executionstore.ProcessToolMachineUnreachableGrace); err != nil ||
		expired != 0 {
		t.Fatalf("early machine-unreachable expiry count=%d err=%v", expired, err)
	}
	if expired, err := fixture.Store.Execution().ExpireMachineUnreachableProcessToolCallsForAllProjects(
		ctx, 0); err != nil ||
		expired != 1 {
		t.Fatalf("machine-unreachable expiry count=%d err=%v", expired, err)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get tool call: %v", err)
	}
	assertCompletedToolCallWithResult(t, fixture.Store, fixture.AgentID, toolCall, executionstore.ProcessToolReasonMachineUnreachable)
	current, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get process: %v", err)
	}
	if current.State != executionstore.ProcessStateStarting ||
		current.SourceEndedAt != nil ||
		current.ExecutionGrantedAt == nil {
		t.Fatalf("process after machine-unreachable tool expiry = %+v, want durable starting/non-terminal", current)
	}
}

func TestMachineUnreachableResolvesTerminalProcessActions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(
		t,
		ctx,
		"machine_unreachable_terminal_actions",
	)
	cases := []struct {
		name      string
		toolName  string
		kind      executionstore.ProcessActionKind
		accepted  bool
		wantState executionstore.ProcessActionState
	}{
		{
			name:      "queued_read",
			toolName:  "read_process",
			kind:      executionstore.ProcessActionKindRead,
			wantState: executionstore.ProcessActionStateFailed,
		},
		{
			name:      "accepted_read",
			toolName:  "read_process",
			kind:      executionstore.ProcessActionKindRead,
			accepted:  true,
			wantState: executionstore.ProcessActionStateFailed,
		},
		{
			name:      "accepted_write",
			toolName:  "write_process",
			kind:      executionstore.ProcessActionKindWrite,
			accepted:  true,
			wantState: executionstore.ProcessActionStateUnknown,
		},
	}
	type seededAction struct {
		actionID   ID
		toolCallID ID
		wantState  executionstore.ProcessActionState
	}
	inputs := make([]terminalProcessActionTestInput, len(cases))
	for index, testCase := range cases {
		inputs[index] = terminalProcessActionTestInput{
			Name:     "machine_unreachable_terminal_" + testCase.name,
			ToolName: testCase.toolName,
			Kind:     testCase.kind,
			Accepted: testCase.accepted,
		}
	}
	results := createTerminalProcessActionsForLifecycleTest(t, ctx, fixture, inputs)
	seeded := make([]seededAction, 0, len(cases))
	for index, testCase := range cases {
		result := results[index]
		seeded = append(seeded, seededAction{
			actionID:   result.Action.ID,
			toolCallID: result.ToolCallID,
			wantState:  testCase.wantState,
		})
	}
	expireDaemonRuntimeForTest(t, ctx, fixture)
	if records, err := fixture.Store.Execution().EndExpiredDaemonRuntimes(
		ctx,
		10); err != nil || len(records) != 1 {
		t.Fatalf("end expired daemon runtime records=%d err=%v", len(records), err)
	}
	if expired, err :=
		fixture.Store.Execution().ExpireMachineUnreachableProcessToolCallsForAllProjects(
			ctx, 0); err != nil || expired != int64(len(seeded)) {
		t.Fatalf(
			"terminal action machine-unreachable expiry count=%d err=%v",
			expired,
			err,
		)
	}
	for _, expected := range seeded {
		action, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
			ctx,
			testProjectID,
			fixture.AgentID,
			expected.toolCallID,
		)
		if err != nil {
			t.Fatalf("get terminal process action: %v", err)
		}
		if !found ||
			action.ID != expected.actionID ||
			action.State != expected.wantState ||
			action.StateReasonCode !=
				executionstore.ProcessToolReasonMachineUnreachable {
			t.Fatalf(
				"terminal action after machine unreachable = found %t %+v",
				found,
				action,
			)
		}
		toolCall, err := fixture.Store.Execution().GetToolCall(
			ctx,
			testProjectID,
			fixture.AgentID,
			expected.toolCallID,
		)
		if err != nil {
			t.Fatalf("get terminal process action tool call: %v", err)
		}
		assertCompletedToolCallWithResult(
			t,
			fixture.Store,
			fixture.AgentID,
			toolCall,
			executionstore.ProcessToolReasonMachineUnreachable,
		)
		completed := completedToolCallForTest(
			t,
			fixture.Store,
			fixture.AgentID,
			toolCall.TurnID,
			toolCall.ID,
		)
		var parts []struct {
			Type  string `json:"type"`
			Value struct {
				Retryable bool `json:"retryable"`
			} `json:"value"`
		}
		if err := json.Unmarshal(completed.ResultContentParts, &parts); err != nil {
			t.Fatalf("decode machine-unreachable action result: %v", err)
		}
		if len(parts) != 1 ||
			parts[0].Type != "structured_data" ||
			!parts[0].Value.Retryable {
			t.Fatalf(
				"machine-unreachable action result must be retryable: %s",
				completed.ResultContentParts,
			)
		}
	}
}

func TestLateProcessFinishedReportPreservesMachineUnreachableToolResult(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "machine_unreachable_late_process_report")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "machine_unreachable_late_process_report", "run_command")
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
	expiredAt := fixture.Now.Add(3 * time.Second)
	expireDaemonRuntimeForTest(t, ctx, fixture)
	processPublicID := publicResourceID(publicid.KindProcess, process.ID)
	unreachableResult, err := executionstore.MachineUnreachableToolResult(
		map[string]any{"process_id": processPublicID, "state": process.State},
	)
	if err != nil {
		t.Fatalf("machine-unreachable result: %v", err)
	}
	completed, err := fixture.Store.Execution().IntegrationCompleteMachineUnreachableToolCall(
		ctx,
		fixture.OrgID,
		fixture.MachineID,
		process.CreatedAt,
		testProjectID,
		fixture.AgentID,
		toolCallID,
		unreachableResult,
		0,
	)

	if err != nil || !completed {
		t.Fatalf("complete machine-unreachable tool call completed=%v err=%v", completed, err)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get tool call after machine unreachable: %v", err)
	}
	assertCompletedToolCallWithResult(t, fixture.Store, fixture.AgentID, toolCall, executionstore.ProcessToolReasonMachineUnreachable)

	exitCode := 0
	input := executionstore.CompleteDaemonProcessInput{
		ProjectID: testProjectID,
		AgentID:   fixture.AgentID,
		ID:        process.ID,
		Authority: fixture.authority(),
		State:     executionstore.ProcessStateExited,
		ExitCode:  &exitCode,
		Result: json.RawMessage(
			`{"process_id":"prc_late","state":"exited","output":"late\n","done":true}`,
		),
		SourceStartedAt: expiredAt.Add(time.Second),
		SourceEndedAt:   expiredAt.Add(2 * time.Second),
	}
	first, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, input)
	if err != nil {
		t.Fatalf("complete late daemon process: %v", err)
	}
	if first.ToolResultCommitted {
		t.Fatalf("late result replaced machine-unreachable result: %+v", first)
	}
	replayed, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, input)
	if err != nil {
		t.Fatalf("replay late daemon process: %v", err)
	}
	if replayed.ToolResultCommitted {
		t.Fatalf("replayed late result replaced machine-unreachable result: %+v", replayed)
	}
	if replayed.Process.ID != first.Process.ID {
		t.Fatalf("replayed process id = %s, want %s", replayed.Process.ID, first.Process.ID)
	}
	current, err := fixture.Store.Execution().GetProcess(
		ctx,
		testProjectID,
		fixture.AgentID,
		process.ID,
	)
	if err != nil {
		t.Fatalf("get process after late report: %v", err)
	}
	if current.ID != first.Process.ID || current.State != executionstore.ProcessStateExited {
		t.Fatalf("process after late report = %+v, want process %s exited", current, first.Process.ID)
	}
	toolCall, err = fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get tool call after late daemon process: %v", err)
	}
	assertCompletedToolCallWithResult(t, fixture.Store, fixture.AgentID, toolCall, executionstore.ProcessToolReasonMachineUnreachable)
}

func TestMachineUnreachableResolvesAcceptedActionsInSequence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "machine_unreachable_action_tool")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"machine_unreachable_action_sequence",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("machine_unreachable_action_process", "run_command"),
			builtInProcessToolCallBatchItem("machine_unreachable_action_tool", "write_process"),
			builtInProcessToolCallBatchItem("machine_unreachable_second_action_tool", "read_process"),
		},
	)
	processToolCallID, actionToolCallID, secondToolCallID :=
		toolCallIDs[0], toolCallIDs[1], toolCallIDs[2]
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
		t.Fatalf("create write action: %v", err)
	}
	accepted, ok, err := fixture.Store.Execution().AcceptDaemonProcessAction(
		ctx,
		executionstore.AcceptDaemonProcessActionInput{
			Authority: fixture.authority(),
			ProcessID: process.ID,
			ID:        action.ID,
		},
	)
	if err != nil || !ok {
		t.Fatalf("accept write action ok=%v err=%v", ok, err)
	}
	if accepted.Action.State != executionstore.ProcessActionStateAccepted {
		t.Fatalf("accepted action state = %s, want accepted", accepted.Action.State)
	}
	secondAction, err := createProcessActionForTest(
		ctx,
		fixture.Store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    secondToolCallID,
			RuntimeLockID: fixture.Lock.ID,
		},
		executionstore.CreateProcessActionInput{
			ProcessID:  process.ID,
			ActionKind: executionstore.ProcessActionKindRead,
			Payload:    json.RawMessage(`{"cursor":0}`),
		},
	)
	if err != nil {
		t.Fatalf("create second action: %v", err)
	}
	if _, ok, err := fixture.Store.Execution().AcceptDaemonProcessAction(
		ctx,
		executionstore.AcceptDaemonProcessActionInput{
			Authority: fixture.authority(),
			ProcessID: process.ID,
			ID:        secondAction.ID,
		},
	); err != nil {
		t.Fatalf("attempt second action acceptance: %v", err)
	} else if ok {
		t.Fatal("second same-process action accepted before the first resolved")
	}
	if action.Seq != 1 || secondAction.Seq != 2 {
		t.Fatalf(
			"accepted action sequence = %d, %d; want 1, 2",
			action.Seq,
			secondAction.Seq,
		)
	}
	expireDaemonRuntimeForTest(t, ctx, fixture)
	if records, err := fixture.Store.Execution().EndExpiredDaemonRuntimes(
		ctx,
		10); err != nil ||
		len(records) != 1 {
		t.Fatalf("end expired daemon runtime records=%d err=%v", len(records), err)
	}
	if expired, err := fixture.Store.Execution().ExpireMachineUnreachableProcessToolCallsForAllProjects(
		ctx, 0); err != nil ||
		expired != 2 {
		t.Fatalf("machine-unreachable action expiry count=%d err=%v", expired, err)
	}
	for _, expected := range []struct {
		toolCallID ID
		actionID   ID
		state      executionstore.ProcessActionState
	}{
		{
			toolCallID: actionToolCallID,
			actionID:   action.ID,
			state:      executionstore.ProcessActionStateUnknown,
		},
		{
			toolCallID: secondToolCallID,
			actionID:   secondAction.ID,
			state:      executionstore.ProcessActionStateFailed,
		},
	} {
		toolCall, err := fixture.Store.Execution().GetToolCall(
			ctx,
			testProjectID,
			fixture.AgentID,
			expected.toolCallID,
		)
		if err != nil {
			t.Fatalf("get action tool call: %v", err)
		}
		assertCompletedToolCallWithResult(
			t,
			fixture.Store,
			fixture.AgentID,
			toolCall,
			executionstore.ProcessToolReasonMachineUnreachable,
		)
		current, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
			ctx,
			testProjectID,
			fixture.AgentID,
			expected.toolCallID,
		)
		if err != nil {
			t.Fatalf("get action by tool call: %v", err)
		}
		if !found || current.ID != expected.actionID ||
			current.State != expected.state ||
			current.StateReasonCode !=
				executionstore.ProcessToolReasonMachineUnreachable {
			t.Fatalf(
				"action after machine-unreachable resolution found=%v action=%+v",
				found,
				current,
			)
		}
	}
}

func TestLateAcceptedActionReportAfterMachineUnreachableIsCleanupOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "machine_unreachable_late_action_report")
	processToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"machine_unreachable_late_action_process",
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
		"machine_unreachable_late_action_tool",
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
		t.Fatalf("create write action: %v", err)
	}
	if _, ok, err := fixture.Store.Execution().AcceptDaemonProcessAction(
		ctx,
		executionstore.AcceptDaemonProcessActionInput{
			Authority: fixture.authority(),
			ProcessID: process.ID,
			ID:        action.ID,
		},
	); err != nil ||
		!ok {
		t.Fatalf("accept write action ok=%v err=%v", ok, err)
	}
	expireDaemonRuntimeForTest(t, ctx, fixture)
	if records, err := fixture.Store.Execution().EndExpiredDaemonRuntimes(
		ctx,
		10); err != nil ||
		len(records) != 1 {
		t.Fatalf("end expired daemon runtime records=%d err=%v", len(records), err)
	}
	if expired, err := fixture.Store.Execution().ExpireMachineUnreachableProcessToolCallsForAllProjects(
		ctx, 0); err != nil ||
		expired != 1 {
		t.Fatalf("machine-unreachable action expiry count=%d err=%v", expired, err)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, actionToolCallID)
	if err != nil {
		t.Fatalf("get action tool call after machine unreachable: %v", err)
	}
	assertCompletedToolCallWithResult(t, fixture.Store, fixture.AgentID, toolCall, executionstore.ProcessToolReasonMachineUnreachable)

	input := executionstore.CompleteDaemonProcessActionInput{
		ProjectID: testProjectID,
		AgentID:   fixture.AgentID,
		ProcessID: process.ID,
		ID:        action.ID,
		Authority: fixture.authority(),
		Result:    json.RawMessage(`{"output":"","cursor":0,"next_cursor":0}`),
	}
	application, err := fixture.Store.Execution().ApplyDaemonProcessAction(
		ctx,
		input,
	)
	if err != nil || application.ToolResultCommitted {
		t.Fatalf(
			"late action report = %+v err=%v, want cleanup-only",
			application,
			err,
		)
	}
	current, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		actionToolCallID,
	)
	if err != nil {
		t.Fatalf("get action after late report: %v", err)
	}
	if !found || current.ID != action.ID ||
		current.State != executionstore.ProcessActionStateUnknown ||
		current.StateReasonCode != executionstore.ProcessToolReasonMachineUnreachable {
		t.Fatalf("action after late report found=%v action=%+v", found, current)
	}
	toolCall, err = fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, actionToolCallID)
	if err != nil {
		t.Fatalf("get action tool call after late report: %v", err)
	}
	assertCompletedToolCallWithResult(t, fixture.Store, fixture.AgentID, toolCall, executionstore.ProcessToolReasonMachineUnreachable)
}

func TestMachineUnreachableQueuedProcessFailsBeforeExecutionGrant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "machine_unreachable_queued_process")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "machine_unreachable_queued_process", "run_command")
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
	expireDaemonRuntimeForTest(t, ctx, fixture)
	if records, err := fixture.Store.Execution().EndExpiredDaemonRuntimes(
		ctx,
		10); err != nil ||
		len(records) != 1 {
		t.Fatalf("end expired daemon runtime records=%d err=%v", len(records), err)
	}
	if expired, err := fixture.Store.Execution().ExpireMachineUnreachableProcessToolCallsForAllProjects(
		ctx, executionstore.ProcessToolMachineUnreachableGrace); err != nil ||
		expired != 0 {
		t.Fatalf(
			"early machine-unreachable queued process expiry count=%d err=%v, want none before offline grace",
			expired,
			err,
		)
	}
	if expired, err := fixture.Store.Execution().ExpireMachineUnreachableProcessToolCallsForAllProjects(
		ctx, 0); err != nil ||
		expired != 1 {
		t.Fatalf("machine-unreachable queued process expiry count=%d err=%v", expired, err)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get tool call: %v", err)
	}
	assertCompletedToolCallWithResult(t, fixture.Store, fixture.AgentID, toolCall, executionstore.ProcessToolReasonMachineUnreachable)
	current, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get process: %v", err)
	}
	if current.State != executionstore.ProcessStateFailed || current.StateReasonCode != executionstore.ProcessToolReasonMachineUnreachable ||
		current.SourceEndedAt != nil ||
		current.ExecutionGrantedAt != nil {
		t.Fatalf("queued process after machine-unreachable expiry = %+v, want failed", current)
	}
}

func TestMachineUnreachableGraceDoesNotRestartWhenExpiredRuntimeIsReaped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "machine_unreachable_late_runtime_reap")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"machine_unreachable_late_runtime_reap",
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
	leaseExpiresAt := expireDaemonRuntimeForTest(t, ctx, fixture)

	const unreachableGrace = time.Second
	waitForDatabaseTime(t, ctx, fixture.Store.pool, leaseExpiresAt.Add(unreachableGrace))
	if records, err := fixture.Store.Execution().EndExpiredDaemonRuntimes(ctx, 10); err != nil || len(records) != 1 {
		t.Fatalf("end expired daemon runtime records=%d err=%v", len(records), err)
	}

	var endedAt, effectiveEndAt time.Time
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT runtime.lease_expires_at, runtime.ended_at, fact.effective_end_at
FROM daemon_runtimes runtime
JOIN daemon_runtime_connection_facts fact ON fact.id = runtime.id
WHERE runtime.org_id = $1 AND runtime.machine_id = $2 AND runtime.id = $3
`, fixture.OrgID, fixture.MachineID, fixture.RuntimeID).Scan(
		&leaseExpiresAt,
		&endedAt,
		&effectiveEndAt,
	); err != nil {
		t.Fatalf("load ended daemon runtime boundary: %v", err)
	}
	if !endedAt.After(leaseExpiresAt) || !effectiveEndAt.Equal(leaseExpiresAt) {
		t.Fatalf(
			"ended runtime boundary = lease %s, ended %s, effective %s",
			leaseExpiresAt,
			endedAt,
			effectiveEndAt,
		)
	}
	if leaseExpiresAt.Before(process.CreatedAt) || endedAt.Before(leaseExpiresAt.Add(unreachableGrace)) {
		t.Fatalf(
			"test boundaries do not distinguish lease expiry from reaping: process %s, effective %s, ended %s",
			process.CreatedAt,
			effectiveEndAt,
			endedAt,
		)
	}

	if expired, err := fixture.Store.Execution().ExpireMachineUnreachableProcessToolCallsForAllProjects(
		ctx,
		unreachableGrace,
	); err != nil || expired != 1 {
		t.Fatalf("machine-unreachable expiry count=%d err=%v", expired, err)
	}
	current, err := fixture.Store.Execution().GetProcess(ctx, testProjectID, fixture.AgentID, process.ID)
	if err != nil {
		t.Fatalf("get expired process: %v", err)
	}
	if current.State != executionstore.ProcessStateFailed ||
		current.StateReasonCode != executionstore.ProcessToolReasonMachineUnreachable {
		t.Fatalf("expired process = %+v, want failed/machine_unreachable", current)
	}
}

func TestStartProcessFailsForNeverConnectedMachine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessMachineFixtureWithoutDaemonRuntime(t, ctx, "machine_unreachable_never_connected")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "machine_unreachable_never_connected", "run_command")
	if _, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 3600",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	}); !errors.Is(err, storeerr.ErrMachineNotReachable) {
		t.Fatalf("start process err=%v, want ErrMachineNotReachable", err)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get tool call: %v", err)
	}
	if toolCall.State != "ready" || toolCall.CompletedAt != nil {
		t.Fatalf(
			"tool call state=%s completed=%v, want ready because no process was queued",
			toolCall.State,
			toolCall.CompletedAt,
		)
	}
	if _, found, err := fixture.Store.Execution().GetProcessByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	); err != nil ||
		found {
		t.Fatalf("process by tool call found=%v err=%v, want no queued process for offline machine", found, err)
	}
}

func TestMachineUnreachableToolExpiryRecheckPredicate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "machine_unreachable_recheck_predicate")
	if got := machineUnreachableRecheckForTest(
		t,
		ctx,
		fixture,
		fixture.Now); got {
		t.Fatalf("machine-unreachable recheck for online machine = true, want false")
	}
	expireDaemonRuntimeForTest(t, ctx, fixture)
	if records, err := fixture.Store.Execution().EndExpiredDaemonRuntimes(
		ctx,
		10); err != nil ||
		len(records) != 1 {
		t.Fatalf("end expired daemon runtime records=%d err=%v", len(records), err)
	}
	if got := machineUnreachableRecheckForTest(
		t,
		ctx,
		fixture,
		fixture.Now); !got {
		t.Fatalf("machine-unreachable recheck for offline past-grace machine = false, want true")
	}
	if _, err := fixture.Store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: testID("daemon-replacement-recheck-predicate"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     time.Hour,
		},
	); err != nil {
		t.Fatalf("register replacement runtime: %v", err)
	}
	if got := machineUnreachableRecheckForTest(
		t,
		ctx,
		fixture,
		fixture.Now); got {
		t.Fatalf("machine-unreachable recheck after replacement runtime = true, want false")
	}
}

func TestMachineUnreachableToolExpiryRecheckUsesTimeAfterMachineLock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "machine_unreachable_post_lock_time")

	blockingTx, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin machine-unreachable blocker: %v", err)
	}
	defer func() { _ = blockingTx.Rollback(ctx) }()
	var blockingPID int32
	if err := blockingTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockingPID); err != nil {
		t.Fatalf("get machine-unreachable blocker backend: %v", err)
	}
	if _, err := blockingTx.Exec(ctx, `
SELECT id
FROM machines
WHERE org_id = $1 AND id = $2
FOR UPDATE
`, fixture.OrgID, fixture.MachineID); err != nil {
		t.Fatalf("lock machine for unreachable boundary: %v", err)
	}
	type recheckResult struct {
		unreachable bool
		err         error
	}
	done := make(chan recheckResult, 1)
	go func() {
		tx, beginErr := fixture.Store.pool.Begin(context.Background())
		if beginErr != nil {
			done <- recheckResult{err: beginErr}
			return
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		unreachable, recheckErr := executionstore.IntegrationMachineStillUnreachableForToolExpiryTx(
			context.Background(),
			dbsqlc.New(tx),
			fixture.OrgID,
			fixture.MachineID,
			fixture.Now,
			0,
		)
		done <- recheckResult{unreachable: unreachable, err: recheckErr}
	}()
	recheckStartedAt := waitForDatabaseLockWait(
		t,
		ctx,
		fixture.Store.pool,
		"-- name: LockMachineForLifecycle",
		blockingPID,
	)
	var endedAt time.Time
	if err := blockingTx.QueryRow(ctx, `
UPDATE daemon_runtimes
SET state = 'ended',
    state_reason_code = 'test_offline',
    ended_at = statement_timestamp(),
    updated_at = statement_timestamp()
WHERE org_id = $1 AND machine_id = $2 AND id = $3
RETURNING ended_at
`, fixture.OrgID, fixture.MachineID, fixture.RuntimeID).Scan(&endedAt); err != nil {
		t.Fatalf("end daemon runtime after recheck begins: %v", err)
	}
	if !endedAt.After(recheckStartedAt) {
		t.Fatalf("runtime ended at %s, want after recheck began at %s", endedAt, recheckStartedAt)
	}
	if err := blockingTx.Commit(ctx); err != nil {
		t.Fatalf("release machine-unreachable blocker: %v", err)
	}
	result := <-done
	if result.err != nil {
		t.Fatalf("recheck machine unreachable after lock wait: %v", result.err)
	}
	if !result.unreachable {
		t.Fatal("machine-unreachable recheck used time from before the machine lock wait")
	}
}

func TestMachineUnreachableQueuedActionFailsBeforeActionGrant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "machine_unreachable_queued_action")
	processToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"machine_unreachable_queued_action_process",
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
	actionToolCallID := createToolCallForProcessTest(t, ctx, fixture, "machine_unreachable_queued_action", "write_process")
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
		t.Fatalf("create write action: %v", err)
	}
	if action.State != executionstore.ProcessActionStateQueued {
		t.Fatalf("created action state = %s, want queued", action.State)
	}
	expireDaemonRuntimeForTest(t, ctx, fixture)
	if records, err := fixture.Store.Execution().EndExpiredDaemonRuntimes(
		ctx,
		10); err != nil ||
		len(records) != 1 {
		t.Fatalf("end expired daemon runtime records=%d err=%v", len(records), err)
	}
	if expired, err := fixture.Store.Execution().ExpireMachineUnreachableProcessToolCallsForAllProjects(
		ctx, 0); err != nil ||
		expired != 1 {
		t.Fatalf("machine-unreachable queued action expiry count=%d err=%v", expired, err)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, actionToolCallID)
	if err != nil {
		t.Fatalf("get action tool call: %v", err)
	}
	assertCompletedToolCallWithResult(t, fixture.Store, fixture.AgentID, toolCall, executionstore.ProcessToolReasonMachineUnreachable)
	current, found, err := fixture.Store.Execution().GetProcessActionByToolCall(ctx, testProjectID, fixture.AgentID, actionToolCallID)
	if err != nil {
		t.Fatalf("get action by tool call: %v", err)
	}
	if !found || current.ID != action.ID || current.State != executionstore.ProcessActionStateFailed ||
		current.StateReasonCode != executionstore.ProcessToolReasonMachineUnreachable {
		t.Fatalf("queued action after machine-unreachable expiry found=%v action=%+v, want failed", found, current)
	}
}

func TestMachineUnreachableCandidatesAreOfflineMachineFirst(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	offline := newProcessDaemonFixture(t, ctx, "machine_unreachable_candidate_offline")
	online := newProcessDaemonFixtureInStore(
		t,
		ctx,
		offline.Store,
		offline.UserID,
		"machine_unreachable_candidate_online",
		offline.Now,
	)
	offlineToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		offline,
		"machine_unreachable_candidate_offline",
		"run_command",
	)
	if _, err := startProcessForTest(ctx, offline.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       offline.AgentID,
		ToolCallID:    offlineToolCallID,
		RuntimeLockID: offline.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: offline.BindingID,
		Command:               "sleep 3600",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	}); err != nil {
		t.Fatalf("start offline process: %v", err)
	}
	onlineToolCallID := createToolCallForProcessTest(t, ctx, online, "machine_unreachable_candidate_online", "run_command")
	if _, err := startProcessForTest(ctx, online.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       online.AgentID,
		ToolCallID:    onlineToolCallID,
		RuntimeLockID: online.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: online.BindingID,
		Command:               "sleep 3600",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	}); err != nil {
		t.Fatalf("start online process: %v", err)
	}
	expireDaemonRuntimeForTest(t, ctx, offline)
	if records, err := offline.Store.Execution().EndExpiredDaemonRuntimes(
		ctx,
		10); err != nil ||
		len(records) != 1 {
		t.Fatalf("end expired daemon runtime records=%d err=%v", len(records), err)
	}
	candidates, err := offline.Store.q.ListMachineUnreachableMachineCandidates(
		ctx,
		dbsqlc.ListMachineUnreachableMachineCandidatesParams{
			MachineUnreachableGraceSeconds: 0,
			LimitCount:                     10,
		},
	)
	if err != nil {
		t.Fatalf("list machine-unreachable candidates: %v", err)
	}
	if len(candidates) != 1 || candidates[0].MachineID != offline.MachineID {
		t.Fatalf("machine-unreachable candidates = %+v, want only offline machine %s", candidates, offline.MachineID)
	}
	if expired, err := offline.Store.Execution().ExpireMachineUnreachableProcessToolCallsForAllProjects(
		ctx, 0); err != nil ||
		expired != 1 {
		t.Fatalf("machine-unreachable expiry count=%d err=%v", expired, err)
	}
	onlineToolCall, err := online.Store.Execution().GetToolCall(ctx, testProjectID, online.AgentID, onlineToolCallID)
	if err != nil {
		t.Fatalf("get online tool call: %v", err)
	}
	if onlineToolCall.State != "waiting" || onlineToolCall.CompletedAt != nil {
		t.Fatalf(
			"online machine tool call state=%s completed=%v, want waiting/incomplete",
			onlineToolCall.State,
			onlineToolCall.CompletedAt,
		)
	}
}

func TestMachineUnreachableCandidatesUseLatestRuntimeRecency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "machine_unreachable_candidate_latest_runtime")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"machine_unreachable_candidate_latest_runtime",
		"run_command",
	)
	if _, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 3600",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	}); err != nil {
		t.Fatalf("start process: %v", err)
	}
	expireDaemonRuntimeForTest(t, ctx, fixture)
	if records, err := fixture.Store.Execution().EndExpiredDaemonRuntimes(
		ctx,
		10); err != nil ||
		len(records) != 1 {
		t.Fatalf("end expired daemon runtime records=%d err=%v", len(records), err)
	}
	replacementRuntime, err := fixture.Store.Execution().RegisterDaemonRuntime(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            fixture.OrgID,
			MachineID:        fixture.MachineID,
			DaemonTokenID:    fixture.TokenID,
			DaemonInstanceID: testID("daemon-replacement-latest-runtime"),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     time.Hour,
		},
	)
	if err != nil {
		t.Fatalf("register replacement runtime: %v", err)
	}
	expireDaemonRuntimeLeaseForTest(
		t,
		ctx,
		fixture.Store,
		fixture.OrgID,
		fixture.MachineID,
		replacementRuntime.ID,
	)
	candidates, err := fixture.Store.q.ListMachineUnreachableMachineCandidates(
		ctx,
		dbsqlc.ListMachineUnreachableMachineCandidatesParams{
			MachineUnreachableGraceSeconds: int32(executionstore.ProcessToolMachineUnreachableGrace / time.Second),
			LimitCount:                     10,
		},
	)
	if err != nil {
		t.Fatalf("list early machine-unreachable candidates: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("early candidates = %+v, want none because latest runtime is not past grace", candidates)
	}
	candidates, err = fixture.Store.q.ListMachineUnreachableMachineCandidates(
		ctx,
		dbsqlc.ListMachineUnreachableMachineCandidatesParams{
			MachineUnreachableGraceSeconds: 0,
			LimitCount:                     10,
		},
	)
	if err != nil {
		t.Fatalf("list late machine-unreachable candidates: %v", err)
	}
	if len(candidates) != 1 || candidates[0].MachineID != fixture.MachineID {
		t.Fatalf("late candidates = %+v, want machine %s", candidates, fixture.MachineID)
	}
}

func TestMachineUnreachableCandidatesSkipEarlierWorkWithinLatestRuntimeGrace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	recent := newProcessDaemonFixture(t, ctx, "machine_unreachable_candidate_recent_runtime")
	mature := newProcessDaemonFixtureInStore(
		t,
		ctx,
		recent.Store,
		recent.UserID,
		"machine_unreachable_candidate_mature_runtime",
		recent.Now,
	)
	for _, fixture := range []processDaemonFixture{recent, mature} {
		toolCallID := createToolCallForProcessTest(
			t,
			ctx,
			fixture,
			"machine_unreachable_candidate_process_"+fixture.MachineID.String(),
			"run_command",
		)
		if _, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    toolCallID,
			RuntimeLockID: fixture.Lock.ID,
		}, executionstore.CreateProcessInput{
			AgentMachineBindingID: fixture.BindingID,
			Command:               "sleep 3600",
			ShellSelector:         "sh",
			Cwd:                   "/work",
		}); err != nil {
			t.Fatalf("start process for machine %s: %v", fixture.MachineID, err)
		}
	}
	matureDisconnectedAt := expireDaemonRuntimeForTest(t, ctx, mature)
	waitForDatabaseTime(t, ctx, recent.Store.pool, matureDisconnectedAt.Add(time.Second))
	expireDaemonRuntimeForTest(t, ctx, recent)
	candidates, err := recent.Store.q.ListMachineUnreachableMachineCandidates(
		ctx,
		dbsqlc.ListMachineUnreachableMachineCandidatesParams{
			MachineUnreachableGraceSeconds: 1,
			LimitCount:                     1,
		},
	)
	if err != nil {
		t.Fatalf("list machine-unreachable candidates: %v", err)
	}
	if len(candidates) != 1 || candidates[0].MachineID != mature.MachineID {
		t.Fatalf(
			"machine-unreachable candidates = %+v, want mature machine %s",
			candidates,
			mature.MachineID,
		)
	}
}

func TestMachineUnreachableCandidatesIgnoreQueuedMutationsForTerminalProcess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "machine_unreachable_candidate_terminal_mutations")
	process, _, _ := createTerminalProcessActionForLifecycleTest(
		t,
		ctx,
		fixture,
		"machine_unreachable_candidate_terminal_seed",
		"write_process",
		executionstore.ProcessActionKindWrite,
		false,
	)
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"machine_unreachable_candidate_terminal_mutations",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("terminal_write", "write_process"),
			builtInProcessToolCallBatchItem("terminal_interrupt", "stop_process"),
		},
	)
	for index, kind := range []executionstore.ProcessActionKind{
		executionstore.ProcessActionKindWrite,
		executionstore.ProcessActionKindInterrupt,
	} {
		if _, err := fixture.Store.pool.Exec(ctx, `
			INSERT INTO process_actions(
				org_id,
				project_id,
				agent_id,
				process_id,
				tool_call_id,
				runtime_lock_id,
				action_kind,
				seq,
				payload,
				state,
				created_at,
				updated_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, '{}'::jsonb, 'queued',
			        statement_timestamp() - INTERVAL '2 hours', statement_timestamp())
		`,
			fixture.OrgID,
			testProjectID,
			fixture.AgentID,
			process.ID,
			toolCallIDs[index],
			fixture.Lock.ID,
			kind,
			index+2,
		); err != nil {
			t.Fatalf("insert terminal %s action: %v", kind, err)
		}
	}
	expireDaemonRuntimeForTest(t, ctx, fixture)
	candidates, err := fixture.Store.q.ListMachineUnreachableMachineCandidates(
		ctx,
		dbsqlc.ListMachineUnreachableMachineCandidatesParams{
			MachineUnreachableGraceSeconds: 0,
			LimitCount:                     10,
		},
	)
	if err != nil {
		t.Fatalf("list machine-unreachable candidates: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("terminal queued mutations produced machine-unreachable candidates: %+v", candidates)
	}
}

func TestToolCallDeleteIsRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "tool_call_delete_immutable")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "tool_call_delete_immutable", "run_command")

	if _, err := fixture.Store.pool.Exec(ctx, `
		DELETE FROM tool_calls
		WHERE agent_id = $1 AND id = $2
	`, fixture.AgentID, toolCallID); !isPgReadOnlySQLTransaction(err) {
		t.Fatalf("delete immutable tool call error = %v, want SQLSTATE 25006", err)
	}
}
