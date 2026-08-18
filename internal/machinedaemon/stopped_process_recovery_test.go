package machinedaemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
)

func TestStoppedSupervisorRecoveryPreservesDurableOutput(
	t *testing.T,
) {
	t.Parallel()

	client, store, process, outputPath := stoppedRecoveryFixture(t)
	const baseOffset = int64(7)
	const durableOutput = "durable output before supervisor loss"
	if err := writeProcessOutputFile(
		outputPath,
		[]byte(durableOutput),
		baseOffset,
	); err != nil {
		t.Fatal(err)
	}

	if err := client.recoverStoppedReleasedProcess(
		context.Background(),
		process.ProcessID,
		process.SupervisorInstanceID,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	event, result := recoveredTerminalResult(t, store, process.ProcessID)
	if event.StateReasonCode != "local_process_unrecoverable" {
		t.Fatalf("terminal reason = %q", event.StateReasonCode)
	}
	if !event.EndedAt.IsZero() {
		t.Fatalf("unobserved physical end was reported as %s", event.EndedAt)
	}
	if result.Output != durableOutput ||
		result.Cursor != baseOffset ||
		result.NextCursor != baseOffset+int64(len(durableOutput)) ||
		!result.Truncated {
		t.Fatalf("recovered terminal observation = %+v", result)
	}
}

func TestStoppedSupervisorRecoveryMakesCorruptOutputExplicit(
	t *testing.T,
) {
	t.Parallel()

	client, store, process, outputPath := stoppedRecoveryFixture(t)
	if err := os.WriteFile(outputPath, []byte("not an output buffer"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := client.recoverStoppedReleasedProcess(
		context.Background(),
		process.ProcessID,
		process.SupervisorInstanceID,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	event, result := recoveredTerminalResult(t, store, process.ProcessID)
	if event.StateReasonCode != "local_process_and_output_unrecoverable" {
		t.Fatalf("terminal reason = %q", event.StateReasonCode)
	}
	if result.Output != "" || result.Cursor != 0 ||
		result.NextCursor != 0 || !result.Truncated {
		t.Fatalf("corrupt output observation = %+v", result)
	}
}

func TestStoppedSupervisorRecoveryMakesMissingOutputExplicit(
	t *testing.T,
) {
	t.Parallel()

	client, store, process, outputPath := stoppedRecoveryFixture(t)
	if err := os.Remove(filepath.Dir(outputPath)); err != nil {
		t.Fatal(err)
	}

	if err := client.recoverStoppedReleasedProcess(
		context.Background(),
		process.ProcessID,
		process.SupervisorInstanceID,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	event, result := recoveredTerminalResult(t, store, process.ProcessID)
	if event.StateReasonCode != "local_process_and_output_unrecoverable" {
		t.Fatalf("terminal reason = %q", event.StateReasonCode)
	}
	if result.Output != "" || result.Cursor != 0 ||
		result.NextCursor != 0 || !result.Truncated {
		t.Fatalf("missing output observation = %+v", result)
	}
}

func stoppedRecoveryFixture(
	t *testing.T,
) (*Client, *statedb.Store, statedb.Process, string) {
	t.Helper()

	ctx := context.Background()
	const (
		installationID       = "ins_stopped_recovery"
		machineID            = "mch_stopped_recovery"
		processID            = "prc_stopped_recovery"
		supervisorInstanceID = "supervisor-instance-stopped-recovery"
		supervisorToken      = "supervisor-token-stopped-recovery"
	)
	client := New(Config{OmnaraHome: t.TempDir()}, nil, nil)
	client.bootstrap = daemonBootstrap{
		InstallationID: installationID,
		MachineID:      machineID,
	}
	machine, err := client.machineStore()
	if err != nil {
		t.Fatal(err)
	}
	store, err := statedb.Open(
		ctx,
		machine.StateDBPath(),
		installationID,
		machineID,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	client.state = store

	process := statedb.Process{
		ProcessID:            processID,
		SupervisorInstanceID: supervisorInstanceID,
		SupervisorToken:      supervisorToken,
	}
	if err := store.ReserveProcess(ctx, process); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPrepared(ctx, processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAccepted(ctx, processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}
	supervisor, err := statedb.OpenSupervisor(
		ctx,
		machine.StateDBPath(),
		installationID,
		machineID,
		processID,
		supervisorInstanceID,
		supervisorToken,
	)
	if err != nil {
		t.Fatal(err)
	}
	execute, err := supervisor.AuthorizeSpawnOnce(ctx)
	if err != nil || !execute {
		_ = supervisor.Close()
		t.Fatalf("commit execution: execute=%t err=%v", execute, err)
	}
	if err := supervisor.RecordSpawned(
		ctx,
		"process_group",
		"123",
	); err != nil {
		_ = supervisor.Close()
		t.Fatal(err)
	}
	if err := supervisor.MarkContainmentEmpty(ctx); err != nil {
		_ = supervisor.Close()
		t.Fatal(err)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkServerReleased(ctx, processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}

	outputPath, err := machine.OutputBufferPath(processID)
	if err != nil {
		t.Fatal(err)
	}
	if err := localstore.EnsurePrivateDir(filepath.Dir(outputPath)); err != nil {
		t.Fatal(err)
	}
	return &client, store, process, outputPath
}

func recoveredTerminalResult(
	t *testing.T,
	store *statedb.Store,
	processID string,
) (
	struct {
		StateReasonCode string          `json:"state_reason_code"`
		Result          json.RawMessage `json:"result"`
		EndedAt         time.Time       `json:"ended_at"`
	},
	struct {
		Output     string `json:"output"`
		Cursor     int64  `json:"cursor"`
		NextCursor int64  `json:"next_cursor"`
		Truncated  bool   `json:"truncated"`
	},
) {
	t.Helper()

	report, found, err := store.ReportBySlot(
		context.Background(),
		statedb.ReportProcessTerminal,
		processID,
		"",
	)
	if err != nil || !found {
		t.Fatalf("recovered terminal report: found=%t err=%v", found, err)
	}
	var event struct {
		StateReasonCode string          `json:"state_reason_code"`
		Result          json.RawMessage `json:"result"`
		EndedAt         time.Time       `json:"ended_at"`
	}
	if err := json.Unmarshal(report.Body, &event); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Output     string `json:"output"`
		Cursor     int64  `json:"cursor"`
		NextCursor int64  `json:"next_cursor"`
		Truncated  bool   `json:"truncated"`
	}
	if err := json.Unmarshal(event.Result, &result); err != nil {
		t.Fatal(err)
	}
	return event, result
}
