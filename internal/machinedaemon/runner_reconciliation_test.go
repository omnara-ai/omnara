package machinedaemon

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localipc"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
)

func TestEmptyRegistrationSnapshotIsAnExhaustiveEmptyArray(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := New(Config{OmnaraHome: t.TempDir()}, nil, nil)
	client.bootstrap = daemonBootstrap{
		InstallationID: "ins_empty_registration",
		MachineID:      "mch_empty_registration",
	}
	defer client.closeState()
	startup, err := client.scanLocalProcessesForRegistrationOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer startup.releaseResources()
	if startup.Claims == nil || len(startup.Claims) != 0 {
		t.Fatalf("empty registration claims = %#v", startup.Claims)
	}
}

func TestReleaseTerminationFailureKeepsSupervisorForReconciliation(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	const (
		processID            = "prc_release_termination_failure"
		supervisorInstanceID = "supervisor-release-termination-failure"
	)
	client := New(Config{OmnaraHome: t.TempDir()}, nil, nil)
	client.bootstrap = daemonBootstrap{
		InstallationID: "ins_release_termination_failure",
		MachineID:      "mch_release_termination_failure",
	}
	defer client.closeState()
	store, err := client.stateStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveProcess(ctx, statedb.Process{
		ProcessID:            processID,
		SupervisorInstanceID: supervisorInstanceID,
		SupervisorToken:      "supervisor-token-release-termination-failure",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPrepared(ctx, processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAccepted(ctx, processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}

	terminationErr := errors.New("injected reconciliation termination failure")
	runtime := &processRuntime{
		processID:            processID,
		supervisorInstanceID: supervisorInstanceID,
		runner: &recordingProcessRunner{
			terminateCalls: make(chan string, 1),
			terminateErr:   terminationErr,
			done:           make(chan struct{}),
		},
	}
	startup := localStartupState{
		Runners:       map[string]*processRuntime{processID: runtime},
		ForcedReports: make(map[string]struct{}),
	}
	err = client.applyProcessDisposition(
		ctx,
		&startup,
		ProcessReconciliationClaim{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
		},
		ProcessReconciliationDirective{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
			Disposition:          daemonprotocol.ProcessDispositionRelease,
		},
	)
	if !errors.Is(err, terminationErr) {
		t.Fatalf("release error = %v", err)
	}
	if startup.Runners[processID] != runtime {
		t.Fatal("failed termination discarded the live supervisor")
	}
	process, found, err := store.Process(ctx, processID)
	if err != nil || !found || !process.ServerReleased {
		t.Fatalf(
			"released process state: found=%t process=%+v err=%v",
			found,
			process,
			err,
		)
	}
}

func TestRegistrationRejectsMissingTerminalLifetimeLock(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := New(Config{OmnaraHome: t.TempDir()}, nil, nil)
	client.bootstrap = daemonBootstrap{
		InstallationID: "ins_missing_terminal_lock",
		MachineID:      "mch_missing_terminal_lock",
	}
	defer client.closeState()
	store, err := client.stateStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const (
		processID            = "prc_missing_terminal_lock"
		supervisorInstanceID = "supervisor-instance-missing-terminal-lock"
	)
	if err := store.ReserveProcess(
		ctx,
		statedb.Process{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
			SupervisorToken:      "supervisor-token-missing-terminal-lock",
		}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPrepared(ctx, processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAccepted(ctx, processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}
	process, found, err := store.Process(ctx, processID)
	if err != nil || !found {
		t.Fatalf("read accepted process: found=%t err=%v", found, err)
	}
	report, err := client.stoppedProcessTerminalReport(process)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FreezeRecoveredTerminalReport(
		ctx,
		processID,
		supervisorInstanceID,
		report,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkServerReleased(
		ctx,
		processID,
		supervisorInstanceID,
	); err != nil {
		t.Fatal(err)
	}

	startup, err := client.scanLocalProcessesForRegistrationOnce(ctx)
	startup.releaseResources()
	if err == nil || !strings.Contains(err.Error(), "missing its lifetime lock") {
		t.Fatalf("registration missing-lock error = %v", err)
	}
}

func TestRegistrationReclaimsMissingPreparedLifetimeLock(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := New(Config{OmnaraHome: t.TempDir()}, nil, nil)
	client.bootstrap = daemonBootstrap{
		InstallationID: "ins_missing_prepared_lock",
		MachineID:      "mch_missing_prepared_lock",
	}
	defer client.closeState()
	store, err := client.stateStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const (
		processID            = "prc_missing_prepared_lock"
		supervisorInstanceID = "supervisor-instance-missing-prepared-lock"
	)
	if err := store.ReserveProcess(
		ctx,
		statedb.Process{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
			SupervisorToken:      "supervisor-token-missing-prepared-lock",
		}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPrepared(ctx, processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}

	startup, err := client.scanLocalProcessesForRegistrationOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer startup.releaseResources()
	if len(startup.Claims) != 1 ||
		startup.Claims[0].ProcessID != processID ||
		startup.Claims[0].Phase != statedb.ProcessPrepared ||
		startup.Claims[0].SupervisorLive {
		t.Fatalf("missing prepared-lock claim = %+v", startup.Claims)
	}
	if startup.stoppedLocks[processID] == nil {
		t.Fatal("missing prepared lifetime lock was not safely reacquired")
	}
}

type registrationProbeFixture struct {
	client          *Client
	store           *statedb.Store
	releaseLifetime func() error
	listener        localipc.Listener
}

func newRegistrationProbeFixture(
	t *testing.T,
	ctx context.Context,
	installationID, machineID string,
	process statedb.Process,
) registrationProbeFixture {
	t.Helper()
	client := New(Config{OmnaraHome: t.TempDir()}, nil, nil)
	client.bootstrap = daemonBootstrap{
		InstallationID: installationID,
		MachineID:      machineID,
	}
	store, err := client.stateStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.closeState() })
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
	machine, err := client.machineStore()
	if err != nil {
		t.Fatal(err)
	}
	lockPath, err := machine.LifetimeLockPath(process.ProcessID)
	if err != nil {
		t.Fatal(err)
	}
	lifetime, err := localstore.TryAcquireLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	releaseLifetime := sync.OnceValue(lifetime.ReleaseAndRemove)
	t.Cleanup(func() { _ = releaseLifetime() })
	endpoint, err := machine.ControlEndpointPath(process.ProcessID)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := localipc.Listen(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = localipc.Cleanup(endpoint)
	})
	return registrationProbeFixture{
		client:          &client,
		store:           store,
		releaseLifetime: releaseLifetime,
		listener:        listener,
	}
}

func TestRegistrationRetriesSameCountSupervisorIdentityReplacement(
	t *testing.T,
) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const (
		installationID          = "ins_registration_identity_race"
		machineID               = "mch_registration_identity_race"
		processID               = "prc_registration_identity_race"
		oldSupervisorInstanceID = "supervisor-instance-registration-identity-old"
		oldSupervisorToken      = "supervisor-token-registration-identity-old"
		newSupervisorInstanceID = "supervisor-instance-registration-identity-new"
		newSupervisorToken      = "supervisor-token-registration-identity-new"
	)
	oldProcess := statedb.Process{
		ProcessID:            processID,
		SupervisorInstanceID: oldSupervisorInstanceID,
		SupervisorToken:      oldSupervisorToken,
	}
	fixture := newRegistrationProbeFixture(
		t,
		ctx,
		installationID,
		machineID,
		oldProcess,
	)
	replacement := statedb.Process{
		ProcessID:            processID,
		SupervisorInstanceID: newSupervisorInstanceID,
		SupervisorToken:      newSupervisorToken,
	}
	serverDone := make(chan error, 1)
	// BeginReconciliation is sent only after registration reads Processes.
	// Replace before acknowledging so Snapshot must see the new supervisor.
	go func() {
		conn, acceptErr := fixture.listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer conn.Close()
		var request runnerRequest
		if err := readRunnerMessage(ctx, conn, &request); err != nil {
			serverDone <- err
			return
		}
		if request.Method != "begin_reconciliation" ||
			request.SupervisorInstanceID != oldSupervisorInstanceID ||
			request.SupervisorToken != oldSupervisorToken {
			serverDone <- unexpectedRunnerRequestError(
				string(request.Method) + "/" + request.SupervisorInstanceID,
			)
			return
		}
		if err := fixture.store.DeleteRejectedPreparationAfterArtifacts(
			ctx,
			processID,
			oldSupervisorInstanceID,
		); err != nil {
			serverDone <- err
			return
		}
		if err := fixture.store.ReserveProcess(
			ctx,
			replacement); err != nil {
			serverDone <- err
			return
		}
		if err := fixture.store.MarkPrepared(
			ctx,
			processID,
			newSupervisorInstanceID,
		); err != nil {
			serverDone <- err
			return
		}
		if err := fixture.releaseLifetime(); err != nil {
			serverDone <- err
			return
		}
		if err := writeRunnerMessage(
			ctx,
			conn,
			runnerResponse{OK: true},
		); err != nil {
			serverDone <- err
			return
		}
		for {
			if err := readRunnerMessage(ctx, conn, &request); err != nil {
				if errors.Is(err, io.EOF) {
					serverDone <- nil
				} else {
					serverDone <- err
				}
				return
			}
			if request.Method != "status" {
				serverDone <- unexpectedRunnerRequestError(request.Method)
				return
			}
			if err := writeRunnerMessage(
				ctx,
				conn,
				runnerResponse{OK: true},
			); err != nil {
				serverDone <- err
				return
			}
		}
	}()

	startup, err := fixture.client.scanLocalProcessesForRegistration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	startup.releaseResources()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stale supervisor reconciliation fence was not released")
	}
	if len(startup.Claims) != 1 ||
		startup.Claims[0].ProcessID != processID ||
		startup.Claims[0].SupervisorInstanceID != newSupervisorInstanceID ||
		startup.Claims[0].SupervisorLive {
		t.Fatalf(
			"registration claim after identity replacement = %+v",
			startup.Claims,
		)
	}
	if _, found := startup.Runners[processID]; found {
		t.Fatal("replacement process inherited the old supervisor")
	}
}

func TestRunnerReconciliationSessionFencesMutationsUntilDisconnect(
	t *testing.T,
) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const (
		installationID       = "ins_reconciliation"
		machineID            = "mch_reconciliation"
		processID            = "prc_reconciliation"
		supervisorInstanceID = "supervisor-instance-reconciliation"
		supervisorToken      = "supervisor-token-reconciliation"
	)
	root := t.TempDir()
	store, err := statedb.Open(
		ctx,
		filepath.Join(root, "state.sqlite"),
		installationID,
		machineID,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ReserveProcess(
		ctx,
		statedb.Process{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
			SupervisorToken:      supervisorToken,
		}); err != nil {
		t.Fatal(err)
	}
	supervisor, err := statedb.OpenSupervisor(
		ctx,
		filepath.Join(root, "state.sqlite"),
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

	endpoint := filepath.Join(root, "runner.sock")
	listener, err := localipc.Listen(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	defer localipc.Cleanup(endpoint)

	state := &runnerServerState{
		bootstrap: supervisorIdentityBootstrap{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
			SupervisorToken:      supervisorToken,
		},
		processState: supervisor,
		shutdown:     func() {},
		inflight:     make(map[string]*runnerActionCall),
		startedDone:  make(chan struct{}),
	}
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go serveRunnerConn(ctx, conn, state)
		}
	}()

	owner := &ipcProcessRunner{
		endpoint:             endpoint,
		supervisorToken:      supervisorToken,
		supervisorInstanceID: supervisorInstanceID,
		done:                 make(chan struct{}),
	}
	if err := owner.BeginReconciliation(ctx); err != nil {
		t.Fatal(err)
	}

	if err := owner.Terminate(ctx, "test"); err == nil {
		t.Fatal("terminate of an unstarted process unexpectedly succeeded")
	}

	outsider := &ipcProcessRunner{
		endpoint:             endpoint,
		supervisorToken:      supervisorToken,
		supervisorInstanceID: supervisorInstanceID,
		done:                 make(chan struct{}),
	}
	result := make(chan error, 1)
	go func() {
		result <- outsider.Terminate(ctx, "test")
	}()
	select {
	case err := <-result:
		t.Fatalf("mutation crossed active reconciliation fence: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	if err := owner.EndReconciliation(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("terminate of an unstarted process unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("mutation did not resume after reconciliation disconnect")
	}

	if err := store.MarkPrepared(ctx, processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAccepted(ctx, processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}
	if execute, err := supervisor.AuthorizeSpawnOnce(
		ctx,
	); err != nil || !execute {
		t.Fatalf("commit execution: execute=%t err=%v", execute, err)
	}
	if err := owner.BeginReconciliation(ctx); err != nil {
		t.Fatal(err)
	}
	parked := make(chan struct{}, 1)
	state.setFenceWriterParked(func() {
		select {
		case parked <- struct{}{}:
		default:
		}
	})
	terminalDone := make(chan struct{})
	go func() {
		defer close(terminalDone)
		state.finishStartWithoutExactOutcome(
			ctx,
			nil,
			processRunnerExit{
				State:              "failed",
				StateReasonCode:    "start_failed",
				StateReasonMessage: "test start failure",
				EndedAt:            time.Now().UTC(),
			},
			true,
		)
	}()
	select {
	case <-parked:
	case <-time.After(time.Second):
		t.Fatal("autonomous terminal write did not reach the reconciliation fence")
	}
	process, found, err := store.Process(ctx, processID)
	if err != nil || !found {
		t.Fatalf("read fenced process: found=%t err=%v", found, err)
	}
	if process.Phase != statedb.ProcessAccepted {
		t.Fatalf(
			"autonomous terminal write crossed reconciliation fence: %+v",
			process,
		)
	}
	select {
	case <-terminalDone:
		t.Fatal("autonomous terminal transition finished under the fence")
	default:
	}

	if err := owner.EndReconciliation(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-terminalDone:
	case <-time.After(time.Second):
		t.Fatal("autonomous terminal transition did not resume after the fence")
	}
	process, found, err = store.Process(ctx, processID)
	if err != nil || !found {
		t.Fatalf("read terminal process: found=%t err=%v", found, err)
	}
	if process.Phase != statedb.ProcessTerminal || !process.LocalClosed {
		t.Fatalf("terminal process after fence = %+v", process)
	}

	_ = listener.Close()
	select {
	case <-serveDone:
	case <-time.After(time.Second):
		t.Fatal("runner test server did not stop")
	}
}

func TestRegistrationRetriesSupervisorClosureDuringLivenessProbe(
	t *testing.T,
) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const (
		installationID       = "ins_registration_close_race"
		machineID            = "mch_registration_close_race"
		processID            = "prc_registration_close_race"
		supervisorInstanceID = "supervisor-instance-registration-close-race"
		supervisorToken      = "supervisor-token-registration-close-race"
	)
	process := statedb.Process{
		ProcessID:            processID,
		SupervisorInstanceID: supervisorInstanceID,
		SupervisorToken:      supervisorToken,
	}
	fixture := newRegistrationProbeFixture(
		t,
		ctx,
		installationID,
		machineID,
		process,
	)
	if err := fixture.store.MarkAccepted(
		ctx,
		processID,
		supervisorInstanceID,
	); err != nil {
		t.Fatal(err)
	}

	supervisorClosed := make(chan error, 1)
	go func() {
		conn, acceptErr := fixture.listener.Accept()
		if acceptErr != nil {
			supervisorClosed <- acceptErr
			return
		}
		defer conn.Close()
		var request runnerRequest
		if err := readRunnerMessage(ctx, conn, &request); err != nil {
			supervisorClosed <- err
			return
		}
		report, err := fixture.client.stoppedProcessTerminalReport(process)
		if err == nil {
			_, err = fixture.store.FreezeRecoveredTerminalReport(
				ctx,
				processID,
				supervisorInstanceID,
				report,
			)
		}
		if err == nil {
			err = fixture.store.MarkContainmentEmpty(
				ctx,
				processID,
				supervisorInstanceID,
			)
		}
		if err == nil {
			err = fixture.store.MarkRecoveredLocalClosed(
				ctx,
				processID,
				supervisorInstanceID,
			)
		}
		if releaseErr := fixture.releaseLifetime(); err == nil {
			err = releaseErr
		}
		supervisorClosed <- err
	}()

	startup, err := fixture.client.scanLocalProcessesForRegistration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer startup.releaseResources()
	if err := <-supervisorClosed; err != nil {
		t.Fatal(err)
	}
	if len(startup.Claims) != 1 ||
		startup.Claims[0].ProcessID != processID ||
		startup.Claims[0].Phase != statedb.ProcessTerminal ||
		startup.Claims[0].SupervisorLive {
		t.Fatalf("registration claim after supervisor closure = %+v", startup.Claims)
	}
}
