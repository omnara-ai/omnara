//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestBackgroundProcessExitNotifiesAgent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "background_exit_notifies")
	process := startBackgroundProcessForTest(t, ctx, fixture, "background_exit_notifies", true)

	exitCode := 1
	complete := executionstore.CompleteDaemonProcessInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ID:            process.ID,
		Authority:     fixture.authority(),
		State:         executionstore.ProcessStateExited,
		ExitCode:      &exitCode,
		Result:        json.RawMessage(`{"state":"exited","output":"building\nerror: missing semicolon\n","cursor":0,"next_cursor":34,"truncated":false,"done":true}`),
		SourceEndedAt: fixture.Now.Add(65 * time.Second),
	}
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, complete); err != nil {
		t.Fatalf("complete background process: %v", err)
	}
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, complete); err != nil {
		t.Fatalf("replay background process completion: %v", err)
	}

	texts := processNotificationTexts(t, ctx, fixture)
	if len(texts) != 1 {
		t.Fatalf("process notifications = %d, want 1: %q", len(texts), texts)
	}
	processID, err := publicid.Encode(publicid.KindProcess, process.ID)
	if err != nil {
		t.Fatalf("encode process id: %v", err)
	}
	for _, want := range []string{
		"Background process " + processID + " (`sleep 60`) exited with code 1 after 1m3s.",
		"Output: 34 bytes.",
		"error: missing semicolon",
	} {
		if !strings.Contains(texts[0], want) {
			t.Fatalf("notification %q does not contain %q", texts[0], want)
		}
	}
	var readyAt *time.Time
	if err := fixture.Store.pool.QueryRow(ctx,
		`SELECT logical_ready_at FROM agent_execution_state WHERE agent_id = $1`,
		fixture.AgentID,
	).Scan(&readyAt); err != nil {
		t.Fatalf("load readiness: %v", err)
	}
	if readyAt == nil {
		t.Fatal("process notification did not make the agent ready")
	}
}

func TestDetachedProcessExitDoesNotNotifyAgent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "detached_exit_silent")
	process := startBackgroundProcessForTest(t, ctx, fixture, "detached_exit_silent", false)

	exitCode := 0
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, executionstore.CompleteDaemonProcessInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ID:            process.ID,
		Authority:     fixture.authority(),
		State:         executionstore.ProcessStateExited,
		ExitCode:      &exitCode,
		Result:        json.RawMessage(`{"state":"exited","output":"","cursor":0,"next_cursor":0,"truncated":false,"done":true}`),
		SourceEndedAt: fixture.Now.Add(65 * time.Second),
	}); err != nil {
		t.Fatalf("complete detached process: %v", err)
	}
	if texts := processNotificationTexts(t, ctx, fixture); len(texts) != 0 {
		t.Fatalf("detached process sent notifications: %q", texts)
	}
}

func TestPendingReadSuppressesExitNotification(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "pending_read_exit_silent")
	process := startBackgroundProcessForTest(t, ctx, fixture, "pending_read_exit_silent", true)

	readToolCallID := createToolCallForProcessTest(t, ctx, fixture, "pending_read_exit_silent_read", "read_process")
	read, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    readToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessActionInput{
		ProcessID:  process.ID,
		ActionKind: executionstore.ProcessActionKindRead,
		Payload:    json.RawMessage(`{"wait_ms":300000}`),
	})
	if err != nil {
		t.Fatalf("create read action: %v", err)
	}
	accept := executionstore.AcceptDaemonProcessActionInput{
		Authority: fixture.authority(),
		ProcessID: process.ID,
		ID:        read.ID,
	}
	if _, found, err := fixture.Store.Execution().AcceptDaemonProcessAction(ctx, accept); err != nil || !found {
		t.Fatalf("accept read found=%t err=%v", found, err)
	}

	exitCode := 0
	completion := executionstore.CompleteDaemonProcessInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ID:            process.ID,
		Authority:     fixture.authority(),
		State:         executionstore.ProcessStateExited,
		ExitCode:      &exitCode,
		Result:        json.RawMessage(`{"state":"exited","output":"done\n","cursor":0,"next_cursor":5,"truncated":false,"done":true}`),
		SourceEndedAt: fixture.Now.Add(65 * time.Second),
	}
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, completion); err != nil {
		t.Fatalf("complete process: %v", err)
	}
	if texts := processNotificationTexts(t, ctx, fixture); len(texts) != 0 {
		t.Fatalf("process with a pending read sent notifications: %q", texts)
	}

	if _, err := fixture.Store.Execution().FailDaemonProcessAction(ctx, executionstore.CompleteDaemonProcessActionInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ProcessID:          process.ID,
		ID:                 read.ID,
		Authority:          fixture.authority(),
		StateReasonCode:    "output_unavailable",
		StateReasonMessage: "the retained output file is missing",
	}); err != nil {
		t.Fatalf("finish read: %v", err)
	}
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, completion); err != nil {
		t.Fatalf("replay completion: %v", err)
	}
	if texts := processNotificationTexts(t, ctx, fixture); len(texts) != 0 {
		t.Fatalf("replay after the read finished sent notifications: %q", texts)
	}
}

func TestOnlyEffectiveStopSuppressesExitNotification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		kind   executionstore.ProcessActionKind
		update string
		notify bool
	}{
		{name: "queued_interrupt", kind: executionstore.ProcessActionKindInterrupt},
		{
			name:   "recent_interrupt",
			kind:   executionstore.ProcessActionKindInterrupt,
			update: `state = 'applied', updated_at = now()`,
		},
		{
			name:   "survived_interrupt",
			kind:   executionstore.ProcessActionKindInterrupt,
			update: `state = 'applied', updated_at = now() - interval '1 minute'`,
			notify: true,
		},
		{
			name:   "old_terminate",
			kind:   executionstore.ProcessActionKindTerminate,
			update: `state = 'applied', updated_at = now() - interval '1 minute'`,
		},
		{
			name:   "failed_terminate",
			kind:   executionstore.ProcessActionKindTerminate,
			update: `state = 'failed', state_reason_code = 'process_not_running'`,
			notify: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			name := "stop_exit_" + tc.name
			fixture := newProcessDaemonFixture(t, ctx, name)
			process := startBackgroundProcessForTest(t, ctx, fixture, name, true)
			stopToolCallID := createToolCallForProcessTest(t, ctx, fixture, name+"_stop", "stop_process")
			action, err := createProcessActionForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
				ProjectID:     testProjectID,
				AgentID:       fixture.AgentID,
				ToolCallID:    stopToolCallID,
				RuntimeLockID: fixture.Lock.ID,
			}, executionstore.CreateProcessActionInput{
				ProcessID:  process.ID,
				ActionKind: tc.kind,
			})
			if err != nil {
				t.Fatalf("create stop action: %v", err)
			}
			if tc.update != "" {
				if _, err := fixture.Store.pool.Exec(ctx,
					`UPDATE process_actions SET `+tc.update+` WHERE agent_id = $1 AND id = $2`,
					fixture.AgentID, action.ID,
				); err != nil {
					t.Fatalf("update stop action: %v", err)
				}
			}

			exitCode := 0
			completion := executionstore.CompleteDaemonProcessInput{
				ProjectID:     testProjectID,
				AgentID:       fixture.AgentID,
				ID:            process.ID,
				Authority:     fixture.authority(),
				State:         executionstore.ProcessStateExited,
				ExitCode:      &exitCode,
				Result:        json.RawMessage(`{"state":"exited","output":"","cursor":0,"next_cursor":0,"truncated":false,"done":true}`),
				SourceEndedAt: fixture.Now.Add(65 * time.Second),
			}
			if _, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, completion); err != nil {
				t.Fatalf("complete process: %v", err)
			}
			if texts := processNotificationTexts(t, ctx, fixture); (len(texts) == 1) != tc.notify {
				t.Fatalf("notify = %v, notifications = %q", tc.notify, texts)
			}

			// A replay after the stop settles or ages out keeps the first decision.
			if _, err := fixture.Store.pool.Exec(ctx,
				`UPDATE process_actions SET state = 'failed', updated_at = now() - interval '1 minute',
				 state_reason_code = coalesce(state_reason_code, 'process_not_running')
				 WHERE agent_id = $1 AND id = $2`, fixture.AgentID, action.ID,
			); err != nil {
				t.Fatalf("age stop action: %v", err)
			}
			if _, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, completion); err != nil {
				t.Fatalf("replay completion: %v", err)
			}
			if texts := processNotificationTexts(t, ctx, fixture); (len(texts) == 1) != tc.notify {
				t.Fatalf("after replay notify = %v, notifications = %q", tc.notify, texts)
			}
		})
	}
}

func startBackgroundProcessForTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	name string,
	notifyOnExit bool,
) executionstore.ProcessRecord {
	t.Helper()
	return startBackgroundProcessWithInputForTest(t, ctx, fixture, name, executionstore.CreateProcessInput{
		NotifyOnExit: notifyOnExit,
	})
}

func startBackgroundProcessWithInputForTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	name string,
	input executionstore.CreateProcessInput,
) executionstore.ProcessRecord {
	t.Helper()
	input.ExecutionSpec = processcmd.ForShell("sleep 60", "sh", "")
	input.AgentMachineBindingID = fixture.BindingID
	input.Cwd = "/work"
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, name, "run_command")
	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, input)
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx, fixture.Store, testOrgID, fixture.MachineID, fixture.RuntimeID, uuid.Nil,
	); err != nil || !found {
		t.Fatalf("accept process found=%v err=%v", found, err)
	}
	if _, err := fixture.Store.Execution().MarkProcessStarted(ctx, executionstore.MarkProcessStartedInput{
		ProjectID:       testProjectID,
		AgentID:         fixture.AgentID,
		ID:              process.ID,
		Authority:       fixture.authority(),
		SourceStartedAt: fixture.Now.Add(2 * time.Second),
		Result:          json.RawMessage(`{"output":"","cursor":0,"next_cursor":0,"truncated":false,"done":false}`),
	}); err != nil {
		t.Fatalf("mark process started: %v", err)
	}
	return process
}

func processNotificationTexts(t *testing.T, ctx context.Context, fixture processDaemonFixture) []string {
	t.Helper()
	rows, err := fixture.Store.pool.Query(ctx, `
SELECT block.text_content
FROM agent_inputs input
JOIN content_blocks block ON block.agent_id = input.agent_id AND block.owner_agent_input_id = input.id
WHERE input.project_id = $1 AND input.agent_id = $2 AND input.idempotency_scope = 'process_notification'
  AND input.delivery_mode = 'steering'
ORDER BY input.queued_at`, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("query process notifications: %v", err)
	}
	defer rows.Close()
	var texts []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			t.Fatalf("scan process notification: %v", err)
		}
		texts = append(texts, text)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read process notifications: %v", err)
	}
	return texts
}

func TestBackgroundProcessMakesAgentWaitOnWork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "background_waiting_on_work")
	process := startBackgroundProcessForTest(t, ctx, fixture, "background_waiting_on_work", true)
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx, testProjectID, fixture.AgentID, fixture.Lock.ID,
	); err != nil {
		t.Fatalf("release runtime lock: %v", err)
	}
	// Stand in for the model ending its turn after the run_command result.
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE agent_execution_state SET logical_ready_at = NULL, incomplete_tools = false WHERE agent_id = $1;
`, fixture.AgentID); err != nil {
		t.Fatalf("settle agent: %v", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx,
		`DELETE FROM agent_wakeups WHERE agent_id = $1`, fixture.AgentID,
	); err != nil {
		t.Fatalf("clear wakeups: %v", err)
	}

	activity, err := fixture.Store.Execution().GetAgentActivity(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("get activity: %v", err)
	}
	if activity.State != executionstore.SubagentStateWaitingOnWork ||
		len(activity.WaitingOn.ProcessIDs) != 1 || activity.WaitingOn.ProcessIDs[0] != process.ID {
		t.Fatalf("activity = %+v, want waiting_on_work on %s", activity, process.ID)
	}

	if _, err := fixture.Store.pool.Exec(ctx,
		`UPDATE processes SET created_at = statement_timestamp() - interval '2 hours' WHERE id = $1`, process.ID,
	); err != nil {
		t.Fatalf("age process: %v", err)
	}
	activity, err = fixture.Store.Execution().GetAgentActivity(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("get activity: %v", err)
	}
	if activity.State != executionstore.SubagentStateWaitingOnWork || len(activity.WaitingOn.ProcessIDs) != 1 {
		t.Fatalf("activity after two hours = %+v, want waiting_on_work", activity)
	}
}

func TestBackgroundProcessOutputMatchNotifiesAgent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "background_output_match")
	process := startBackgroundProcessWithInputForTest(t, ctx, fixture, "background_output_match",
		executionstore.CreateProcessInput{NotifyMatch: "listening|ERROR"})

	match := executionstore.NotifyProcessOutputMatchInput{
		Authority: fixture.authority(),
		ProjectID: testProjectID,
		AgentID:   fixture.AgentID,
		ID:        process.ID,
		Sequence:  1,
		Result:    json.RawMessage(`{"lines":["listening on :3000"]}`),
	}
	if err := fixture.Store.Execution().NotifyProcessOutputMatch(ctx, match); err != nil {
		t.Fatalf("notify output match: %v", err)
	}
	if err := fixture.Store.Execution().NotifyProcessOutputMatch(ctx, match); err != nil {
		t.Fatalf("replay output match: %v", err)
	}
	match.Sequence = 2
	match.Result = json.RawMessage(`{"lines":["ERROR boom"],"dropped":3}`)
	if err := fixture.Store.Execution().NotifyProcessOutputMatch(ctx, match); err != nil {
		t.Fatalf("notify second output match: %v", err)
	}

	texts := processNotificationTexts(t, ctx, fixture)
	if len(texts) != 2 {
		t.Fatalf("process notifications = %d, want 2: %q", len(texts), texts)
	}
	if !strings.Contains(texts[0], "printed lines matching `listening|ERROR`") ||
		!strings.Contains(texts[0], "listening on :3000") ||
		!strings.Contains(texts[1], "ERROR boom") ||
		!strings.Contains(texts[1], "3 more matching lines") {
		t.Fatalf("output match notifications = %q", texts)
	}
}
