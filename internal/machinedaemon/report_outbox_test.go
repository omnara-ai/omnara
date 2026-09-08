package machinedaemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
	"github.com/stretchr/testify/require"
)

type rejectingReportTransport struct {
	sent chan statedb.Report
}

func (t rejectingReportTransport) SendReport(
	ctx context.Context,
	report statedb.Report,
) (daemonReportAck, error) {
	select {
	case t.sent <- report:
	case <-ctx.Done():
		return daemonReportAck{}, ctx.Err()
	}
	return daemonReportAck{
		status: daemonprotocol.AckStatusPermanentReject,
		code:   daemonprotocol.ErrorCodeValidationFailed,
		err:    "test rejection",
	}, nil
}

type rejectActionReportTransport struct {
	sent chan statedb.Report
}

type closeStateRejectingReportTransport struct {
	store *statedb.Store
	sent  chan statedb.Report
}

func (t closeStateRejectingReportTransport) SendReport(
	ctx context.Context,
	report statedb.Report,
) (daemonReportAck, error) {
	if err := t.store.Close(); err != nil {
		return daemonReportAck{}, err
	}
	select {
	case t.sent <- report:
	case <-ctx.Done():
		return daemonReportAck{}, ctx.Err()
	}
	return daemonReportAck{
		status: daemonprotocol.AckStatusPermanentReject,
		code:   daemonprotocol.ErrorCodeValidationFailed,
		err:    "test rejection after local database failure",
	}, nil
}

func (t rejectActionReportTransport) SendReport(
	ctx context.Context,
	report statedb.Report,
) (daemonReportAck, error) {
	select {
	case t.sent <- report:
	case <-ctx.Done():
		return daemonReportAck{}, ctx.Err()
	}
	if report.Kind == statedb.ReportActionTerminal {
		return daemonReportAck{
			status: daemonprotocol.AckStatusPermanentReject,
			code:   daemonprotocol.ErrorCodeValidationFailed,
			err:    "test action rejection",
		}, nil
	}
	return daemonReportAck{
		status: daemonprotocol.AckStatusCommitted,
	}, nil
}

func TestOutboxForcedReportKeepsLifecycleOrder(t *testing.T) {
	ctx := context.Background()
	const (
		processID            = "prc_outbox"
		supervisorInstanceID = "supervisor-instance-outbox"
		supervisorToken      = "supervisor-token-outbox"
	)
	dbPath := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := statedb.Open(
		ctx,
		dbPath,
		"ins_outbox",
		"mch_outbox",
	)
	require.NoError(t, err)
	defer store.Close()
	process := statedb.Process{
		ProcessID:            processID,
		SupervisorInstanceID: supervisorInstanceID,
		SupervisorToken:      supervisorToken,
	}
	require.NoError(t, store.ReserveProcess(ctx, process))
	require.NoError(t, store.MarkPrepared(ctx, processID, supervisorInstanceID))
	require.NoError(t, store.MarkAccepted(ctx, processID, supervisorInstanceID))
	supervisor, err := statedb.OpenSupervisor(
		ctx,
		dbPath,
		"ins_outbox",
		"mch_outbox",
		processID,
		supervisorInstanceID,
		supervisorToken,
	)
	require.NoError(t, err)
	defer supervisor.Close()
	if execute, err := supervisor.AuthorizeSpawnOnce(
		ctx,
	); err != nil || !execute {
		t.Fatalf("commit execution: execute=%t err=%v", execute, err)
	}
	require.NoError(t, supervisor.RecordSpawned(
		ctx,
		"process_group",
		"123",
	))

	started := freezeOutboxReport(
		t,
		supervisor.FreezeStartedReport,
		ctx,
		statedb.Report{
			ProcessID: processID,
			Kind:      statedb.ReportProcessStarted,
			Body: mustOutboxJSON(t, map[string]any{
				"type":        "process_started",
				"process_id":  processID,
				"started_at":  "2026-07-27T11:59:59Z",
				"observed_at": "2026-07-27T12:00:00Z",
				"result": map[string]any{
					"output":      "",
					"next_cursor": 0,
				},
			}),
		},
	)
	action := statedb.Action{
		ID:        "act_outbox",
		ProcessID: processID,
		Kind:      "write",
		Seq:       1,
	}
	if decision, _, err := supervisor.ApplyOnce(
		ctx,
		action,
	); err != nil || decision != statedb.ApplyExecute {
		t.Fatalf("apply action: decision=%v err=%v", decision, err)
	}
	actionReport, err := supervisor.FreezeActionReport(
		ctx,
		action.ID,
		statedb.Report{
			ProcessID: processID,
			ActionID:  action.ID,
			Kind:      statedb.ReportActionTerminal,
			Body: mustOutboxJSON(t, map[string]any{
				"type":              "process_action_applied",
				"process_id":        processID,
				"process_action_id": action.ID,
			}),
		},
	)
	require.NoError(t, err)

	sent := make(chan statedb.Report, 2)
	client := New(Config{}, nil, nil)
	client.state = store
	client.addProcess(&processRuntime{
		processID:            processID,
		supervisorInstanceID: supervisorInstanceID,
		cleanupOnly:          true,
	})
	if !client.daemonIdle(ctx) {
		t.Fatal("server-resolved storage cleanup prevented idleness")
	}
	client.removeProcessInstance(processID, supervisorInstanceID)
	client.transport = rejectingReportTransport{sent: sent}
	replayCtx, cancelReplay := context.WithCancel(ctx)
	replayDone := make(chan struct{})
	go func() {
		client.replayReportOutbox(replayCtx, nil)
		close(replayDone)
	}()
	select {
	case report := <-sent:
		if report.ID != started.ID {
			t.Fatalf("first delivered report = %s, want %s", report.ID, started.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("outbox did not deliver its report frontier")
	}
	select {
	case report := <-sent:
		t.Fatalf("outbox delivered past its rejected frontier: %s", report.ID)
	case <-time.After(100 * time.Millisecond):
	}
	cancelReplay()
	select {
	case <-replayDone:
	case <-time.After(time.Second):
		t.Fatal("outbox did not stop")
	}

	reports, err := client.outboxReports(ctx, nil)
	require.NoError(t, err)
	if len(reports) != 0 {
		t.Fatalf(
			"unforced rejected predecessor did not block later reports: %+v",
			reports,
		)
	}
	reports, err = client.outboxReports(
		ctx,
		map[string]struct{}{started.ID: {}},
	)
	require.NoError(t, err)
	if len(reports) != 2 ||
		reports[0].ID != started.ID ||
		reports[1].ID != actionReport.ID {
		t.Fatalf("forced outbox order = %+v", reports)
	}

	forced := map[string]struct{}{started.ID: {}}
	redelivered := make(chan statedb.Report)
	client.transport = closeStateRejectingReportTransport{
		store: store,
		sent:  redelivered,
	}
	replayCtx, cancelReplay = context.WithCancel(ctx)
	replayDone = make(chan struct{})
	go func() {
		client.replayReportOutbox(replayCtx, forced)
		close(replayDone)
	}()
	select {
	case report := <-redelivered:
		if report.ID != started.ID {
			t.Fatalf("redelivered report = %s, want %s", report.ID, started.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("forced rejected report was not redelivered")
	}
	cancelReplay()
	select {
	case <-replayDone:
	case <-time.After(time.Second):
		t.Fatal("outbox did not stop after local database failure")
	}
	if _, retained := forced[started.ID]; !retained {
		t.Fatal("failed local settlement consumed the forced report retry")
	}
}

func TestRejectedActionReportDoesNotBlockProcessTerminalReport(t *testing.T) {
	ctx := context.Background()
	const (
		processID            = "prc_rejected_action_terminal"
		supervisorInstanceID = "supervisor-instance-rejected-action-terminal"
		supervisorToken      = "supervisor-token-rejected-action-terminal"
	)
	dbPath := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := statedb.Open(
		ctx,
		dbPath,
		"ins_rejected_action_terminal",
		"mch_rejected_action_terminal",
	)
	require.NoError(t, err)
	defer store.Close()
	require.NoError(t, store.ReserveProcess(
		ctx,
		statedb.Process{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
			SupervisorToken:      supervisorToken,
		}))
	require.NoError(t, store.MarkPrepared(ctx, processID, supervisorInstanceID))
	require.NoError(t, store.MarkAccepted(ctx, processID, supervisorInstanceID))
	supervisor, err := statedb.OpenSupervisor(
		ctx,
		dbPath,
		"ins_rejected_action_terminal",
		"mch_rejected_action_terminal",
		processID,
		supervisorInstanceID,
		supervisorToken,
	)
	require.NoError(t, err)
	defer supervisor.Close()
	if execute, err := supervisor.AuthorizeSpawnOnce(
		ctx,
	); err != nil || !execute {
		t.Fatalf("commit execution: execute=%t err=%v", execute, err)
	}
	require.NoError(t, supervisor.RecordSpawned(
		ctx,
		"process_group",
		"123",
	))
	action := statedb.Action{
		ID:        "act_rejected_action_terminal",
		ProcessID: processID,
		Kind:      "write",
		Seq:       1,
	}
	if decision, _, err := supervisor.ApplyOnce(
		ctx,
		action,
	); err != nil || decision != statedb.ApplyExecute {
		t.Fatalf("apply action: decision=%v err=%v", decision, err)
	}
	actionReport, err := supervisor.FreezeActionReport(
		ctx,
		action.ID,
		statedb.Report{
			ProcessID: processID,
			ActionID:  action.ID,
			Kind:      statedb.ReportActionTerminal,
			Body: mustOutboxJSON(t, map[string]any{
				"type":              "process_action_applied",
				"process_id":        processID,
				"process_action_id": action.ID,
			}),
		},
	)
	require.NoError(t, err)
	terminalReport, err := supervisor.FreezeTerminalReport(
		ctx,
		statedb.Report{
			ProcessID: processID,
			Kind:      statedb.ReportProcessTerminal,
			Body: mustOutboxJSON(t, map[string]any{
				"type":       "process_finished",
				"process_id": processID,
				"state":      "exited",
				"ended_at":   "2026-07-27T12:00:02Z",
			}),
		},
	)
	require.NoError(t, err)

	const (
		barrierProcessID   = "prc_zz_settlement_barrier"
		barrierInstanceID  = "supervisor-instance-settlement-barrier"
		barrierSupervisorT = "supervisor-token-settlement-barrier"
	)
	require.NoError(t, store.ReserveProcess(
		ctx,
		statedb.Process{
			ProcessID:            barrierProcessID,
			SupervisorInstanceID: barrierInstanceID,
			SupervisorToken:      barrierSupervisorT,
		}))
	require.NoError(t, store.MarkPrepared(ctx, barrierProcessID, barrierInstanceID))
	require.NoError(t, store.MarkAccepted(ctx, barrierProcessID, barrierInstanceID))
	barrierSupervisor, err := statedb.OpenSupervisor(
		ctx,
		dbPath,
		"ins_rejected_action_terminal",
		"mch_rejected_action_terminal",
		barrierProcessID,
		barrierInstanceID,
		barrierSupervisorT,
	)
	require.NoError(t, err)
	defer barrierSupervisor.Close()
	if execute, err := barrierSupervisor.AuthorizeSpawnOnce(
		ctx,
	); err != nil || !execute {
		t.Fatalf("commit barrier execution: execute=%t err=%v", execute, err)
	}
	require.NoError(t, barrierSupervisor.RecordSpawned(
		ctx,
		"process_group",
		"456",
	))
	barrierReport := freezeOutboxReport(
		t,
		barrierSupervisor.FreezeStartedReport,
		ctx,
		statedb.Report{
			ProcessID: barrierProcessID,
			Kind:      statedb.ReportProcessStarted,
			Body: mustOutboxJSON(t, map[string]any{
				"type":        "process_started",
				"process_id":  barrierProcessID,
				"started_at":  "2026-07-27T11:59:59Z",
				"observed_at": "2026-07-27T12:00:00Z",
				"result": map[string]any{
					"output":      "",
					"next_cursor": 0,
				},
			}),
		},
	)

	sent := make(chan statedb.Report, 3)
	client := New(Config{RetryInterval: 10 * time.Millisecond}, nil, nil)
	client.state = store
	client.transport = rejectActionReportTransport{sent: sent}
	replayCtx, cancelReplay := context.WithCancel(ctx)
	replayDone := make(chan struct{})
	go func() {
		client.replayReportOutbox(replayCtx, nil)
		close(replayDone)
	}()
	for _, expected := range []statedb.Report{
		actionReport,
		terminalReport,
		barrierReport,
	} {
		select {
		case report := <-sent:
			if report.ID != expected.ID {
				t.Fatalf(
					"delivered report = %s, want %s",
					report.ID,
					expected.ID,
				)
			}
		case <-time.After(time.Second):
			t.Fatalf("outbox did not deliver report %s", expected.ID)
		}
	}
	cancelReplay()
	select {
	case <-replayDone:
	case <-time.After(time.Second):
		t.Fatal("outbox did not stop")
	}

	rejected, found, err := store.ReportBySlot(
		ctx,
		statedb.ReportActionTerminal,
		processID,
		action.ID,
	)
	if err != nil || !found {
		t.Fatalf("read rejected action report: found=%t err=%v", found, err)
	}
	if rejected.State != statedb.ReportRejected {
		t.Fatalf("action report state = %s, want rejected", rejected.State)
	}
	terminal, found, err := store.ReportBySlot(
		ctx,
		statedb.ReportProcessTerminal,
		processID,
		"",
	)
	if err != nil || !found {
		t.Fatalf("read terminal report: found=%t err=%v", found, err)
	}
	if terminal.State != statedb.ReportAcknowledged {
		t.Fatalf(
			"terminal report state = %s, want acknowledged",
			terminal.State,
		)
	}
	actions, err := store.Actions(ctx, processID)
	require.NoError(t, err)
	if len(actions) != 1 || actions[0].ID != action.ID {
		t.Fatalf("rejected action evidence was released: %+v", actions)
	}
}

func mustOutboxJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	require.NoError(t, err)
	return body
}

func freezeOutboxReport(
	t *testing.T,
	freeze func(
		context.Context,
		statedb.Report,
	) (statedb.Report, error),
	ctx context.Context,
	report statedb.Report,
) statedb.Report {
	t.Helper()
	frozen, err := freeze(ctx, report)
	require.NoError(t, err)
	return frozen
}
