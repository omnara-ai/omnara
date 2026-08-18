package machinedaemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localipc"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
	"github.com/omnara-ai/omnara/internal/processaction"
)

type reconciledAction struct {
	processID string
	action    ProcessAction
}

type localStartupState struct {
	Claims        []ProcessReconciliationClaim
	Runners       map[string]*processRuntime
	Actions       []reconciledAction
	ForcedReports map[string]struct{}
	stoppedLocks  map[string]*localstore.Lock
	fencedRunners map[string]*ipcProcessRunner
}

var errLocalStateChangedDuringRegistration = errors.New(
	"local process state changed during registration",
)
var errLocalSupervisorProbeUnavailable = errors.New(
	"local supervisor unavailable during registration",
)

func (s *localStartupState) releaseLocks() {
	for processID, lock := range s.stoppedLocks {
		_ = lock.Release()
		delete(s.stoppedLocks, processID)
	}
}

func (s *localStartupState) releaseFences() {
	for processID, runner := range s.fencedRunners {
		_ = runner.EndReconciliation()
		delete(s.fencedRunners, processID)
	}
}

func (s *localStartupState) releaseResources() {
	s.releaseFences()
	s.releaseLocks()
}

func (c *Client) scanLocalProcessesForRegistration(
	ctx context.Context,
) (localStartupState, error) {
	consecutiveProbeFailures := 0
	for {
		startup, err := c.scanLocalProcessesForRegistrationOnce(ctx)
		switch {
		case err == nil:
			return startup, nil
		case errors.Is(err, errLocalStateChangedDuringRegistration):
			consecutiveProbeFailures = 0
			if err := sleepContext(ctx, 10*time.Millisecond); err != nil {
				return localStartupState{}, err
			}
		case errors.Is(err, errLocalSupervisorProbeUnavailable):
			consecutiveProbeFailures++
			backoff := retryBackoffDelay(
				c.cfg.RetryInterval,
				30*time.Second,
				consecutiveProbeFailures,
			)
			c.log.Warn(
				"local supervisor probe failed; retrying registration snapshot",
				"error",
				err,
				"retry_after",
				backoff,
			)
			if err := sleepContext(ctx, backoff); err != nil {
				return localStartupState{}, err
			}
		default:
			return startup, err
		}
	}
}

func (c *Client) scanLocalProcessesForRegistrationOnce(
	ctx context.Context,
) (localStartupState, error) {
	startup := localStartupState{
		Claims:        make([]ProcessReconciliationClaim, 0),
		Runners:       make(map[string]*processRuntime),
		ForcedReports: make(map[string]struct{}),
		stoppedLocks:  make(map[string]*localstore.Lock),
		fencedRunners: make(map[string]*ipcProcessRunner),
	}
	stateStore, err := c.stateStore(ctx)
	if err != nil {
		return startup, err
	}
	processes, err := stateStore.Processes(ctx)
	if err != nil {
		return startup, err
	}
	machine, err := c.machineStore()
	if err != nil {
		return startup, err
	}

	failed := true
	defer func() {
		if failed {
			startup.releaseResources()
		}
	}()
	for _, process := range processes {
		runner, lock, live, err := c.inspectLocalSupervisor(
			ctx,
			machine,
			process,
		)
		if err != nil {
			return startup, err
		}
		if live {
			startup.fencedRunners[process.ProcessID] = runner
			startup.Runners[process.ProcessID] = &processRuntime{
				processID:            process.ProcessID,
				supervisorInstanceID: process.SupervisorInstanceID,
				runner:               runner,
			}
		} else if lock != nil {
			startup.stoppedLocks[process.ProcessID] = lock
		}

	}

	snapshot, err := stateStore.SnapshotForReconciliation(ctx)
	if err != nil {
		return startup, err
	}
	if len(snapshot.Processes) != len(processes) {
		return startup, fmt.Errorf(
			"%w: inventory changed",
			errLocalStateChangedDuringRegistration,
		)
	}
	for index, before := range processes {
		after := snapshot.Processes[index].Process
		sameSupervisorIdentity :=
			before.ProcessID == after.ProcessID &&
				before.SupervisorInstanceID == after.SupervisorInstanceID &&
				before.SupervisorToken == after.SupervisorToken
		if !sameSupervisorIdentity {
			return startup, fmt.Errorf(
				"%w: supervisor identity changed",
				errLocalStateChangedDuringRegistration,
			)
		}
	}
	for _, local := range snapshot.Processes {
		process := local.Process
		_, live := startup.Runners[process.ProcessID]
		for _, reportID := range local.RejectedReportIDs {
			startup.ForcedReports[reportID] = struct{}{}
		}
		claim := ProcessReconciliationClaim{
			ProcessID:             process.ProcessID,
			SupervisorInstanceID:  process.SupervisorInstanceID,
			Phase:                 process.Phase,
			SupervisorLive:        live,
			ExecutionCommitted:    process.ExecCommitted,
			ActionAdmissionClosed: process.ActionAdmissionClosed,
			ResolvedActionSeq:     process.ResolvedActionSeq,
			Actions: make(
				[]ProcessActionReconciliationClaim,
				0,
				len(local.Actions),
			),
		}
		for _, localAction := range local.Actions {
			action := localAction.Action
			position := daemonprotocol.ActionPositionEffectCommitted
			if localAction.Reported {
				position = daemonprotocol.ActionPositionTerminal
			} else if !action.EffectCommitted {
				return startup, fmt.Errorf(
					"action %s has neither effect nor terminal evidence",
					action.ID,
				)
			}
			claim.Actions = append(
				claim.Actions,
				ProcessActionReconciliationClaim{
					ProcessActionID: action.ID,
					ActionKind:      action.Kind,
					Seq:             action.Seq,
					Position:        position,
				},
			)
		}
		startup.Claims = append(startup.Claims, claim)
	}
	for processID, runner := range startup.fencedRunners {
		statusCtx, cancel := context.WithTimeout(ctx, time.Second)
		statusErr := runner.Status(statusCtx)
		cancel()
		if errors.Is(statusErr, errStorageExhaustionTerminalReady) {
			continue
		}
		if statusErr != nil {
			return startup, fmt.Errorf(
				"%w: supervisor %s ended during registration snapshot: %w",
				errLocalStateChangedDuringRegistration,
				processID,
				statusErr,
			)
		}
	}
	sort.Slice(startup.Claims, func(i, j int) bool {
		return startup.Claims[i].ProcessID < startup.Claims[j].ProcessID
	})
	failed = false
	return startup, nil
}

func (c *Client) inspectLocalSupervisor(
	ctx context.Context,
	machine localstore.MachineStore,
	process statedb.Process,
) (*ipcProcessRunner, *localstore.Lock, bool, error) {
	lockPath, err := machine.LifetimeLockPath(process.ProcessID)
	if err != nil {
		return nil, nil, false, err
	}
	lock, err := localstore.TryAcquireExistingLock(lockPath)
	switch {
	case err == nil:
		return nil, lock, false, nil
	case errors.Is(err, localstore.ErrLockHeld):
		endpoint, pathErr := machine.ControlEndpointPath(process.ProcessID)
		if pathErr != nil {
			return nil, nil, false, pathErr
		}
		runner := &ipcProcessRunner{
			endpoint:             endpoint,
			supervisorToken:      process.SupervisorToken,
			supervisorInstanceID: process.SupervisorInstanceID,
			done:                 make(chan struct{}),
		}
		probeCtx, cancel := context.WithTimeout(ctx, time.Second)
		probeErr := runner.BeginReconciliation(probeCtx)
		cancel()
		if probeErr != nil {
			if errors.Is(probeErr, errRunnerIdentityMismatch) {
				return nil, nil, false, fmt.Errorf(
					"supervisor %s failed authentication: %w",
					process.ProcessID,
					probeErr,
				)
			}
			recheck, recheckErr := localstore.TryAcquireExistingLock(
				lockPath,
			)
			switch {
			case recheckErr == nil:
				_ = recheck.Release()
				return nil, nil, false, fmt.Errorf(
					"%w: supervisor %s stopped while its reconciliation fence was opening: %w",
					errLocalStateChangedDuringRegistration,
					process.ProcessID,
					probeErr,
				)
			case errors.Is(recheckErr, os.ErrNotExist):
				return nil, nil, false, fmt.Errorf(
					"%w: supervisor %s closed while its reconciliation fence was opening: %w",
					errLocalStateChangedDuringRegistration,
					process.ProcessID,
					probeErr,
				)
			case errors.Is(recheckErr, localstore.ErrLockHeld):
				return nil, nil, false, fmt.Errorf(
					"%w: supervisor %s still holds its lifetime lock but did not answer: %w",
					errLocalSupervisorProbeUnavailable,
					process.ProcessID,
					probeErr,
				)
			default:
				return nil, nil, false, fmt.Errorf(
					"recheck supervisor %s lifetime after failed probe: %w",
					process.ProcessID,
					recheckErr,
				)
			}
		}
		return runner, nil, true, nil
	case errors.Is(err, os.ErrNotExist):
		if !process.LocalClosed &&
			(process.Phase == statedb.ProcessAccepted ||
				process.Phase == statedb.ProcessTerminal) {
			current, found, readErr := c.currentLocalProcessState(
				ctx,
				process.ProcessID,
			)
			if readErr != nil {
				return nil, nil, false, readErr
			}
			if !found ||
				current.SupervisorInstanceID != process.SupervisorInstanceID ||
				current.Phase != process.Phase ||
				current.LocalClosed != process.LocalClosed {
				return nil, nil, false, fmt.Errorf(
					"%w: supervisor %s closed while its lifetime was inspected",
					errLocalStateChangedDuringRegistration,
					process.ProcessID,
				)
			}
			return nil, nil, false, fmt.Errorf(
				"process %s in phase %s is missing its lifetime lock; stop the daemon and clear its local state",
				process.ProcessID,
				process.Phase,
			)
		}
		lock, createErr := localstore.TryAcquireLock(lockPath)
		if createErr != nil {
			return nil, nil, false, createErr
		}
		return nil, lock, false, nil
	default:
		return nil, nil, false, fmt.Errorf(
			"inspect supervisor lifetime lock for %s: %w",
			process.ProcessID,
			err,
		)
	}
}

func (c *Client) currentLocalProcessState(
	ctx context.Context,
	processID string,
) (statedb.Process, bool, error) {
	stateStore, err := c.stateStore(ctx)
	if err != nil {
		return statedb.Process{}, false, err
	}
	return stateStore.Process(ctx, processID)
}

func (c *Client) applyRegistrationReconciliation(
	ctx context.Context,
	startup *localStartupState,
	reconciliation DaemonRuntimeReconciliation,
) error {
	defer startup.releaseLocks()
	defer startup.releaseFences()
	claims := make(map[string]ProcessReconciliationClaim, len(startup.Claims))
	for _, claim := range startup.Claims {
		claims[claim.ProcessID] = claim
	}
	if len(reconciliation.Processes) != len(claims) {
		return fmt.Errorf(
			"server returned %d process directives for %d reconciliation claims",
			len(reconciliation.Processes),
			len(claims),
		)
	}
	seen := make(map[string]struct{}, len(reconciliation.Processes))
	for _, disposition := range reconciliation.Processes {
		claim, ok := claims[disposition.ProcessID]
		if !ok || disposition.SupervisorInstanceID != claim.SupervisorInstanceID {
			return fmt.Errorf(
				"server returned a disposition for unknown supervisor instance %s/%s",
				disposition.ProcessID,
				disposition.SupervisorInstanceID,
			)
		}
		if _, duplicate := seen[disposition.ProcessID]; duplicate {
			return fmt.Errorf(
				"server repeated process disposition %s",
				disposition.ProcessID,
			)
		}
		seen[disposition.ProcessID] = struct{}{}
		if err := c.applyProcessDisposition(
			ctx,
			startup,
			claim,
			disposition,
		); err != nil {
			return err
		}
	}
	c.replaceProcesses(startup.Runners)
	return nil
}

func (c *Client) applyProcessDisposition(
	ctx context.Context,
	startup *localStartupState,
	claim ProcessReconciliationClaim,
	disposition ProcessReconciliationDirective,
) error {
	stateStore, err := c.stateStore(ctx)
	if err != nil {
		return err
	}
	runner := startup.Runners[claim.ProcessID]

	switch disposition.Disposition {
	case daemonprotocol.ProcessDispositionClosePreparation:
		lock := startup.stoppedLocks[claim.ProcessID]
		delete(startup.stoppedLocks, claim.ProcessID)
		cleanupPending, err := c.closeRejectedPreparation(
			ctx,
			claim.ProcessID,
			claim.SupervisorInstanceID,
			runner,
			lock,
		)
		if cleanupPending {
			startup.Runners[claim.ProcessID] = &processRuntime{
				processID:            claim.ProcessID,
				supervisorInstanceID: claim.SupervisorInstanceID,
				cleanupOnly:          true,
			}
			c.log.Warn(
				"rejected process preparation cleanup remains pending",
				"process_id",
				claim.ProcessID,
				"error",
				err,
			)
			return nil
		}
		if err != nil {
			return err
		}
		delete(startup.Runners, claim.ProcessID)
		return nil

	case daemonprotocol.ProcessDispositionStart:
		if runner == nil {
			return fmt.Errorf(
				"server requested start for process %s without its exact live supervisor",
				claim.ProcessID,
			)
		}
		if processRuntimeStorageExhaustionReady(runner) {
			return nil
		}
		storageErr := retryWhileStateDBBusy(ctx, func() error {
			return stateStore.MarkAccepted(
				ctx,
				claim.ProcessID,
				claim.SupervisorInstanceID,
			)
		})
		if storageErr != nil && !isStorageExhaustion(storageErr) {
			return storageErr
		}
		if storageErr == nil {
			if err := runner.runner.StartOnce(ctx); err != nil {
				if errors.Is(err, errStorageExhaustionTerminalReady) {
					return nil
				}
				if !isStorageExhaustion(err) {
					return fmt.Errorf(
						"start accepted process %s: %w",
						claim.ProcessID,
						err,
					)
				}
				storageErr = err
			}
		}
		if storageErr != nil {
			terminateErr := runner.runner.Terminate(
				context.WithoutCancel(ctx),
				daemonprotocol.ProcessReasonMachineStorageExhausted,
			)
			if errors.Is(terminateErr, errStorageExhaustionTerminalReady) {
				return nil
			}
			if terminateErr != nil {
				return terminateErr
			}
			return storageErr
		}
	case daemonprotocol.ProcessDispositionRetain:

	case daemonprotocol.ProcessDispositionRelease:
		storageCleanup := processRuntimeStorageExhaustionReady(runner)
		markReleasedErr := retryWhileStateDBBusy(ctx, func() error {
			return stateStore.MarkServerReleased(
				ctx,
				claim.ProcessID,
				claim.SupervisorInstanceID,
			)
		})
		if markReleasedErr != nil &&
			!(storageCleanup && errors.Is(markReleasedErr, statedb.ErrFull)) {
			return markReleasedErr
		}
		if storageCleanup {
			terminateCtx, cancel := context.WithTimeout(
				context.WithoutCancel(ctx),
				10*time.Second,
			)
			terminateErr := runner.runner.Terminate(terminateCtx, "server_resolved")
			cancel()
			if terminateErr != nil {
				return terminateErr
			}
			runner.runner = nil
			runner.cleanupOnly = true
			return nil
		}
		reports, err := stateStore.ReportsForProcess(ctx, claim.ProcessID)
		if err != nil {
			return err
		}
		for _, report := range reports {
			startup.ForcedReports[report.ID] = struct{}{}
		}
		// Advance released actions before recovery can freeze conflicting evidence.
		for _, action := range disposition.Actions {
			if err := c.applyActionDisposition(
				ctx,
				startup,
				claim,
				action,
			); err != nil {
				return err
			}
		}
		if runner != nil {
			terminateCtx, cancel := context.WithTimeout(
				context.WithoutCancel(ctx),
				10*time.Second,
			)
			terminateErr := runner.runner.Terminate(
				terminateCtx,
				"server_resolved",
			)
			cancel()
			if terminateErr != nil {
				return fmt.Errorf(
					"terminate released process %s: %w",
					claim.ProcessID,
					terminateErr,
				)
			}
			delete(startup.Runners, claim.ProcessID)
		} else if err := c.recoverStoppedReleasedProcess(
			ctx,
			claim.ProcessID,
			claim.SupervisorInstanceID,
			startup.stoppedLocks[claim.ProcessID],
		); err != nil {
			c.log.Warn(
				"released process remains locally unresolved",
				"process_id",
				claim.ProcessID,
				"error",
				err,
			)
		}
		return nil

	default:
		return fmt.Errorf(
			"unsupported process reconciliation directive %q",
			disposition.Disposition,
		)
	}

	for _, action := range disposition.Actions {
		if err := c.applyActionDisposition(
			ctx,
			startup,
			claim,
			action,
		); err != nil {
			return err
		}
	}
	return nil
}

func processRuntimeStorageExhaustionReady(runtime *processRuntime) bool {
	if runtime == nil {
		return false
	}
	runner, ok := runtime.runner.(*ipcProcessRunner)
	return ok && runner.storageExhaustionReady()
}

func (c *Client) applyActionDisposition(
	ctx context.Context,
	startup *localStartupState,
	claim ProcessReconciliationClaim,
	disposition ProcessActionReconciliationDirective,
) error {
	stateStore, err := c.stateStore(ctx)
	if err != nil {
		return err
	}
	switch disposition.Disposition {
	case daemonprotocol.ActionDispositionRetain:
		return nil
	case daemonprotocol.ActionDispositionSettle:
		report, found, err := stateStore.ReportBySlot(
			ctx,
			statedb.ReportActionTerminal,
			claim.ProcessID,
			disposition.ProcessActionID,
		)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf(
				"server requested settlement for action %s without a frozen report",
				disposition.ProcessActionID,
			)
		}
		startup.ForcedReports[report.ID] = struct{}{}
		return nil
	case daemonprotocol.ActionDispositionRelease:
		return retryWhileStateDBBusy(ctx, func() error {
			return stateStore.ReleaseAction(
				ctx,
				claim.ProcessID,
				claim.SupervisorInstanceID,
				disposition.ProcessActionID,
				disposition.Seq,
			)
		})
	case daemonprotocol.ActionDispositionApply:
		runner := startup.Runners[claim.ProcessID]
		if runner == nil {
			return fmt.Errorf(
				"server requested %s for action %s without its exact live supervisor",
				disposition.Disposition,
				disposition.ProcessActionID,
			)
		}
		startup.Actions = append(startup.Actions, reconciledAction{
			processID: claim.ProcessID,
			action: ProcessAction{
				ID:         disposition.ProcessActionID,
				ActionKind: disposition.ActionKind,
				Seq:        disposition.Seq,
				Payload:    disposition.Payload,
			},
		})
		return nil
	default:
		return fmt.Errorf(
			"unsupported process action reconciliation directive %q",
			disposition.Disposition,
		)
	}
}

func (c *Client) recoverStoppedReleasedProcess(
	ctx context.Context,
	processID, supervisorInstanceID string,
	heldLock *localstore.Lock,
) error {
	stateStore, err := c.stateStore(ctx)
	if err != nil {
		return err
	}
	process, found, err := stateStore.Process(ctx, processID)
	if err != nil || !found {
		return err
	}
	if process.SupervisorInstanceID != supervisorInstanceID {
		return statedb.ErrSupervisorIdentityMismatch
	}
	if process.Phase == statedb.ProcessPreparing ||
		process.Phase == statedb.ProcessPrepared {
		_, err := c.closeRejectedPreparation(
			ctx,
			processID,
			supervisorInstanceID,
			nil,
			heldLock,
		)
		return err
	}

	if err := retryWhileStateDBBusy(ctx, func() error {
		return stateStore.CloseActionAdmission(ctx, processID, supervisorInstanceID)
	}); err != nil {
		return err
	}
	actions, err := stateStore.Actions(ctx, processID)
	if err != nil {
		return err
	}
	for _, action := range actions {
		if _, found, err := stateStore.ReportBySlot(
			ctx,
			statedb.ReportActionTerminal,
			processID,
			action.ID,
		); err != nil {
			return err
		} else if found {
			continue
		}
		report, err := unknownActionRecoveryReport(processID, action.ID)
		if err != nil {
			return err
		}
		if _, err := stateStore.FreezeRecoveredActionReport(
			ctx,
			processID,
			supervisorInstanceID,
			action.ID,
			report,
		); err != nil {
			return err
		}
	}

	if process.Phase != statedb.ProcessTerminal {
		report, err := c.stoppedProcessTerminalReport(process)
		if err != nil {
			return err
		}
		if _, err := stateStore.FreezeRecoveredTerminalReport(
			ctx,
			processID,
			supervisorInstanceID,
			report,
		); err != nil {
			return err
		}
	}
	if !process.ContainmentEmpty {
		switch {
		case !process.ExecCommitted:
			if err := stateStore.MarkContainmentEmpty(
				ctx,
				processID,
				supervisorInstanceID,
			); err != nil {
				return err
			}
		case process.ContainmentKind == "":
			return errors.New(
				"execution was committed without a recoverable containment identity",
			)
		default:
			containmentID, err := strconv.Atoi(process.ContainmentID)
			if err != nil {
				return fmt.Errorf(
					"parse recorded process containment: %w",
					err,
				)
			}
			closeCtx, cancel := context.WithTimeout(
				ctx,
				time.Second,
			)
			err = closeRecordedProcessContainment(
				closeCtx,
				containmentID,
				process.ContainmentKind,
			)
			cancel()
			if err != nil {
				return err
			}
			if err := stateStore.MarkContainmentEmpty(
				ctx,
				processID,
				supervisorInstanceID,
			); err != nil {
				return err
			}
		}
	}
	return stateStore.MarkRecoveredLocalClosed(ctx, processID, supervisorInstanceID)
}

func unknownActionRecoveryReport(
	processID, actionID string,
) (statedb.Report, error) {
	event := daemonReportedEvent{
		Type:               daemonprotocol.EventProcessActionUnknown,
		ProcessID:          processID,
		ProcessActionID:    actionID,
		StateReasonCode:    daemonprotocol.ProcessActionReasonOutcomeUnknown,
		StateReasonMessage: "the action crossed its effect boundary without recoverable result evidence",
	}
	body, err := json.Marshal(event)
	if err != nil {
		return statedb.Report{}, err
	}
	return statedb.Report{
		ProcessID: processID,
		ActionID:  actionID,
		Kind:      statedb.ReportActionTerminal,
		Body:      body,
	}, nil
}

func (c *Client) stoppedProcessTerminalReport(
	process statedb.Process,
) (statedb.Report, error) {
	state := daemonprotocol.ProcessStateUnknown
	reason := "local_process_unrecoverable"
	message := "the accepted process no longer has a live supervisor"
	if !process.ExecCommitted {
		state = daemonprotocol.ProcessStateFailed
		reason = "execution_not_started"
		message = "the process grant closed before execution was committed"
	}

	var (
		output    string
		cursor    int64
		next      int64
		truncated bool
	)
	if process.ExecCommitted {
		machine, err := c.machineStore()
		if err != nil {
			return statedb.Report{}, err
		}
		processDir, err := machine.ProcessDir(process.ProcessID)
		if err != nil {
			return statedb.Report{}, err
		}
		root, err := os.OpenRoot(processDir)
		if err == nil {
			output, cursor, next, truncated, err = readProcessOutputSnapshot(
				root,
				0,
				processaction.MaxObservationBytes,
			)
			_ = root.Close()
		}
		if err != nil {
			c.log.Warn(
				"recover stopped process output failed",
				"process_id",
				process.ProcessID,
				"error",
				err,
			)
			truncated = true
			reason = "local_process_and_output_unrecoverable"
			message = "the accepted process no longer has a live supervisor and its durable output could not be recovered"
		}
	}
	result, err := marshalJSON(map[string]any{
		"state":       state,
		"done":        true,
		"output":      output,
		"error":       message,
		"cursor":      cursor,
		"next_cursor": next,
		"truncated":   truncated,
	})
	if err != nil {
		return statedb.Report{}, err
	}
	event := daemonReportedEvent{
		Type:               daemonprotocol.EventProcessFinished,
		ProcessID:          process.ProcessID,
		State:              state,
		StateReasonCode:    reason,
		StateReasonMessage: message,
		Result:             result,
	}
	body, err := json.Marshal(event)
	if err != nil {
		return statedb.Report{}, err
	}
	return statedb.Report{
		ProcessID: process.ProcessID,
		Kind:      statedb.ReportProcessTerminal,
		Body:      body,
	}, nil
}

func (c *Client) closeRejectedPreparation(
	ctx context.Context,
	processID, supervisorInstanceID string,
	runtime *processRuntime,
	heldLock *localstore.Lock,
) (bool, error) {
	cleanupCtx, cancelCleanup := context.WithTimeout(
		context.WithoutCancel(ctx),
		5*time.Second,
	)
	defer cancelCleanup()

	var closeErr error
	if runtime != nil && runtime.runner != nil && !runtime.runner.IsDone() {
		closeCtx, cancel := context.WithTimeout(
			cleanupCtx,
			2*time.Second,
		)
		closeErr = runtime.runner.CloseUngranted(closeCtx)
		cancel()
		if runner, ok := runtime.runner.(*ipcProcessRunner); ok {
			closeErr = errors.Join(
				closeErr,
				runner.EndReconciliation(),
			)
		}
	}
	lock := heldLock
	if lock == nil {
		machine, err := c.machineStore()
		if err != nil {
			return false, errors.Join(closeErr, err)
		}
		lockPath, err := machine.LifetimeLockPath(processID)
		if err != nil {
			return false, errors.Join(closeErr, err)
		}
		for {
			lock, err = localstore.TryAcquireExistingLock(lockPath)
			if errors.Is(err, os.ErrNotExist) {
				lock, err = localstore.TryAcquireLock(lockPath)
			}
			if err == nil {
				break
			}
			if !errors.Is(err, localstore.ErrLockHeld) {
				return closeErr == nil && isStorageExhaustion(err), errors.Join(closeErr, err)
			}
			if err := sleepContext(cleanupCtx, 25*time.Millisecond); err != nil {
				return closeErr == nil, errors.Join(
					closeErr,
					fmt.Errorf(
						"wait for ungranted supervisor %s to stop: %w",
						processID,
						err,
					),
				)
			}
		}
	}
	defer func() { _ = lock.Release() }()

	stateStore, err := c.stateStore(cleanupCtx)
	if err != nil {
		return true, errors.Join(closeErr, err)
	}
	process, found, err := stateStore.Process(cleanupCtx, processID)
	if err != nil {
		return true, errors.Join(closeErr, err)
	}
	if found {
		if process.SupervisorInstanceID != supervisorInstanceID {
			return false, errors.Join(
				closeErr,
				statedb.ErrSupervisorIdentityMismatch,
			)
		}
		if process.ExecCommitted ||
			(process.Phase != statedb.ProcessPreparing &&
				process.Phase != statedb.ProcessPrepared) {
			return false, errors.Join(
				closeErr,
				fmt.Errorf(
					"%w: process %s is not an unexecuted preparation",
					statedb.ErrStateConflict,
					processID,
				),
			)
		}
	}
	if err := c.removeRejectedProcessArtifacts(
		processID,
		supervisorInstanceID,
	); err != nil {
		return true, errors.Join(closeErr, err)
	}
	if err := stateStore.DeleteRejectedPreparationAfterArtifacts(
		cleanupCtx,
		processID,
		supervisorInstanceID,
	); err != nil {
		if errors.Is(err, statedb.ErrSupervisorIdentityMismatch) ||
			errors.Is(err, statedb.ErrStateConflict) {
			return false, errors.Join(closeErr, err)
		}
		return true, errors.Join(closeErr, err)
	}
	if err := lock.ReleaseAndRemove(); err != nil {
		return true, errors.Join(closeErr, err)
	}
	return false, nil
}

func (c *Client) closeStorageExhaustedProcess(
	ctx context.Context,
	runtime *processRuntime,
) (bool, error) {
	if runtime.runner != nil && !runtime.runner.IsDone() {
		terminateCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			2*time.Second,
		)
		err := runtime.runner.Terminate(terminateCtx, "server_resolved")
		cancel()
		if runner, ok := runtime.runner.(*ipcProcessRunner); ok {
			err = errors.Join(err, runner.EndReconciliation())
		}
		if err != nil {
			return true, err
		}
		runtime.runner = nil
	}
	stateStore, err := c.stateStore(ctx)
	if err != nil {
		return true, err
	}
	process, found, err := stateStore.Process(ctx, runtime.processID)
	if err != nil {
		return true, err
	}
	if found && process.SupervisorInstanceID != runtime.supervisorInstanceID {
		return false, statedb.ErrSupervisorIdentityMismatch
	}
	if found && process.Phase != statedb.ProcessAccepted &&
		process.Phase != statedb.ProcessTerminal {
		return false, statedb.ErrStateConflict
	}
	machine, err := c.machineStore()
	if err != nil {
		return true, err
	}
	lockPath, err := machine.LifetimeLockPath(runtime.processID)
	if err != nil {
		return true, err
	}
	lock, err := localstore.TryAcquireExistingLock(lockPath)
	if errors.Is(err, os.ErrNotExist) {
		lock, err = localstore.TryAcquireLock(lockPath)
	}
	if err == nil {
		defer func() { _ = lock.Release() }()
	}
	if err == nil {
		err = c.removeProcessArtifacts(
			runtime.processID,
			runtime.supervisorInstanceID,
			true,
		)
	}
	if err == nil {
		err = stateStore.DeleteStorageExhaustedAfterArtifacts(
			ctx,
			runtime.processID,
			runtime.supervisorInstanceID,
		)
	}
	if err == nil {
		err = lock.ReleaseAndRemove()
	}
	permanent := errors.Is(err, statedb.ErrSupervisorIdentityMismatch) ||
		errors.Is(err, statedb.ErrStateConflict)
	return err != nil && !permanent, err
}

func (c *Client) removeRejectedProcessArtifacts(
	processID, supervisorInstanceID string,
) error {
	return c.removeProcessArtifacts(processID, supervisorInstanceID, true)
}

func (c *Client) releaseProcessRuntimeArtifacts(
	processID, supervisorInstanceID string,
) error {
	return c.removeProcessRuntimeArtifacts(processID, supervisorInstanceID, false)
}

func (c *Client) removeProcessRuntimeArtifacts(
	processID, supervisorInstanceID string,
	removeOutput bool,
) error {
	machine, err := c.machineStore()
	if err != nil {
		return err
	}
	lockPath, err := machine.LifetimeLockPath(processID)
	if err != nil {
		return err
	}
	lock, err := localstore.TryAcquireExistingLock(lockPath)
	switch {
	case err == nil:
		defer func(acquired *localstore.Lock) {
			_ = acquired.Release()
		}(lock)
	case errors.Is(err, os.ErrNotExist):
		lock = nil
	case errors.Is(err, localstore.ErrLockHeld):
		return fmt.Errorf(
			"%w: process %s supervisor still holds its lifetime lock",
			localstore.ErrLockHeld,
			processID,
		)
	default:
		return err
	}
	if err := c.removeProcessArtifacts(
		processID,
		supervisorInstanceID,
		removeOutput,
	); err != nil {
		return err
	}
	if lock != nil {
		return lock.ReleaseAndRemove()
	}
	return nil
}

func (c *Client) removeProcessArtifacts(
	processID, supervisorInstanceID string,
	removeOutput bool,
) error {
	machine, err := c.machineStore()
	if err != nil {
		return err
	}
	processDir, err := machine.ProcessDir(processID)
	if err != nil {
		return err
	}
	if removeOutput {
		if err := os.RemoveAll(processDir); err != nil {
			return fmt.Errorf("remove process artifacts: %w", err)
		}
	}
	endpoint, err := machine.ControlEndpointPath(processID)
	if err != nil {
		return err
	}
	if err := localipc.Cleanup(endpoint); err != nil {
		return err
	}
	if supervisorInstanceID != "" {
		if err := removeSupervisorIdentityBootstrap(
			machine,
			processID,
			supervisorInstanceID,
		); err != nil {
			return err
		}
	}
	if removeOutput {
		return localstore.SyncDir(filepath.Dir(processDir))
	}
	return nil
}

func (c *Client) addProcess(runtime *processRuntime) {
	if runtime == nil {
		return
	}
	c.processMu.Lock()
	current := c.processes[runtime.processID]
	if runtime.cleanupOnly &&
		current != nil &&
		!current.cleanupOnly &&
		current.supervisorInstanceID != runtime.supervisorInstanceID {
		c.processMu.Unlock()
		return
	}
	if runtime.cleanupOnly || runtime.runner == nil || !runtime.runner.IsDone() {
		c.processes[runtime.processID] = runtime
	}
	c.processMu.Unlock()
}

func (c *Client) replaceProcesses(processes map[string]*processRuntime) {
	c.processMu.Lock()
	replacement := make(map[string]*processRuntime, len(processes))
	for processID, runtime := range processes {
		if runtime != nil &&
			(runtime.cleanupOnly || runtime.runner == nil || !runtime.runner.IsDone()) {
			replacement[processID] = runtime
		}
	}
	c.processes = replacement
	c.processMu.Unlock()
}

func (c *Client) removeProcessInstance(processID, supervisorInstanceID string) {
	c.processMu.Lock()
	if runtime := c.processes[processID]; runtime != nil &&
		runtime.supervisorInstanceID == supervisorInstanceID {
		delete(c.processes, processID)
	}
	c.processMu.Unlock()
}

func (c *Client) localProcess(
	processID string,
) (*processRuntime, bool) {
	c.processMu.RLock()
	defer c.processMu.RUnlock()
	runtime, ok := c.processes[processID]
	return runtime, ok
}

func (c *Client) cleanupProcesses() []*processRuntime {
	c.processMu.RLock()
	defer c.processMu.RUnlock()
	runtimes := make([]*processRuntime, 0)
	for _, runtime := range c.processes {
		if runtime != nil && runtime.cleanupOnly {
			runtimes = append(runtimes, runtime)
		}
	}
	return runtimes
}

func retryWhileStateDBBusy(
	ctx context.Context,
	operation func() error,
) error {
	for {
		err := operation()
		if err == nil {
			return nil
		}
		if !errors.Is(err, statedb.ErrBusy) {
			return err
		}
		if err := sleepContext(ctx, 50*time.Millisecond); err != nil {
			return err
		}
	}
}
