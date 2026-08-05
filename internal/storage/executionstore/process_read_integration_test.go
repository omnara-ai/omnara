//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestProcessReadCursorAdvancesOnlyForCommittedImplicitResults(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "read_cursor")
	process := startRunningProcessForReadTest(
		t,
		ctx,
		fixture,
		"read_cursor",
		json.RawMessage(`{"output":"boot","cursor":0,"next_cursor":4,"truncated":false}`),
	)
	assertProcessDefaultOutputCursor(t, ctx, fixture, process.ID, 4)

	implicitToolCallID := createToolCallForProcessActionTest(
		t,
		ctx,
		fixture,
		"read_cursor_implicit",
	)
	implicit, err := createProcessActionForTest(
		ctx,
		fixture.Store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    implicitToolCallID,
			RuntimeLockID: fixture.Lock.ID,
		},
		executionstore.CreateProcessActionInput{
			ProcessID:  process.ID,
			ActionKind: executionstore.ProcessActionKindRead,
			Payload:    json.RawMessage(`{}`),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	implicitGrant, found, err := fixture.Store.Execution().AcceptDaemonProcessAction(
		ctx,
		executionstore.AcceptDaemonProcessActionInput{
			Authority: fixture.authority(),
			ProcessID: process.ID,
			ID:        implicit.ID,
		},
	)
	if err != nil || !found {
		t.Fatalf("accept implicit read found=%t err=%v", found, err)
	}
	if implicitGrant.DefaultOutputCursor != 4 ||
		implicitGrant.ProcessState != executionstore.ProcessStateRunning {
		t.Fatalf("implicit read grant = %+v", implicitGrant)
	}
	publicProcessID := publicResourceID(publicid.KindProcess, process.ID)
	implicitResult := json.RawMessage(
		`{"process_id":"` + publicProcessID +
			`","output":"next","cursor":4,"next_cursor":8,"truncated":false}`,
	)
	applied, err := fixture.Store.Execution().ApplyDaemonProcessAction(
		ctx,
		executionstore.CompleteDaemonProcessActionInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			ProcessID: process.ID,
			ID:        implicit.ID,
			Authority: fixture.authority(),
			Result:    implicitResult,
		},
	)
	if err != nil || !applied.ToolResultCommitted {
		t.Fatalf("apply implicit read = %+v err=%v", applied, err)
	}
	assertProcessDefaultOutputCursor(t, ctx, fixture, process.ID, 8)

	replayed, err := fixture.Store.Execution().ApplyDaemonProcessAction(
		ctx,
		executionstore.CompleteDaemonProcessActionInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			ProcessID: process.ID,
			ID:        implicit.ID,
			Authority: fixture.authority(),
			Result:    implicitResult,
		},
	)
	if err != nil ||
		!replayed.ToolResultCommitted ||
		replayed.Action.State != executionstore.ProcessActionStateApplied {
		t.Fatalf("replay implicit read = %+v err=%v", replayed, err)
	}
	assertProcessDefaultOutputCursor(t, ctx, fixture, process.ID, 8)

	explicitToolCallID := createToolCallForProcessActionTest(
		t,
		ctx,
		fixture,
		"read_cursor_explicit",
	)
	explicit, err := createProcessActionForTest(
		ctx,
		fixture.Store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    explicitToolCallID,
			RuntimeLockID: fixture.Lock.ID,
		},
		executionstore.CreateProcessActionInput{
			ProcessID:  process.ID,
			ActionKind: executionstore.ProcessActionKindRead,
			Payload:    json.RawMessage(`{"cursor":0}`),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := fixture.Store.Execution().AcceptDaemonProcessAction(
		ctx,
		executionstore.AcceptDaemonProcessActionInput{
			Authority: fixture.authority(),
			ProcessID: process.ID,
			ID:        explicit.ID,
		},
	); err != nil || !found {
		t.Fatalf("accept explicit read found=%t err=%v", found, err)
	}
	explicitResult := json.RawMessage(
		`{"process_id":"` + publicProcessID +
			`","output":"allbytes","cursor":0,"next_cursor":8,"truncated":false}`,
	)
	if application, err := fixture.Store.Execution().ApplyDaemonProcessAction(
		ctx,
		executionstore.CompleteDaemonProcessActionInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			ProcessID: process.ID,
			ID:        explicit.ID,
			Authority: fixture.authority(),
			Result:    explicitResult,
		},
	); err != nil || !application.ToolResultCommitted {
		t.Fatalf("apply explicit read = %+v err=%v", application, err)
	}
	assertProcessDefaultOutputCursor(t, ctx, fixture, process.ID, 8)

	exitCode := 7
	endedAt := fixture.Now.Add(10 * time.Second)
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(
		ctx,
		executionstore.CompleteDaemonProcessInput{
			ProjectID:          testProjectID,
			AgentID:            fixture.AgentID,
			ID:                 process.ID,
			Authority:          fixture.authority(),
			State:              executionstore.ProcessStateExited,
			ExitCode:           &exitCode,
			StateReasonCode:    "command_failed",
			StateReasonMessage: "command exited with status 7",
			SourceEndedAt:      endedAt,
		},
	); err != nil {
		t.Fatal(err)
	}
	assertProcessDefaultOutputCursor(t, ctx, fixture, process.ID, 8)
	terminalToolCallID := createToolCallForProcessActionTest(
		t,
		ctx,
		fixture,
		"read_cursor_terminal",
	)
	terminalRead, err := createProcessActionForTest(
		ctx,
		fixture.Store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    terminalToolCallID,
			RuntimeLockID: fixture.Lock.ID,
		},
		executionstore.CreateProcessActionInput{
			ProcessID:  process.ID,
			ActionKind: executionstore.ProcessActionKindRead,
			Payload:    json.RawMessage(`{}`),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	terminalGrant, found, err := fixture.Store.Execution().AcceptDaemonProcessAction(
		ctx,
		executionstore.AcceptDaemonProcessActionInput{
			Authority: fixture.authority(),
			ProcessID: process.ID,
			ID:        terminalRead.ID,
		},
	)
	if err != nil || !found {
		t.Fatalf("accept terminal read found=%t err=%v", found, err)
	}
	if terminalGrant.ProcessState != executionstore.ProcessStateExited ||
		terminalGrant.DefaultOutputCursor != 8 {
		t.Fatalf("terminal read grant = %+v", terminalGrant)
	}
	terminalObservation := json.RawMessage(
		`{"process_id":"` + publicProcessID +
			`","state":"running","output":"tail","cursor":8,"next_cursor":12,"truncated":false,"done":false}`,
	)
	if application, err := fixture.Store.Execution().ApplyDaemonProcessAction(
		ctx,
		executionstore.CompleteDaemonProcessActionInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			ProcessID: process.ID,
			ID:        terminalRead.ID,
			Authority: fixture.authority(),
			Result:    terminalObservation,
		},
	); err != nil || !application.ToolResultCommitted {
		t.Fatalf("apply terminal read = %+v err=%v", application, err)
	}
	assertProcessDefaultOutputCursor(t, ctx, fixture, process.ID, 12)

	toolCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		terminalToolCallID,
	)
	if err != nil {
		t.Fatal(err)
	}
	completed := completedToolCallForTest(
		t,
		fixture.Store,
		fixture.AgentID,
		toolCall.TurnID,
		terminalToolCallID,
	)
	var parts []struct {
		Type  string `json:"type"`
		Value struct {
			State           executionstore.ProcessState `json:"state"`
			Done            bool                        `json:"done"`
			ExitCode        *int                        `json:"exit_code"`
			StateReasonCode string                      `json:"state_reason_code"`
		} `json:"value"`
	}
	if err := json.Unmarshal(completed.ResultContentParts, &parts); err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 ||
		parts[0].Type != "structured_data" ||
		parts[0].Value.State != executionstore.ProcessStateExited ||
		!parts[0].Value.Done ||
		parts[0].Value.ExitCode == nil ||
		*parts[0].Value.ExitCode != 7 ||
		parts[0].Value.StateReasonCode != "command_failed" {
		t.Fatalf("terminal read result = %s", completed.ResultContentParts)
	}
}

func TestLateImplicitReadDoesNotAdvanceCursorWhenAnotherResultWon(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "read_cursor_result_race")
	process := startRunningProcessForReadTest(
		t,
		ctx,
		fixture,
		"read_cursor_result_race",
		json.RawMessage(`{"output":"boot","cursor":0,"next_cursor":4,"truncated":false}`),
	)
	assertProcessDefaultOutputCursor(t, ctx, fixture, process.ID, 4)

	toolCallID := createToolCallForProcessActionTest(
		t,
		ctx,
		fixture,
		"read_cursor_result_race_action",
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
			ActionKind: executionstore.ProcessActionKindRead,
			Payload:    json.RawMessage(`{}`),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := fixture.Store.Execution().AcceptDaemonProcessAction(
		ctx,
		executionstore.AcceptDaemonProcessActionInput{
			Authority: fixture.authority(),
			ProcessID: process.ID,
			ID:        action.ID,
		},
	); err != nil || !found {
		t.Fatalf("accept implicit read found=%t err=%v", found, err)
	}
	forceToolCallResultForTest(
		t,
		ctx,
		fixture.Store,
		testProjectID,
		fixture.AgentID,
		toolCallID,
		executionstore.ToolResultOutcomeCanceled,
		json.RawMessage(`{"reason":"canceled before the read report arrived"}`))

	publicProcessID := publicResourceID(publicid.KindProcess, process.ID)
	application, err := fixture.Store.Execution().ApplyDaemonProcessAction(
		ctx,
		executionstore.CompleteDaemonProcessActionInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			ProcessID: process.ID,
			ID:        action.ID,
			Authority: fixture.authority(),
			Result: json.RawMessage(
				`{"process_id":"` + publicProcessID +
					`","output":"next","cursor":4,"next_cursor":8,"truncated":false}`,
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if application.ToolResultCommitted ||
		application.Action.State != executionstore.ProcessActionStateApplied {
		t.Fatalf("late implicit read application = %+v", application)
	}
	assertProcessDefaultOutputCursor(t, ctx, fixture, process.ID, 4)

	toolCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if toolCall.Outcome != executionstore.ToolResultOutcomeCanceled {
		t.Fatalf("winning tool call outcome = %q, want canceled", toolCall.Outcome)
	}
}

func TestFastTerminalResultAdvancesInitialOutputCursor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "fast_terminal_cursor")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"fast_terminal_cursor",
		"run_command",
	)
	process, err := startProcessForTest(
		ctx,
		fixture.Store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    toolCallID,
			RuntimeLockID: fixture.Lock.ID,
		},
		executionstore.CreateProcessInput{
			AgentMachineBindingID: fixture.BindingID,
			Command:               "echo fast",
			ShellSelector:         "sh",
			Cwd:                   "/work",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID); err != nil || !found {
		t.Fatalf("accept process found=%t err=%v", found, err)
	}
	exitCode := 0
	input := executionstore.CompleteDaemonProcessInput{
		ProjectID:       testProjectID,
		AgentID:         fixture.AgentID,
		ID:              process.ID,
		Authority:       fixture.authority(),
		State:           executionstore.ProcessStateExited,
		ExitCode:        &exitCode,
		Result:          json.RawMessage(`{"output":"fast\n","cursor":0,"next_cursor":5,"truncated":false}`),
		SourceStartedAt: fixture.Now.Add(1500 * time.Millisecond),
		SourceEndedAt:   fixture.Now.Add(2 * time.Second),
	}
	first, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, input)
	if err != nil || !first.ToolResultCommitted {
		t.Fatalf("complete fast process = %+v err=%v", first, err)
	}
	assertProcessDefaultOutputCursor(t, ctx, fixture, process.ID, 5)
	replayed, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, input)
	if err != nil || !replayed.ToolResultCommitted {
		t.Fatalf("replay fast process = %+v err=%v", replayed, err)
	}
	assertProcessDefaultOutputCursor(t, ctx, fixture, process.ID, 5)
}

func TestImplicitReadObservationBeforeGrantedCursorIsRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "invalid_read_cursor")
	process := startRunningProcessForReadTest(
		t,
		ctx,
		fixture,
		"invalid_read_cursor",
		json.RawMessage(
			`{"output":"boot","cursor":0,"next_cursor":4,"truncated":false}`,
		),
	)
	toolCallID := createToolCallForProcessActionTest(
		t,
		ctx,
		fixture,
		"invalid_read_cursor_action",
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
			ActionKind: executionstore.ProcessActionKindRead,
			Payload:    json.RawMessage(`{}`),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	grant, found, err := fixture.Store.Execution().AcceptDaemonProcessAction(
		ctx,
		executionstore.AcceptDaemonProcessActionInput{
			Authority: fixture.authority(),
			ProcessID: process.ID,
			ID:        action.ID,
		},
	)
	if err != nil || !found {
		t.Fatalf("accept read found=%t err=%v", found, err)
	}
	if grant.DefaultOutputCursor != 4 {
		t.Fatalf("implicit read grant cursor = %d, want 4", grant.DefaultOutputCursor)
	}
	publicProcessID := publicResourceID(publicid.KindProcess, process.ID)
	application, err := fixture.Store.Execution().ApplyDaemonProcessAction(
		ctx,
		executionstore.CompleteDaemonProcessActionInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			ProcessID: process.ID,
			ID:        action.ID,
			Authority: fixture.authority(),
			Result: json.RawMessage(
				`{"process_id":"` + publicProcessID +
					`","output":"boot","cursor":0,"next_cursor":4,"truncated":false}`,
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if application.ToolResultCommitted ||
		application.Action.State != executionstore.ProcessActionStateFailed ||
		application.Action.StateReasonCode != "invalid_read_observation" {
		t.Fatalf("invalid read application = %+v", application)
	}
	assertProcessDefaultOutputCursor(t, ctx, fixture, process.ID, 4)
}

func TestFailedTerminalReadPreservesProcessFactsAndCursor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "read_output_unavailable")
	process := startRunningProcessForReadTest(
		t,
		ctx,
		fixture,
		"read_output_unavailable",
		json.RawMessage(`{"output":"","cursor":0,"next_cursor":0,"truncated":false}`),
	)
	exitCode := 9
	endedAt := fixture.Now.Add(3 * time.Second)
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(
		ctx,
		executionstore.CompleteDaemonProcessInput{
			ProjectID:          testProjectID,
			AgentID:            fixture.AgentID,
			ID:                 process.ID,
			Authority:          fixture.authority(),
			State:              executionstore.ProcessStateExited,
			ExitCode:           &exitCode,
			StateReasonCode:    "command_failed",
			StateReasonMessage: "command exited with status 9",
			SourceEndedAt:      endedAt,
		},
	); err != nil {
		t.Fatal(err)
	}

	toolCallID := createToolCallForProcessActionTest(
		t,
		ctx,
		fixture,
		"read_output_unavailable_action",
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
			ActionKind: executionstore.ProcessActionKindRead,
			Payload:    json.RawMessage(`{}`),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := fixture.Store.Execution().AcceptDaemonProcessAction(
		ctx,
		executionstore.AcceptDaemonProcessActionInput{
			Authority: fixture.authority(),
			ProcessID: process.ID,
			ID:        action.ID,
		},
	); err != nil || !found {
		t.Fatalf("accept terminal read found=%t err=%v", found, err)
	}
	input := executionstore.CompleteDaemonProcessActionInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ProcessID:          process.ID,
		ID:                 action.ID,
		Authority:          fixture.authority(),
		StateReasonCode:    "output_unavailable",
		StateReasonMessage: "the retained output file is missing",
	}
	first, err := fixture.Store.Execution().FailDaemonProcessAction(ctx, input)
	if err != nil || !first.ToolResultCommitted {
		t.Fatalf("fail terminal read = %+v err=%v", first, err)
	}
	replayed, err := fixture.Store.Execution().FailDaemonProcessAction(ctx, input)
	if err != nil || !replayed.ToolResultCommitted {
		t.Fatalf("replay failed terminal read = %+v err=%v", replayed, err)
	}
	assertProcessDefaultOutputCursor(t, ctx, fixture, process.ID, 0)

	toolCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	)
	if err != nil {
		t.Fatal(err)
	}
	completed := completedToolCallForTest(
		t,
		fixture.Store,
		fixture.AgentID,
		toolCall.TurnID,
		toolCallID,
	)
	var parts []struct {
		Type  string `json:"type"`
		Value struct {
			ErrorCode          string                      `json:"error_code"`
			Retryable          bool                        `json:"retryable"`
			State              executionstore.ProcessState `json:"state"`
			Done               bool                        `json:"done"`
			ExitCode           *int                        `json:"exit_code"`
			StateReasonCode    string                      `json:"state_reason_code"`
			StateReasonMessage string                      `json:"state_reason_message"`
		} `json:"value"`
	}
	if err := json.Unmarshal(completed.ResultContentParts, &parts); err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 ||
		parts[0].Type != "structured_data" ||
		parts[0].Value.ErrorCode != "output_unavailable" ||
		parts[0].Value.Retryable ||
		parts[0].Value.State != executionstore.ProcessStateExited ||
		!parts[0].Value.Done ||
		parts[0].Value.ExitCode == nil ||
		*parts[0].Value.ExitCode != 9 ||
		parts[0].Value.StateReasonCode != "command_failed" ||
		parts[0].Value.StateReasonMessage != "command exited with status 9" {
		t.Fatalf("failed terminal read result = %s", completed.ResultContentParts)
	}
}

func startRunningProcessForReadTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	name string,
	result json.RawMessage,
) executionstore.ProcessRecord {
	t.Helper()
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		name+"_process",
		"run_command",
	)
	process, err := startProcessForTest(
		ctx,
		fixture.Store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    toolCallID,
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
		t.Fatal(err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		process.ID); err != nil || !found {
		t.Fatalf("accept process found=%t err=%v", found, err)
	}
	application, err := fixture.Store.Execution().MarkProcessStarted(
		ctx,
		executionstore.MarkProcessStartedInput{
			ProjectID:       testProjectID,
			AgentID:         fixture.AgentID,
			ID:              process.ID,
			Authority:       fixture.authority(),
			Result:          result,
			SourceStartedAt: fixture.Now.Add(2 * time.Second),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !application.ToolResultCommitted {
		t.Fatalf("started process result was not committed: %+v", application)
	}
	return application.Process
}

func assertProcessDefaultOutputCursor(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	processID ID,
	want int64,
) {
	t.Helper()
	process, err := fixture.Store.Execution().GetProcess(
		ctx,
		testProjectID,
		fixture.AgentID,
		processID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if process.DefaultOutputCursor != want {
		t.Fatalf(
			"process default output cursor = %d, want %d",
			process.DefaultOutputCursor,
			want,
		)
	}
}
