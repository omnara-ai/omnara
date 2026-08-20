package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
)

func completeMachineLifecycleTerminalWorkTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	orgID, machineID ID,
	reason string,
) error {
	agents, err := qtx.ListMachineLifecycleTerminalAgentRefs(
		ctx,
		dbsqlc.ListMachineLifecycleTerminalAgentRefsParams{
			OrgID: orgID, MachineID: machineID,
		},
	)
	if err != nil {
		return fmt.Errorf("list agents for machine lifecycle termination: %w", err)
	}
	refs := make([]lifecyclelock.AgentRef, 0, len(agents))
	for _, agent := range agents {
		refs = append(refs, lifecyclelock.AgentRef{
			ProjectID: agent.ProjectID,
			AgentID:   agent.AgentID,
		})
	}
	if err := lifecyclelock.Agents(ctx, tx, refs); err != nil {
		return err
	}
	if err := completeMachineLifecycleTerminalProcessesTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		orgID,
		machineID,
		reason,
	); err != nil {
		return err
	}
	return completeMachineLifecycleTerminalQueuedProcessToolCallsTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		orgID,
		machineID,
		reason,
	)
}

type executionRevokedProcessScope struct {
	projectID                 ID
	agentID                   ID
	projectMachineGrantID     ID
	projectMachinePoolGrantID ID
}

func completeExecutionRevokedProcessesTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	scope executionRevokedProcessScope,
	reason string,
) error {
	lockParams := dbsqlc.LockAgentsForExecutionRevokedParams{
		ProjectID:                 scope.projectID,
		AgentID:                   sqlcIDFromNil(scope.agentID),
		ProjectMachineGrantID:     sqlcIDFromNil(scope.projectMachineGrantID),
		ProjectMachinePoolGrantID: sqlcIDFromNil(scope.projectMachinePoolGrantID),
	}
	if _, err := qtx.LockAgentsForExecutionRevoked(ctx, lockParams); err != nil {
		return fmt.Errorf("lock agents for execution revoke: %w", err)
	}
	rows, err := qtx.ListProcessesForExecutionRevoked(
		ctx,
		dbsqlc.ListProcessesForExecutionRevokedParams{
			ProjectID:                 scope.projectID,
			AgentID:                   sqlcIDFromNil(scope.agentID),
			ProjectMachineGrantID:     sqlcIDFromNil(scope.projectMachineGrantID),
			ProjectMachinePoolGrantID: sqlcIDFromNil(scope.projectMachinePoolGrantID),
		},
	)
	if err != nil {
		return fmt.Errorf("list processes for execution revoke: %w", err)
	}
	if err := lockAgentsForProcessesTx(ctx, qtx, rows); err != nil {
		return err
	}
	for _, row := range rows {
		process := processRecordFromSQLC(row)
		switch process.State {
		case ProcessStateQueued:
			failed, err := qtx.MarkQueuedProcessFailedByMachine(
				ctx,
				dbsqlc.MarkQueuedProcessFailedByMachineParams{
					ProjectID:       process.ProjectID,
					AgentID:         process.AgentID,
					ID:              process.ID,
					OrgID:           process.OrgID,
					MachineID:       process.MachineID,
					StateReasonCode: sqlcTextFromEmpty(reason),
				},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return fmt.Errorf("mark queued process failed for execution revoke: %w", err)
			}
			record := processRecordFromSQLC(failed)
			if err := completeProcessToolCallFromRecordTx(
				ctx,
				txNotifications,
				tx,
				qtx,
				record,
				nil,
				reason,
			); err != nil {
				return err
			}
		case ProcessStateStarting, ProcessStateRunning:
			txNotifications.AddDaemonProcessTermination(process.MachineID, process.ID)
			if _, err := completeProcessUnknownByMachineTx(
				ctx,
				txNotifications,
				tx,
				qtx,
				process.OrgID,
				process.MachineID,
				process.ID,
				reason,
				"",
			); err != nil {
				return err
			}
		case ProcessStateExited, ProcessStateFailed, ProcessStateKilled, ProcessStateUnknown:
			if err := completeUnresolvedProcessActionsForClosedProcessTx(
				ctx,
				txNotifications,
				tx,
				qtx,
				process.OrgID,
				process.ID,
				reason,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func completeMachineLifecycleTerminalProcessesTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	orgID, machineID ID,
	reason string,
) error {
	rows, err := qtx.ListProcessesForMachineLifecycleTermination(
		ctx,
		dbsqlc.ListProcessesForMachineLifecycleTerminationParams{
			OrgID: orgID, MachineID: machineID,
		},
	)
	if err != nil {
		return fmt.Errorf("list process work for machine lifecycle termination: %w", err)
	}
	for _, row := range rows {
		record := processRecordFromSQLC(row)
		switch record.State {
		case ProcessStateStarting, ProcessStateRunning:
			if _, err := completeProcessUnknownByMachineTx(
				ctx,
				txNotifications,
				tx,
				qtx,
				orgID,
				machineID,
				record.ID,
				reason,
				"",
			); err != nil {
				return err
			}
		case ProcessStateExited, ProcessStateFailed,
			ProcessStateKilled, ProcessStateUnknown:
			if err := completeUnresolvedProcessActionsForClosedProcessTx(
				ctx,
				txNotifications,
				tx,
				qtx,
				record.OrgID,
				record.ID,
				reason,
			); err != nil {
				return err
			}
		case ProcessStateQueued:
		}
	}
	return nil
}

func completeMachineLifecycleTerminalQueuedProcessToolCallsTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	orgID, machineID ID,
	reason string,
) error {
	for {
		rows, err := qtx.ListQueuedProcessToolCallsForMachineDeletion(
			ctx,
			dbsqlc.ListQueuedProcessToolCallsForMachineDeletionParams{
				OrgID:      orgID,
				MachineID:  machineID,
				LimitCount: processToolMachineUnreachableBatchSize,
			},
		)
		if err != nil {
			return fmt.Errorf("list queued process tool calls for machine delete: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			process := processRecordFromSQLC(row)
			failed, err := qtx.MarkQueuedProcessFailedByMachine(ctx, dbsqlc.MarkQueuedProcessFailedByMachineParams{
				ProjectID:       process.ProjectID,
				AgentID:         process.AgentID,
				ID:              process.ID,
				OrgID:           orgID,
				MachineID:       machineID,
				StateReasonCode: sqlcTextFromEmpty(reason),
			})
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return fmt.Errorf("mark queued process failed for machine delete: %w", err)
			}
			record := processRecordFromSQLC(failed)
			if err := completeProcessToolCallFromRecordTx(ctx, txNotifications, tx, qtx, record, nil, reason); err != nil {
				return err
			}
		}
	}
}

func completeTerminalProcessActionToolCallTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	record ProcessActionRecord,
	state ProcessActionState,
	reason string,
) error {
	applied := state == ProcessActionStateApplied
	errText := reason
	outcome := ToolResultOutcomeFailed
	if applied {
		errText = ""
		outcome = ToolResultOutcomeSucceeded
	}
	var result json.RawMessage
	if record.ActionKind == ProcessActionKindRead && !applied {
		processRow, err := qtx.GetProcessForUpdate(
			ctx,
			dbsqlc.GetProcessForUpdateParams{
				ProjectID: record.ProjectID,
				AgentID:   record.AgentID,
				ID:        record.ProcessID,
			},
		)
		if err != nil {
			return fmt.Errorf("load process for failed read result: %w", err)
		}
		result, err = canonicalProcessReadFailureResult(
			processRecordFromSQLC(processRow),
			record,
			reason,
			errText,
		)
		if err != nil {
			return err
		}
	} else {
		var err error
		result, err = processActionToolResult(
			record.ProcessID,
			record.ID,
			state,
			reason,
			errText,
		)
		if err != nil {
			return err
		}
	}
	contentParts, err := ToolResultContentParts(result)
	if err != nil {
		return err
	}
	toolRow, err := qtx.CompleteToolCallFromProcessAction(
		ctx,
		dbsqlc.CompleteToolCallFromProcessActionParams{
			ProjectID:       record.ProjectID,
			AgentID:         record.AgentID,
			ToolCallID:      record.ToolCallID,
			ProcessID:       record.ProcessID,
			ProcessActionID: record.ID,
			Outcome:         string(outcome),
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			ok, checkErr := completedToolCallMissIsBenignTx(
				ctx,
				qtx,
				record.ProjectID,
				record.AgentID,
				record.ToolCallID,
				false,
			)
			if checkErr != nil {
				return checkErr
			}
			if ok {
				return nil
			}
		}
		return fmt.Errorf("complete unknown process action tool call: %w", err)
	}
	resultRecord := toolCallRecordFromProcessActionCompleteSQLC(toolRow)
	resultRecord.ResultContentParts = contentParts
	if _, err := appendToolResultEventTx(ctx, txNotifications, tx, resultRecord); err != nil {
		return err
	}
	metadata, err := marshalJSON(
		map[string]any{
			"reason":            "process_action_" + string(state),
			"process_id":        record.ProcessID,
			"process_action_id": record.ID,
			"tool_call_id":      record.ToolCallID,
		},
	)
	if err != nil {
		return fmt.Errorf("marshal process action wakeup metadata: %w", err)
	}
	if err := qtx.MarkAgentWakeup(
		ctx,
		dbsqlc.MarkAgentWakeupParams{
			ProjectID: record.ProjectID,
			AgentID:   record.AgentID,
			Metadata:  metadata,
		},
	); err != nil {
		return fmt.Errorf("mark process action %s wakeup: %w", state, err)
	}
	return nil
}

func completeProcessToolCallFromRecordTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	record ProcessRecord,
	overrideResult json.RawMessage,
	wakeReason string,
) error {
	if isNilID(record.ToolCallID) {
		return nil
	}
	outcome, result, resultErr := processToolResult(record)
	if resultErr != nil {
		return resultErr
	}
	if len(overrideResult) > 0 && string(overrideResult) != "null" {
		var err error
		result, err = commandTerminalToolResult(record.ID, overrideResult)
		if err != nil {
			return err
		}
	}
	contentParts, err := ToolResultContentParts(result)
	if err != nil {
		return err
	}
	toolRow, err := qtx.CompleteToolCallFromProcess(
		ctx,
		dbsqlc.CompleteToolCallFromProcessParams{
			ProjectID: record.ProjectID,
			AgentID:   record.AgentID,
			ID:        record.ToolCallID,
			ProcessID: record.ID,
			Outcome:   string(outcome),
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if ok, checkErr := completedToolCallMissIsBenignTx(
				ctx,
				qtx,
				record.ProjectID,
				record.AgentID,
				record.ToolCallID,
				true,
			); checkErr != nil {
				return checkErr
			} else if ok {
				return nil
			}
		}
		return fmt.Errorf("complete process tool call: %w", err)
	}
	resultRecord := toolCallRecordFromProcessCompleteSQLC(toolRow)
	resultRecord.ResultContentParts = contentParts
	if _, err := appendToolResultEventTx(ctx, txNotifications, tx, resultRecord); err != nil {
		return err
	}
	metadata, err := marshalJSON(
		map[string]any{"reason": wakeReason, "process_id": record.ID, "tool_call_id": record.ToolCallID},
	)
	if err != nil {
		return fmt.Errorf("marshal process tool call wakeup metadata: %w", err)
	}
	if err := qtx.MarkAgentWakeup(
		ctx,
		dbsqlc.MarkAgentWakeupParams{
			ProjectID: record.ProjectID,
			AgentID:   record.AgentID,
			Metadata:  metadata,
		},
	); err != nil {
		return fmt.Errorf("mark process tool call wakeup: %w", err)
	}
	return nil
}

func completeProcessUnknownByMachineTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	orgID, machineID, processID ID,
	reason, message string,
) (bool, error) {
	if _, found, err := lockProcessAgentByMachineTx(ctx, qtx, orgID, machineID, processID); err != nil {
		return false, err
	} else if !found {
		return false, nil
	}
	row, err := qtx.MarkActiveProcessUnknownByMachine(
		ctx,
		dbsqlc.MarkActiveProcessUnknownByMachineParams{
			OrgID:              orgID,
			MachineID:          machineID,
			ID:                 processID,
			StateReasonCode:    sqlcTextFromEmpty(reason),
			StateReasonMessage: message,
		},
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("mark process unknown by machine: %w", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	record := processRecordFromSQLC(row)
	if err := completeProcessToolCallFromRecordTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		record,
		nil,
		"process_unknown",
	); err != nil {
		return false, err
	}
	if err := completeUnresolvedProcessActionsForClosedProcessTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		orgID,
		processID,
		reason,
	); err != nil {
		return false, err
	}
	return true, nil
}

func completeUnresolvedProcessActionsForClosedProcessTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	orgID, processID ID,
	reason string,
) error {
	if err := completeQueuedProcessActionsFailedTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		orgID,
		processID,
		reason,
	); err != nil {
		return err
	}
	return completeAcceptedProcessActionsWithoutEvidenceTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		orgID,
		processID,
		reason,
	)
}

func completeQueuedProcessActionsFailedTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	orgID, processID ID,
	reason string,
) error {
	rows, err := qtx.MarkQueuedProcessActionsFailedForProcess(
		ctx,
		dbsqlc.MarkQueuedProcessActionsFailedForProcessParams{
			OrgID:              orgID,
			ProcessID:          processID,
			StateReasonCode:    sqlcTextFromEmpty(reason),
			StateReasonMessage: "",
		},
	)
	if err != nil {
		return fmt.Errorf("mark queued process actions failed: %w", err)
	}
	for _, row := range rows {
		record := processActionRecordFromSQLC(row)
		if err := completeTerminalProcessActionToolCallTx(
			ctx,
			txNotifications,
			tx,
			qtx,
			record,
			ProcessActionStateFailed,
			reason,
		); err != nil {
			return err
		}
	}
	return nil
}

func completeQueuedProcessActionsForTerminalProcessTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	process ProcessRecord,
) error {
	failedRows, err := qtx.MarkQueuedMutatingProcessActionsFailedForTerminalProcess(
		ctx,
		dbsqlc.MarkQueuedMutatingProcessActionsFailedForTerminalProcessParams{
			OrgID:     process.OrgID,
			ProcessID: process.ID,
		},
	)
	if err != nil {
		return fmt.Errorf("fail queued actions for terminal process: %w", err)
	}
	for _, row := range failedRows {
		record := processActionRecordFromSQLC(row)
		if err := completeTerminalProcessActionToolCallTx(
			ctx,
			txNotifications,
			tx,
			qtx,
			record,
			ProcessActionStateFailed,
			"process_terminal",
		); err != nil {
			return err
		}
	}
	resolvedRows, err := qtx.ResolveQueuedTerminateActionsForTerminalProcess(
		ctx,
		dbsqlc.ResolveQueuedTerminateActionsForTerminalProcessParams{
			OrgID:        process.OrgID,
			ProcessID:    process.ID,
			ProcessState: string(process.State),
		},
	)
	if err != nil {
		return fmt.Errorf("resolve queued stops for terminal process: %w", err)
	}
	for _, row := range resolvedRows {
		record := processActionRecordFromSQLC(row)
		if err := completeTerminalProcessActionToolCallTx(
			ctx,
			txNotifications,
			tx,
			qtx,
			record,
			record.State,
			record.StateReasonCode,
		); err != nil {
			return err
		}
	}
	return nil
}

func completeAcceptedProcessActionsWithoutEvidenceTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	orgID, processID ID,
	reason string,
) error {
	rows, err := qtx.ResolveAcceptedProcessActionsWithoutEvidence(
		ctx,
		dbsqlc.ResolveAcceptedProcessActionsWithoutEvidenceParams{
			OrgID:              orgID,
			ProcessID:          processID,
			StateReasonCode:    sqlcTextFromEmpty(reason),
			StateReasonMessage: "",
		},
	)
	if err != nil {
		return fmt.Errorf("mark accepted process actions unknown: %w", err)
	}
	for _, row := range rows {
		record := processActionRecordFromSQLC(row)
		if err := completeTerminalProcessActionToolCallTx(
			ctx,
			txNotifications,
			tx,
			qtx,
			record,
			record.State,
			reason,
		); err != nil {
			return err
		}
	}
	return nil
}
