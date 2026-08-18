package machinedaemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"syscall"
	"testing"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb/statedbtest"
)

type storageExhaustionRunner struct {
	done      chan struct{}
	startErr  error
	terminate []string
}

func (r *storageExhaustionRunner) Status(context.Context) error                   { return nil }
func (r *storageExhaustionRunner) StartOnce(context.Context) error                { return r.startErr }
func (r *storageExhaustionRunner) ApplyOnce(context.Context, ProcessAction) error { return nil }
func (r *storageExhaustionRunner) CloseUngranted(context.Context) error           { return nil }
func (r *storageExhaustionRunner) Terminate(_ context.Context, reason string) error {
	r.terminate = append(r.terminate, reason)
	if reason == daemonprotocol.ProcessReasonMachineStorageExhausted {
		return errStorageExhaustionTerminalReady
	}
	return nil
}
func (r *storageExhaustionRunner) Done() <-chan struct{} { return r.done }
func (r *storageExhaustionRunner) IsDone() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

type storageExhaustionTransport struct {
	report  statedb.Report
	reports int
}

func (t *storageExhaustionTransport) SendReport(
	_ context.Context,
	report statedb.Report,
) (daemonReportAck, error) {
	t.report = report
	t.reports++
	return daemonReportAck{status: daemonprotocol.AckStatusCommitted}, nil
}

func TestStorageExhaustionClassifierIsNarrow(t *testing.T) {
	t.Parallel()

	for _, err := range []error{
		syscall.ENOSPC,
		syscall.EDQUOT,
		fmt.Errorf("wrapped: %w", statedb.ErrFull),
		fmt.Errorf("wrapped: %w", errRunnerStorageExhaustion),
	} {
		if !isStorageExhaustion(err) {
			t.Fatalf("error %v was not classified as storage exhaustion", err)
		}
	}
	for _, err := range []error{
		errors.New("database or disk is full"),
		errors.New("input/output error"),
		context.DeadlineExceeded,
	} {
		if isStorageExhaustion(err) {
			t.Fatalf("error %v was classified as storage exhaustion", err)
		}
	}
}

func TestSupervisorStateWriteDoesNotRetryFull(t *testing.T) {
	t.Parallel()

	calls := 0
	err := retrySupervisorStateWrite(context.Background(), func() error {
		calls++
		return statedb.ErrFull
	})
	if !errors.Is(err, statedb.ErrFull) || calls != 1 {
		t.Fatalf("retry result: calls=%d err=%v", calls, err)
	}
}

func TestAutonomousStateWriteStopsAfterStorageExhaustion(t *testing.T) {
	t.Parallel()

	state := &runnerServerState{storageExhaustionReady: true}
	called := false
	err := state.autonomousStateWrite(context.Background(), func() error {
		called = true
		return nil
	})
	if !errors.Is(err, errStorageExhaustionTerminalReady) || called {
		t.Fatalf("autonomous write after storage exhaustion: called=%t err=%v", called, err)
	}
}

func TestStorageExhaustionBeforeSpawnBecomesTerminalReady(t *testing.T) {
	t.Parallel()

	state := &runnerServerState{
		prepared: &localProcessRunner{startErr: syscall.ENOSPC},
	}
	response := state.handle(
		context.Background(),
		runnerRequest{Method: runnerMethodStartOnce},
	)
	if response.ErrorCode != runnerErrorStorageExhaustionReady ||
		state.prepared.cmd != nil {
		t.Fatalf("storage start response = %+v", response)
	}
}

func TestAcceptedStorageFailureReportsAfterContainment(t *testing.T) {
	t.Parallel()

	client := New(Config{}, nil, nil)
	runner := &storageExhaustionRunner{done: make(chan struct{})}
	runtime := &processRuntime{
		processID:            "prc_storage_report",
		supervisorInstanceID: "supervisor-storage-report",
		runner:               runner,
	}
	transport := &storageExhaustionTransport{}
	handled, err := client.handleAcceptedStorageFailure(
		context.Background(),
		transport,
		runtime,
		statedb.ErrFull,
	)
	resolved, found := client.localProcess(runtime.processID)
	if err != nil || !handled || !found || resolved == runtime ||
		!resolved.cleanupOnly || resolved.runner != nil ||
		runtime.cleanupOnly || runtime.runner != runner {
		t.Fatalf(
			"storage failure: handled=%t runtime=%+v resolved=%+v err=%v",
			handled,
			runtime,
			resolved,
			err,
		)
	}
	var event daemonReportedEvent
	if err := json.Unmarshal(transport.report.Body, &event); err != nil {
		t.Fatal(err)
	}
	if event.State != daemonprotocol.ProcessStateFailed ||
		event.StateReasonCode != daemonprotocol.ProcessReasonMachineStorageExhausted ||
		event.StateReasonMessage != daemonprotocol.ProcessMessageMachineStorageExhausted {
		t.Fatalf("storage report = %+v", event)
	}
	want := []string{daemonprotocol.ProcessReasonMachineStorageExhausted, "server_resolved"}
	if !slices.Equal(runner.terminate, want) {
		t.Fatalf("termination calls = %v, want %v", runner.terminate, want)
	}
	handled, err = client.handleAcceptedStorageFailure(
		context.Background(),
		transport,
		runtime,
		errStorageExhaustionTerminalReady,
	)
	if err != nil || !handled || transport.reports != 1 {
		t.Fatalf(
			"repeated storage failure: handled=%t reports=%d err=%v",
			handled,
			transport.reports,
			err,
		)
	}
}

func TestStartupSkipsActionsForStorageExhaustedProcess(t *testing.T) {
	t.Parallel()

	runner := &ipcProcessRunner{}
	runner.storageFailureReady.Store(true)
	startup := localStartupState{
		Runners: map[string]*processRuntime{
			"prc_storage_startup": {runner: runner},
		},
		Actions: []reconciledAction{{
			processID: "prc_storage_startup",
			action:    ProcessAction{ID: "act_storage_startup"},
		}},
	}
	client := New(Config{}, nil, nil)
	transport := newDaemonSocketTransport(&client, DaemonRuntime{}, startup)
	transport.resumeStartupActions(context.Background())
	if len(transport.pendingActions) != 0 || len(transport.actionQueues) != 0 {
		t.Fatal("storage-exhausted process resumed stale actions")
	}
}

func TestRegistrationStartRetainsStorageExhaustedSupervisor(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const (
		processID            = "prc_storage_registration"
		supervisorInstanceID = "supervisor-storage-registration"
	)
	client := New(Config{OmnaraHome: t.TempDir()}, nil, nil)
	client.bootstrap = daemonBootstrap{
		InstallationID: "ins_storage_registration",
		MachineID:      "mch_storage_registration",
	}
	t.Cleanup(func() { _ = client.closeState() })
	store, err := client.stateStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveProcess(ctx, statedb.Process{
		ProcessID:            processID,
		SupervisorInstanceID: supervisorInstanceID,
		SupervisorToken:      "supervisor-token-storage-registration",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPrepared(ctx, processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}
	runner := &storageExhaustionRunner{
		done:     make(chan struct{}),
		startErr: syscall.ENOSPC,
	}
	runtime := &processRuntime{
		processID:            processID,
		supervisorInstanceID: supervisorInstanceID,
		runner:               runner,
	}
	startup := localStartupState{
		Runners:       map[string]*processRuntime{processID: runtime},
		ForcedReports: make(map[string]struct{}),
	}
	if err := client.applyProcessDisposition(
		ctx,
		&startup,
		ProcessReconciliationClaim{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
		},
		ProcessReconciliationDirective{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
			Disposition:          daemonprotocol.ProcessDispositionStart,
		},
	); err != nil {
		t.Fatal(err)
	}
	if startup.Runners[processID] != runtime {
		t.Fatal("storage-exhausted supervisor was not retained")
	}
	if !slices.Equal(
		runner.terminate,
		[]string{daemonprotocol.ProcessReasonMachineStorageExhausted},
	) {
		t.Fatalf("termination calls = %v", runner.terminate)
	}
}

func TestRegistrationStartRetainsTerminalReadySupervisor(t *testing.T) {
	t.Parallel()

	client := New(Config{OmnaraHome: t.TempDir()}, nil, nil)
	client.bootstrap = daemonBootstrap{
		InstallationID: "ins_storage_registration_ready",
		MachineID:      "mch_storage_registration_ready",
	}
	t.Cleanup(func() { _ = client.closeState() })
	runner := &ipcProcessRunner{}
	runner.storageFailureReady.Store(true)
	const processID = "prc_storage_registration_ready"
	runtime := &processRuntime{processID: processID, runner: runner}
	startup := localStartupState{
		Runners:       map[string]*processRuntime{processID: runtime},
		ForcedReports: make(map[string]struct{}),
	}
	if err := client.applyProcessDisposition(
		context.Background(),
		&startup,
		ProcessReconciliationClaim{ProcessID: processID},
		ProcessReconciliationDirective{
			ProcessID:   processID,
			Disposition: daemonprotocol.ProcessDispositionStart,
		},
	); err != nil {
		t.Fatal(err)
	}
	if startup.Runners[processID] != runtime {
		t.Fatal("terminal-ready supervisor was not retained")
	}
}

func TestAcceptedCleanupHandlesLifetimeLock(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const (
		processID            = "prc_storage_lock"
		supervisorInstanceID = "supervisor-storage-lock"
	)
	client := New(Config{OmnaraHome: t.TempDir()}, nil, nil)
	client.bootstrap = daemonBootstrap{InstallationID: "ins_storage_lock", MachineID: "mch_storage_lock"}
	t.Cleanup(func() { _ = client.closeState() })
	store, err := client.stateStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveProcess(ctx, statedb.Process{
		ProcessID:            processID,
		SupervisorInstanceID: supervisorInstanceID,
		SupervisorToken:      "supervisor-token-storage-lock",
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
	if err := localstore.EnsurePrivateDir(machine.RunDir()); err != nil {
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
	if err := statedbtest.SetProcessDeleteFailure(ctx, machine.StateDBPath(), true); err != nil {
		t.Fatal(err)
	}
	runtime := &processRuntime{
		processID:            processID,
		supervisorInstanceID: supervisorInstanceID,
	}
	pending, err := client.closeStorageExhaustedProcess(ctx, runtime)
	if err == nil || !pending {
		t.Fatalf("cleanup with state deletion failure: pending=%t err=%v", pending, err)
	}
	retained, err := localstore.TryAcquireExistingLock(lockPath)
	if err != nil {
		t.Fatalf("retained lifetime lock = %v", err)
	}
	if err := retained.Release(); err != nil {
		t.Fatal(err)
	}
	if err := statedbtest.SetProcessDeleteFailure(ctx, machine.StateDBPath(), false); err != nil {
		t.Fatal(err)
	}
	pending, err = client.closeStorageExhaustedProcess(ctx, runtime)
	if err != nil || pending {
		t.Fatalf("cleanup retry: pending=%t err=%v", pending, err)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lifetime lock after cleanup = %v", err)
	}
	if _, found, err := store.Process(ctx, processID); err != nil || found {
		t.Fatalf("process after cleanup: found=%t err=%v", found, err)
	}
	if err := store.ReserveProcess(ctx, statedb.Process{
		ProcessID:            processID,
		SupervisorInstanceID: supervisorInstanceID,
		SupervisorToken:      "supervisor-token-storage-lock",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPrepared(ctx, processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAccepted(ctx, processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}
	pending, err = client.closeStorageExhaustedProcess(ctx, runtime)
	if err != nil || pending {
		t.Fatalf("cleanup with missing lifetime lock: pending=%t err=%v", pending, err)
	}
	if _, found, err := store.Process(ctx, processID); err != nil || found {
		t.Fatalf("process after missing-lock cleanup: found=%t err=%v", found, err)
	}
}
