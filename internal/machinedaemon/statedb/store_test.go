package statedb

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/processaction"
	sqlite3 "modernc.org/sqlite"
)

const (
	testInstallation = "ins_test"
	testMachine      = "mch_test"
)

func TestEmptyCollectionsAreNil(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _ := openTestStore(t)

	processes, err := store.Processes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if processes != nil {
		t.Fatalf("processes = %#v, want nil", processes)
	}

	actions, err := store.Actions(ctx, "prc_missing")
	if err != nil {
		t.Fatal(err)
	}
	if actions != nil {
		t.Fatalf("actions = %#v, want nil", actions)
	}

	reports, err := store.DeliveryCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reports != nil {
		t.Fatalf("delivery candidates = %#v, want nil", reports)
	}

	reports, err = store.ReportsForProcess(ctx, "prc_missing")
	if err != nil {
		t.Fatal(err)
	}
	if reports != nil {
		t.Fatalf("process reports = %#v, want nil", reports)
	}
}

func TestDatabaseFullHasTypedCause(t *testing.T) {
	t.Parallel()

	store, _ := openTestStore(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, "CREATE TABLE storage_fill (body BLOB NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	var pages int
	if err := store.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, "PRAGMA max_page_count = "+strconv.Itoa(pages)); err != nil {
		t.Fatal(err)
	}
	_, err := store.db.ExecContext(ctx, "INSERT INTO storage_fill(body) VALUES (zeroblob(10485760))")
	classified := dbError("fill state database", err)
	if err == nil || !errors.Is(classified, ErrFull) {
		t.Fatalf("database full error = %v", classified)
	}
	var sqliteErr *sqlite3.Error
	if !errors.As(classified, &sqliteErr) {
		t.Fatalf("database full error lost SQLite cause: %v", classified)
	}
}

func TestDeleteRejectedPreparationRequiresExactUngrantedIdentity(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _ := openTestStore(t)
	if err := store.DeleteRejectedPreparationAfterArtifacts(
		ctx,
		"prc_missing_rejected_cleanup",
		"supervisor-instance-missing-rejected-cleanup",
	); err != nil {
		t.Fatalf("idempotent missing cleanup: %v", err)
	}
	process := Process{
		ProcessID:            "prc_rejected_cleanup_identity",
		SupervisorInstanceID: "supervisor-instance-rejected-cleanup",
		SupervisorToken:      "supervisor-token-rejected-cleanup",
	}
	if err := store.ReserveProcess(ctx, process); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPrepared(
		ctx,
		process.ProcessID,
		process.SupervisorInstanceID,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteRejectedPreparationAfterArtifacts(
		ctx,
		process.ProcessID,
		"supervisor-instance-replacement",
	); !errors.Is(err, ErrSupervisorIdentityMismatch) {
		t.Fatalf("replacement cleanup error = %v, want identity mismatch", err)
	}
	if err := store.MarkAccepted(
		ctx,
		process.ProcessID,
		process.SupervisorInstanceID,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteRejectedPreparationAfterArtifacts(
		ctx,
		process.ProcessID,
		process.SupervisorInstanceID,
	); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("accepted cleanup error = %v, want state conflict", err)
	}
	if err := store.DeleteStorageExhaustedAfterArtifacts(
		ctx,
		process.ProcessID,
		process.SupervisorInstanceID,
	); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Process(ctx, process.ProcessID); err != nil || found {
		t.Fatalf("process after accepted cleanup: found=%t err=%v", found, err)
	}
}

func TestProcessCrossesExecutionBoundaryOnce(t *testing.T) {
	ctx := context.Background()
	store, path := openTestStore(t)
	process := Process{
		ProcessID:            "prc_one",
		SupervisorInstanceID: "supervisor-instance-one",
		SupervisorToken:      "supervisor-token-one",
	}
	if err := store.ReserveProcess(ctx, process); err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveProcess(ctx, process); err != nil {
		t.Fatalf("idempotent reservation: %v", err)
	}
	if err := store.ReserveProcess(ctx, Process{
		ProcessID:            "prc_two",
		SupervisorInstanceID: "supervisor-instance-two",
		SupervisorToken:      "supervisor-token-two",
	}); err != nil {
		t.Fatalf("reserve independent process: %v", err)
	}

	supervisor := openTestSupervisor(t, path, process)
	if _, err := supervisor.AuthorizeSpawnOnce(ctx); !errors.Is(
		err,
		ErrStateConflict,
	) {
		t.Fatalf("execution before accept error = %v, want conflict", err)
	}
	if err := store.MarkPrepared(
		ctx,
		process.ProcessID,
		process.SupervisorInstanceID,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAccepted(
		ctx,
		process.ProcessID,
		process.SupervisorInstanceID,
	); err != nil {
		t.Fatal(err)
	}
	execute, err := supervisor.AuthorizeSpawnOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !execute {
		t.Fatal("first execution commit did not grant the physical boundary")
	}
	execute, err = supervisor.AuthorizeSpawnOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if execute {
		t.Fatal("execution replay granted the physical boundary twice")
	}
	if err := supervisor.RecordSpawned(
		ctx,
		"process_group",
		"123",
	); err != nil {
		t.Fatal(err)
	}

	started := processReport(
		process.ProcessID,
		ReportProcessStarted,
		"process_started",
		nil,
	)
	first, err := supervisor.FreezeStartedReport(ctx, started)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := supervisor.FreezeStartedReport(ctx, started)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || replayed.ID != first.ID {
		t.Fatalf("canonical report IDs: first=%q replay=%q", first.ID, replayed.ID)
	}
	changed := started
	changed.Body = append([]byte(nil), started.Body...)
	changed.Body = []byte(strings.Replace(
		string(changed.Body),
		`"output":""`,
		`"output":"changed"`,
		1,
	))
	if _, err := supervisor.FreezeStartedReport(
		ctx,
		changed,
	); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("changed frozen report error = %v, want conflict", err)
	}

	stored, found, err := store.Process(ctx, process.ProcessID)
	if err != nil || !found {
		t.Fatalf("read process: found=%t err=%v", found, err)
	}
	if !stored.ExecCommitted ||
		stored.ContainmentKind != "process_group" ||
		stored.ContainmentID != "123" {
		t.Fatalf("stored process = %+v", stored)
	}
}

func TestFrozenReportMustFitDaemonWireEnvelope(t *testing.T) {
	t.Parallel()

	_, _, process, supervisor := runningTestProcess(t)
	report := Report{
		ID:        newReportID(),
		ProcessID: process.ProcessID,
		Kind:      ReportProcessTerminal,
	}
	event := daemonprotocol.ReportedEvent{
		Type:               "process_finished",
		ProcessID:          report.ProcessID,
		State:              "unknown",
		StateReasonCode:    "wire_limit",
		StateReasonMessage: "x",
		EndedAt:            time.Now().UTC(),
		Result:             json.RawMessage(`{"ok":false}`),
	}
	baseBody, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	baseWire, err := json.Marshal(daemonprotocol.Message{
		Type:     "report",
		ReportID: report.ID,
		Event:    &event,
	})
	if err != nil {
		t.Fatal(err)
	}
	envelopeBytes := len(baseWire) - len(baseBody)
	messageBytes := daemonprotocol.MaxMessageBytes -
		len(baseBody) -
		envelopeBytes +
		2
	if messageBytes <= 1 {
		t.Fatalf("invalid report wire test budget %d", messageBytes)
	}

	event.StateReasonMessage = strings.Repeat("x", messageBytes-1)
	report.Body, err = json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateReportWireEnvelope(report); err != nil {
		t.Fatalf("maximum-sized report envelope: %v", err)
	}

	event.StateReasonMessage += "x"
	report.Body, err = json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateReportWireEnvelope(report); err == nil ||
		!strings.Contains(err.Error(), "frozen report envelope") {
		t.Fatalf("oversized report envelope error = %v", err)
	}
	report.ID = ""
	if _, err := supervisor.FreezeTerminalReport(
		context.Background(),
		report,
	); !errors.Is(err, ErrStateConflict) {
		t.Fatalf(
			"oversized report freeze error = %v, want state conflict",
			err,
		)
	}
}

func TestMaximumProcessObservationFitsDaemonReportEnvelopes(t *testing.T) {
	t.Parallel()

	// NUL has the largest JSON expansion of any valid UTF-8 byte, making this
	// the maximum-shaped wire envelope.
	result, err := json.Marshal(map[string]any{
		"ok":        true,
		"state":     "exited",
		"exit_code": 0,
		"output": strings.Repeat(
			"\x00",
			processaction.MaxObservationBytes,
		),
		"cursor":      0,
		"next_cursor": processaction.MaxObservationBytes,
		"truncated":   false,
		"done":        true,
		"error":       "",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name   string
		report Report
		event  daemonprotocol.ReportedEvent
	}{
		{
			name: "process terminal",
			report: Report{
				ID:        newReportID(),
				ProcessID: "prc_aaaaaaaaaaaaaaaaaaaaaaaaaa",
				Kind:      ReportProcessTerminal,
			},
			event: daemonprotocol.ReportedEvent{
				Type:      "process_finished",
				ProcessID: "prc_aaaaaaaaaaaaaaaaaaaaaaaaaa",
				State:     "exited",
				Result:    result,
				StartedAt: now,
				EndedAt:   now,
			},
		},
		{
			name: "action terminal",
			report: Report{
				ID:        newReportID(),
				ProcessID: "prc_aaaaaaaaaaaaaaaaaaaaaaaaaa",
				ActionID:  "pac_aaaaaaaaaaaaaaaaaaaaaaaaaa",
				Kind:      ReportActionTerminal,
			},
			event: daemonprotocol.ReportedEvent{
				Type:            "process_action_applied",
				ProcessID:       "prc_aaaaaaaaaaaaaaaaaaaaaaaaaa",
				ProcessActionID: "pac_aaaaaaaaaaaaaaaaaaaaaaaaaa",
				Result:          result,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			body, err := json.Marshal(test.event)
			if err != nil {
				t.Fatal(err)
			}
			test.report.Body = body
			if err := validateReportWireEnvelope(test.report); err != nil {
				t.Fatalf(
					"maximum process observation exceeds report wire limit: %v",
					err,
				)
			}
		})
	}
}

func TestApplyOnceUsesOnePreEffectBoundaryAndSequenceFrontier(
	t *testing.T,
) {
	ctx := context.Background()
	store, _, process, supervisor := runningTestProcess(t)
	first := Action{
		ID:        "act_write",
		ProcessID: process.ProcessID,
		Kind:      "write",
		Seq:       1,
	}
	decision, _, err := supervisor.ApplyOnce(ctx, first)
	if err != nil || decision != ApplyExecute {
		t.Fatalf("first apply: decision=%v err=%v", decision, err)
	}
	decision, _, err = supervisor.ApplyOnce(ctx, first)
	if err != nil || decision != ApplyOutcomeUnknown {
		t.Fatalf("apply across marked boundary: decision=%v err=%v", decision, err)
	}

	report := actionReport(first, nil)
	frozen, err := supervisor.FreezeActionReport(ctx, first.ID, report)
	if err != nil {
		t.Fatal(err)
	}
	decision, replayed, err := supervisor.ApplyOnce(ctx, first)
	if err != nil || decision != ApplyAlreadyReported ||
		replayed.ID != frozen.ID {
		t.Fatalf(
			"reported replay: decision=%v report=%+v err=%v",
			decision,
			replayed,
			err,
		)
	}

	second := Action{
		ID:        "act_interrupt",
		ProcessID: process.ProcessID,
		Kind:      "interrupt",
		Seq:       2,
	}
	decision, _, err = supervisor.ApplyOnce(ctx, second)
	if err != nil || decision != ApplyBlocked {
		t.Fatalf("out-of-order apply: decision=%v err=%v", decision, err)
	}
	if err := store.AcknowledgeReport(
		ctx,
		frozen.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Action(
		ctx,
		first.ID,
	); err != nil || found {
		t.Fatalf("acknowledged action remains: found=%t err=%v", found, err)
	}
	afterAck, _, err := store.Process(ctx, process.ProcessID)
	if err != nil {
		t.Fatal(err)
	}
	if afterAck.ResolvedActionSeq != 1 {
		t.Fatalf("process after action ack = %+v", afterAck)
	}

	decision, _, err = supervisor.ApplyOnce(ctx, first)
	if err != nil || decision != ApplyAlreadyResolved {
		t.Fatalf("compacted replay: decision=%v err=%v", decision, err)
	}
	decision, _, err = supervisor.ApplyOnce(ctx, second)
	if err != nil || decision != ApplyExecute {
		t.Fatalf("next action apply: decision=%v err=%v", decision, err)
	}
}

func TestEffectDecisionZeroValuesDoNotAuthorizeWork(t *testing.T) {
	t.Parallel()

	if ApplyDecision(0) == ApplyExecute {
		t.Fatal("zero apply decision authorizes an effect")
	}
	if NoEffectDecision(0) == NoEffectRecorded {
		t.Fatal("zero no-effect decision records an outcome")
	}
}

func TestActionEffectAndNoEffectResolutionCompeteAtomically(t *testing.T) {
	ctx := context.Background()
	store, path, process, applySupervisor := runningTestProcess(t)
	noEffectSupervisor := openTestSupervisor(t, path, process)
	action := Action{
		ID:        "act_write",
		ProcessID: process.ProcessID,
		Kind:      "write",
		Seq:       1,
	}
	report := actionReportWithType(
		action,
		"process_action_failed",
		nil,
	)

	start := make(chan struct{})
	var wait sync.WaitGroup
	var applyDecision ApplyDecision
	var noEffectDecision NoEffectDecision
	var applyErr, noEffectErr error
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		applyDecision, _, applyErr = applySupervisor.ApplyOnce(ctx, action)
	}()
	go func() {
		defer wait.Done()
		<-start
		noEffectDecision, _, noEffectErr =
			noEffectSupervisor.RecordActionWithoutEffect(
				ctx,
				action,
				report,
			)
	}()
	close(start)
	wait.Wait()
	if applyErr != nil || noEffectErr != nil {
		t.Fatalf(
			"apply error=%v no-effect error=%v",
			applyErr,
			noEffectErr,
		)
	}
	switch {
	case applyDecision == ApplyExecute &&
		noEffectDecision == NoEffectLostToApply:
	case applyDecision == ApplyAlreadyReported &&
		(noEffectDecision == NoEffectRecorded ||
			noEffectDecision == NoEffectAlreadyReported):
	default:
		t.Fatalf(
			"apply decision=%v no-effect decision=%v, no single boundary winner",
			applyDecision,
			noEffectDecision,
		)
	}

	stored, found, err := store.Action(ctx, action.ID)
	if err != nil || !found {
		t.Fatalf("read action boundary: found=%t err=%v", found, err)
	}
	_, reportFound, err := store.ReportBySlot(
		ctx,
		ReportActionTerminal,
		process.ProcessID,
		action.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if stored.EffectCommitted == reportFound {
		t.Fatalf(
			"effect_committed=%t report_found=%t, want exactly one winner",
			stored.EffectCommitted,
			reportFound,
		)
	}
}

func TestIndependentSupervisorsSerializeWritesAcrossProcesses(t *testing.T) {
	ctx := context.Background()
	store, path := openTestStore(t)
	processes := make([]Process, 4)
	for index := range processes {
		process := Process{
			ProcessID:            fmt.Sprintf("prc_multiprocess_%d", index),
			SupervisorInstanceID: fmt.Sprintf("supervisor-instance-multiprocess-%d", index),
			SupervisorToken:      fmt.Sprintf("supervisor-token-multiprocess-%d", index),
		}
		processes[index] = process
		if err := store.ReserveProcess(ctx, process); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkPrepared(
			ctx,
			process.ProcessID,
			process.SupervisorInstanceID,
		); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkAccepted(
			ctx,
			process.ProcessID,
			process.SupervisorInstanceID,
		); err != nil {
			t.Fatal(err)
		}
	}

	type child struct {
		command *exec.Cmd
		gate    io.WriteCloser
		stdout  bytes.Buffer
		stderr  bytes.Buffer
		ready   chan struct{}
		done    chan struct{}
	}
	children := make([]*child, len(processes))
	for index := range processes {
		process := processes[index]
		command := exec.Command(
			os.Args[0],
			"-test.run=^TestStateDBSubprocessWriterHelper$",
		)
		command.Env = append(
			os.Environ(),
			"OMNARA_STATE_DB_WRITER_HELPER=1",
			"OMNARA_STATE_DB_WRITER_PATH="+path,
			"OMNARA_STATE_DB_WRITER_PROCESS="+process.ProcessID,
			"OMNARA_STATE_DB_WRITER_SUPERVISOR_INSTANCE_ID="+process.SupervisorInstanceID,
			"OMNARA_STATE_DB_WRITER_SUPERVISOR_TOKEN="+process.SupervisorToken,
		)
		gate, err := command.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := command.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		children[index] = &child{
			command: command,
			gate:    gate,
			ready:   make(chan struct{}),
			done:    make(chan struct{}),
		}
		command.Stderr = &children[index].stderr
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		go func(c *child) {
			defer close(c.done)
			if _, err := io.ReadFull(stdout, make([]byte, 1)); err != nil {
				return
			}
			close(c.ready)
			_, _ = io.Copy(&c.stdout, stdout)
		}(children[index])
	}
	for index := range children {
		select {
		case <-children[index].ready:
		case <-children[index].done:
			for kill := range children {
				_ = children[kill].command.Process.Kill()
			}
			for wait := range children {
				<-children[wait].done
				_ = children[wait].command.Wait()
			}
			t.Fatalf(
				"subprocess writer %d did not reach the concurrency gate\n%s%s",
				index,
				children[index].stdout.String(),
				children[index].stderr.String(),
			)
		}
	}
	for index := range children {
		if err := children[index].gate.Close(); err != nil {
			t.Fatal(err)
		}
	}
	waitErrors := make([]error, len(children))
	for index := range children {
		<-children[index].done
		waitErrors[index] = children[index].command.Wait()
	}
	for index, err := range waitErrors {
		if err != nil {
			t.Fatalf(
				"subprocess writer %d: %v\n%s%s",
				index,
				err,
				children[index].stdout.String(),
				children[index].stderr.String(),
			)
		}
	}
	for _, process := range processes {
		actions, err := store.Actions(ctx, process.ProcessID)
		if err != nil {
			t.Fatal(err)
		}
		if len(actions) != 1 || actions[0].ID != "act_"+process.ProcessID {
			t.Fatalf(
				"process %s actions = %+v",
				process.ProcessID,
				actions,
			)
		}
	}
}

func TestStateDBSubprocessWriterHelper(t *testing.T) {
	if os.Getenv("OMNARA_STATE_DB_WRITER_HELPER") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	process := Process{
		ProcessID: os.Getenv("OMNARA_STATE_DB_WRITER_PROCESS"),
		SupervisorInstanceID: os.Getenv(
			"OMNARA_STATE_DB_WRITER_SUPERVISOR_INSTANCE_ID",
		),
		SupervisorToken: os.Getenv("OMNARA_STATE_DB_WRITER_SUPERVISOR_TOKEN"),
	}
	supervisor, err := OpenSupervisor(
		ctx,
		os.Getenv("OMNARA_STATE_DB_WRITER_PATH"),
		testInstallation,
		testMachine,
		process.ProcessID,
		process.SupervisorInstanceID,
		process.SupervisorToken,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	if _, err := os.Stdout.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stdin.Read(make([]byte, 1)); err != nil &&
		!errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	execute, err := supervisor.AuthorizeSpawnOnce(ctx)
	if err != nil || !execute {
		t.Fatalf("commit execution: execute=%t err=%v", execute, err)
	}
	if err := supervisor.RecordSpawned(
		ctx,
		"process_group",
		strconv.Itoa(os.Getpid()),
	); err != nil {
		t.Fatal(err)
	}
	action := Action{
		ID:        "act_" + process.ProcessID,
		ProcessID: process.ProcessID,
		Kind:      "write",
		Seq:       1,
	}
	decision, _, err := supervisor.ApplyOnce(ctx, action)
	if err != nil || decision != ApplyExecute {
		t.Fatalf("apply: decision=%v err=%v", decision, err)
	}
	if _, err := supervisor.FreezeActionReport(
		ctx,
		action.ID,
		actionReport(action, nil),
	); err != nil {
		t.Fatal(err)
	}
}

func TestReportValidationRejectsSemanticallyInvalidEvidence(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	report := func(
		kind ReportKind,
		event daemonprotocol.ReportedEvent,
	) Report {
		body, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		return Report{
			ProcessID: event.ProcessID,
			ActionID:  event.ProcessActionID,
			Kind:      kind,
			Body:      body,
		}
	}
	tests := []struct {
		name   string
		report Report
	}{
		{
			name: "started kind with terminal event",
			report: report(ReportProcessStarted, daemonprotocol.ReportedEvent{
				Type:      "process_finished",
				ProcessID: "prc_invalid",
				State:     "exited",
				EndedAt:   now,
			}),
		},
		{
			name: "started observation before physical start",
			report: report(ReportProcessStarted, daemonprotocol.ReportedEvent{
				Type:       "process_started",
				ProcessID:  "prc_invalid",
				StartedAt:  now.Add(time.Second),
				ObservedAt: now,
			}),
		},
		{
			name: "terminal end before physical start",
			report: report(ReportProcessTerminal, daemonprotocol.ReportedEvent{
				Type:      "process_finished",
				ProcessID: "prc_invalid",
				State:     "exited",
				StartedAt: now.Add(time.Second),
				EndedAt:   now,
			}),
		},
		{
			name: "exited process without physical end",
			report: report(ReportProcessTerminal, daemonprotocol.ReportedEvent{
				Type:      "process_finished",
				ProcessID: "prc_invalid",
				State:     "exited",
			}),
		},
		{
			name: "failed process with physical start but no end",
			report: report(ReportProcessTerminal, daemonprotocol.ReportedEvent{
				Type:            "process_finished",
				ProcessID:       "prc_invalid",
				State:           "failed",
				StateReasonCode: "wait_failed",
				StartedAt:       now,
			}),
		},
		{
			name: "terminal process with running state",
			report: report(ReportProcessTerminal, daemonprotocol.ReportedEvent{
				Type:      "process_finished",
				ProcessID: "prc_invalid",
				State:     "running",
				EndedAt:   now,
			}),
		},
		{
			name: "unknown process without reason",
			report: report(ReportProcessTerminal, daemonprotocol.ReportedEvent{
				Type:      "process_finished",
				ProcessID: "prc_invalid",
				State:     "unknown",
				EndedAt:   now,
			}),
		},
		{
			name: "action with observation time",
			report: report(ReportActionTerminal, daemonprotocol.ReportedEvent{
				Type:            "process_action_applied",
				ProcessID:       "prc_invalid",
				ProcessActionID: "act_invalid",
				ObservedAt:      now,
			}),
		},
		{
			name: "unknown action without reason",
			report: report(ReportActionTerminal, daemonprotocol.ReportedEvent{
				Type:            "process_action_unknown",
				ProcessID:       "prc_invalid",
				ProcessActionID: "act_invalid",
			}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateReport(test.report); err == nil {
				t.Fatalf(
					"semantically invalid report was accepted: %+v",
					test.report,
				)
			}
		})
	}
}

func TestReportValidationAcceptsTerminalEvidenceWithoutUnobservedEnd(t *testing.T) {
	tests := []daemonprotocol.ReportedEvent{
		{
			Type:            daemonprotocol.EventProcessFinished,
			ProcessID:       "prc_failed_before_start",
			State:           daemonprotocol.ProcessStateFailed,
			StateReasonCode: "start_failed",
		},
		{
			Type:            daemonprotocol.EventProcessFinished,
			ProcessID:       "prc_unknown_end",
			State:           daemonprotocol.ProcessStateUnknown,
			StateReasonCode: "local_process_unrecoverable",
		},
	}
	for _, event := range tests {
		body, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateReport(Report{
			ProcessID: event.ProcessID,
			Kind:      ReportProcessTerminal,
			Body:      body,
		}); err != nil {
			t.Fatalf("terminal evidence for %s was rejected: %v", event.ProcessID, err)
		}
	}
}

func TestTerminalClosureRetainsEvidenceUntilBothAuthoritiesRelease(
	t *testing.T,
) {
	ctx := context.Background()
	store, _, process, supervisor := acceptedTestProcess(t)
	terminal, err := supervisor.FreezeTerminalReport(
		ctx,
		processReport(
			process.ProcessID,
			ReportProcessTerminal,
			"process_finished",
			nil,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.MarkLocalClosed(
		ctx,
	); !errors.Is(err, ErrClosureBlocked) {
		t.Fatalf("closure before containment error = %v", err)
	}
	if err := supervisor.MarkContainmentEmpty(ctx); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.MarkLocalClosed(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteClosedAfterArtifacts(
		ctx,
		process.ProcessID,
		process.SupervisorInstanceID,
	); !errors.Is(err, ErrClosureBlocked) {
		t.Fatalf("cleanup before server release error = %v", err)
	}
	if err := store.AcknowledgeReport(
		ctx,
		terminal.ID,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteClosedAfterArtifacts(
		ctx,
		process.ProcessID,
		process.SupervisorInstanceID,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteClosedAfterArtifacts(
		ctx,
		process.ProcessID,
		process.SupervisorInstanceID,
	); err != nil {
		t.Fatalf("idempotent row-last cleanup: %v", err)
	}
}

func TestPermanentRejectionRetainsEvidence(t *testing.T) {
	ctx := context.Background()
	store, _, process, supervisor := acceptedTestProcess(t)
	terminal, err := supervisor.FreezeTerminalReport(
		ctx,
		processReport(
			process.ProcessID,
			ReportProcessTerminal,
			"process_finished",
			nil,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.MarkContainmentEmpty(ctx); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.MarkLocalClosed(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.RejectReport(
		ctx,
		terminal.ID,
		"validation_failed",
		"bad report",
	); err != nil {
		t.Fatal(err)
	}
	stored, found, err := store.ReportBySlot(
		ctx,
		terminal.Kind,
		terminal.ProcessID,
		terminal.ActionID,
	)
	if err != nil || !found {
		t.Fatalf("read rejected report: found=%t err=%v", found, err)
	}
	if stored.State != ReportRejected ||
		stored.ID != terminal.ID ||
		string(stored.Body) != string(terminal.Body) {
		t.Fatalf("rejected report = %+v", stored)
	}
}

func TestReleaseMissingAcceptedActionAdvancesFrontier(t *testing.T) {
	ctx := context.Background()
	store, _, process, supervisor := runningTestProcess(t)
	if err := store.ReleaseAction(
		ctx,
		process.ProcessID,
		process.SupervisorInstanceID,
		"act_missing",
		1,
	); err != nil {
		t.Fatal(err)
	}
	action := Action{
		ID:        "act_next",
		ProcessID: process.ProcessID,
		Kind:      "write",
		Seq:       2,
	}
	decision, _, err := supervisor.ApplyOnce(ctx, action)
	if err != nil || decision != ApplyExecute {
		t.Fatalf("apply after released absence: decision=%v err=%v", decision, err)
	}
}

func TestReleaseMissingLaterActionPreservesEarlierEvidence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _, process, supervisor := runningTestProcess(t)
	first := Action{
		ID:        "act_pending_predecessor",
		ProcessID: process.ProcessID,
		Kind:      "write",
		Seq:       1,
	}
	if decision, _, err := supervisor.ApplyOnce(
		ctx,
		first,
	); err != nil || decision != ApplyExecute {
		t.Fatalf("apply predecessor: decision=%v err=%v", decision, err)
	}
	report, err := supervisor.FreezeActionReport(
		ctx,
		first.ID,
		actionReport(first, nil),
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.ReleaseAction(
		ctx,
		process.ProcessID,
		process.SupervisorInstanceID,
		"act_never_received",
		2,
	); err != nil {
		t.Fatal(err)
	}
	stored, found, err := store.Process(ctx, process.ProcessID)
	if err != nil || !found {
		t.Fatalf("read process: found=%t err=%v", found, err)
	}
	if stored.ResolvedActionSeq != 0 {
		t.Fatalf(
			"missing later action advanced frontier to %d",
			stored.ResolvedActionSeq,
		)
	}
	if _, found, err := store.Action(
		ctx,
		first.ID,
	); err != nil || !found {
		t.Fatalf("predecessor action state: found=%t err=%v", found, err)
	}
	if storedReport, found, err := store.ReportBySlot(
		ctx,
		report.Kind,
		report.ProcessID,
		report.ActionID,
	); err != nil || !found || storedReport.ID != report.ID {
		t.Fatalf(
			"predecessor evidence: found=%t report=%+v err=%v",
			found,
			storedReport,
			err,
		)
	}
	if err := store.AcknowledgeReport(
		ctx,
		report.ID,
	); err != nil {
		t.Fatal(err)
	}
	stored, found, err = store.Process(ctx, process.ProcessID)
	if err != nil || !found {
		t.Fatalf("read process after acknowledgement: found=%t err=%v", found, err)
	}
	if stored.ResolvedActionSeq != first.Seq {
		t.Fatalf(
			"frontier after predecessor acknowledgement = %d, want %d",
			stored.ResolvedActionSeq,
			first.Seq,
		)
	}
}

func TestReleaseActionCannotAdvanceAcrossDifferentSequenceOwner(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _, process, supervisor := runningTestProcess(t)
	action := Action{
		ID:        "act_sequence_owner",
		ProcessID: process.ProcessID,
		Kind:      "write",
		Seq:       1,
	}
	if decision, _, err := supervisor.ApplyOnce(
		ctx,
		action,
	); err != nil || decision != ApplyExecute {
		t.Fatalf("apply sequence owner: decision=%v err=%v", decision, err)
	}
	if err := store.ReleaseAction(
		ctx,
		process.ProcessID,
		process.SupervisorInstanceID,
		"act_wrong_identity",
		action.Seq,
	); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("release different sequence owner error = %v", err)
	}
	stored, found, err := store.Process(ctx, process.ProcessID)
	if err != nil || !found {
		t.Fatalf("read process: found=%t err=%v", found, err)
	}
	if stored.ResolvedActionSeq != 0 {
		t.Fatalf(
			"conflicting release advanced frontier to %d",
			stored.ResolvedActionSeq,
		)
	}
	if _, found, err := store.Action(
		ctx,
		action.ID,
	); err != nil || !found {
		t.Fatalf("sequence owner after conflicting release: found=%t err=%v", found, err)
	}
}

func TestOrderedDeliveryCompactsAbsentActionPrefix(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _, process, supervisor := runningTestProcess(t)
	action := Action{
		ID:        "act_after_absent_prefix",
		ProcessID: process.ProcessID,
		Kind:      "write",
		Seq:       3,
	}
	decision, _, err := supervisor.ApplyOnce(ctx, action)
	if err != nil || decision != ApplyExecute {
		t.Fatalf("apply after absent prefix: decision=%v err=%v", decision, err)
	}
	stored, found, err := store.Process(ctx, process.ProcessID)
	if err != nil || !found {
		t.Fatalf("read process: found=%t err=%v", found, err)
	}
	if stored.ResolvedActionSeq != 2 {
		t.Fatalf(
			"resolved action frontier = %d, want compacted prefix 2",
			stored.ResolvedActionSeq,
		)
	}
}

func TestReleaseActionCannotDeleteFrozenEvidence(t *testing.T) {
	ctx := context.Background()
	store, _, process, supervisor := runningTestProcess(t)
	action := Action{
		ID:        "act_frozen",
		ProcessID: process.ProcessID,
		Kind:      "write",
		Seq:       1,
	}
	if decision, _, err := supervisor.ApplyOnce(
		ctx,
		action,
	); err != nil || decision != ApplyExecute {
		t.Fatalf("apply: decision=%v err=%v", decision, err)
	}
	report, err := supervisor.FreezeActionReport(
		ctx,
		action.ID,
		actionReport(action, nil),
	)
	if err != nil {
		t.Fatal(err)
	}

	err = store.ReleaseAction(
		ctx,
		process.ProcessID,
		process.SupervisorInstanceID,
		action.ID,
		action.Seq,
	)
	if !errors.Is(err, ErrStateConflict) {
		t.Fatalf("release frozen action error = %v, want conflict", err)
	}
	if _, found, err := store.Action(
		ctx,
		action.ID,
	); err != nil || !found {
		t.Fatalf("frozen action survived release: found=%t err=%v", found, err)
	}
	if stored, found, err := store.ReportBySlot(
		ctx,
		report.Kind,
		report.ProcessID,
		report.ActionID,
	); err != nil || !found || stored.ID != report.ID ||
		stored.State != ReportPending {
		t.Fatalf(
			"frozen evidence survived release: found=%t report=%+v err=%v",
			found,
			stored,
			err,
		)
	}
}

func TestPragmasApplyToReplacementConnections(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _ := openTestStore(t)

	store.db.SetMaxIdleConns(0)
	connection, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	store.db.SetMaxIdleConns(1)
	if err := verifyPragmas(ctx, store.db); err != nil {
		t.Fatalf("verify replacement SQLite connection: %v", err)
	}
}

func TestDeliveryCandidatesMergeRejectedAndPendingInLifecycleOrder(
	t *testing.T,
) {
	ctx := context.Background()
	store, _, process, supervisor := runningTestProcess(t)
	started, err := supervisor.FreezeStartedReport(
		ctx,
		processReport(
			process.ProcessID,
			ReportProcessStarted,
			"process_started",
			nil,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RejectReport(
		ctx,
		started.ID,
		"validation_failed",
		"retry after reconciliation",
	); err != nil {
		t.Fatal(err)
	}
	action := Action{
		ID:        "act_pending",
		ProcessID: process.ProcessID,
		Kind:      "write",
		Seq:       1,
	}
	if decision, _, err := supervisor.ApplyOnce(
		ctx,
		action,
	); err != nil || decision != ApplyExecute {
		t.Fatalf("apply: decision=%v err=%v", decision, err)
	}
	actionTerminal, err := supervisor.FreezeActionReport(
		ctx,
		action.ID,
		actionReport(action, nil),
	)
	if err != nil {
		t.Fatal(err)
	}

	candidates, err := store.DeliveryCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 ||
		candidates[0].ID != started.ID ||
		candidates[1].ID != actionTerminal.ID {
		t.Fatalf("delivery order = %+v", candidates)
	}
}

func TestSnapshotIncludesActionsAndRejectedEvidenceAtomically(t *testing.T) {
	ctx := context.Background()
	store, _, process, supervisor := runningTestProcess(t)
	action := Action{
		ID:        "act_snapshot",
		ProcessID: process.ProcessID,
		Kind:      "write",
		Seq:       1,
	}
	if decision, _, err := supervisor.ApplyOnce(
		ctx,
		action,
	); err != nil || decision != ApplyExecute {
		t.Fatalf("apply: decision=%v err=%v", decision, err)
	}
	report, err := supervisor.FreezeActionReport(
		ctx,
		action.ID,
		actionReport(action, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RejectReport(
		ctx,
		report.ID,
		"validation_failed",
		"retry after reconciliation",
	); err != nil {
		t.Fatal(err)
	}

	snapshot, err := store.SnapshotForReconciliation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Processes) != 1 {
		t.Fatalf("snapshot processes = %+v", snapshot.Processes)
	}
	got := snapshot.Processes[0]
	if got.Process.ProcessID != process.ProcessID ||
		len(got.Actions) != 1 ||
		got.Actions[0].Action.ID != action.ID ||
		!got.Actions[0].Reported ||
		len(got.RejectedReportIDs) != 1 ||
		got.RejectedReportIDs[0] != report.ID {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestOpenRejectsAnotherMachineIdentity(t *testing.T) {
	store, path := openTestStore(t)
	_ = store
	if _, err := Open(
		context.Background(),
		path,
		testInstallation,
		"mch_other",
	); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("identity mismatch error = %v", err)
	}
}

func TestVerifyExistingStateDatabaseRejectsAnotherMachine(t *testing.T) {
	t.Parallel()

	store, _ := openTestStore(t)
	if err := verifyExistingStateDatabase(
		context.Background(),
		store.db,
		testInstallation,
		"mch_other",
	); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("bound identity mismatch error = %v", err)
	}
}

func TestOpenRecordsGooseMigrationOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, path := openTestStore(t)

	var applied int
	if err := store.db.QueryRowContext(
		ctx,
		`SELECT count(*)
		 FROM goose_db_version
		 WHERE version_id = 1 AND is_applied = 1`,
	).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("applied migration records = %d, want 1", applied)
	}

	reopened, err := Open(ctx, path, testInstallation, testMachine)
	if err != nil {
		t.Fatalf("reopen migrated database: %v", err)
	}
	defer reopened.Close()
	if err := reopened.db.QueryRowContext(
		ctx,
		`SELECT count(*)
		 FROM goose_db_version
		 WHERE version_id = 1 AND is_applied = 1`,
	).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("applied migration records after reopen = %d, want 1", applied)
	}
}

func TestOpenRecoversMigrationCommittedBeforeIdentity(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := sql.Open("sqlite", stateDSN(path, false))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyPragmas(ctx, db); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := applyEmbeddedMigrations(ctx, db); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path, testInstallation, testMachine)
	if err != nil {
		t.Fatalf("recover identity binding: %v", err)
	}
	defer store.Close()
	if err := verifyIdentity(
		ctx,
		store.db,
		testInstallation,
		testMachine,
	); err != nil {
		t.Fatal(err)
	}
}

func TestStateMigrationFailureRollsBack(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := sql.Open("sqlite", stateDSN(path, false))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	failing := fstest.MapFS{
		"000001_failing.sql": {
			Data: []byte(`-- +goose Up
CREATE TABLE migration_transaction_probe(id INTEGER PRIMARY KEY);
SELECT missing_migration_function();
`),
		},
	}
	if err := applyMigrations(ctx, db, failing); err == nil {
		t.Fatal("failing migration unexpectedly succeeded")
	}

	var probeExists bool
	if err := db.QueryRowContext(
		ctx,
		`SELECT EXISTS (
			SELECT 1
			FROM sqlite_schema
			WHERE type = 'table' AND name = 'migration_transaction_probe'
		)`,
	).Scan(&probeExists); err != nil {
		t.Fatal(err)
	}
	if probeExists {
		t.Fatal("failed migration left its schema change behind")
	}

	var applied int
	if err := db.QueryRowContext(
		ctx,
		`SELECT count(*)
		 FROM goose_db_version
		 WHERE version_id = 1 AND is_applied = 1`,
	).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 0 {
		t.Fatalf("failed migration records = %d, want 0", applied)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path, testInstallation, testMachine)
	if err != nil {
		t.Fatalf("recover after rolled-back migration: %v", err)
	}
	defer store.Close()
}

func TestOnlyMainDaemonRequiresCurrentStateMigration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, path := openTestStore(t)
	process := Process{
		ProcessID:            "prc_future_schema",
		SupervisorInstanceID: "supervisor-instance-future-schema",
		SupervisorToken:      "supervisor-token-future-schema",
	}
	if err := store.ReserveProcess(ctx, process); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(
		ctx,
		`INSERT INTO goose_db_version(version_id, is_applied)
		 VALUES(2, 1)`,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(
		ctx,
		path,
		testInstallation,
		testMachine,
	); err == nil || !strings.Contains(err.Error(), "newer than binary target") {
		t.Fatalf("main daemon future migration error = %v", err)
	}
	supervisor, err := OpenSupervisor(
		ctx,
		path,
		testInstallation,
		testMachine,
		process.ProcessID,
		process.SupervisorInstanceID,
		process.SupervisorToken,
	)
	if err != nil {
		t.Fatalf("supervisor open with additive future migration: %v", err)
	}
	defer supervisor.Close()
}

func TestSupervisorWritesAfterAdditiveStateMigration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _, _, supervisor := acceptedTestProcess(t)
	initial, err := stateMigrations.ReadFile("migrations/000001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	migrations := fstest.MapFS{
		"000001_initial.sql": {Data: initial},
		"000002_additive.sql": {
			Data: []byte(`-- +goose Up
ALTER TABLE processes
ADD COLUMN future_metadata TEXT NOT NULL DEFAULT '';
`),
		},
	}
	if err := applyMigrations(ctx, store.db, migrations); err != nil {
		t.Fatal(err)
	}

	execute, err := supervisor.AuthorizeSpawnOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !execute {
		t.Fatal("supervisor did not commit execution after additive migration")
	}
	if err := supervisor.RecordSpawned(ctx, "process_group", "789"); err != nil {
		t.Fatal(err)
	}
}

func TestOpenFinishesInitializationOfExistingEmptyDatabase(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.sqlite")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(
		context.Background(),
		path,
		testInstallation,
		testMachine,
	)
	if err != nil {
		t.Fatalf("open crash-created empty database: %v", err)
	}
	defer store.Close()
	if err := store.Audit(context.Background()); err != nil {
		t.Fatalf("audit initialized database: %v", err)
	}
}

func TestOpenRejectsForeignSQLiteDatabase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := sql.Open("sqlite", stateDSN(path, false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(
		ctx,
		`CREATE TABLE foreign_state(id INTEGER PRIMARY KEY) STRICT`,
	); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(
		ctx,
		path,
		testInstallation,
		testMachine,
	); err == nil || !strings.Contains(
		err.Error(),
		"unrecognized non-empty schema",
	) {
		t.Fatalf("foreign database error = %v", err)
	}
}

func TestAuditRejectsSemanticCorruption(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)
	process := Process{
		ProcessID:            "prc_corrupt",
		SupervisorInstanceID: "supervisor-instance-corrupt",
		SupervisorToken:      "supervisor-token-corrupt",
	}
	if err := store.ReserveProcess(ctx, process); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPrepared(ctx, process.ProcessID, process.SupervisorInstanceID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAccepted(ctx, process.ProcessID, process.SupervisorInstanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(
		ctx,
		`UPDATE processes
		 SET phase = 'terminal', action_admission_closed = 1
		 WHERE process_id = ?`,
		process.ProcessID,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Audit(ctx); err == nil ||
		!strings.Contains(err.Error(), "terminal process has no terminal report") {
		t.Fatalf("semantic audit error = %v", err)
	}
}

func acceptedTestProcess(
	t *testing.T,
) (*Store, string, Process, *Supervisor) {
	t.Helper()
	ctx := context.Background()
	store, path := openTestStore(t)
	process := Process{
		ProcessID:            "prc_test",
		SupervisorInstanceID: "supervisor-instance-test",
		SupervisorToken:      "supervisor-token-test",
	}
	if err := store.ReserveProcess(ctx, process); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPrepared(
		ctx,
		process.ProcessID,
		process.SupervisorInstanceID,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAccepted(
		ctx,
		process.ProcessID,
		process.SupervisorInstanceID,
	); err != nil {
		t.Fatal(err)
	}
	supervisor := openTestSupervisor(t, path, process)
	return store, path, process, supervisor
}

func runningTestProcess(
	t *testing.T,
) (*Store, string, Process, *Supervisor) {
	t.Helper()
	store, path, process, supervisor := acceptedTestProcess(t)
	execute, err := supervisor.AuthorizeSpawnOnce(context.Background())
	if err != nil || !execute {
		t.Fatalf("commit test process execution: execute=%t err=%v", execute, err)
	}
	if err := supervisor.RecordSpawned(
		context.Background(),
		"process_group",
		"456",
	); err != nil {
		t.Fatal(err)
	}
	return store, path, process, supervisor
}

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := Open(
		context.Background(),
		path,
		testInstallation,
		testMachine,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func openTestSupervisor(
	t *testing.T,
	path string,
	process Process,
) *Supervisor {
	t.Helper()
	supervisor, err := OpenSupervisor(
		context.Background(),
		path,
		testInstallation,
		testMachine,
		process.ProcessID,
		process.SupervisorInstanceID,
		process.SupervisorToken,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close() })
	return supervisor
}

func processReport(
	processID string,
	kind ReportKind,
	eventType string,
	cursor *int64,
) Report {
	return reportForEvent(processID, "", kind, eventType, cursor)
}

func actionReport(action Action, cursor *int64) Report {
	return actionReportWithType(action, "process_action_applied", cursor)
}

func actionReportWithType(
	action Action,
	eventType string,
	cursor *int64,
) Report {
	return reportForEvent(
		action.ProcessID,
		action.ID,
		ReportActionTerminal,
		eventType,
		cursor,
	)
}

func reportForEvent(
	processID, actionID string,
	kind ReportKind,
	eventType string,
	cursor *int64,
) Report {
	result := map[string]any{
		"ok":          true,
		"output":      "",
		"next_cursor": int64(0),
	}
	if cursor != nil {
		result["next_cursor"] = *cursor
	}
	event := map[string]any{
		"type":       eventType,
		"process_id": processID,
		"result":     result,
	}
	switch kind {
	case ReportProcessStarted:
		event["started_at"] = time.Date(
			2026,
			7,
			27,
			11,
			59,
			59,
			0,
			time.UTC,
		)
		event["observed_at"] = time.Date(
			2026,
			7,
			27,
			12,
			0,
			0,
			0,
			time.UTC,
		)
	case ReportProcessTerminal:
		event["state"] = "exited"
		event["ended_at"] = time.Date(
			2026,
			7,
			27,
			12,
			0,
			1,
			0,
			time.UTC,
		)
	case ReportActionTerminal:
		event["process_action_id"] = actionID
		if eventType != "process_action_applied" {
			event["state_reason_code"] = "test_reason"
		}
	}
	body, err := json.Marshal(event)
	if err != nil {
		panic(err)
	}
	return Report{
		ProcessID: processID,
		ActionID:  actionID,
		Kind:      kind,
		Body:      body,
	}
}
