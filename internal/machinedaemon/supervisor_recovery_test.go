package machinedaemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb/statedbtest"
)

func TestAuthorityLossDeletesStateAndReportsUnresolvedContainment(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	var logs bytes.Buffer
	client := New(
		Config{OmnaraHome: t.TempDir()},
		nil,
		slog.New(slog.NewTextHandler(&logs, nil)),
	)
	client.bootstrap = daemonBootstrap{
		InstallationID: "ins_authority_loss",
		MachineID:      "mch_authority_loss",
	}
	const (
		processID            = "prc_authority_loss"
		supervisorInstanceID = "supervisor-instance-authority-loss"
		supervisorToken      = "supervisor-token-authority-loss"
	)
	store, err := client.stateStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveProcess(
		ctx,
		statedb.Process{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
			SupervisorToken:      supervisorToken,
		}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPrepared(ctx, processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAccepted(ctx, processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}
	machine, err := client.machineStore()
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := statedb.OpenSupervisor(
		ctx,
		machine.StateDBPath(),
		client.bootstrap.InstallationID,
		client.bootstrap.MachineID,
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
		t.Fatalf("authorize spawn: execute=%t err=%v", execute, err)
	}
	if err := supervisor.RecordSpawned(
		ctx,
		processContainmentKind,
		"4242",
	); err != nil {
		_ = supervisor.Close()
		t.Fatal(err)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	lockPath, err := machine.LifetimeLockPath(processID)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := localstore.TryAcquireLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}

	client.shutdownAfterAuthorityLoss(ctx, "machine_deleted")

	if _, err := os.Stat(machine.MachineDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("decommissioned machine state still exists: %v", err)
	}
	if got := logs.String(); !strings.Contains(
		got,
		"could not confirm agent process prc_authority_loss stopped",
	) {
		t.Fatalf("authority-loss warning = %q", got)
	}
}

func TestStopLocalMachineRejectsMissingState(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	machine, err := localstore.Machine(home, "ins_missing_state", "mch_missing_state")
	if err != nil {
		t.Fatal(err)
	}
	lockPath, err := machine.LifetimeLockPath("prc_missing_state")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := localstore.TryAcquireLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	err = StopLocalMachine(
		context.Background(),
		home,
		"ins_missing_state",
		"mch_missing_state",
		"daemon_uninstalled",
	)
	if err == nil || !strings.Contains(err.Error(), "local process state is missing") {
		t.Fatalf("stop error = %v", err)
	}
}

func TestStopLocalMachinePreservesStoppedState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	home := t.TempDir()
	client := New(Config{
		OmnaraHome:             home,
		ExpectedInstallationID: "ins_stopped",
		ExpectedMachineID:      "mch_stopped",
	}, nil, nil)
	_, err := client.stateStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	machine, err := client.machineStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.closeState(); err != nil {
		t.Fatal(err)
	}
	if err := StopLocalMachine(
		ctx,
		home,
		"ins_stopped",
		"mch_stopped",
		"daemon_uninstalled",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(machine.StateDBPath()); err != nil {
		t.Fatalf("stopped machine state was removed: %v", err)
	}
}

func TestDeleteStoppedLocalMachinePreservesStateWhileSupervisorLives(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := New(Config{
		OmnaraHome:             t.TempDir(),
		ExpectedInstallationID: "ins_live",
		ExpectedMachineID:      "mch_live",
	}, nil, nil)
	if _, err := client.stateStore(ctx); err != nil {
		t.Fatal(err)
	}
	machine, err := client.machineStore()
	if err != nil {
		t.Fatal(err)
	}
	lockPath, err := machine.LifetimeLockPath("prc_live")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := localstore.TryAcquireLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := client.deleteStoppedLocalMachine(canceled, machine); err == nil {
		t.Fatal("delete stopped local machine succeeded")
	}
	if _, err := os.Stat(machine.StateDBPath()); err != nil {
		t.Fatalf("live machine state was removed: %v", err)
	}
}

func TestVerifyProcessSupervisorsUsesLifetimeLock(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := New(Config{OmnaraHome: t.TempDir()}, nil, nil)
	client.bootstrap = daemonBootstrap{
		InstallationID: "ins_supervisor_recovery",
		MachineID:      "mch_supervisor_recovery",
	}
	runtime := &processRuntime{
		processID:            "prc_supervisor_recovery",
		supervisorInstanceID: "supervisor-instance-supervisor-recovery",
	}
	store, err := client.stateStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer client.closeState()
	if err := store.ReserveProcess(
		ctx,
		statedb.Process{
			ProcessID:            runtime.processID,
			SupervisorInstanceID: runtime.supervisorInstanceID,
			SupervisorToken:      "supervisor-token-supervisor-recovery",
		}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPrepared(
		ctx,
		runtime.processID,
		runtime.supervisorInstanceID,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAccepted(
		ctx,
		runtime.processID,
		runtime.supervisorInstanceID,
	); err != nil {
		t.Fatal(err)
	}
	client.addProcess(runtime)

	machine, err := client.machineStore()
	if err != nil {
		t.Fatal(err)
	}
	lockPath, err := machine.LifetimeLockPath(runtime.processID)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := localstore.TryAcquireLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.verifyProcessSupervisors(ctx); err != nil {
		t.Fatalf("held supervisor lifetime lock: %v", err)
	}

	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if err := client.verifyProcessSupervisors(ctx); err == nil {
		t.Fatal("released supervisor lifetime lock was treated as live")
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := client.verifyProcessSupervisors(ctx); err == nil {
		t.Fatal("missing supervisor lifetime lock was treated as live")
	}

	client.removeProcessInstance(runtime.processID, runtime.supervisorInstanceID)
	if err := client.verifyProcessSupervisors(ctx); err == nil {
		t.Fatal("accepted durable process state disappeared with its routing cache")
	}

	supervisor, err := statedb.OpenSupervisor(
		ctx,
		machine.StateDBPath(),
		client.bootstrap.InstallationID,
		client.bootstrap.MachineID,
		runtime.processID,
		runtime.supervisorInstanceID,
		"supervisor-token-supervisor-recovery",
	)
	if err != nil {
		t.Fatal(err)
	}
	execute, err := supervisor.AuthorizeSpawnOnce(ctx)
	if err != nil || !execute {
		_ = supervisor.Close()
		t.Fatalf("commit orphaned execution: execute=%t err=%v", execute, err)
	}
	if err := supervisor.RecordSpawned(
		ctx,
		"process_group",
		"424242",
	); err != nil {
		_ = supervisor.Close()
		t.Fatal(err)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	orphanLock, err := localstore.TryAcquireLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := orphanLock.Release(); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkServerReleased(
		ctx,
		runtime.processID,
		runtime.supervisorInstanceID,
	); err != nil {
		t.Fatal(err)
	}
	client.addProcess(runtime)
	if err := client.verifyProcessSupervisors(ctx); err != nil {
		t.Fatalf("released process state poisoned daemon health: %v", err)
	}
	if _, found := client.localProcess(runtime.processID); found {
		t.Fatal("released process state retained stale action routing")
	}
}

func TestRejectedPreparationCleanupRetriesWithoutBlockingSleep(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const (
		processID            = "prc_rejected_cleanup_retry"
		supervisorInstanceID = "supervisor-instance-rejected-cleanup-retry"
	)
	home := t.TempDir()
	client := New(Config{OmnaraHome: home}, nil, nil)
	client.bootstrap = daemonBootstrap{
		InstallationID: "ins_rejected_cleanup_retry",
		MachineID:      "mch_rejected_cleanup_retry",
	}
	defer client.closeState()
	store, err := client.stateStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveProcess(ctx, statedb.Process{
		ProcessID:            processID,
		SupervisorInstanceID: supervisorInstanceID,
		SupervisorToken:      "supervisor-token-rejected-cleanup-retry",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPrepared(ctx, processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}
	machine, err := client.machineStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := localstore.EnsurePrivateDir(machine.RunDir()); err != nil {
		t.Fatal(err)
	}
	if err := localstore.EnsurePrivateDir(machine.ProcessesDir()); err != nil {
		t.Fatal(err)
	}
	processDir, err := machine.ProcessDir(processID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(processDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(processDir, "partial"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	lockPath, err := machine.LifetimeLockPath(processID)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := localstore.TryAcquireLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	startup := localStartupState{
		Claims: []ProcessReconciliationClaim{{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
		}},
		Runners:       make(map[string]*processRuntime),
		ForcedReports: make(map[string]struct{}),
		stoppedLocks: map[string]*localstore.Lock{
			processID: lock,
		},
		fencedRunners: make(map[string]*ipcProcessRunner),
	}
	if err := statedbtest.SetProcessDeleteFailure(
		ctx,
		machine.StateDBPath(),
		true,
	); err != nil {
		t.Fatal(err)
	}
	if err := client.applyRegistrationReconciliation(
		ctx,
		&startup,
		DaemonRuntimeReconciliation{
			Processes: []ProcessReconciliationDirective{{
				ProcessID:            processID,
				SupervisorInstanceID: supervisorInstanceID,
				Disposition:          daemonprotocol.ProcessDispositionClosePreparation,
			}},
		},
	); err != nil {
		t.Fatalf("registration cleanup failure = %v", err)
	}
	runtime, found := client.localProcess(processID)
	if !found || !runtime.cleanupOnly {
		t.Fatalf("pending cleanup runtime = %+v, found=%t", runtime, found)
	}
	if _, err := os.Stat(processDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("process artifacts remained before database deletion: %v", err)
	}
	if _, found, err := store.Process(ctx, processID); err != nil || !found {
		t.Fatalf("retained process row: found=%t err=%v", found, err)
	}
	retainedLock, err := localstore.TryAcquireExistingLock(lockPath)
	if err != nil {
		t.Fatalf("retained cleanup lock = %v", err)
	}
	if err := retainedLock.Release(); err != nil {
		t.Fatal(err)
	}
	if err := client.verifyProcessSupervisors(ctx); err != nil {
		t.Fatalf("pending cleanup poisoned heartbeat: %v", err)
	}
	if !client.daemonIdle(ctx) {
		t.Fatal("pending cleanup prevented daemon idleness")
	}
	if err := statedbtest.SetProcessDeleteFailure(
		ctx,
		machine.StateDBPath(),
		false,
	); err != nil {
		t.Fatal(err)
	}
	if err := client.closeState(); err != nil {
		t.Fatal(err)
	}

	restarted := New(Config{OmnaraHome: home}, nil, nil)
	restarted.bootstrap = client.bootstrap
	defer restarted.closeState()
	restartedStartup, err := restarted.scanLocalProcessesForRegistrationOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(restartedStartup.Claims) != 1 ||
		restartedStartup.Claims[0].ProcessID != processID ||
		restartedStartup.stoppedLocks[processID] == nil {
		t.Fatalf("restarted cleanup claim = %+v", restartedStartup.Claims)
	}
	if err := restarted.applyRegistrationReconciliation(
		ctx,
		&restartedStartup,
		DaemonRuntimeReconciliation{
			Processes: []ProcessReconciliationDirective{{
				ProcessID:            processID,
				SupervisorInstanceID: supervisorInstanceID,
				Disposition:          daemonprotocol.ProcessDispositionClosePreparation,
			}},
		},
	); err != nil {
		t.Fatal(err)
	}
	reopened, err := restarted.stateStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := reopened.Process(ctx, processID); err != nil || found {
		t.Fatalf("finalized process row: found=%t err=%v", found, err)
	}
	if _, err := os.Stat(processDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("finalized process artifacts: %v", err)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("finalized lifetime lock: %v", err)
	}
	if _, found := restarted.localProcess(processID); found {
		t.Fatal("finalized cleanup remained in memory")
	}
}

func TestRejectedPreparationCleanupFailureDoesNotBlockFinalization(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const (
		rejectedProcessID             = "prc_rejected_cleanup_failure"
		rejectedSupervisorInstanceID  = "supervisor-instance-rejected-cleanup"
		cleanupSupervisorInstanceID   = "supervisor-instance-stale-cleanup"
		finalizedProcessID            = "prc_unrelated_finalization"
		finalizedSupervisorInstanceID = "supervisor-instance-unrelated-finalization"
	)
	var logs bytes.Buffer
	client := New(
		Config{OmnaraHome: t.TempDir()},
		nil,
		slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})),
	)
	client.bootstrap = daemonBootstrap{
		InstallationID: "ins_cleanup_failure_isolation",
		MachineID:      "mch_cleanup_failure_isolation",
	}
	defer client.closeState()
	store, err := client.stateStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	machine, err := client.machineStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := localstore.EnsurePrivateDir(machine.RunDir()); err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveProcess(ctx, statedb.Process{
		ProcessID:            rejectedProcessID,
		SupervisorInstanceID: rejectedSupervisorInstanceID,
		SupervisorToken:      "supervisor-token-rejected-cleanup",
	}); err != nil {
		t.Fatal(err)
	}
	client.addProcess(&processRuntime{
		processID:            rejectedProcessID,
		supervisorInstanceID: cleanupSupervisorInstanceID,
		cleanupOnly:          true,
	})

	if err := store.ReserveProcess(ctx, statedb.Process{
		ProcessID:            finalizedProcessID,
		SupervisorInstanceID: finalizedSupervisorInstanceID,
		SupervisorToken:      "supervisor-token-unrelated-finalization",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPrepared(
		ctx,
		finalizedProcessID,
		finalizedSupervisorInstanceID,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAccepted(
		ctx,
		finalizedProcessID,
		finalizedSupervisorInstanceID,
	); err != nil {
		t.Fatal(err)
	}
	supervisor, err := statedb.OpenSupervisor(
		ctx,
		machine.StateDBPath(),
		client.bootstrap.InstallationID,
		client.bootstrap.MachineID,
		finalizedProcessID,
		finalizedSupervisorInstanceID,
		"supervisor-token-unrelated-finalization",
	)
	if err != nil {
		t.Fatal(err)
	}
	terminalBody, err := json.Marshal(daemonReportedEvent{
		Type:      daemonprotocol.EventProcessFinished,
		ProcessID: finalizedProcessID,
		State:     "exited",
		EndedAt:   time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := supervisor.FreezeTerminalReport(ctx, statedb.Report{
		ProcessID: finalizedProcessID,
		Kind:      statedb.ReportProcessTerminal,
		Body:      terminalBody,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.MarkContainmentEmpty(ctx); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.MarkLocalClosed(ctx); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.AcknowledgeReport(ctx, terminal.ID); err != nil {
		t.Fatal(err)
	}

	if err := client.finalizeReleasedProcesses(ctx); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Process(ctx, finalizedProcessID); err != nil || found {
		t.Fatalf("unrelated process finalization: found=%t err=%v", found, err)
	}
	if _, found, err := store.Process(ctx, rejectedProcessID); err != nil || !found {
		t.Fatalf("failed cleanup process state: found=%t err=%v", found, err)
	}
	runtime, found := client.localProcess(rejectedProcessID)
	if !found || !runtime.cleanupOnly ||
		runtime.supervisorInstanceID != cleanupSupervisorInstanceID {
		t.Fatalf("retained cleanup runtime = %+v, found=%t", runtime, found)
	}
	if !client.daemonIdle(ctx) {
		t.Fatal("failed cleanup prevented daemon idleness")
	}
	if got := logs.String(); !strings.Contains(got, "rejected process preparation cleanup failed") ||
		!strings.Contains(got, rejectedProcessID) {
		t.Fatalf("cleanup failure log = %q", got)
	}
}

func TestRunnerTerminalClosureRetriesUntilAdmittedActionHasEvidence(
	t *testing.T,
) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	const (
		installationID       = "ins_terminal_action_race"
		machineID            = "mch_terminal_action_race"
		processID            = "prc_terminal_action_race"
		supervisorInstanceID = "supervisor-instance-terminal-action-race"
		supervisorToken      = "supervisor-token-terminal-action-race"
		actionID             = "act_terminal_action_race"
	)
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := statedb.Open(
		ctx,
		path,
		installationID,
		machineID,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
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
		path,
		installationID,
		machineID,
		processID,
		supervisorInstanceID,
		supervisorToken,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	execute, err := supervisor.AuthorizeSpawnOnce(ctx)
	if err != nil || !execute {
		t.Fatalf("commit execution: execute=%t err=%v", execute, err)
	}
	if err := supervisor.RecordSpawned(
		ctx,
		"process_group",
		"123",
	); err != nil {
		t.Fatal(err)
	}
	action := statedb.Action{
		ID:        actionID,
		ProcessID: processID,
		Kind:      "write",
		Seq:       1,
	}
	decision, _, err := supervisor.ApplyOnce(ctx, action)
	if err != nil || decision != statedb.ApplyExecute {
		t.Fatalf("commit action effect: decision=%v err=%v", decision, err)
	}

	endedAt := time.Now().UTC()
	terminalBody, err := json.Marshal(daemonReportedEvent{
		Type:      "process_finished",
		ProcessID: processID,
		State:     "exited",
		EndedAt:   endedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.FreezeTerminalReport(
		ctx,
		statedb.Report{
			ProcessID: processID,
			Kind:      statedb.ReportProcessTerminal,
			Body:      terminalBody,
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.MarkContainmentEmpty(ctx); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.MarkLocalClosed(
		ctx,
	); !errors.Is(err, statedb.ErrClosureBlocked) {
		t.Fatalf("closure before action evidence error = %v", err)
	}

	shutdown := make(chan struct{})
	var shutdownOnce sync.Once
	state := &runnerServerState{
		processState: supervisor,
		shutdown: func() {
			shutdownOnce.Do(func() { close(shutdown) })
		},
	}
	closureDone := make(chan error, 1)
	go func() {
		err := retrySupervisorStateWrite(ctx, func() error {
			return supervisor.MarkLocalClosed(ctx)
		})
		if err == nil {
			state.markLocallyClosed()
		}
		closureDone <- err
	}()

	select {
	case <-shutdown:
		t.Fatal("supervisor closed before admitted action evidence was durable")
	case <-time.After(100 * time.Millisecond):
	}

	actionBody, err := json.Marshal(daemonReportedEvent{
		Type:            "process_action_applied",
		ProcessID:       processID,
		ProcessActionID: actionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.FreezeActionReport(
		ctx,
		actionID,
		statedb.Report{
			ProcessID: processID,
			ActionID:  actionID,
			Kind:      statedb.ReportActionTerminal,
			Body:      actionBody,
		},
	); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-closureDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-shutdown:
	default:
		t.Fatal("supervisor did not shut down after action evidence became durable")
	}
	stored, found, err := store.Process(ctx, processID)
	if err != nil || !found {
		t.Fatalf("read closed process state: found=%t err=%v", found, err)
	}
	if !stored.LocalClosed {
		t.Fatal("terminal process state remained open after action evidence was durable")
	}
}

func TestFinalizationResumesAfterArtifactsWereAlreadyRemoved(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const (
		installationID       = "ins_resumable_finalization"
		machineID            = "mch_resumable_finalization"
		processID            = "prc_resumable_finalization"
		supervisorInstanceID = "supervisor-instance-resumable-finalization"
		supervisorToken      = "supervisor-token-resumable-finalization"
	)
	client := New(Config{OmnaraHome: t.TempDir()}, nil, nil)
	client.bootstrap = daemonBootstrap{
		InstallationID: installationID,
		MachineID:      machineID,
	}
	store, err := client.stateStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer client.closeState()
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
	machine, err := client.machineStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := localstore.EnsurePrivateDir(machine.RunDir()); err != nil {
		t.Fatal(err)
	}
	processDir, err := machine.ProcessDir(processID)
	if err != nil {
		t.Fatal(err)
	}
	if err := localstore.EnsurePrivateDir(processDir); err != nil {
		t.Fatal(err)
	}
	outputPath, err := machine.OutputBufferPath(processID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outputPath, []byte("durable output"), 0o600); err != nil {
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
		t.Fatalf("commit execution: execute=%t err=%v", execute, err)
	}
	if err := supervisor.RecordSpawned(
		ctx,
		"process_group",
		"123",
	); err != nil {
		t.Fatal(err)
	}
	terminalBody, err := json.Marshal(daemonReportedEvent{
		Type:      "process_finished",
		ProcessID: processID,
		State:     "exited",
		EndedAt:   time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := supervisor.FreezeTerminalReport(
		ctx,
		statedb.Report{
			ProcessID: processID,
			Kind:      statedb.ReportProcessTerminal,
			Body:      terminalBody,
		},
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
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.AcknowledgeReport(
		ctx,
		terminal.ID,
	); err != nil {
		t.Fatal(err)
	}

	if err := client.releaseProcessRuntimeArtifacts(processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}
	if output, err := os.ReadFile(outputPath); err != nil ||
		string(output) != "durable output" {
		t.Fatalf("retained process output = %q, err=%v", output, err)
	}
	if _, found, err := store.Process(ctx, processID); err != nil || !found {
		t.Fatalf("closed process state before resume: found=%t err=%v", found, err)
	}
	client.addProcess(&processRuntime{
		processID:            processID,
		supervisorInstanceID: supervisorInstanceID,
		runner:               &ipcProcessRunner{},
	})
	if err := client.verifyProcessSupervisors(ctx); err != nil {
		t.Fatalf("locally closed process state failed heartbeat verification: %v", err)
	}
	if _, found := client.localProcess(processID); found {
		t.Fatal("locally closed process state retained stale action routing")
	}
	client.addProcess(&processRuntime{
		processID:            processID,
		supervisorInstanceID: supervisorInstanceID,
		runner:               &ipcProcessRunner{},
	})

	if err := client.finalizeReleasedProcesses(ctx); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Process(ctx, processID); err != nil || found {
		t.Fatalf("process state after resumed finalization: found=%t err=%v", found, err)
	}
	if _, found := client.localProcess(processID); found {
		t.Fatal("finalized process state left a stale in-memory process")
	}
	if err := client.verifyProcessSupervisors(ctx); err != nil {
		t.Fatalf("finalized process state poisoned later heartbeats: %v", err)
	}
}

func TestProcessCacheRemovalCannotDeleteReplacementSupervisorInstanceID(t *testing.T) {
	t.Parallel()

	client := New(Config{}, nil, nil)
	replacement := &processRuntime{
		processID:            "prc_replaced_supervisor_instance",
		supervisorInstanceID: "supervisor-instance-new",
	}
	client.addProcess(replacement)
	client.removeProcessInstance(
		replacement.processID,
		"supervisor-instance-old",
	)

	current, found := client.localProcess(replacement.processID)
	if !found || current != replacement {
		t.Fatal("old supervisor instance cleanup removed the replacement runtime")
	}
}

func TestCleanupOnlyCannotReplaceLiveSupervisorInstance(t *testing.T) {
	t.Parallel()

	client := New(Config{}, nil, nil)
	cleanup := &processRuntime{
		processID:            "prc_cleanup_before_replacement",
		supervisorInstanceID: "supervisor-instance-old",
		cleanupOnly:          true,
	}
	replacement := &processRuntime{
		processID:            cleanup.processID,
		supervisorInstanceID: "supervisor-instance-new",
	}
	client.addProcess(cleanup)
	client.addProcess(replacement)
	client.addProcess(cleanup)

	current, found := client.localProcess(cleanup.processID)
	if !found || current != replacement {
		t.Fatal("stale cleanup replaced the live supervisor runtime")
	}
}

func TestRegistrationPublishesProcessClaimsAsCompleteCache(t *testing.T) {
	t.Parallel()

	client := New(Config{}, nil, nil)
	client.addProcess(&processRuntime{
		processID:            "prc_stale_before_registration",
		supervisorInstanceID: "supervisor-instance-stale",
	})
	startup := localStartupState{
		Runners:       make(map[string]*processRuntime),
		ForcedReports: make(map[string]struct{}),
		stoppedLocks:  make(map[string]*localstore.Lock),
		fencedRunners: make(map[string]*ipcProcessRunner),
	}
	if err := client.applyRegistrationReconciliation(
		context.Background(),
		&startup,
		DaemonRuntimeReconciliation{},
	); err != nil {
		t.Fatal(err)
	}
	client.processMu.RLock()
	cached := len(client.processes)
	client.processMu.RUnlock()
	if cached != 0 {
		t.Fatalf("registration retained %d stale cached processes", cached)
	}
}

func TestRegistrationReleasesNeverReceivedActionBehindPendingEvidence(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	const (
		installationID       = "ins_registration_missing_action"
		machineID            = "mch_registration_missing_action"
		processID            = "prc_registration_missing_action"
		supervisorInstanceID = "supervisor-instance-registration-missing-action"
		supervisorToken      = "supervisor-token-registration-missing-action"
		firstActionID        = "act_registration_pending"
		secondActionID       = "act_registration_never_received"
	)
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := statedb.Open(
		ctx,
		path,
		installationID,
		machineID,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
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
		path,
		installationID,
		machineID,
		processID,
		supervisorInstanceID,
		supervisorToken,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	if execute, err := supervisor.AuthorizeSpawnOnce(
		ctx,
	); err != nil || !execute {
		t.Fatalf("commit execution: execute=%t err=%v", execute, err)
	}
	if err := supervisor.RecordSpawned(
		ctx,
		"process_group",
		"123",
	); err != nil {
		t.Fatal(err)
	}
	first := statedb.Action{
		ID:        firstActionID,
		ProcessID: processID,
		Kind:      "write",
		Seq:       1,
	}
	if decision, _, err := supervisor.ApplyOnce(
		ctx,
		first,
	); err != nil || decision != statedb.ApplyExecute {
		t.Fatalf("apply predecessor: decision=%v err=%v", decision, err)
	}
	body, err := json.Marshal(daemonReportedEvent{
		Type:            "process_action_applied",
		ProcessID:       processID,
		ProcessActionID: firstActionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := supervisor.FreezeActionReport(
		ctx,
		firstActionID,
		statedb.Report{
			ProcessID: processID,
			ActionID:  firstActionID,
			Kind:      statedb.ReportActionTerminal,
			Body:      body,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	client := New(Config{}, nil, nil)
	client.state = store
	startup := localStartupState{
		Claims: []ProcessReconciliationClaim{{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
			Actions: []ProcessActionReconciliationClaim{{
				ProcessActionID: firstActionID,
				ActionKind:      "write",
				Seq:             1,
				Position:        "terminal",
			}},
		}},
		Runners:       make(map[string]*processRuntime),
		ForcedReports: make(map[string]struct{}),
	}
	if err := client.applyRegistrationReconciliation(
		ctx,
		&startup,
		DaemonRuntimeReconciliation{
			Processes: []ProcessReconciliationDirective{{
				ProcessID:            processID,
				SupervisorInstanceID: supervisorInstanceID,
				Disposition:          "retain",
				Actions: []ProcessActionReconciliationDirective{
					{
						ProcessActionID: firstActionID,
						ActionKind:      "write",
						Seq:             1,
						Disposition:     "settle",
					},
					{
						ProcessActionID: secondActionID,
						ActionKind:      "write",
						Seq:             2,
						Disposition:     "release",
					},
				},
			}},
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, forced := startup.ForcedReports[report.ID]; !forced {
		t.Fatal("registration did not force the pending predecessor report")
	}
	stored, found, err := store.Process(ctx, processID)
	if err != nil || !found {
		t.Fatalf("read process: found=%t err=%v", found, err)
	}
	if stored.ResolvedActionSeq != 0 {
		t.Fatalf(
			"never-received action advanced frontier to %d",
			stored.ResolvedActionSeq,
		)
	}
	if storedReport, found, err := store.ReportBySlot(
		ctx,
		statedb.ReportActionTerminal,
		processID,
		firstActionID,
	); err != nil || !found || storedReport.ID != report.ID {
		t.Fatalf(
			"predecessor report after registration: found=%t report=%+v err=%v",
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
	stored, found, err = store.Process(ctx, processID)
	if err != nil || !found {
		t.Fatalf("read acknowledged process: found=%t err=%v", found, err)
	}
	if stored.ResolvedActionSeq != first.Seq {
		t.Fatalf(
			"frontier after predecessor acknowledgement = %d, want %d",
			stored.ResolvedActionSeq,
			first.Seq,
		)
	}
}

func TestExecutionBoundaryPersistenceFailurePreventsSpawn(t *testing.T) {
	ctx := context.Background()
	bootstrap, store, supervisor := acceptedRunnerServerState(t)

	commandDir := t.TempDir()
	markerPath := filepath.Join(commandDir, "should-not-exist")
	command := `printf ran > "$MARKER"`
	if runtime.GOOS == "windows" {
		command = `set /p =ran<nul > "%MARKER%"`
	}
	assignment := ProcessAssignment{
		ID: bootstrap.ProcessID,
		Process: Process{
			Command:       command,
			ShellSelector: "default",
			Cwd:           commandDir,
			IOMode:        "pipe",
		},
		Env: map[string]string{"MARKER": markerPath},
	}
	runner, err := prepareLocalRunner(ctx, bootstrap, assignment)
	if err != nil {
		t.Fatalf("prepare command: %v", err)
	}
	t.Cleanup(func() { _ = runner.output.Close() })

	if err := supervisor.Close(); err != nil {
		t.Fatalf("close execution ledger: %v", err)
	}
	state := &runnerServerState{
		bootstrap:    bootstrap,
		processState: supervisor,
		prepared:     runner,
		assignment:   assignment,
	}
	response := state.startOnce(ctx)
	if response.OK || response.Error == "" {
		t.Fatalf("start after persistence failure = %+v", response)
	}
	if runner.cmd.Process != nil {
		t.Fatal("persistence failure still called the OS spawn primitive")
	}
	if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("command ran after persistence failure: %v", err)
	}
	process, found, err := store.Process(ctx, bootstrap.ProcessID)
	if err != nil || !found {
		t.Fatalf("read process state: found=%t err=%v", found, err)
	}
	if process.ExecCommitted {
		t.Fatalf("failed execution boundary was recorded: %+v", process)
	}
}

func TestActionBoundaryPersistenceFailurePreventsEffect(t *testing.T) {
	ctx := context.Background()
	bootstrap, store, supervisor := acceptedRunnerServerState(t)
	commitRunnerExecution(t, supervisor)

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	runner := &localProcessRunner{
		cmd: &exec.Cmd{
			Process: &os.Process{Pid: os.Getpid()},
		},
		stdin:   writer,
		stdinOK: true,
	}
	state := &runnerServerState{
		bootstrap:    bootstrap,
		processState: supervisor,
		prepared:     runner,
		assignment: ProcessAssignment{
			ID:      bootstrap.ProcessID,
			Process: Process{IOMode: "pipe"},
		},
	}
	if err := supervisor.Close(); err != nil {
		t.Fatalf("close action ledger: %v", err)
	}
	action := ProcessAction{
		ID:         "act_boundary_failure",
		ActionKind: "write",
		Seq:        1,
		Payload:    json.RawMessage(`{"data":"must-not-write"}`),
	}
	if err := state.runApplyOnce(ctx, action); err == nil {
		t.Fatal("action succeeded after its persistence boundary failed")
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 0 {
		t.Fatalf("action effect reached stdin: %q", body)
	}
	if _, found, err := store.Action(
		ctx,
		action.ID,
	); err != nil || found {
		t.Fatalf("failed action boundary persisted: found=%t err=%v", found, err)
	}
}

func TestActionAfterExecutionClosureFreezesNoEffectOutcome(t *testing.T) {
	tests := []struct {
		name       string
		kind       daemonprotocol.ProcessActionKind
		payload    json.RawMessage
		wantEvent  daemonprotocol.ReportedEventType
		wantReason string
	}{
		{
			name:       "terminate",
			kind:       "terminate",
			payload:    json.RawMessage(`{}`),
			wantEvent:  "process_action_applied",
			wantReason: "already_stopped",
		},
		{
			name:       "write",
			kind:       "write",
			payload:    json.RawMessage(`{"data":"late"}`),
			wantEvent:  "process_action_failed",
			wantReason: "process_not_running",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			bootstrap, store, supervisor := acceptedRunnerServerState(t)
			commitRunnerExecution(t, supervisor)
			if err := supervisor.CloseActionAdmission(ctx); err != nil {
				t.Fatal(err)
			}
			runner := &localProcessRunner{
				cmd: &exec.Cmd{
					Process: &os.Process{Pid: 999999999},
				},
				terminalResultReady: make(chan struct{}),
			}
			state := runnerServerState{
				bootstrap:    bootstrap,
				processState: supervisor,
				prepared:     runner,
				assignment: ProcessAssignment{
					ID:      bootstrap.ProcessID,
					Process: Process{IOMode: "pipe"},
				},
			}
			action := ProcessAction{
				ID:         "act_after_execution_closed_" + test.name,
				ActionKind: test.kind,
				Seq:        1,
				Payload:    test.payload,
			}
			if err := state.runApplyOnce(ctx, action); err != nil {
				t.Fatal(err)
			}
			runner.terminalMu.Lock()
			override := runner.terminal
			runner.terminalMu.Unlock()
			if override.State != "" {
				t.Fatalf("closed execution received terminal override: %+v", override)
			}
			report, found, err := store.ReportBySlot(
				ctx,
				statedb.ReportActionTerminal,
				bootstrap.ProcessID,
				action.ID,
			)
			if err != nil || !found {
				t.Fatalf("read action outcome: found=%t err=%v", found, err)
			}
			var event daemonReportedEvent
			if err := json.Unmarshal(report.Body, &event); err != nil {
				t.Fatal(err)
			}
			if event.Type != test.wantEvent ||
				event.StateReasonCode != test.wantReason {
				t.Fatalf("closed action event = %+v", event)
			}
			if len(event.Result) != 0 {
				t.Fatalf("daemon supplied canonical server result: %s", event.Result)
			}
			stored, found, err := store.Action(ctx, action.ID)
			if err != nil || !found {
				t.Fatalf(
					"read closed action boundary: found=%t err=%v",
					found,
					err,
				)
			}
			if stored.EffectCommitted {
				t.Fatalf("closed action crossed its effect boundary: %+v", stored)
			}
		})
	}
}

func acceptedRunnerServerState(
	t *testing.T,
) (supervisorIdentityBootstrap, *statedb.Store, *statedb.Supervisor) {
	t.Helper()
	ctx := context.Background()
	bootstrap := supervisorIdentityBootstrap{
		OmnaraHome:           t.TempDir(),
		InstallationID:       "ins_runner_state",
		MachineID:            "mch_runner_state",
		ProcessID:            "prc_runner_state",
		SupervisorInstanceID: "supervisor-instance-runner-state",
		SupervisorToken:      "supervisor-token-runner-state",
	}
	machine, err := localstore.Machine(
		bootstrap.OmnaraHome,
		bootstrap.InstallationID,
		bootstrap.MachineID,
	)
	if err != nil {
		t.Fatal(err)
	}
	store, err := statedb.Open(
		ctx,
		machine.StateDBPath(),
		bootstrap.InstallationID,
		bootstrap.MachineID,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	process := statedb.Process{
		ProcessID:            bootstrap.ProcessID,
		SupervisorInstanceID: bootstrap.SupervisorInstanceID,
		SupervisorToken:      bootstrap.SupervisorToken,
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
	supervisor, err := statedb.OpenSupervisor(
		ctx,
		machine.StateDBPath(),
		bootstrap.InstallationID,
		bootstrap.MachineID,
		bootstrap.ProcessID,
		bootstrap.SupervisorInstanceID,
		bootstrap.SupervisorToken,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close() })
	return bootstrap, store, supervisor
}

func commitRunnerExecution(t *testing.T, supervisor *statedb.Supervisor) {
	t.Helper()
	execute, err := supervisor.AuthorizeSpawnOnce(context.Background())
	if err != nil || !execute {
		t.Fatalf("commit test execution: execute=%t err=%v", execute, err)
	}
	if err := supervisor.RecordSpawned(
		context.Background(),
		"process_group",
		"123",
	); err != nil {
		t.Fatal(err)
	}
}
