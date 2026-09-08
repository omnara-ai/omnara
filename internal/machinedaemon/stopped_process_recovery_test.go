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
	"github.com/stretchr/testify/require"
)

func TestStoppedSupervisorRecoveryPreservesDurableOutput(
	t *testing.T,
) {
	t.Parallel()

	client, store, process, outputPath := stoppedRecoveryFixture(t)
	const baseOffset = int64(7)
	const durableOutput = "durable output before supervisor loss"
	require.NoError(t, writeProcessOutputFile(
		outputPath,
		[]byte(durableOutput),
		baseOffset,
	))

	require.NoError(t, client.recoverStoppedReleasedProcess(
		context.Background(),
		process.ProcessID,
		process.SupervisorInstanceID,
		nil,
	))
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
	require.NoError(t, os.WriteFile(outputPath, []byte("not an output buffer"), 0o600))

	require.NoError(t, client.recoverStoppedReleasedProcess(
		context.Background(),
		process.ProcessID,
		process.SupervisorInstanceID,
		nil,
	))
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
	require.NoError(t, os.Remove(filepath.Dir(outputPath)))

	require.NoError(t, client.recoverStoppedReleasedProcess(
		context.Background(),
		process.ProcessID,
		process.SupervisorInstanceID,
		nil,
	))
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
	require.NoError(t, err)
	store, err := statedb.Open(
		ctx,
		machine.StateDBPath(),
		installationID,
		machineID,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	client.state = store

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
		machine.StateDBPath(),
		installationID,
		machineID,
		processID,
		supervisorInstanceID,
		supervisorToken,
	)
	require.NoError(t, err)
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
	require.NoError(t, supervisor.Close())
	require.NoError(t, store.MarkServerReleased(ctx, processID, supervisorInstanceID))

	outputPath, err := machine.OutputBufferPath(processID)
	require.NoError(t, err)
	require.NoError(t, localstore.EnsurePrivateDir(filepath.Dir(outputPath)))
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
	require.NoError(t, json.Unmarshal(report.Body, &event))
	var result struct {
		Output     string `json:"output"`
		Cursor     int64  `json:"cursor"`
		NextCursor int64  `json:"next_cursor"`
		Truncated  bool   `json:"truncated"`
	}
	require.NoError(t, json.Unmarshal(event.Result, &result))
	return event, result
}
