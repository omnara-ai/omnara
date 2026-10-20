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

func startBackgroundProcessForTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	name string,
	notifyOnExit bool,
) executionstore.ProcessRecord {
	t.Helper()
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, name, "run_command")
	process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		ExecutionSpec:         processcmd.ForShell("sleep 60", "sh", ""),
		AgentMachineBindingID: fixture.BindingID,
		Cwd:                   "/work",
		NotifyOnExit:          notifyOnExit,
	})
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
	if activity.State != executionstore.SubagentStateIdle || !activity.WaitingOn.Empty() {
		t.Fatalf("activity after an hour = %+v, want idle", activity)
	}
}
