package executionstore

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func reconcileRegisteredRuntimeTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	input RegisterDaemonRuntimeInput,
) (DaemonRuntimeReconciliation, error) {
	claims, err := validateProcessReconciliationClaims(input.ProcessClaims)
	if err != nil {
		return DaemonRuntimeReconciliation{}, err
	}
	processes, err := lockAndLoadReconciliationProcesses(
		ctx,
		qtx,
		input.OrgID,
		input.MachineID,
		claims,
	)
	if err != nil {
		return DaemonRuntimeReconciliation{}, err
	}
	if _, err := qtx.ResetAcceptedReadProcessActionsForMachine(
		ctx,
		dbsqlc.ResetAcceptedReadProcessActionsForMachineParams{
			OrgID:     input.OrgID,
			MachineID: input.MachineID,
		},
	); err != nil {
		return DaemonRuntimeReconciliation{}, fmt.Errorf(
			"reset interrupted process reads: %w",
			err,
		)
	}

	reconciliation := DaemonRuntimeReconciliation{
		Processes: make([]ProcessReconciliationDirective, 0, len(claims)),
	}
	for processID, process := range processes {
		claim, claimed := claims[processID]
		if !claimed {
			switch process.State {
			case ProcessStateStarting, ProcessStateRunning:
				if _, err := completeProcessUnknownByMachineTx(
					ctx,
					txNotifications,
					tx,
					qtx,
					input.OrgID,
					input.MachineID,
					process.ID,
					LocalProcessMissingAfterDaemonReconnectReason,
					"",
				); err != nil {
					return DaemonRuntimeReconciliation{}, err
				}
			case ProcessStateExited, ProcessStateFailed,
				ProcessStateKilled, ProcessStateUnknown:
				if err := completeQueuedProcessActionsForTerminalProcessTx(
					ctx,
					txNotifications,
					tx,
					qtx,
					process,
				); err != nil {
					return DaemonRuntimeReconciliation{}, err
				}
				if err := completeAcceptedProcessActionsWithoutEvidenceTx(
					ctx,
					txNotifications,
					tx,
					qtx,
					process.OrgID,
					process.ID,
					LocalProcessMissingAfterDaemonReconnectReason,
				); err != nil {
					return DaemonRuntimeReconciliation{}, err
				}
			case ProcessStateQueued:
			}
			continue
		}

		disposition, err := reconcileProcessTx(
			ctx,
			txNotifications,
			tx,
			qtx,
			input,
			process,
			claim,
		)
		if err != nil {
			return DaemonRuntimeReconciliation{}, err
		}
		reconciliation.Processes = append(
			reconciliation.Processes,
			disposition,
		)
	}

	sort.Slice(reconciliation.Processes, func(i, j int) bool {
		return reconciliation.Processes[i].ProcessID.String() <
			reconciliation.Processes[j].ProcessID.String()
	})
	return reconciliation, nil
}

func validateProcessReconciliationClaims(
	input []ProcessReconciliationClaim,
) (map[ID]ProcessReconciliationClaim, error) {
	claims := make(map[ID]ProcessReconciliationClaim, len(input))
	for _, claim := range input {
		if isNilID(claim.ProcessID) || claim.SupervisorInstanceID == "" {
			return nil, errors.New(
				"process reconciliation claims require a process and supervisor instance ID",
			)
		}
		if claim.ResolvedActionSeq < 0 {
			return nil, fmt.Errorf(
				"process %s has a negative resolved action sequence",
				claim.ProcessID,
			)
		}
		switch claim.Phase {
		case daemonprotocol.ProcessPhasePreparing,
			daemonprotocol.ProcessPhasePrepared,
			daemonprotocol.ProcessPhaseAccepted,
			daemonprotocol.ProcessPhaseTerminal:
		default:
			return nil, fmt.Errorf(
				"process %s has unsupported reconciliation phase %q",
				claim.ProcessID,
				claim.Phase,
			)
		}
		if _, exists := claims[claim.ProcessID]; exists {
			return nil, fmt.Errorf(
				"process %s has duplicate reconciliation claims",
				claim.ProcessID,
			)
		}

		actionIDs := make(map[ID]struct{}, len(claim.Actions))
		actionSeqs := make(map[int64]struct{}, len(claim.Actions))
		for _, action := range claim.Actions {
			if isNilID(action.ProcessActionID) ||
				action.Seq <= claim.ResolvedActionSeq {
				return nil, fmt.Errorf(
					"process %s has invalid local action %s at sequence %d",
					claim.ProcessID,
					action.ProcessActionID,
					action.Seq,
				)
			}
			if !action.ActionKind.Mutating() {
				return nil, fmt.Errorf(
					"process %s action %s has unsupported kind %q",
					claim.ProcessID,
					action.ProcessActionID,
					action.ActionKind,
				)
			}
			switch action.Position {
			case daemonprotocol.ActionPositionEffectCommitted,
				daemonprotocol.ActionPositionTerminal:
			default:
				return nil, fmt.Errorf(
					"process %s action %s has unsupported position %q",
					claim.ProcessID,
					action.ProcessActionID,
					action.Position,
				)
			}
			if _, exists := actionIDs[action.ProcessActionID]; exists {
				return nil, fmt.Errorf(
					"process %s repeats local action %s",
					claim.ProcessID,
					action.ProcessActionID,
				)
			}
			if _, exists := actionSeqs[action.Seq]; exists {
				return nil, fmt.Errorf(
					"process %s repeats local action sequence %d",
					claim.ProcessID,
					action.Seq,
				)
			}
			actionIDs[action.ProcessActionID] = struct{}{}
			actionSeqs[action.Seq] = struct{}{}
		}
		claims[claim.ProcessID] = claim
	}
	return claims, nil
}

func lockAndLoadReconciliationProcesses(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID, machineID ID,
	claims map[ID]ProcessReconciliationClaim,
) (map[ID]ProcessRecord, error) {
	rows, err := qtx.ListProcessesForMachineReconciliation(
		ctx,
		dbsqlc.ListProcessesForMachineReconciliationParams{
			OrgID: orgID, MachineID: machineID,
		},
	)
	if err != nil {
		return nil, fmt.Errorf(
			"list machine processes before reconciliation locks: %w",
			err,
		)
	}
	keys := make([]agentLockKey, 0, len(rows)+len(claims))
	seen := make(map[ID]struct{}, len(rows)+len(claims))
	for _, row := range rows {
		record := processRecordFromSQLC(row)
		keys = append(keys, agentLockKey{
			projectID: record.ProjectID,
			agentID:   record.AgentID,
			id:        record.ID,
		})
		seen[record.ID] = struct{}{}
	}
	for processID := range claims {
		if _, exists := seen[processID]; exists {
			continue
		}
		row, err := qtx.GetProcessByMachine(
			ctx,
			dbsqlc.GetProcessByMachineParams{
				OrgID: orgID, MachineID: machineID, ID: processID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf(
				"reconciliation claim names process %s outside this machine",
				processID,
			)
		}
		if err != nil {
			return nil, fmt.Errorf(
				"load claimed process before reconciliation locks: %w",
				err,
			)
		}
		record := processRecordFromSQLC(row)
		keys = append(keys, agentLockKey{
			projectID: record.ProjectID,
			agentID:   record.AgentID,
			id:        record.ID,
		})
	}
	if err := lockAgentsForKeysTx(ctx, qtx, keys); err != nil {
		return nil, err
	}
	return loadReconciliationProcesses(
		ctx,
		qtx,
		orgID,
		machineID,
		claims,
	)
}

func loadReconciliationProcesses(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID, machineID ID,
	claims map[ID]ProcessReconciliationClaim,
) (map[ID]ProcessRecord, error) {
	rows, err := qtx.ListProcessesForMachineReconciliation(
		ctx,
		dbsqlc.ListProcessesForMachineReconciliationParams{
			OrgID: orgID, MachineID: machineID,
		},
	)
	if err != nil {
		return nil, fmt.Errorf(
			"list machine processes for reconciliation: %w",
			err,
		)
	}
	processes := make(map[ID]ProcessRecord, len(rows)+len(claims))
	for _, row := range rows {
		record := processRecordFromSQLC(row)
		processes[record.ID] = record
	}
	for processID := range claims {
		if _, exists := processes[processID]; exists {
			continue
		}
		row, err := qtx.GetProcessByMachine(
			ctx,
			dbsqlc.GetProcessByMachineParams{
				OrgID: orgID, MachineID: machineID, ID: processID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf(
				"reconciliation claim names process %s outside this machine",
				processID,
			)
		}
		if err != nil {
			return nil, fmt.Errorf(
				"load claimed process for reconciliation: %w",
				err,
			)
		}
		processes[processID] = processRecordFromSQLC(row)
	}
	return processes, nil
}

func reconcileProcessTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	input RegisterDaemonRuntimeInput,
	process ProcessRecord,
	claim ProcessReconciliationClaim,
) (ProcessReconciliationDirective, error) {
	disposition := ProcessReconciliationDirective{
		ProcessID:            process.ID,
		SupervisorInstanceID: claim.SupervisorInstanceID,
	}

	switch process.State {
	case ProcessStateQueued:
		if claim.ExecutionCommitted ||
			claim.Phase == daemonprotocol.ProcessPhaseAccepted ||
			claim.Phase == daemonprotocol.ProcessPhaseTerminal {
			return disposition, fmt.Errorf(
				"queued process %s contradicts granted local phase %q",
				process.ID,
				claim.Phase,
			)
		}
		disposition.Disposition = daemonprotocol.ProcessDispositionClosePreparation
		return disposition, nil

	case ProcessStateStarting:
		switch {
		case claim.Phase == daemonprotocol.ProcessPhasePreparing:
			return disposition, fmt.Errorf(
				"accepted process %s has only a preparing supervisor",
				process.ID,
			)
		case claim.Phase == daemonprotocol.ProcessPhaseTerminal:
			disposition.Disposition = daemonprotocol.ProcessDispositionRetain
		case claim.ExecutionCommitted && claim.SupervisorLive:
			disposition.Disposition = daemonprotocol.ProcessDispositionRetain
		case claim.ExecutionCommitted:
			if _, err := completeProcessUnknownByMachineTx(
				ctx,
				txNotifications,
				tx,
				qtx,
				input.OrgID,
				input.MachineID,
				process.ID,
				LocalProcessMissingAfterDaemonReconnectReason,
				"",
			); err != nil {
				return disposition, err
			}
			disposition.Disposition = daemonprotocol.ProcessDispositionRelease
		case claim.SupervisorLive:
			disposition.Disposition = daemonprotocol.ProcessDispositionStart
		default:
			if err := completeProcessFailedWithoutExecutionTx(
				ctx,
				txNotifications,
				tx,
				qtx,
				process,
			); err != nil {
				return disposition, err
			}
			disposition.Disposition = daemonprotocol.ProcessDispositionRelease
		}

	case ProcessStateRunning:
		switch {
		case claim.Phase == daemonprotocol.ProcessPhaseTerminal:
			disposition.Disposition = daemonprotocol.ProcessDispositionRetain
		case claim.Phase == daemonprotocol.ProcessPhaseAccepted &&
			claim.ExecutionCommitted &&
			claim.SupervisorLive:
			disposition.Disposition = daemonprotocol.ProcessDispositionRetain
		case claim.Phase == daemonprotocol.ProcessPhaseAccepted &&
			claim.ExecutionCommitted:
			if _, err := completeProcessUnknownByMachineTx(
				ctx,
				txNotifications,
				tx,
				qtx,
				input.OrgID,
				input.MachineID,
				process.ID,
				LocalProcessMissingAfterDaemonReconnectReason,
				"",
			); err != nil {
				return disposition, err
			}
			disposition.Disposition = daemonprotocol.ProcessDispositionRelease
		default:
			return disposition, fmt.Errorf(
				"running process %s contradicts local phase=%q execution_committed=%t",
				process.ID,
				claim.Phase,
				claim.ExecutionCommitted,
			)
		}

	case ProcessStateExited,
		ProcessStateFailed,
		ProcessStateKilled,
		ProcessStateUnknown:
		if !claim.ExecutionCommitted &&
			(claim.Phase == daemonprotocol.ProcessPhasePreparing ||
				claim.Phase == daemonprotocol.ProcessPhasePrepared) {
			if err := completeQueuedProcessActionsForTerminalProcessTx(
				ctx,
				txNotifications,
				tx,
				qtx,
				process,
			); err != nil {
				return disposition, err
			}
			if err := completeAcceptedProcessActionsWithoutEvidenceTx(
				ctx,
				txNotifications,
				tx,
				qtx,
				process.OrgID,
				process.ID,
				ProcessActionOutcomeUnrecoverableReason,
			); err != nil {
				return disposition, err
			}
			disposition.Disposition =
				daemonprotocol.ProcessDispositionClosePreparation
			return disposition, nil
		}
		if err := completeQueuedProcessActionsForTerminalProcessTx(
			ctx,
			txNotifications,
			tx,
			qtx,
			process,
		); err != nil {
			return disposition, err
		}
		disposition.Disposition = daemonprotocol.ProcessDispositionRelease

	default:
		return disposition, fmt.Errorf(
			"process %s has unsupported state %q",
			process.ID,
			process.State,
		)
	}

	actions, err := reconcileProcessActionsTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		input,
		process,
		claim,
		disposition.Disposition,
	)
	if err != nil {
		return disposition, err
	}
	disposition.Actions = actions
	return disposition, nil
}

func completeProcessFailedWithoutExecutionTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	process ProcessRecord,
) error {
	row, err := qtx.FailProcessBeforeExecution(
		ctx,
		dbsqlc.FailProcessBeforeExecutionParams{
			OrgID:              process.OrgID,
			MachineID:          process.MachineID,
			ID:                 process.ID,
			StateReasonCode:    sqlcTextFromEmpty(processExecutionNotStartedReason),
			StateReasonMessage: "",
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf(
				"process %s changed while resolving an unstarted grant",
				process.ID,
			)
		}
		return fmt.Errorf("resolve process that never executed: %w", err)
	}
	failed := processRecordFromCompleteSQLC(row)
	if err := completeProcessToolCallFromRecordTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		failed,
		nil,
		processExecutionNotStartedReason,
	); err != nil {
		return err
	}
	if err := completeQueuedProcessActionsFailedTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		process.OrgID,
		process.ID,
		processExecutionNotStartedReason,
	); err != nil {
		return err
	}
	return completeAcceptedProcessActionsWithoutEvidenceTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		process.OrgID,
		process.ID,
		processExecutionNotStartedReason,
	)
}

func reconcileProcessActionsTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	input RegisterDaemonRuntimeInput,
	process ProcessRecord,
	claim ProcessReconciliationClaim,
	processDisposition daemonprotocol.ProcessDisposition,
) ([]ProcessActionReconciliationDirective, error) {
	blocked, err := qtx.AcceptedProcessActionAtOrBelowResolvedSequenceExists(
		ctx,
		dbsqlc.AcceptedProcessActionAtOrBelowResolvedSequenceExistsParams{
			OrgID:             input.OrgID,
			MachineID:         input.MachineID,
			ProcessID:         process.ID,
			ResolvedActionSeq: claim.ResolvedActionSeq,
		},
	)
	if err != nil {
		return nil, fmt.Errorf(
			"check process action resolved sequence: %w",
			err,
		)
	}
	if blocked {
		return nil, fmt.Errorf(
			"process %s local action frontier %d passed a PostgreSQL-accepted action",
			process.ID,
			claim.ResolvedActionSeq,
		)
	}

	rows, err := qtx.ListProcessActionsAfterResolvedSequence(
		ctx,
		dbsqlc.ListProcessActionsAfterResolvedSequenceParams{
			OrgID:             input.OrgID,
			MachineID:         input.MachineID,
			ProcessID:         process.ID,
			ResolvedActionSeq: claim.ResolvedActionSeq,
		},
	)
	if err != nil {
		return nil, fmt.Errorf(
			"list process actions for reconciliation: %w",
			err,
		)
	}
	local := make(map[ID]ProcessActionReconciliationClaim, len(claim.Actions))
	for _, action := range claim.Actions {
		local[action.ProcessActionID] = action
	}

	dispositions := make([]ProcessActionReconciliationDirective, 0, len(rows))
	seenLocal := make(map[ID]struct{}, len(local))
	for _, row := range rows {
		action := processActionRecordFromSQLC(row)
		localAction, locallyPresent := local[action.ID]
		if locallyPresent {
			if localAction.Seq != action.Seq ||
				localAction.ActionKind != action.ActionKind {
				return nil, fmt.Errorf(
					"process %s local action %s identity differs from PostgreSQL",
					process.ID,
					action.ID,
				)
			}
			seenLocal[action.ID] = struct{}{}
		}

		disposition := ProcessActionReconciliationDirective{
			ProcessActionID: action.ID,
			Seq:             action.Seq,
			ActionKind:      action.ActionKind,
		}
		switch action.State {
		case ProcessActionStateQueued:
			if locallyPresent {
				return nil, fmt.Errorf(
					"queued process action %s has a locally committed effect",
					action.ID,
				)
			}
			continue

		case ProcessActionStateAccepted:
			if action.ActionKind == ProcessActionKindRead {
				return nil, fmt.Errorf(
					"accepted process read %s survived registration reset",
					action.ID,
				)
			}
			switch {
			case locallyPresent &&
				localAction.Position == daemonprotocol.ActionPositionTerminal:
				disposition.Disposition = daemonprotocol.ActionDispositionRetain
			case locallyPresent &&
				localAction.Position == daemonprotocol.ActionPositionEffectCommitted &&
				claim.SupervisorLive:
				disposition.Disposition = daemonprotocol.ActionDispositionRetain
			case locallyPresent:
				if err := resolveAcceptedProcessActionForReconciliationTx(
					ctx,
					txNotifications,
					tx,
					qtx,
					action,
					ProcessActionStateUnknown,
					ProcessActionOutcomeUnrecoverableReason,
				); err != nil {
					return nil, err
				}
				disposition.Disposition = daemonprotocol.ActionDispositionRelease
			case processDisposition == daemonprotocol.ProcessDispositionStart ||
				(processDisposition == daemonprotocol.ProcessDispositionRetain &&
					claim.SupervisorLive &&
					claim.Phase == daemonprotocol.ProcessPhaseAccepted &&
					!claim.ActionAdmissionClosed):
				disposition.Disposition = daemonprotocol.ActionDispositionApply
				disposition.Payload = action.Payload
			default:
				if err := resolveAcceptedProcessActionForReconciliationTx(
					ctx,
					txNotifications,
					tx,
					qtx,
					action,
					ProcessActionStateFailed,
					processActionNotDeliveredReason,
				); err != nil {
					return nil, err
				}
				disposition.Disposition = daemonprotocol.ActionDispositionRelease
			}

		case ProcessActionStateApplied,
			ProcessActionStateFailed,
			ProcessActionStateUnknown:
			switch {
			case locallyPresent &&
				localAction.Position == daemonprotocol.ActionPositionTerminal:
				disposition.Disposition = daemonprotocol.ActionDispositionSettle
			case locallyPresent &&
				localAction.Position == daemonprotocol.ActionPositionEffectCommitted &&
				claim.SupervisorLive:
				disposition.Disposition = daemonprotocol.ActionDispositionRetain
			case action.State == ProcessActionStateApplied &&
				action.ActionKind == ProcessActionKindRead &&
				!locallyPresent:
				disposition.Disposition = daemonprotocol.ActionDispositionRelease
			case action.State == ProcessActionStateApplied &&
				action.ActionKind == ProcessActionKindTerminate &&
				action.StateReasonCode == daemonprotocol.ProcessActionReasonAlreadyStopped &&
				!locallyPresent:
				continue
			case action.State == ProcessActionStateApplied && locallyPresent:
				return nil, fmt.Errorf(
					"applied process action %s has a locally committed effect but no frozen report",
					action.ID,
				)
			case action.State == ProcessActionStateApplied:
				return nil, fmt.Errorf(
					"applied process action %s is above the local frontier without frozen evidence",
					action.ID,
				)
			default:
				disposition.Disposition = daemonprotocol.ActionDispositionRelease
			}

		default:
			return nil, fmt.Errorf(
				"process action %s has unsupported state %q",
				action.ID,
				action.State,
			)
		}
		dispositions = append(dispositions, disposition)
	}

	for actionID := range local {
		if _, seen := seenLocal[actionID]; !seen {
			return nil, fmt.Errorf(
				"process %s local action %s has no matching PostgreSQL action above frontier %d",
				process.ID,
				actionID,
				claim.ResolvedActionSeq,
			)
		}
	}
	return dispositions, nil
}

func resolveAcceptedProcessActionForReconciliationTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	action ProcessActionRecord,
	state ProcessActionState,
	reason string,
) error {
	_, err := completeDaemonProcessActionTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		processActionCompletionInput{
			ProjectID:       action.ProjectID,
			AgentID:         action.AgentID,
			ProcessID:       action.ProcessID,
			ID:              action.ID,
			StateReasonCode: reason,
		},
		state,
	)
	if err != nil {
		return fmt.Errorf(
			"resolve accepted process action %s during reconciliation: %w",
			action.ID,
			err,
		)
	}
	return nil
}

func lockProcessAgentByMachineTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID, machineID, processID ID,
) (ProcessRecord, bool, error) {
	row, err := qtx.GetProcessByMachine(
		ctx,
		dbsqlc.GetProcessByMachineParams{OrgID: orgID, MachineID: machineID, ID: processID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProcessRecord{}, false, nil
	}
	if err != nil {
		return ProcessRecord{}, false, fmt.Errorf("load process before agent lock: %w", err)
	}
	record := processRecordFromSQLC(row)
	if err := lockAgentForProcessRecordTx(ctx, qtx, record); err != nil {
		return ProcessRecord{}, false, err
	}
	return record, true, nil
}

func lockAgentForProcessRecordTx(ctx context.Context, qtx *dbsqlc.Queries, record ProcessRecord) error {
	if isNilID(record.ProjectID) || isNilID(record.AgentID) {
		return nil
	}
	if _, err := qtx.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{
			ProjectID: record.ProjectID,
			ID:        record.AgentID,
		},
	); err != nil {
		return fmt.Errorf("lock agent for process authority mutation: %w", err)
	}
	return nil
}

type agentLockKey struct {
	projectID ID
	agentID   ID
	id        ID
}

func lockAgentsForKeysTx(ctx context.Context, qtx *dbsqlc.Queries, keys []agentLockKey) error {
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].projectID != keys[j].projectID {
			return keys[i].projectID.String() < keys[j].projectID.String()
		}
		if keys[i].agentID != keys[j].agentID {
			return keys[i].agentID.String() < keys[j].agentID.String()
		}
		return keys[i].id.String() < keys[j].id.String()
	})
	locked := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if isNilID(key.projectID) || isNilID(key.agentID) {
			continue
		}
		mapKey := key.projectID.String() + ":" + key.agentID.String()
		if _, ok := locked[mapKey]; ok {
			continue
		}
		if _, err := qtx.LockAgentInProject(
			ctx,
			dbsqlc.LockAgentInProjectParams{
				ProjectID: key.projectID,
				ID:        key.agentID,
			},
		); err != nil {
			return fmt.Errorf("lock agent for process action authority mutation: %w", err)
		}
		locked[mapKey] = struct{}{}
	}
	return nil
}

func lockAgentsForProcessesTx(ctx context.Context, qtx *dbsqlc.Queries, rows []dbsqlc.Process) error {
	keys := make([]agentLockKey, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, agentLockKey{projectID: row.ProjectID, agentID: row.AgentID, id: row.ID})
	}
	return lockAgentsForKeysTx(ctx, qtx, keys)
}
