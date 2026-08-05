package machinedaemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localipc"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
	"github.com/omnara-ai/omnara/internal/processaction"
	"github.com/omnara-ai/omnara/internal/processcmd"
)

func RunCommandSupervisorFromBootstrap(
	ctx context.Context,
	bootstrapPath string,
	inheritedLockFD int,
) error {
	body, err := localstore.ReadPrivateFile(bootstrapPath)
	if err != nil {
		return err
	}
	var bootstrap supervisorIdentityBootstrap
	if err := json.Unmarshal(body, &bootstrap); err != nil {
		return fmt.Errorf("decode supervisor bootstrap: %w", err)
	}
	if err := validateSupervisorIdentityBootstrap(bootstrap); err != nil {
		return err
	}
	machine, err := localstore.Machine(
		bootstrap.OmnaraHome,
		bootstrap.InstallationID,
		bootstrap.MachineID,
	)
	if err != nil {
		return err
	}
	lockPath, err := machine.LifetimeLockPath(bootstrap.ProcessID)
	if err != nil {
		return err
	}
	lifetimeLock, err := localstore.AdoptLock(lockPath, inheritedLockFD)
	if err != nil {
		return fmt.Errorf("adopt supervisor lifetime lock: %w", err)
	}
	if err := os.Remove(bootstrapPath); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		_ = lifetimeLock.Release()
		return fmt.Errorf("remove supervisor bootstrap: %w", err)
	}
	if err := localstore.SyncDir(filepath.Dir(bootstrapPath)); err != nil {
		_ = lifetimeLock.Release()
		return fmt.Errorf("sync removed supervisor bootstrap: %w", err)
	}
	return runCommandSupervisor(ctx, bootstrap, machine, lifetimeLock)
}

func validateSupervisorIdentityBootstrap(
	bootstrap supervisorIdentityBootstrap,
) error {
	if bootstrap.OmnaraHome == "" ||
		bootstrap.InstallationID == "" ||
		bootstrap.MachineID == "" ||
		bootstrap.ProcessID == "" ||
		bootstrap.SupervisorInstanceID == "" ||
		bootstrap.SupervisorToken == "" {
		return errors.New("supervisor bootstrap identity is incomplete")
	}
	return nil
}

func runCommandSupervisor(
	ctx context.Context,
	bootstrap supervisorIdentityBootstrap,
	machine localstore.MachineStore,
	lifetimeLock *localstore.Lock,
) (resultErr error) {
	endpoint, err := machine.ControlEndpointPath(bootstrap.ProcessID)
	if err != nil {
		_ = lifetimeLock.Release()
		return err
	}
	processState, err := statedb.OpenSupervisor(
		ctx,
		machine.StateDBPath(),
		bootstrap.InstallationID,
		bootstrap.MachineID,
		bootstrap.ProcessID,
		bootstrap.SupervisorInstanceID,
		bootstrap.SupervisorToken,
	)
	if err != nil {
		_ = lifetimeLock.Release()
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, processState.Close())
	}()

	if err := localipc.Cleanup(endpoint); err != nil {
		_ = lifetimeLock.Release()
		return err
	}
	listener, err := localipc.Listen(ctx, endpoint)
	if err != nil {
		_ = lifetimeLock.Release()
		return err
	}

	shutdown := make(chan struct{})
	var shutdownOnce sync.Once
	requestShutdown := func() {
		shutdownOnce.Do(func() {
			close(shutdown)
			_ = listener.Close()
		})
	}
	state := &runnerServerState{
		bootstrap:    bootstrap,
		processState: processState,
		machine:      machine,
		inflight:     make(map[string]*runnerActionCall),
		shutdown:     requestShutdown,
		startedDone:  make(chan struct{}),
	}
	defer func() {
		_ = listener.Close()
		resultErr = errors.Join(resultErr, localipc.Cleanup(endpoint))
		if state.safeToReleaseLock() {
			resultErr = errors.Join(
				resultErr,
				lifetimeLock.ReleaseAndRemove(),
			)
		} else {
			resultErr = errors.Join(resultErr, lifetimeLock.Release())
		}
	}()

	return serveCommandSupervisor(ctx, listener, state, shutdown)
}

func serveCommandSupervisor(
	ctx context.Context,
	listener localipc.Listener,
	state *runnerServerState,
	shutdown <-chan struct{},
) (resultErr error) {
	serveCtx, cancelServe := context.WithCancel(ctx)
	var connections sync.WaitGroup
	defer func() {
		state.shutdown()
		cancelServe()
		connections.Wait()
		resultErr = errors.Join(
			resultErr,
			state.stopForSupervisorExit(ctx),
		)
	}()

	go func() {
		select {
		case <-ctx.Done():
			state.shutdown()
		case <-shutdown:
		}
	}()

	for {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			select {
			case <-shutdown:
				return nil
			default:
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return acceptErr
		}
		connections.Add(1)
		go func() {
			defer connections.Done()
			serveRunnerConn(serveCtx, conn, state)
		}()
	}
}

type runnerServerState struct {
	bootstrap    supervisorIdentityBootstrap
	processState *statedb.Supervisor
	machine      localstore.MachineStore
	shutdown     func()

	reconciliationMu sync.RWMutex

	mu              sync.RWMutex
	prepared        *localProcessRunner
	assignment      ProcessAssignment
	packageBody     []byte
	startOutcome    processStartOutcome
	localClosed     bool
	ungrantedClosed bool

	startMu          sync.Mutex
	effectBoundaryMu sync.Mutex
	inflight         map[string]*runnerActionCall
	startedDone      chan struct{}
	startedOnce      sync.Once
}

type runnerActionCall struct {
	action ProcessAction
	done   chan struct{}
	err    error
}

func serveRunnerConn(
	ctx context.Context,
	conn net.Conn,
	state *runnerServerState,
) {
	defer func() { _ = conn.Close() }()
	var request runnerRequest
	if err := readRunnerMessage(ctx, conn, &request); err != nil {
		_ = writeRunnerMessage(
			context.WithoutCancel(ctx),
			conn,
			runnerResponse{OK: false, Error: err.Error()},
		)
		return
	}
	if request.SupervisorInstanceID != state.bootstrap.SupervisorInstanceID {
		_ = writeRunnerMessage(
			context.WithoutCancel(ctx),
			conn,
			runnerResponse{
				OK:        false,
				Error:     "supervisor instance ID mismatch",
				ErrorCode: runnerErrorSupervisorIdentityMismatch,
			},
		)
		return
	}
	if request.SupervisorToken != state.bootstrap.SupervisorToken {
		_ = writeRunnerMessage(
			context.WithoutCancel(ctx),
			conn,
			runnerResponse{
				OK:        false,
				Error:     "supervisor token mismatch",
				ErrorCode: runnerErrorSupervisorIdentityMismatch,
			},
		)
		return
	}
	if request.Method == runnerMethodBeginReconciliation {
		serveRunnerReconciliationConn(ctx, conn, state, request)
		return
	}
	if runnerMethodMutates(request.Method) {
		state.reconciliationMu.RLock()
		defer state.reconciliationMu.RUnlock()
	}
	response := state.handle(ctx, request)
	_ = writeRunnerMessage(context.WithoutCancel(ctx), conn, response)
}

func serveRunnerReconciliationConn(
	ctx context.Context,
	conn net.Conn,
	state *runnerServerState,
	request runnerRequest,
) {
	state.reconciliationMu.Lock()
	defer state.reconciliationMu.Unlock()

	if err := writeRunnerMessage(
		context.WithoutCancel(ctx),
		conn,
		state.status(ctx),
	); err != nil {
		return
	}
	for {
		var next runnerRequest
		if err := readRunnerMessage(ctx, conn, &next); err != nil {
			return
		}
		if next.SupervisorInstanceID != state.bootstrap.SupervisorInstanceID ||
			next.SupervisorToken != state.bootstrap.SupervisorToken {
			_ = writeRunnerMessage(
				context.WithoutCancel(ctx),
				conn,
				runnerResponse{
					OK:        false,
					Error:     "supervisor reconciliation identity changed",
					ErrorCode: runnerErrorSupervisorIdentityMismatch,
				},
			)
			return
		}
		if next.Method == runnerMethodBeginReconciliation {
			_ = writeRunnerMessage(
				context.WithoutCancel(ctx),
				conn,
				runnerResponse{
					OK:    false,
					Error: "supervisor reconciliation is already active",
				},
			)
			return
		}
		if err := writeRunnerMessage(
			context.WithoutCancel(ctx),
			conn,
			state.handle(ctx, next),
		); err != nil {
			return
		}
	}
}

func runnerMethodMutates(method runnerMethod) bool {
	switch method {
	case runnerMethodPrepare,
		runnerMethodStartOnce,
		runnerMethodApplyOnce,
		runnerMethodCloseUngranted,
		runnerMethodTerminate:
		return true
	default:
		return false
	}
}

func (s *runnerServerState) handle(
	ctx context.Context,
	request runnerRequest,
) runnerResponse {
	switch request.Method {
	case runnerMethodStatus:
		return s.status(ctx)
	case runnerMethodPrepare:
		return s.prepare(ctx, request.Payload)
	case runnerMethodStartOnce:
		return s.startOnce(ctx)
	case runnerMethodApplyOnce:
		return s.applyOnce(ctx, request.Payload)
	case runnerMethodCloseUngranted:
		return s.closeUngranted(ctx)
	case runnerMethodTerminate:
		return s.terminate(ctx, request.Payload)
	default:
		return runnerResponse{OK: false, Error: "unsupported supervisor method"}
	}
}

func (s *runnerServerState) status(ctx context.Context) runnerResponse {
	if _, err := s.processState.Process(ctx); err != nil {
		return runnerResponse{OK: false, Error: err.Error()}
	}
	return runnerResponse{OK: true}
}

func (s *runnerServerState) prepare(
	ctx context.Context,
	body json.RawMessage,
) runnerResponse {
	var assignment ProcessAssignment
	if err := json.Unmarshal(body, &assignment); err != nil {
		return runnerResponse{OK: false, Error: err.Error()}
	}
	if assignment.ID != s.bootstrap.ProcessID {
		return runnerResponse{
			OK:    false,
			Error: "process assignment does not match supervisor",
		}
	}
	canonical, err := json.Marshal(assignment)
	if err != nil {
		return runnerResponse{OK: false, Error: err.Error()}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.prepared != nil {
		if bytes.Equal(s.packageBody, canonical) {
			return runnerResponse{OK: true}
		}
		return runnerResponse{
			OK:    false,
			Error: "supervisor package conflicts with prepared package",
		}
	}
	if s.startOutcome != "" || s.ungrantedClosed {
		return runnerResponse{
			OK:    false,
			Error: "supervisor can no longer prepare a launch package",
		}
	}
	runner, err := prepareLocalRunner(ctx, s.bootstrap, assignment)
	if err != nil {
		return runnerResponse{OK: false, Error: err.Error()}
	}
	s.prepared = runner
	s.assignment = assignment
	s.packageBody = canonical
	return runnerResponse{OK: true}
}

func (s *runnerServerState) startOnce(ctx context.Context) runnerResponse {
	s.startMu.Lock()
	defer s.startMu.Unlock()

	s.mu.RLock()
	if s.startOutcome != "" {
		outcome := s.startOutcome
		s.mu.RUnlock()
		return runnerResponse{
			OK:           true,
			StartOutcome: outcome,
		}
	}
	prepared := s.prepared
	ungrantedClosed := s.ungrantedClosed
	s.mu.RUnlock()
	if ungrantedClosed {
		return runnerResponse{
			OK:    false,
			Error: "ungranted supervisor preparation is closed",
		}
	}
	if prepared == nil {
		return runnerResponse{OK: false, Error: "supervisor is not prepared"}
	}

	startError := s.assignment.PreparationError
	if startError == "" && prepared.startErr != nil {
		startError = prepared.startErr.Error()
	}
	if startError != "" {
		_ = prepared.output.Close()
		response, exit := terminalStartResult(
			processNotStarted,
			daemonprotocol.ProcessStateFailed,
			"start_failed",
			startError,
		)
		s.rememberStartOutcome(processNotStarted)
		go s.finishStartWithoutExactOutcome(
			ctx,
			nil,
			exit,
			true,
		)
		return response
	}

	authorized, err := s.processState.AuthorizeSpawnOnce(ctx)
	if err != nil {
		return runnerResponse{OK: false, Error: err.Error()}
	}
	if !authorized {
		response, exit := terminalStartResult(
			processAmbiguous,
			daemonprotocol.ProcessStateUnknown,
			"execution_boundary_already_committed",
			"execution was already committed without live start state",
		)
		s.rememberStartOutcome(processAmbiguous)
		go s.finishStartWithoutExactOutcome(
			ctx,
			nil,
			exit,
			false,
		)
		return response
	}
	runner, outcome, startErr := startPreparedLocalRunner(prepared)
	if startErr != nil {
		state := daemonprotocol.ProcessStateFailed
		reason := "start_failed"
		if outcome == processAmbiguous {
			state = daemonprotocol.ProcessStateUnknown
			reason = "spawn_outcome_ambiguous"
		}
		response, exit := terminalStartResult(
			outcome,
			state,
			reason,
			startErr.Error(),
		)
		s.mu.Lock()
		s.prepared = runner
		s.mu.Unlock()
		s.rememberStartOutcome(outcome)
		go s.finishStartWithoutExactOutcome(
			ctx,
			runner,
			exit,
			outcome == processNotStarted,
		)
		return response
	}

	containmentID := processCommandContainmentID(runner)
	if err := s.processState.RecordSpawned(
		ctx,
		processContainmentKind,
		strconv.Itoa(containmentID),
	); err != nil {
		response, exit := terminalStartResult(
			processAmbiguous,
			daemonprotocol.ProcessStateUnknown,
			"spawn_evidence_failed",
			err.Error(),
		)
		s.mu.Lock()
		s.prepared = runner
		s.mu.Unlock()
		s.rememberStartOutcome(processAmbiguous)
		go s.finishStartWithoutExactOutcome(
			ctx,
			runner,
			exit,
			false,
		)
		return response
	}

	response := runnerResponse{
		OK:           true,
		StartOutcome: processStarted,
	}
	s.mu.Lock()
	s.prepared = runner
	s.startOutcome = processStarted
	s.mu.Unlock()
	go s.freezeInitialObservation(ctx, runner)
	go s.observeProcessExit(ctx, runner)
	if timeout := s.assignment.TimeoutSeconds; timeout > 0 {
		go s.enforceTimeout(ctx, runner, time.Duration(timeout)*time.Second)
	}
	return response
}

func (s *runnerServerState) rememberStartOutcome(
	outcome processStartOutcome,
) {
	s.mu.Lock()
	s.startOutcome = outcome
	s.mu.Unlock()
}

func terminalStartResult(
	outcome processStartOutcome,
	state daemonprotocol.ProcessState,
	reason, message string,
) (runnerResponse, processRunnerExit) {
	response := runnerResponse{
		OK:           true,
		StartOutcome: outcome,
	}
	exit := processRunnerExit{
		State:              state,
		StateReasonCode:    reason,
		StateReasonMessage: message,
	}
	return response, exit
}

func (s *runnerServerState) freezeInitialObservation(
	ctx context.Context,
	runner *localProcessRunner,
) {
	defer s.startedOnce.Do(func() { close(s.startedDone) })
	result, _, observedAt, done, err := initialProcessObservation(
		ctx,
		s.assignment,
		runner,
	)
	if err != nil || done {
		return
	}
	event := daemonReportedEvent{
		Type:       daemonprotocol.EventProcessStarted,
		ProcessID:  s.assignment.ID,
		Result:     result,
		StartedAt:  runner.startedAt.UTC(),
		ObservedAt: observedAt,
	}
	body, err := json.Marshal(event)
	if err != nil {
		return
	}
	report := statedb.Report{
		ProcessID: s.assignment.ID,
		Kind:      statedb.ReportProcessStarted,
		Body:      body,
	}
	_ = s.autonomousStateWrite(ctx, func() error {
		_, freezeErr := s.processState.FreezeStartedReport(ctx, report)
		return freezeErr
	})
}

func initialProcessObservation(
	ctx context.Context,
	assignment ProcessAssignment,
	runner *localProcessRunner,
) (json.RawMessage, int64, time.Time, bool, error) {
	wait := time.Duration(assignment.WaitMs) * time.Millisecond
	deadline := time.Now().Add(wait)
	cursor := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, 0, time.Time{}, false, err
		}
		output, start, next, truncated, observationErr := runner.Slice(
			&cursor,
			processaction.MaxObservationBytes,
		)
		done := runner.hasTerminalResult()
		if output != "" || done || observationErr != nil || wait <= 0 ||
			!time.Now().Before(deadline) {
			state := daemonprotocol.ProcessStateRunning
			errorMessage := ""
			var exitCode *int
			if observationErr != nil {
				truncated = true
				errorMessage = "process started, but its initial output could not be read: " +
					observationErr.Error()
			}
			if done {
				exit := runner.terminalResult()
				state = exit.State
				if state == "" {
					state = daemonprotocol.ProcessStateUnknown
				}
				exitCode = exit.ExitCode
				errorMessage = processTerminalErrorMessage(exit)
			}
			result := map[string]any{
				"output":      output,
				"cursor":      start,
				"next_cursor": next,
				"truncated":   truncated,
				"done":        done,
				"process_id":  assignment.ID,
				"state":       state,
				"exit_code":   exitCode,
				"error":       errorMessage,
			}
			body, err := marshalJSON(result)
			return body, next, runner.sourceTimeNow(), done, err
		}
		if err := sleepContext(ctx, 25*time.Millisecond); err != nil {
			return nil, 0, time.Time{}, false, err
		}
	}
}

func (s *runnerServerState) observeProcessExit(
	ctx context.Context,
	runner *localProcessRunner,
) {
	observationErr := waitProcessCommandLeaderExit(runner)

	// Closing admission blocks new effects but lets already-marked calls finish.
	s.reconciliationMu.RLock()
	s.effectBoundaryMu.Lock()
	_ = retrySupervisorStateWrite(ctx, func() error {
		return s.processState.CloseActionAdmission(ctx)
	})
	s.reconciliationMu.RUnlock()
	closureCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		15*time.Second,
	)
	closure := closeAndReapProcessCommand(
		closureCtx,
		runner,
		observationErr,
	)
	cancel()
	s.effectBoundaryMu.Unlock()

	exit := runner.exitFromWait(closure.WaitErr, "")
	s.finishTerminalRunner(
		ctx,
		runner,
		exit,
		closure.ContainmentErr,
		true,
	)
}

func (s *runnerServerState) finishStartWithoutExactOutcome(
	ctx context.Context,
	runner *localProcessRunner,
	exit processRunnerExit,
	containmentKnownEmpty bool,
) {
	if runner == nil {
		_ = s.autonomousStateWrite(ctx, func() error {
			return s.processState.CloseActionAdmission(ctx)
		})
		result, exit := captureTerminalResult(nil, exit, nil)
		report, err := s.processTerminalReport(exit, result, nil)
		if err != nil {
			return
		}
		_ = s.autonomousStateWrite(ctx, func() error {
			_, err := s.processState.FreezeTerminalReport(ctx, report)
			return err
		})
		if containmentKnownEmpty {
			_ = s.autonomousStateWrite(ctx, func() error {
				return s.processState.MarkContainmentEmpty(ctx)
			})
			s.autonomousLocalClosure(ctx)
		}
		return
	}

	s.reconciliationMu.RLock()
	s.effectBoundaryMu.Lock()
	_ = retrySupervisorStateWrite(ctx, func() error {
		return s.processState.CloseActionAdmission(ctx)
	})
	s.reconciliationMu.RUnlock()
	closureCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		15*time.Second,
	)
	closure := terminateAndReapProcessCommand(closureCtx, runner)
	cancel()
	s.effectBoundaryMu.Unlock()
	exit.WaitErr = errors.Join(exit.WaitErr, closure.WaitErr)
	s.finishTerminalRunner(
		ctx,
		runner,
		exit,
		closure.ContainmentErr,
		false,
	)
}

func (s *runnerServerState) finishTerminalRunner(
	ctx context.Context,
	runner *localProcessRunner,
	exit processRunnerExit,
	containmentErr error,
	waitForStarted bool,
) {
	containmentClosed := containmentErr == nil
	if containmentClosed {
		if err := releaseProcessCommand(
			context.WithoutCancel(ctx),
			runner,
		); err != nil {
			containmentErr = err
			containmentClosed = false
		}
	}

	var outputErr error
	if containmentClosed {
		outputErr = runner.waitOutputDrain()
	}
	if containmentErr != nil {
		exit.State = daemonprotocol.ProcessStateUnknown
		exit.ExitCode = nil
		exit.StateReasonCode = "containment_close_failed"
		exit.StateReasonMessage = containmentErr.Error()
		exit.WaitErr = errors.Join(exit.WaitErr, containmentErr)
		exit.EndedAt = time.Time{}
	} else if exit.EndedAt.IsZero() {
		exit.EndedAt = runner.sourceTimeNow()
	}
	result, exit := captureTerminalResult(runner, exit, outputErr)
	runner.publishTerminalResult(exit)

	if waitForStarted {
		select {
		case <-s.startedDone:
		case <-ctx.Done():
			return
		}
	}
	report, err := s.processTerminalReport(
		exit,
		result,
		runner,
	)
	if err != nil {
		return
	}
	if err := s.autonomousStateWrite(ctx, func() error {
		_, freezeErr := s.processState.FreezeTerminalReport(ctx, report)
		return freezeErr
	}); err != nil {
		return
	}

	if !containmentClosed {
		if err := s.retryContainmentClosure(ctx, runner); err != nil {
			return
		}
		containmentClosed = true
		_ = runner.waitOutputDrain()
	}
	if containmentClosed {
		if err := s.autonomousStateWrite(ctx, func() error {
			return s.processState.MarkContainmentEmpty(ctx)
		}); err != nil {
			return
		}
	}
	s.autonomousLocalClosure(ctx)
}

func recordOutputCaptureFailure(
	exit *processRunnerExit,
	err error,
) {
	if exit == nil || err == nil {
		return
	}
	if exit.StateReasonCode == "" {
		exit.StateReasonCode = "output_capture_failed"
		if exit.StateReasonMessage == "" {
			exit.StateReasonMessage = err.Error()
		}
	} else {
		detail := "output capture failed: " + err.Error()
		if exit.StateReasonMessage == "" {
			exit.StateReasonMessage = detail
		} else {
			exit.StateReasonMessage += "; " + detail
		}
	}
	exit.WaitErr = errors.Join(exit.WaitErr, err)
}

func (s *runnerServerState) retryContainmentClosure(
	ctx context.Context,
	runner *localProcessRunner,
) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		closeCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			15*time.Second,
		)
		closeErr := closeProcessCommandContainment(closeCtx, runner)
		if closeErr == nil {
			closeErr = releaseProcessCommand(closeCtx, runner)
		}
		cancel()
		if closeErr == nil {
			return nil
		}
		if err := sleepContext(ctx, time.Second); err != nil {
			return err
		}
	}
}

func (s *runnerServerState) processTerminalReport(
	exit processRunnerExit,
	result json.RawMessage,
	runner *localProcessRunner,
) (statedb.Report, error) {
	startedAt := time.Time{}
	if runner != nil {
		startedAt = runner.startedAt.UTC()
	}
	event := daemonReportedEvent{
		Type:               daemonprotocol.EventProcessFinished,
		ProcessID:          s.bootstrap.ProcessID,
		State:              exit.State,
		ExitCode:           exit.ExitCode,
		ExitSignal:         exit.ExitSignal,
		StateReasonCode:    exit.StateReasonCode,
		StateReasonMessage: exit.StateReasonMessage,
		Result:             result,
		StartedAt:          startedAt,
		EndedAt:            exit.EndedAt,
	}
	body, err := json.Marshal(event)
	if err != nil {
		return statedb.Report{}, err
	}
	return statedb.Report{
		ProcessID: s.bootstrap.ProcessID,
		Kind:      statedb.ReportProcessTerminal,
		Body:      body,
	}, nil
}

func captureTerminalResult(
	runner *localProcessRunner,
	exit processRunnerExit,
	captureErr error,
) (json.RawMessage, processRunnerExit) {
	var (
		output    string
		start     int64
		next      int64
		truncated bool
	)
	if runner != nil {
		cursor := int64(0)
		var readErr error
		output, start, next, truncated, readErr = runner.Slice(
			&cursor,
			processaction.MaxObservationBytes,
		)
		if captureErr == nil {
			captureErr = readErr
		}
	}
	if captureErr != nil {
		truncated = true
		recordOutputCaptureFailure(&exit, captureErr)
	}
	result, _ := marshalJSON(map[string]any{
		"state":       exit.State,
		"exit_code":   exit.ExitCode,
		"output":      output,
		"cursor":      start,
		"next_cursor": next,
		"truncated":   truncated,
		"done":        true,
		"error":       processTerminalErrorMessage(exit),
	})
	return result, exit
}

func (s *runnerServerState) enforceTimeout(
	ctx context.Context,
	runner *localProcessRunner,
	timeout time.Duration,
) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-runner.terminalResultSignal():
		return
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	if runner.hasTerminalResult() {
		return
	}
	runner.setTerminalOverride(processRunnerExit{
		State:              daemonprotocol.ProcessStateFailed,
		StateReasonCode:    "timeout",
		StateReasonMessage: "process exceeded its configured timeout",
	})
	terminateCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		10*time.Second,
	)
	_ = runner.terminate(terminateCtx)
	cancel()
}

func (s *runnerServerState) applyOnce(
	ctx context.Context,
	body json.RawMessage,
) runnerResponse {
	var action ProcessAction
	if err := json.Unmarshal(body, &action); err != nil {
		return runnerResponse{OK: false, Error: err.Error()}
	}
	call, first, err := s.actionCall(action)
	if err != nil {
		return runnerResponse{OK: false, Error: err.Error()}
	}
	if first {
		call.err = s.runApplyOnce(ctx, action)
		close(call.done)
		s.finishActionCall(action.ID, call)
	} else {
		select {
		case <-call.done:
		case <-ctx.Done():
			return runnerResponse{OK: false, Error: ctx.Err().Error()}
		}
	}
	if call.err != nil {
		return runnerErrorResponse(call.err)
	}
	return runnerResponse{OK: true}
}

func runnerErrorResponse(err error) runnerResponse {
	response := runnerResponse{OK: false, Error: err.Error()}
	if errors.Is(err, statedb.ErrActionBlocked) {
		response.ErrorCode = runnerErrorActionBlocked
	}
	return response
}

func (s *runnerServerState) actionCall(
	action ProcessAction,
) (*runnerActionCall, bool, error) {
	if action.ID == "" || action.Seq <= 0 || !validRunnerActionKind(action.ActionKind) {
		return nil, false, errors.New("valid process action identity is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing := s.inflight[action.ID]; existing != nil {
		if existing.action.ActionKind != action.ActionKind ||
			existing.action.Seq != action.Seq ||
			!bytes.Equal(existing.action.Payload, action.Payload) {
			return nil, false, errors.New("process action identity changed")
		}
		return existing, false, nil
	}
	call := &runnerActionCall{action: action, done: make(chan struct{})}
	s.inflight[action.ID] = call
	return call, true, nil
}

func (s *runnerServerState) finishActionCall(
	actionID string,
	call *runnerActionCall,
) {
	s.mu.Lock()
	if s.inflight[actionID] == call {
		delete(s.inflight, actionID)
	}
	s.mu.Unlock()
}

func (s *runnerServerState) runApplyOnce(
	ctx context.Context,
	action ProcessAction,
) error {
	if preflight := preflightActionResult(
		action,
		s.assignment.Process.IOMode,
	); preflight != nil {
		return s.recordActionWithoutEffect(ctx, action, *preflight)
	}
	localAction := s.localAction(action)

	s.effectBoundaryMu.Lock()
	decision, _, err := s.processState.ApplyOnce(ctx, localAction)
	if err != nil {
		s.effectBoundaryMu.Unlock()
		return err
	}
	var applied *processActionResult
	var noEffectDecision statedb.NoEffectDecision
	if decision == statedb.ApplyExecute {
		result := s.applyLiveAction(ctx, action)
		applied = &result
	} else if decision == statedb.ApplyAdmissionClosed {
		result := terminalProcessActionResult(action)
		report, reportErr := s.actionReport(action, result)
		if reportErr != nil {
			s.effectBoundaryMu.Unlock()
			return reportErr
		}
		noEffectDecision, _, err = s.processState.RecordActionWithoutEffect(
			ctx,
			localAction,
			report,
		)
		if err != nil {
			s.effectBoundaryMu.Unlock()
			return err
		}
	}
	s.effectBoundaryMu.Unlock()

	switch decision {
	case statedb.ApplyExecute:
		return s.freezeActionResult(ctx, action, *applied)
	case statedb.ApplyAlreadyReported,
		statedb.ApplyAlreadyResolved:
		return nil
	case statedb.ApplyOutcomeUnknown:
		return s.freezeActionResult(ctx, action, processActionResult{
			EventType:          daemonprotocol.EventProcessActionUnknown,
			StateReasonCode:    daemonprotocol.ProcessActionReasonOutcomeUnknown,
			StateReasonMessage: "the action crossed its effect boundary without durable result evidence",
		})
	case statedb.ApplyBlocked:
		return statedb.ErrActionBlocked
	case statedb.ApplyAdmissionClosed:
		return s.finishNoEffectDecision(
			ctx,
			action,
			noEffectDecision,
		)
	default:
		return errors.New("invalid local action decision")
	}
}

func (s *runnerServerState) recordActionWithoutEffect(
	ctx context.Context,
	action ProcessAction,
	result processActionResult,
) error {
	report, err := s.actionReport(action, result)
	if err != nil {
		return err
	}
	s.effectBoundaryMu.Lock()
	decision, _, err := s.processState.RecordActionWithoutEffect(
		ctx,
		s.localAction(action),
		report,
	)
	s.effectBoundaryMu.Unlock()
	if err != nil {
		return err
	}
	return s.finishNoEffectDecision(ctx, action, decision)
}

func (s *runnerServerState) finishNoEffectDecision(
	ctx context.Context,
	action ProcessAction,
	decision statedb.NoEffectDecision,
) error {
	switch decision {
	case statedb.NoEffectRecorded,
		statedb.NoEffectAlreadyReported,
		statedb.NoEffectAlreadyResolved:
		return nil
	case statedb.NoEffectLostToApply:
		return s.freezeActionResult(ctx, action, processActionResult{
			EventType:          daemonprotocol.EventProcessActionUnknown,
			StateReasonCode:    daemonprotocol.ProcessActionReasonOutcomeUnknown,
			StateReasonMessage: "the action crossed its effect boundary before the no-effect outcome was recorded",
		})
	case statedb.NoEffectBlocked:
		return statedb.ErrActionBlocked
	case statedb.NoEffectAdmissionClosed:
		return statedb.ErrStateConflict
	default:
		return errors.New("invalid local no-effect decision")
	}
}

func (s *runnerServerState) localAction(
	action ProcessAction,
) statedb.Action {
	return statedb.Action{
		ID:        action.ID,
		ProcessID: s.bootstrap.ProcessID,
		Kind:      action.ActionKind,
		Seq:       action.Seq,
	}
}

func (s *runnerServerState) applyLiveAction(
	ctx context.Context,
	action ProcessAction,
) processActionResult {
	s.mu.RLock()
	runner := s.prepared
	s.mu.RUnlock()
	if runner == nil || runner.cmd == nil || runner.cmd.Process == nil {
		return processActionResult{
			EventType:          daemonprotocol.EventProcessActionUnknown,
			StateReasonCode:    daemonprotocol.ProcessActionReasonRunnerUnavailable,
			StateReasonMessage: "the accepted action has no live process runner",
		}
	}
	result := processActionResult{
		EventType: daemonprotocol.EventProcessActionApplied,
	}
	if runner.hasTerminalResult() {
		return terminalProcessActionResult(action)
	}
	switch action.ActionKind {
	case daemonprotocol.ProcessActionWrite:
		payload, err := processaction.DecodeWritePayload(action.Payload)
		if err != nil {
			return failedActionResult(daemonprotocol.ProcessActionReasonInvalidPayload, err)
		}
		if payload.Data != "" {
			writeCtx, cancel := context.WithTimeout(
				ctx,
				processInputWriteTimeout,
			)
			err := runner.WriteInput(writeCtx, payload.Data)
			cancel()
			if err != nil {
				return unknownActionResult(daemonprotocol.ProcessActionReasonStdinWriteUnknown, err)
			}
		}
		if payload.CloseStdin {
			if err := runner.CloseInput(); err != nil {
				return unknownActionResult(daemonprotocol.ProcessActionReasonStdinCloseUnknown, err)
			}
		}
	case daemonprotocol.ProcessActionInterrupt:
		if err := runner.Interrupt(); err != nil {
			if errors.Is(err, errInterruptUnsupported) {
				return failedActionResult(daemonprotocol.ProcessActionReasonNotSupported, err)
			}
			return unknownActionResult(daemonprotocol.ProcessActionReasonInterruptUnknown, err)
		}
	case daemonprotocol.ProcessActionTerminate:
		if err := runner.terminateForAction(ctx); err != nil {
			return unknownActionResult(daemonprotocol.ProcessActionReasonTerminateUnknown, err)
		}
	default:
		return failedActionResult(
			daemonprotocol.ProcessActionReasonUnsupportedAction,
			errors.New("unsupported process action kind"),
		)
	}
	return result
}

func terminalProcessActionResult(action ProcessAction) processActionResult {
	if action.ActionKind != daemonprotocol.ProcessActionTerminate {
		return failedActionResult(
			daemonprotocol.ProcessActionReasonProcessNotRunning,
			errors.New("the process is already terminal"),
		)
	}
	return processActionResult{
		EventType:       daemonprotocol.EventProcessActionApplied,
		StateReasonCode: daemonprotocol.ProcessActionReasonAlreadyStopped,
	}
}

func unknownActionResult(code string, err error) processActionResult {
	return processActionResult{
		EventType:          daemonprotocol.EventProcessActionUnknown,
		StateReasonCode:    code,
		StateReasonMessage: err.Error(),
	}
}

func failedActionResult(code string, err error) processActionResult {
	return processActionResult{
		EventType:          daemonprotocol.EventProcessActionFailed,
		StateReasonCode:    code,
		StateReasonMessage: err.Error(),
	}
}

func preflightActionResult(
	action ProcessAction,
	ioMode processcmd.IOMode,
) *processActionResult {
	invalid := func(err error) *processActionResult {
		result := failedActionResult(daemonprotocol.ProcessActionReasonInvalidPayload, err)
		return &result
	}
	switch action.ActionKind {
	case daemonprotocol.ProcessActionWrite:
		payload, err := processaction.DecodeWritePayload(action.Payload)
		if err != nil {
			return invalid(err)
		}
		if payload.CloseStdin && ioMode == processcmd.IOModePTY {
			result := failedActionResult(
				daemonprotocol.ProcessActionReasonNotSupported,
				errors.New("close_stdin is not supported for PTY-backed processes"),
			)
			return &result
		}
	case daemonprotocol.ProcessActionInterrupt,
		daemonprotocol.ProcessActionTerminate:
		if err := processaction.DecodeEmptyPayload(action.Payload); err != nil {
			return invalid(err)
		}
	default:
		return invalid(fmt.Errorf(
			"unsupported process action kind %q",
			action.ActionKind,
		))
	}
	return nil
}

func (s *runnerServerState) freezeActionResult(
	ctx context.Context,
	action ProcessAction,
	result processActionResult,
) error {
	report, err := s.actionReport(action, result)
	if err != nil {
		return err
	}
	return retrySupervisorStateWrite(ctx, func() error {
		_, freezeErr := s.processState.FreezeActionReport(
			ctx,
			action.ID,
			report,
		)
		return freezeErr
	})
}

func (s *runnerServerState) actionReport(
	action ProcessAction,
	result processActionResult,
) (statedb.Report, error) {
	event := daemonReportedEvent{
		Type:               result.EventType,
		ProcessID:          s.bootstrap.ProcessID,
		ProcessActionID:    action.ID,
		StateReasonCode:    result.StateReasonCode,
		StateReasonMessage: result.StateReasonMessage,
		Result:             result.Result,
	}
	body, err := json.Marshal(event)
	if err != nil {
		return statedb.Report{}, err
	}
	return statedb.Report{
		ProcessID: s.bootstrap.ProcessID,
		ActionID:  action.ID,
		Kind:      statedb.ReportActionTerminal,
		Body:      body,
	}, nil
}

func (s *runnerServerState) closeUngranted(
	ctx context.Context,
) runnerResponse {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	process, err := s.processState.Process(ctx)
	if err != nil {
		return runnerResponse{OK: false, Error: err.Error()}
	}
	if process.ExecCommitted ||
		process.Phase == statedb.ProcessAccepted ||
		process.Phase == statedb.ProcessTerminal {
		return runnerResponse{
			OK:    false,
			Error: "execution grant has already been accepted",
		}
	}
	s.mu.Lock()
	runner := s.prepared
	s.ungrantedClosed = true
	s.mu.Unlock()
	if runner != nil {
		_ = runner.output.Close()
	}
	s.shutdown()
	return runnerResponse{OK: true}
}

func (s *runnerServerState) terminate(
	ctx context.Context,
	body json.RawMessage,
) runnerResponse {
	s.startMu.Lock()
	defer s.startMu.Unlock()

	var payload struct {
		Reason string `json:"reason"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			return runnerResponse{OK: false, Error: err.Error()}
		}
	}
	if payload.Reason == "" {
		payload.Reason = "machine_deleted"
	}
	s.mu.RLock()
	runner := s.prepared
	startOutcome := s.startOutcome
	s.mu.RUnlock()
	if runner == nil || runner.cmd == nil || runner.cmd.Process == nil {
		process, err := s.processState.Process(ctx)
		if err != nil {
			return runnerResponse{OK: false, Error: err.Error()}
		}
		if process.Phase == statedb.ProcessTerminal ||
			(startOutcome != "" && startOutcome != processStarted) {
			return runnerResponse{OK: true}
		}
		if process.Phase == statedb.ProcessAccepted &&
			!process.ExecCommitted {
			_, exit := terminalStartResult(
				processNotStarted,
				daemonprotocol.ProcessStateUnknown,
				payload.Reason,
				"the accepted process was administratively closed before execution",
			)
			s.rememberStartOutcome(processNotStarted)
			go s.finishStartWithoutExactOutcome(
				ctx,
				nil,
				exit,
				true,
			)
			return runnerResponse{OK: true}
		}
		return runnerResponse{
			OK:    false,
			Error: "process has not started",
		}
	}
	runner.setTerminalOverride(processRunnerExit{
		State:              daemonprotocol.ProcessStateUnknown,
		StateReasonCode:    payload.Reason,
		StateReasonMessage: "the local process supervisor was administratively terminated",
	})
	terminateCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		10*time.Second,
	)
	err := runner.terminate(terminateCtx)
	cancel()
	if err != nil {
		return runnerResponse{OK: false, Error: err.Error()}
	}
	return runnerResponse{OK: true}
}

func (s *runnerServerState) markLocallyClosed() {
	s.mu.Lock()
	s.localClosed = true
	s.mu.Unlock()
	s.shutdown()
}

func (s *runnerServerState) autonomousStateWrite(
	ctx context.Context,
	write func() error,
) error {
	s.reconciliationMu.RLock()
	defer s.reconciliationMu.RUnlock()
	return retrySupervisorStateWrite(ctx, write)
}

func (s *runnerServerState) autonomousLocalClosure(ctx context.Context) {
	s.reconciliationMu.RLock()
	defer s.reconciliationMu.RUnlock()
	if err := retrySupervisorStateWrite(ctx, func() error {
		return s.processState.MarkLocalClosed(ctx)
	}); err == nil {
		s.markLocallyClosed()
	}
}

func (s *runnerServerState) safeToReleaseLock() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.localClosed || s.ungrantedClosed
}

func (s *runnerServerState) stopForSupervisorExit(
	ctx context.Context,
) error {
	s.mu.RLock()
	runner := s.prepared
	locallyClosed := s.localClosed
	ungrantedClosed := s.ungrantedClosed
	s.mu.RUnlock()
	if locallyClosed || ungrantedClosed ||
		runner == nil ||
		runner.cmd == nil ||
		runner.cmd.Process == nil {
		return nil
	}
	runner.setTerminalOverride(processRunnerExit{
		State:              daemonprotocol.ProcessStateUnknown,
		StateReasonCode:    "supervisor_shutdown",
		StateReasonMessage: "the local process supervisor stopped",
	})
	stopCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		10*time.Second,
	)
	defer cancel()
	return runner.terminate(stopCtx)
}

func retrySupervisorStateWrite(
	ctx context.Context,
	write func() error,
) error {
	for {
		err := write()
		if err == nil {
			return nil
		}
		if errors.Is(err, statedb.ErrStateConflict) ||
			errors.Is(err, statedb.ErrSupervisorIdentityMismatch) {
			return err
		}
		if sleepErr := sleepContext(ctx, time.Second); sleepErr != nil {
			return errors.Join(err, sleepErr)
		}
	}
}

func validRunnerActionKind(kind daemonprotocol.ProcessActionKind) bool {
	switch kind {
	case daemonprotocol.ProcessActionWrite,
		daemonprotocol.ProcessActionInterrupt,
		daemonprotocol.ProcessActionTerminate:
		return true
	default:
		return false
	}
}

func prepareLocalRunner(
	ctx context.Context,
	bootstrap supervisorIdentityBootstrap,
	assignment ProcessAssignment,
) (*localProcessRunner, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runner := &localProcessRunner{
		terminalResultReady: make(chan struct{}),
	}
	machine, err := localstore.Machine(
		bootstrap.OmnaraHome,
		bootstrap.InstallationID,
		bootstrap.MachineID,
	)
	if err != nil {
		return nil, err
	}
	outputPath, err := machine.OutputBufferPath(assignment.ID)
	if err != nil {
		return nil, err
	}
	if err := localstore.EnsurePrivateDir(filepath.Dir(outputPath)); err != nil {
		return nil, err
	}
	runner.output.limit = defaultProcessOutputBufferBytes
	runner.output.path = outputPath
	if err := writeProcessOutputFile(outputPath, nil, 0); err != nil {
		return nil, err
	}
	if assignment.PreparationError != "" {
		return runner, nil
	}
	info, err := os.Stat(assignment.Process.Cwd)
	if err != nil {
		return preparedRunnerWithStartFailure(runner, err)
	}
	if !info.IsDir() {
		return preparedRunnerWithStartFailure(
			runner,
			errors.New("command working directory is not a directory"),
		)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	argv, err := processArgvForLocalOS(assignment.Process)
	if err != nil {
		return preparedRunnerWithStartFailure(runner, err)
	}
	ioMode, err := processcmd.NormalizeIOMode(assignment.Process.IOMode)
	if err != nil {
		return preparedRunnerWithStartFailure(runner, err)
	}
	switch ioMode {
	case processcmd.IOModePipe:
	case processcmd.IOModePTY:
		if runtime.GOOS == "windows" {
			return preparedRunnerWithStartFailure(runner, errPTYUnsupported)
		}
		runner.ptyMode = true
	}
	if err := validateProcessCommandPlatform(); err != nil {
		return preparedRunnerWithStartFailure(runner, err)
	}
	command := exec.Command( //nolint:noctx // The supervisor owns process-tree shutdown.
		argv[0],
		argv[1:]...,
	)
	command.Dir = assignment.Process.Cwd
	command.Env = processRunnerEnv(
		os.Environ(),
		os.Getenv("PATH"),
		assignment.Env,
	)
	runner.cmd = command
	return runner, nil
}

func preparedRunnerWithStartFailure(
	runner *localProcessRunner,
	err error,
) (*localProcessRunner, error) {
	runner.startErr = err
	return runner, nil
}

func startPreparedLocalRunner(
	runner *localProcessRunner,
) (*localProcessRunner, processStartOutcome, error) {
	if runner == nil || runner.cmd == nil {
		return nil, processNotStarted, errors.New("prepared command is missing")
	}
	command := runner.cmd
	if command.Env == nil {
		return nil, processNotStarted, errors.New(
			"prepared command environment is missing",
		)
	}
	if runner.ptyMode {
		if err := startPTYProcessCommand(command, runner); err != nil {
			return nil, processNotStarted, err
		}
		runner.startedAt = time.Now()
		if err := afterStartProcessCommand(runner); err != nil {
			return runner, processAmbiguous, err
		}
		return runner, processStarted, nil
	}
	if err := setupProcessCommand(command); err != nil {
		return nil, processNotStarted, err
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, processNotStarted, err
	}
	stdinFile, ok := stdin.(*os.File)
	if !ok {
		_ = stdin.Close()
		return nil, processNotStarted, errors.New(
			"process stdin pipe does not support write deadlines",
		)
	}
	runner.stdin = stdinFile
	runner.stdinOK = true
	command.Stdout = &runner.output
	command.Stderr = &runner.output
	if err := command.Start(); err != nil {
		return nil, processNotStarted, err
	}
	runner.startedAt = time.Now()
	if err := afterStartProcessCommand(runner); err != nil {
		return runner, processAmbiguous, err
	}
	return runner, processStarted, nil
}

func processArgvForLocalOS(process Process) ([]string, error) {
	return processcmd.ResolveShellCommand(
		process.Command,
		process.ShellSelector,
		runtime.GOOS,
	)
}

func runnerExitFromWait(
	waitErr error,
	reason string,
) processRunnerExit {
	state := daemonprotocol.ProcessStateExited
	message := ""
	exitCode := 0
	if waitErr != nil {
		message = waitErr.Error()
		exitSignal := exitSignalFromError(waitErr)
		if reason == "timeout" {
			state = daemonprotocol.ProcessStateFailed
		} else if exitCodeFromError(waitErr, &exitCode) {
			state = daemonprotocol.ProcessStateFailed
			if exitSignal != "" {
				reason = "signal"
				message = exitSignal
			} else if reason == "" {
				reason = "nonzero_exit"
				message = ""
			}
		} else {
			state = daemonprotocol.ProcessStateFailed
			if reason == "" {
				reason = "wait_failed"
			}
		}
	}
	var exitCodePtr *int
	if state == daemonprotocol.ProcessStateExited || exitCode != 0 {
		exitCodePtr = &exitCode
	}
	return processRunnerExit{
		State:              state,
		ExitCode:           exitCodePtr,
		ExitSignal:         exitSignalFromError(waitErr),
		StateReasonCode:    reason,
		StateReasonMessage: message,
		WaitErr:            waitErr,
	}
}
