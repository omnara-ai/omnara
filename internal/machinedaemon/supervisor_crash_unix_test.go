//go:build !windows

package machinedaemon

import (
	"context"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
	"github.com/omnara-ai/omnara/internal/processaction"
)

func TestAbruptSupervisorDeathNeverRepeatsCommittedEffects(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	commandDir := t.TempDir()
	markerPath := commandDir + string(os.PathSeparator) + "process-effect"
	syncPath := commandDir + string(os.PathSeparator) + "process-effect-sync"
	if err := syscall.Mkfifo(syncPath, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := newDetachedSupervisorTestFixture(
		t,
		ctx,
		ProcessAssignment{
			ID: "prc_supervisor_sigkill",
			Process: Process{
				Command:       `printf x >> "$MARKER"; printf x > "$SYNC"; sleep 30`,
				ShellSelector: "default",
				Cwd:           commandDir,
				IOMode:        "pipe",
			},
			Env: map[string]string{
				"MARKER": markerPath,
				"SYNC":   syncPath,
			},
		},
	)
	fixture.acceptAndStart(t, ctx)
	startedProcess, found, err := fixture.store.Process(
		ctx,
		fixture.runtime.processID,
	)
	if err != nil || !found {
		t.Fatalf("read spawned process: found=%t err=%v", found, err)
	}
	containmentID, err := strconv.Atoi(startedProcess.ContainmentID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		empty, probeErr := processGroupEmpty(containmentID)
		if probeErr != nil || empty {
			return
		}
		_ = syscall.Kill(-containmentID, syscall.SIGKILL)
	})
	syncFile, err := os.Open(syncPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := syncFile.Read(make([]byte, 1)); err != nil {
		t.Fatalf("process effect rendezvous: %v", err)
	}
	if err := syncFile.Close(); err != nil {
		t.Fatal(err)
	}

	action := ProcessAction{
		ID:         "act_supervisor_sigkill",
		ActionKind: "write",
		Seq:        1,
		Payload: mustOutboxJSON(t, map[string]any{
			"data": strings.Repeat(
				"y",
				processaction.MaxWriteBytes,
			),
		}),
	}
	runner, ok := fixture.runtime.runner.(*ipcProcessRunner)
	if !ok {
		t.Fatalf("runner type = %T", fixture.runtime.runner)
	}
	committed := make(chan string, 1)
	runner.onActionCommitted = func(actionID string) {
		select {
		case committed <- actionID:
		default:
		}
	}
	actionResult := make(chan error, 1)
	go func() {
		actionResult <- fixture.runtime.runner.ApplyOnce(ctx, action)
	}()
	select {
	case actionID := <-committed:
		if actionID != action.ID {
			t.Fatalf("committed action = %q, want %q", actionID, action.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("action %s did not cross its effect boundary", action.ID)
	}

	supervisorPID := fixture.runtime.supervisorPID
	if err := syscall.Kill(supervisorPID, syscall.SIGKILL); err != nil {
		t.Fatalf("kill detached supervisor: %v", err)
	}
	fixture.waitDone(t, 5*time.Second)
	if err := fixture.client.verifyProcessSupervisors(ctx); err == nil {
		t.Fatal("heartbeat treated crashed accepted process as healthy")
	}
	select {
	case err := <-actionResult:
		if err == nil {
			t.Fatal("action interrupted by supervisor death reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("action IPC remained blocked after supervisor death")
	}

	restarted := New(fixture.client.cfg, nil, nil)
	restarted.bootstrap = fixture.client.bootstrap
	defer restarted.closeState()
	startup, err := restarted.scanLocalProcessesForRegistration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(startup.Claims) != 1 ||
		startup.Claims[0].ProcessID != fixture.runtime.processID ||
		startup.Claims[0].SupervisorLive {
		startup.releaseResources()
		t.Fatalf("crashed-supervisor claim = %+v", startup.Claims)
	}
	if err := restarted.applyRegistrationReconciliation(
		ctx,
		&startup,
		DaemonRuntimeReconciliation{
			Processes: []ProcessReconciliationDirective{{
				ProcessID:            fixture.runtime.processID,
				SupervisorInstanceID: fixture.runtime.supervisorInstanceID,
				Disposition:          "release",
				Actions: []ProcessActionReconciliationDirective{{
					ProcessActionID: action.ID,
					Seq:             action.Seq,
					Disposition:     "release",
				}},
			}},
		},
	); err != nil {
		t.Fatal(err)
	}

	process, found, err := fixture.store.Process(
		ctx,
		fixture.runtime.processID,
	)
	if err != nil || !found {
		t.Fatalf("read recovered process: found=%t err=%v", found, err)
	}
	if process.Phase != statedb.ProcessTerminal ||
		!process.ExecCommitted ||
		!process.ServerReleased ||
		process.ResolvedActionSeq != action.Seq {
		t.Fatalf("recovered process state = %+v", process)
	}
	_, terminalEvent := fixture.terminalEvent(t)
	if terminalEvent.State != "unknown" ||
		terminalEvent.StateReasonCode != "local_process_unrecoverable" {
		t.Fatalf("recovered process event = %+v", terminalEvent)
	}
	if _, found, err := fixture.store.Action(
		ctx,
		action.ID,
	); err != nil || found {
		t.Fatalf("released action marker: found=%t err=%v", found, err)
	}
	if _, found, err := fixture.store.ReportBySlot(
		ctx,
		statedb.ReportActionTerminal,
		fixture.runtime.processID,
		action.ID,
	); err != nil || found {
		t.Fatalf("released action report: found=%t err=%v", found, err)
	}
	if contents, err := os.ReadFile(markerPath); err != nil ||
		string(contents) != "x" {
		t.Fatalf(
			"external process effect after recovery = %q, err=%v",
			contents,
			err,
		)
	}

	empty, err := processGroupEmpty(containmentID)
	if err != nil {
		t.Fatal(err)
	}
	if !empty {
		if err := syscall.Kill(-containmentID, syscall.SIGKILL); err != nil &&
			err != syscall.ESRCH {
			t.Fatalf("clean up orphaned process group: %v", err)
		}
		waitForProcessGroupEmpty(t, containmentID, 5*time.Second)
	}
	if err := restarted.recoverStoppedReleasedProcess(
		ctx,
		process.ProcessID,
		process.SupervisorInstanceID,
	); err != nil {
		t.Fatalf("finish recovered process closure: %v", err)
	}
}

func TestNaturalExitWaitsForReconciliationFence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	commandDir := t.TempDir()
	releasePath := commandDir + string(os.PathSeparator) + "release"
	if err := syscall.Mkfifo(releasePath, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := newDetachedSupervisorTestFixture(
		t,
		ctx,
		ProcessAssignment{
			ID: "prc_natural_exit_fence",
			Process: Process{
				Command:       `read -r line < "$RELEASE"`,
				ShellSelector: "default",
				Cwd:           commandDir,
				IOMode:        "pipe",
			},
			Env: map[string]string{
				"RELEASE": releasePath,
			},
		},
	)
	fixture.acceptAndStart(t, ctx)
	runner, ok := fixture.runtime.runner.(*ipcProcessRunner)
	if !ok {
		t.Fatalf("runner type = %T", fixture.runtime.runner)
	}
	runner.notifyFenceParked = true
	if err := runner.BeginReconciliation(ctx); err != nil {
		t.Fatal(err)
	}
	fenceOpen := true
	defer func() {
		if fenceOpen {
			_ = runner.EndReconciliation()
		}
	}()
	release, err := os.OpenFile(releasePath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := release.Write([]byte("go\n")); err != nil {
		t.Fatal(err)
	}
	if err := release.Close(); err != nil {
		t.Fatal(err)
	}
	awaitFenceWriterParked(t, runner, 5*time.Second)

	process, found, err := fixture.store.Process(
		ctx,
		fixture.runtime.processID,
	)
	if err != nil || !found {
		t.Fatalf("read process under reconciliation fence: found=%t err=%v", found, err)
	}
	if process.Phase != statedb.ProcessAccepted ||
		process.ActionAdmissionClosed ||
		process.LocalClosed {
		t.Fatalf("natural exit crossed reconciliation fence: %+v", process)
	}
	select {
	case <-fixture.runtime.runner.Done():
		t.Fatal("supervisor exited while reconciliation still held its write fence")
	default:
	}

	if err := runner.EndReconciliation(); err != nil {
		t.Fatal(err)
	}
	fenceOpen = false
	process = fixture.waitClosed(t, 5*time.Second)
	if process.Phase != statedb.ProcessTerminal ||
		!process.ActionAdmissionClosed ||
		!process.ContainmentEmpty {
		t.Fatalf("natural exit after reconciliation fence = %+v", process)
	}
}

func awaitFenceWriterParked(
	t *testing.T,
	runner *ipcProcessRunner,
	timeout time.Duration,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var frame runnerResponse
	if err := runner.reconciliation.frames.read(ctx, &frame); err != nil {
		t.Fatalf("wait for fence writer: %v", err)
	}
	if frame.Stage != runnerStageFenceWriterParked {
		t.Fatalf("fence frame = %+v", frame)
	}
}

func waitForProcessGroupEmpty(
	t *testing.T,
	groupID int,
	timeout time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		empty, err := processGroupEmpty(groupID)
		if err != nil {
			t.Fatal(err)
		}
		if empty {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process group %d remained active", groupID)
		}
		time.Sleep(10 * time.Millisecond) //nolint:omnaralint // The OS exposes no exit event for a non-child process group.
	}
}
