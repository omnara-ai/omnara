package machinedaemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
	"github.com/omnara-ai/omnara/internal/machinedaemon/skillsync"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
)

type blockingPrepareLauncher struct {
	started chan struct{}
	release chan struct{}
	runtime *processRuntime
}

type unresolvedPrepareLauncher struct{}

type successfulPrepareLauncher struct {
	runtime *processRuntime
}

type cleanupPendingPrepareLauncher struct {
	runtime *processRuntime
	calls   chan<- struct{}
}

func (launcher blockingPrepareLauncher) Prepare(
	context.Context,
	*Client,
	ProcessAssignment,
) (*processRuntime, error) {
	close(launcher.started)
	<-launcher.release
	if launcher.runtime != nil {
		return launcher.runtime, nil
	}
	return nil, errors.New("test preparation stopped")
}

func (unresolvedPrepareLauncher) Prepare(
	context.Context,
	*Client,
	ProcessAssignment,
) (*processRuntime, error) {
	return nil, errors.Join(
		errUnresolvedProcessPreparation,
		errors.New("test rollback remained ambiguous"),
	)
}

func (launcher successfulPrepareLauncher) Prepare(
	context.Context,
	*Client,
	ProcessAssignment,
) (*processRuntime, error) {
	return launcher.runtime, nil
}

func (launcher cleanupPendingPrepareLauncher) Prepare(
	context.Context,
	*Client,
	ProcessAssignment,
) (*processRuntime, error) {
	if launcher.calls != nil {
		launcher.calls <- struct{}{}
	}
	return launcher.runtime, errors.New("test cleanup remains pending")
}

type recordingProcessRunner struct {
	applied        chan ProcessAction
	closeCalls     chan struct{}
	startCalls     chan struct{}
	releaseStart   chan struct{}
	releaseClose   chan struct{}
	statusErr      error
	terminateCalls chan string
	terminateErr   error
	done           chan struct{}
}

type reportSendResult struct {
	ack daemonReportAck
	err error
}

type blockingSkillDownloadTransport struct {
	started chan struct{}
	release chan struct{}
}

func (transport blockingSkillDownloadTransport) RoundTrip(
	*http.Request,
) (*http.Response, error) {
	transport.started <- struct{}{}
	<-transport.release
	return nil, errors.New("test skill download released")
}

func (runner *recordingProcessRunner) Status(
	context.Context,
) error {
	return runner.statusErr
}

func (runner *recordingProcessRunner) StartOnce(
	context.Context,
) error {
	if runner.startCalls != nil {
		runner.startCalls <- struct{}{}
		if runner.releaseStart != nil {
			<-runner.releaseStart
		}
		return nil
	}
	return errors.New("unexpected start")
}

func (runner *recordingProcessRunner) ApplyOnce(
	_ context.Context,
	action ProcessAction,
) error {
	runner.applied <- action
	return nil
}

func (runner *recordingProcessRunner) CloseUngranted(ctx context.Context) error {
	if runner.closeCalls != nil {
		runner.closeCalls <- struct{}{}
		if runner.releaseClose != nil {
			select {
			case <-runner.releaseClose:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	return errors.New("unexpected close")
}

func (runner *recordingProcessRunner) Terminate(
	_ context.Context,
	reason string,
) error {
	if runner.terminateCalls != nil {
		runner.terminateCalls <- reason
		return runner.terminateErr
	}
	return errors.New("unexpected terminate")
}

func (runner *recordingProcessRunner) Done() <-chan struct{} {
	return runner.done
}

func (runner *recordingProcessRunner) IsDone() bool {
	select {
	case <-runner.done:
		return true
	default:
		return false
	}
}

func TestTransportShutdownWaitsForInFlightProcessPreparation(
	t *testing.T,
) {
	t.Parallel()

	prepareStarted := make(chan struct{})
	releasePrepare := make(chan struct{})
	client := New(Config{}, nil, nil)
	client.runnerLauncher = blockingPrepareLauncher{
		started: prepareStarted,
		release: releasePrepare,
	}
	transport := newDaemonSocketTransport(
		&client,
		DaemonRuntime{},
		localStartupState{},
	)
	ctx, cancel := context.WithCancel(context.Background())
	transport.offerProcess(ctx, daemonprotocol.ProcessOffer{
		ProcessID: "prc_delayed_prepare",
	})
	select {
	case <-prepareStarted:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("process preparation did not start")
	}

	shutdownDone := make(chan struct{})
	go func() {
		transport.stopAndWait(cancel)
		close(shutdownDone)
	}()
	select {
	case <-shutdownDone:
		close(releasePrepare)
		t.Fatal("transport shutdown returned while preparation was active")
	case <-time.After(100 * time.Millisecond):
	}

	close(releasePrepare)
	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("transport shutdown did not finish after preparation settled")
	}
	transport.mu.Lock()
	pending := transport.pendingProcesses["prc_delayed_prepare"]
	transport.mu.Unlock()
	if pending != nil {
		t.Fatal("settled preparation remained in the old transport")
	}
}

func TestCleanupPendingPreparationDoesNotForceReconnect(t *testing.T) {
	t.Parallel()

	const processID = "prc_cleanup_pending_prepare"
	done := make(chan struct{})
	close(done)
	runtime := &processRuntime{
		processID:            processID,
		supervisorInstanceID: "supervisor-instance-cleanup-pending-prepare",
		runner:               &recordingProcessRunner{done: done},
		cleanupOnly:          true,
	}
	prepareCalls := make(chan struct{}, 2)
	client := New(Config{}, nil, nil)
	client.runnerLauncher = cleanupPendingPrepareLauncher{
		runtime: runtime,
		calls:   prepareCalls,
	}
	transport := newDaemonSocketTransport(
		&client,
		DaemonRuntime{},
		localStartupState{},
	)
	defer transport.stopAndWait(func() {})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	transport.offerProcess(ctx, daemonprotocol.ProcessOffer{ProcessID: processID})
	select {
	case <-prepareCalls:
	case <-ctx.Done():
		t.Fatal("process preparation was not attempted")
	}
	for {
		cached, found := client.localProcess(processID)
		transport.mu.Lock()
		pending := transport.pendingProcesses[processID]
		transport.mu.Unlock()
		if found && pending == nil {
			if cached != runtime {
				t.Fatal("cleanup runtime identity changed")
			}
			break
		}
		if err := sleepContext(ctx, time.Millisecond); err != nil {
			t.Fatal("cleanup-pending preparation was not retained")
		}
	}
	select {
	case err := <-transport.fatal:
		t.Fatalf("cleanup-pending preparation forced reconnect: %v", err)
	default:
	}
	transport.offerProcess(ctx, daemonprotocol.ProcessOffer{ProcessID: processID})
	transport.workers.Wait()
	select {
	case <-prepareCalls:
		t.Fatal("duplicate offer restarted pending cleanup preparation")
	default:
	}
}

func TestCommandValidationFailureReportsAfterAcceptance(
	t *testing.T,
) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	const (
		processID   = "prc_invalid_command"
		invalidMode = "invalid"
	)
	client := New(
		Config{
			OmnaraHome: t.TempDir(),
			RunnerPath: os.Getenv("PATH"),
		},
		nil,
		nil,
	)
	client.bootstrap = daemonBootstrap{
		InstallationID: "ins_failed_preparation",
		MachineID:      "mch_failed_preparation",
	}
	defer client.closeState()
	transport := newDaemonSocketTransport(
		&client,
		DaemonRuntime{},
		localStartupState{},
	)
	defer transport.stopAndWait(func() {})

	transport.offerProcess(ctx, daemonprotocol.ProcessOffer{
		ProcessID:      processID,
		Command:        "echo should-not-run",
		ShellSelector:  "default",
		Cwd:            t.TempDir(),
		IOMode:         invalidMode,
		TimeoutSeconds: 1,
	})
	select {
	case message := <-transport.send:
		if message.Type != "process_accept" ||
			message.ProcessID != processID {
			t.Fatalf("invalid-command preparation sent %+v", message)
		}
	case <-ctx.Done():
		t.Fatal("invalid command did not produce process acceptance")
	}
	store, err := client.stateStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	process, found, err := store.Process(ctx, processID)
	if err != nil || !found ||
		process.Phase != statedb.ProcessPrepared ||
		process.ExecCommitted {
		t.Fatalf(
			"invalid-command preparation: found=%t process=%+v err=%v",
			found,
			process,
			err,
		)
	}
	if err := transport.handleMessage(
		ctx,
		daemonprotocol.Message{
			Type:      "process_accept_ack",
			ProcessID: processID,
		},
	); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case fatalErr := <-transport.fatal:
			t.Fatalf("known start failure forced reconnect: %v", fatalErr)
		default:
		}
		process, found, err = store.Process(ctx, processID)
		if err != nil {
			t.Fatal(err)
		}
		if found && process.Phase == statedb.ProcessTerminal &&
			process.LocalClosed {
			break
		}
		if err := sleepContext(ctx, 10*time.Millisecond); err != nil {
			t.Fatalf(
				"known start failure did not become terminal: found=%t process=%+v",
				found,
				process,
			)
		}
	}
	if process.ExecCommitted || process.ContainmentKind != "" {
		t.Fatalf("known start failure crossed spawn boundary: %+v", process)
	}
	reports, err := store.ReportsForProcess(ctx, processID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 ||
		reports[0].Kind != statedb.ReportProcessTerminal {
		t.Fatalf("known start failure reports = %+v", reports)
	}
	var event daemonReportedEvent
	if err := json.Unmarshal(reports[0].Body, &event); err != nil {
		t.Fatal(err)
	}
	if event.State != "failed" ||
		event.StateReasonCode != "start_failed" ||
		!strings.Contains(event.StateReasonMessage, `unsupported io_mode "`+invalidMode+`"`) {
		t.Fatalf("known start failure event = %+v", event)
	}
	transport.mu.Lock()
	pending := transport.pendingProcesses[processID]
	transport.mu.Unlock()
	if pending != nil {
		t.Fatalf("terminal start failure remained pending: %+v", pending)
	}
	select {
	case fatalErr := <-transport.fatal:
		t.Fatalf("reported start failure forced reconnect: %v", fatalErr)
	default:
	}
}

func TestTransientPreparationFailureCleansLocalStateWithoutAcceptance(
	t *testing.T,
) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	const processID = "prc_transient_preparation"
	client := New(
		Config{
			OmnaraHome: t.TempDir(),
			RunnerPath: os.Getenv("PATH"),
		},
		nil,
		nil,
	)
	client.bootstrap = daemonBootstrap{
		InstallationID: "ins_transient_preparation",
		MachineID:      "mch_transient_preparation",
	}
	defer client.closeState()
	machine, err := client.machineStore()
	if err != nil {
		t.Fatal(err)
	}
	outputPath, err := machine.OutputBufferPath(processID)
	if err != nil {
		t.Fatal(err)
	}
	if err := localstore.EnsurePrivateDir(filepath.Dir(outputPath)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outputPath, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := client.stateStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	transport := newDaemonSocketTransport(
		&client,
		DaemonRuntime{},
		localStartupState{},
	)
	defer transport.stopAndWait(func() {})

	transport.offerProcess(
		ctx,
		daemonprotocol.ProcessOffer{
			ProcessID:      processID,
			Command:        "echo should-not-run",
			ShellSelector:  "default",
			Cwd:            t.TempDir(),
			IOMode:         "pipe",
			TimeoutSeconds: 1,
		},
	)
	workerDone := make(chan struct{})
	go func() {
		transport.workers.Wait()
		close(workerDone)
	}()
	select {
	case <-workerDone:
	case <-ctx.Done():
		t.Fatal("transient preparation cleanup did not finish")
	}
	_, found, err := store.Process(ctx, processID)
	if err != nil {
		t.Fatal(err)
	}
	transport.mu.Lock()
	pending := transport.pendingProcesses[processID]
	transport.mu.Unlock()
	if found || pending != nil {
		t.Fatalf(
			"transient preparation cleanup incomplete: found=%t pending=%+v",
			found,
			pending,
		)
	}
	select {
	case message := <-transport.send:
		t.Fatalf("transient preparation sent %+v", message)
	case fatalErr := <-transport.fatal:
		t.Fatalf("transient preparation forced reconnect: %v", fatalErr)
	default:
	}
	if _, err := os.Stat(filepath.Dir(outputPath)); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("transient preparation artifacts remain: %v", err)
	}
}

func TestAmbiguousPreparationRollbackForcesReconciliation(t *testing.T) {
	t.Parallel()

	client := New(Config{}, nil, nil)
	client.runnerLauncher = unresolvedPrepareLauncher{}
	transport := newDaemonSocketTransport(
		&client,
		DaemonRuntime{},
		localStartupState{},
	)
	defer transport.stopAndWait(func() {})

	transport.offerProcess(
		context.Background(),
		daemonprotocol.ProcessOffer{
			ProcessID: "prc_ambiguous_preparation",
		},
	)
	select {
	case err := <-transport.fatal:
		if err == nil ||
			!strings.Contains(err.Error(), "retained unresolved local state") {
			t.Fatalf("transport failure = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ambiguous failed preparation left the socket running")
	}
}

func TestRejectedProcessAcceptanceClosesPreparedState(
	t *testing.T,
) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const (
		processID            = "prc_rejected_accept"
		supervisorInstanceID = "supervisor-instance-rejected-accept"
	)
	client := New(
		Config{OmnaraHome: t.TempDir()},
		nil,
		nil,
	)
	client.bootstrap = daemonBootstrap{
		InstallationID: "ins_rejected_accept",
		MachineID:      "mch_rejected_accept",
	}
	defer client.closeState()
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
	store, err := client.stateStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveProcess(
		ctx,
		statedb.Process{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
			SupervisorToken:      "supervisor-token-rejected-accept",
		}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPrepared(ctx, processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}
	runner := &recordingProcessRunner{
		closeCalls:   make(chan struct{}, 1),
		releaseClose: make(chan struct{}),
		done:         make(chan struct{}),
	}
	transport := newDaemonSocketTransport(
		&client,
		DaemonRuntime{},
		localStartupState{},
	)
	defer transport.stopAndWait(func() {})
	transport.pendingProcesses[processID] = &pendingProcess{
		runtime: &processRuntime{
			processID:            processID,
			supervisorInstanceID: supervisorInstanceID,
			runner:               runner,
		},
	}

	transport.handleServerError(ctx, daemonprotocol.Message{
		Type:      "error",
		ErrorCode: daemonprotocol.ErrorCodeProcessOfferUnavailable,
		ProcessID: processID,
	})
	select {
	case <-runner.closeCalls:
	case <-ctx.Done():
		t.Fatal("rejected process offer did not close its supervisor")
	}
	if transport.idle() {
		t.Fatal("rejected process became idle before its supervisor stopped")
	}
	close(runner.releaseClose)
	for {
		_, found, err := store.Process(ctx, processID)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			break
		}
		if err := sleepContext(ctx, time.Millisecond); err != nil {
			t.Fatal("rejected process state was not deleted")
		}
	}
	for {
		transport.mu.Lock()
		pending := transport.pendingProcesses[processID]
		transport.mu.Unlock()
		if pending == nil {
			break
		}
		if err := sleepContext(ctx, time.Millisecond); err != nil {
			t.Fatal("rejected process remained pending")
		}
	}
	select {
	case fatalErr := <-transport.fatal:
		t.Fatalf("conclusive rejection forced reconnect: %v", fatalErr)
	default:
	}
}

func TestRejectedProcessOfferWhilePreparationFinishesClosesState(
	t *testing.T,
) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const (
		processID            = "prc_rejected_during_prepare"
		supervisorInstanceID = "supervisor-instance-rejected-during-prepare"
	)
	client := New(Config{OmnaraHome: t.TempDir()}, nil, nil)
	client.bootstrap = daemonBootstrap{
		InstallationID: "ins_rejected_during_prepare",
		MachineID:      "mch_rejected_during_prepare",
	}
	defer client.closeState()
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
	store, err := client.stateStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveProcess(
		ctx,
		statedb.Process{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
			SupervisorToken:      "supervisor-token-rejected-during-prepare",
		}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPrepared(ctx, processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}
	runner := &recordingProcessRunner{
		closeCalls: make(chan struct{}, 1),
		done:       make(chan struct{}),
	}
	runtime := &processRuntime{
		processID:            processID,
		supervisorInstanceID: supervisorInstanceID,
		runner:               runner,
	}
	prepareStarted := make(chan struct{})
	releasePrepare := make(chan struct{})
	client.runnerLauncher = blockingPrepareLauncher{
		started: prepareStarted,
		release: releasePrepare,
		runtime: runtime,
	}
	transport := newDaemonSocketTransport(
		&client,
		DaemonRuntime{},
		localStartupState{},
	)
	defer transport.stopAndWait(func() {})
	transport.offerProcess(ctx, daemonprotocol.ProcessOffer{
		ProcessID: processID,
	})
	select {
	case <-prepareStarted:
	case <-ctx.Done():
		t.Fatal("process preparation did not start")
	}

	transport.handleServerError(ctx, daemonprotocol.Message{
		Type:      "error",
		ErrorCode: daemonprotocol.ErrorCodeProcessOfferUnavailable,
		ProcessID: processID,
	})
	select {
	case <-transport.fatal:
	case <-ctx.Done():
		t.Fatal("rejection during preparation did not force reconciliation")
	}
	close(releasePrepare)
	select {
	case <-runner.closeCalls:
	case <-ctx.Done():
		t.Fatal("completed rejected preparation was not closed")
	}
	for {
		_, found, err := store.Process(ctx, processID)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			break
		}
		if err := sleepContext(ctx, time.Millisecond); err != nil {
			t.Fatal("rejected preparation state was not deleted")
		}
	}
	select {
	case message := <-transport.send:
		if message.Type == "process_accept" {
			t.Fatal("rejected preparation sent process acceptance")
		}
	default:
	}
}

func TestRejectedActionAcceptanceForgetsInMemoryOffer(t *testing.T) {
	t.Parallel()

	const (
		processID = "prc_rejected_action"
		actionID  = "act_rejected_action"
	)
	client := New(Config{}, nil, nil)
	transport := newDaemonSocketTransport(
		&client,
		DaemonRuntime{},
		localStartupState{},
	)
	defer transport.stopAndWait(func() {})
	transport.pendingActions[actionID] = pendingAction{
		processID: processID,
		action:    ProcessAction{ID: actionID},
	}

	transport.handleServerError(
		context.Background(),
		daemonprotocol.Message{
			Type:            "error",
			ErrorCode:       daemonprotocol.ErrorCodeActionOfferUnavailable,
			ProcessID:       processID,
			ProcessActionID: actionID,
		},
	)
	transport.mu.Lock()
	_, found := transport.pendingActions[actionID]
	transport.mu.Unlock()
	if found {
		t.Fatal("rejected action remained in the socket offer map")
	}
}

func TestDuplicateOffersSendOneAcceptancePerConnection(t *testing.T) {
	t.Parallel()

	t.Run("process", func(t *testing.T) {
		t.Parallel()
		const processID = "prc_duplicate_offer"
		client := New(Config{}, nil, nil)
		client.runnerLauncher = successfulPrepareLauncher{
			runtime: &processRuntime{processID: processID},
		}
		transport := newDaemonSocketTransport(
			&client,
			DaemonRuntime{},
			localStartupState{},
		)
		defer transport.stopAndWait(func() {})
		offer := daemonprotocol.ProcessOffer{ProcessID: processID}
		transport.offerProcess(context.Background(), offer)
		select {
		case message := <-transport.send:
			if message.Type != "process_accept" ||
				message.ProcessID != processID {
				t.Fatalf("process acceptance = %+v", message)
			}
		case <-time.After(time.Second):
			t.Fatal("process offer did not produce an acceptance")
		}
		transport.offerProcess(context.Background(), offer)
		select {
		case message := <-transport.send:
			t.Fatalf("duplicate process offer produced %+v", message)
		default:
		}
	})

	t.Run("action", func(t *testing.T) {
		t.Parallel()
		const (
			processID = "prc_duplicate_action_offer"
			actionID  = "act_duplicate_offer"
		)
		client := New(Config{}, nil, nil)
		client.processes[processID] = &processRuntime{
			processID: processID,
		}
		transport := newDaemonSocketTransport(
			&client,
			DaemonRuntime{},
			localStartupState{},
		)
		defer transport.stopAndWait(func() {})
		offer := daemonprotocol.ActionOffer{
			ProcessID:       processID,
			ProcessActionID: actionID,
			ActionKind:      "read",
			Seq:             1,
			Payload:         json.RawMessage(`{"cursor":0}`),
		}
		transport.offerAction(offer)
		select {
		case message := <-transport.send:
			if message.Type != "action_accept" ||
				message.ProcessActionID != actionID {
				t.Fatalf("action acceptance = %+v", message)
			}
		case <-time.After(time.Second):
			t.Fatal("action offer did not produce an acceptance")
		}
		transport.offerAction(offer)
		select {
		case message := <-transport.send:
			t.Fatalf("duplicate action offer produced %+v", message)
		default:
		}
	})
}

func TestTerminationDuringAcceptedStartIsNotDropped(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const (
		processID            = "prc_terminate_during_start"
		supervisorInstanceID = "supervisor-instance-terminate-during-start"
	)
	client := New(Config{OmnaraHome: t.TempDir()}, nil, nil)
	client.bootstrap = daemonBootstrap{
		InstallationID: "ins_terminate_during_start",
		MachineID:      "mch_terminate_during_start",
	}
	defer client.closeState()
	store, err := client.stateStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveProcess(
		ctx,
		statedb.Process{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
			SupervisorToken:      "supervisor-token-terminate-during-start",
		}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPrepared(ctx, processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}
	runner := &recordingProcessRunner{
		startCalls:     make(chan struct{}, 1),
		releaseStart:   make(chan struct{}),
		terminateCalls: make(chan string, 1),
		terminateErr:   errors.New("injected termination failure"),
		done:           make(chan struct{}),
	}
	runtime := &processRuntime{
		processID:            processID,
		supervisorInstanceID: supervisorInstanceID,
		runner:               runner,
	}
	transport := newDaemonSocketTransport(
		&client,
		DaemonRuntime{},
		localStartupState{},
	)
	defer transport.stopAndWait(cancel)
	transport.pendingProcesses[processID] = &pendingProcess{
		runtime: runtime,
	}

	transport.startAcceptedProcess(ctx, processID)
	select {
	case <-runner.startCalls:
	case <-time.After(time.Second):
		t.Fatal("accepted process did not reach StartOnce")
	}
	transport.terminateProcess(ctx, processID)
	close(runner.releaseStart)
	select {
	case reason := <-runner.terminateCalls:
		if reason != "server_requested" {
			t.Fatalf("termination reason = %q", reason)
		}
	case <-time.After(time.Second):
		t.Fatal("termination was lost during accepted-process handoff")
	}
	select {
	case err := <-transport.fatal:
		if !errors.Is(err, runner.terminateErr) {
			t.Fatalf("transport failure = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("failed handoff termination did not force reconciliation")
	}
}

func TestFailedServerTerminationForcesReconciliation(t *testing.T) {
	t.Parallel()

	terminationErr := errors.New("injected server termination failure")
	runner := &recordingProcessRunner{
		terminateCalls: make(chan string, 1),
		terminateErr:   terminationErr,
		done:           make(chan struct{}),
	}
	client := New(Config{}, nil, nil)
	client.addProcess(&processRuntime{
		processID:            "prc_failed_server_termination",
		supervisorInstanceID: "supervisor-failed-server-termination",
		runner:               runner,
	})
	transport := newDaemonSocketTransport(
		&client,
		DaemonRuntime{},
		localStartupState{},
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer transport.stopAndWait(cancel)

	transport.terminateProcess(ctx, "prc_failed_server_termination")
	select {
	case reason := <-runner.terminateCalls:
		if reason != "server_requested" {
			t.Fatalf("termination reason = %q", reason)
		}
	case <-time.After(time.Second):
		t.Fatal("server termination was not sent to the supervisor")
	}
	select {
	case err := <-transport.fatal:
		if !errors.Is(err, terminationErr) {
			t.Fatalf("transport failure = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("failed server termination did not force reconciliation")
	}
}

func TestReconnectDispatchesAcceptedActionsAcrossMoreThan64Processes(
	t *testing.T,
) {
	t.Parallel()

	const actionCount = 128
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const (
		installationID = "ins_many_accepted_actions"
		machineID      = "mch_many_accepted_actions"
	)
	store, err := statedb.Open(
		ctx,
		filepath.Join(t.TempDir(), "state.sqlite"),
		installationID,
		machineID,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	runner := &recordingProcessRunner{
		applied: make(chan ProcessAction, actionCount),
		done:    make(chan struct{}),
	}
	client := New(Config{}, nil, nil)
	client.state = store
	startup := localStartupState{
		Actions: make([]reconciledAction, 0, actionCount),
	}
	expected := make(map[string]struct{}, actionCount)
	for index := range actionCount {
		processID := fmt.Sprintf("prc_%03d", index)
		actionID := fmt.Sprintf("act_%03d", index)
		client.addProcess(&processRuntime{
			processID: processID,
			runner:    runner,
		})
		startup.Actions = append(startup.Actions, reconciledAction{
			processID: processID,
			action: ProcessAction{
				ID:         actionID,
				ActionKind: "write",
				Seq:        1,
			},
		})
		expected[actionID] = struct{}{}
	}
	transport := newDaemonSocketTransport(
		&client,
		DaemonRuntime{},
		startup,
	)
	defer transport.stopAndWait(cancel)
	transport.resumeStartupActions(ctx)
	for range actionCount {
		select {
		case action := <-runner.applied:
			if _, ok := expected[action.ID]; !ok {
				t.Fatalf("unexpected accepted action %q", action.ID)
			}
			delete(expected, action.ID)
		case err := <-transport.fatal:
			t.Fatalf("accepted action dispatch failed: %v", err)
		case <-ctx.Done():
			t.Fatalf("dispatch accepted actions: %v", ctx.Err())
		}
	}
	if len(expected) != 0 {
		t.Fatalf("%d accepted actions were not dispatched", len(expected))
	}
}

func TestReportAcknowledgementNamesOnlyItsExactLocalReport(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client := New(Config{}, nil, nil)
	transport := newDaemonSocketTransport(
		&client,
		DaemonRuntime{},
		localStartupState{},
	)
	defer transport.stopAndWait(func() {})

	send := func(reportID string) <-chan reportSendResult {
		result := make(chan reportSendResult, 1)
		go func() {
			ack, err := transport.SendReport(ctx, statedb.Report{
				ID: reportID,
				Body: []byte(
					`{"type":"process_started","process_id":"prc_report_ack"}`,
				),
			})
			result <- reportSendResult{ack: ack, err: err}
		}()
		return result
	}
	first := send("report-first")
	second := send("report-second")
	enqueued := make(map[string]bool, 2)
	for range 2 {
		select {
		case message := <-transport.send:
			enqueued[message.ReportID] = true
		case <-ctx.Done():
			t.Fatalf("reports were not enqueued: %v", ctx.Err())
		}
	}
	if !enqueued["report-first"] || !enqueued["report-second"] {
		t.Fatalf("enqueued reports = %v", enqueued)
	}

	transport.ackReport(daemonprotocol.Message{
		Type:      "report_ack",
		ReportID:  "report-second",
		AckStatus: daemonprotocol.AckStatusCommitted,
	})
	select {
	case result := <-second:
		if result.err != nil ||
			result.ack.status != daemonprotocol.AckStatusCommitted {
			t.Fatalf("second report result = %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("second report did not receive its acknowledgement")
	}
	select {
	case result := <-first:
		t.Fatalf(
			"first report was settled by another report's acknowledgement: %+v",
			result,
		)
	case <-time.After(100 * time.Millisecond):
	}

	transport.ackReport(daemonprotocol.Message{
		Type:      "report_ack",
		ReportID:  "report-first",
		AckStatus: daemonprotocol.AckStatusCleanupOnly,
	})
	select {
	case result := <-first:
		if result.err != nil ||
			result.ack.status != daemonprotocol.AckStatusCleanupOnly {
			t.Fatalf("first report result = %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("first report did not receive its own acknowledgement")
	}
}

func TestReconciledAcceptedActionReachesRuntimeQueue(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	const (
		installationID       = "ins_reconciled_action"
		machineID            = "mch_reconciled_action"
		processID            = "prc_reconciled_action"
		supervisorInstanceID = "supervisor-instance-reconciled-action"
		actionID             = "act_reconciled_action"
	)
	store, err := statedb.Open(
		ctx,
		filepath.Join(t.TempDir(), "state.sqlite"),
		installationID,
		machineID,
	)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer store.Close()

	runner := &recordingProcessRunner{
		applied: make(chan ProcessAction, 1),
		done:    make(chan struct{}),
	}
	client := New(Config{}, nil, nil)
	client.state = store
	startup := localStartupState{
		Claims: []ProcessReconciliationClaim{{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
		}},
		Runners: map[string]*processRuntime{
			processID: {
				processID:            processID,
				supervisorInstanceID: supervisorInstanceID,
				runner:               runner,
			},
		},
		ForcedReports: make(map[string]struct{}),
	}
	err = client.applyRegistrationReconciliation(
		ctx,
		&startup,
		DaemonRuntimeReconciliation{
			Processes: []ProcessReconciliationDirective{{
				ProcessID:            processID,
				SupervisorInstanceID: supervisorInstanceID,
				Disposition:          "retain",
				Actions: []ProcessActionReconciliationDirective{{
					ProcessActionID: actionID,
					ActionKind:      "write",
					Seq:             1,
					Disposition:     "apply",
				}},
			}},
		},
	)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if len(startup.Actions) != 1 {
		cancel()
		t.Fatalf("reconciled actions = %d, want 1", len(startup.Actions))
	}

	transport := newDaemonSocketTransport(
		&client,
		DaemonRuntime{},
		startup,
	)
	transport.resumeStartupActions(ctx)
	select {
	case action := <-runner.applied:
		if action.ID != actionID || action.Seq != 1 {
			cancel()
			transport.stopAndWait(func() {})
			t.Fatalf("queued reconciled action = %+v", action)
		}
	case <-time.After(time.Second):
		cancel()
		transport.stopAndWait(func() {})
		t.Fatal("reconciled action never reached the process runner")
	}
	cancel()
	transport.stopAndWait(func() {})
}

func TestSocketShutdownWaitsForSkillOfferWorker(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := New(Config{}, nil, nil)
	transport := newDaemonSocketTransport(
		&client,
		DaemonRuntime{},
		localStartupState{},
	)
	started := make(chan struct{}, 1)
	release := make(chan struct{}, 1)
	defer func() {
		select {
		case release <- struct{}{}:
		default:
		}
	}()

	machine, err := localstore.Machine(
		t.TempDir(),
		"ins_skill_shutdown",
		"mch_skill_shutdown",
	)
	if err != nil {
		t.Fatal(err)
	}
	transport.skillsync = skillsync.NewManager(
		machine,
		transportSkillSender{transport: transport},
		blockingSkillDownloadTransport{
			started: started,
			release: release,
		},
		"https://skills.invalid",
		"test-machine-token",
		client.log,
	)
	transport.handleSkillOffer(ctx, daemonprotocol.SkillOffer{
		RequestID:         "request-skill-shutdown",
		SkillID:           "skl_skill_shutdown",
		RevisionID:        "skr_skill_shutdown",
		DownloadToken:     "test-download-token",
		DownloadExpiresAt: time.Now().Add(time.Minute).Unix(),
		Digest:            "sha256:" + strings.Repeat("0", 64),
	})

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("skill download did not start")
	}

	stopped := make(chan struct{})
	go func() {
		transport.stopAndWait(cancel)
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("socket shutdown returned while the skill worker was active")
	case <-time.After(100 * time.Millisecond):
	}

	release <- struct{}{}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("socket shutdown did not finish after the skill worker stopped")
	}
}
