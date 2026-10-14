package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

func completeMachineLifecycleTerminalWorkTx(
	ctx context.Context,
	unit *agentexecution.Unit,
	qtx *dbsqlc.Queries,
	orgID, machineID uuid.UUID,
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
	if err := unit.LockAgentRefs(ctx, refs, nil); err != nil {
		return err
	}
	if err := completeMachineLifecycleTerminalProcessesTx(
		ctx,
		unit,
		qtx,
		orgID,
		machineID,
		reason,
	); err != nil {
		return err
	}
	return completeMachineLifecycleTerminalQueuedProcessToolCallsTx(
		ctx,
		unit,
		qtx,
		orgID,
		machineID,
		reason,
	)
}

type executionRevokedProcessScope struct {
	projectID                 uuid.UUID
	agentID                   uuid.UUID
	projectMachineGrantID     uuid.UUID
	projectMachinePoolGrantID uuid.UUID
}

func completeExecutionRevokedProcessesTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	unit *agentexecution.Unit,
	qtx *dbsqlc.Queries,
	scope executionRevokedProcessScope,
	reason string,
) error {
	rows, err := qtx.ListProcessesForExecutionRevoked(
		ctx,
		dbsqlc.ListProcessesForExecutionRevokedParams{
			ProjectID:                 scope.projectID,
			AgentID:                   storeutil.IDFromNil(scope.agentID),
			ProjectMachineGrantID:     storeutil.IDFromNil(scope.projectMachineGrantID),
			ProjectMachinePoolGrantID: storeutil.IDFromNil(scope.projectMachinePoolGrantID),
		},
	)
	if err != nil {
		return fmt.Errorf("list processes for execution revoke: %w", err)
	}
	refs := make([]lifecyclelock.AgentRef, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, lifecyclelock.AgentRef{ProjectID: row.ProjectID, AgentID: row.AgentID})
	}
	if err := unit.LockAgentRefs(ctx, refs, nil); err != nil {
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
					StateReasonCode: storeutil.TextFromEmpty(reason),
				},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return fmt.Errorf("mark queued process failed for execution revoke: %w", err)
			}
			record := processRecordFromSQLC(failed)
			if err := settleTerminalProcess(ctx, unit, record); err != nil {
				return err
			}
		case ProcessStateStarting, ProcessStateRunning:
			txNotifications.AddDaemonProcessTermination(process.MachineID, process.ID)
			if _, err := completeProcessUnknownByMachineTx(
				ctx,
				unit,
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
				unit,
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
	unit *agentexecution.Unit,
	qtx *dbsqlc.Queries,
	orgID, machineID uuid.UUID,
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
				unit,
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
				unit,
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
	unit *agentexecution.Unit,
	qtx *dbsqlc.Queries,
	orgID, machineID uuid.UUID,
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
			failed, err := qtx.MarkQueuedProcessFailedByMachine(
				ctx,
				dbsqlc.MarkQueuedProcessFailedByMachineParams{
					ProjectID:       process.ProjectID,
					AgentID:         process.AgentID,
					ID:              process.ID,
					OrgID:           orgID,
					MachineID:       machineID,
					StateReasonCode: storeutil.TextFromEmpty(reason),
				},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return fmt.Errorf("mark queued process failed for machine delete: %w", err)
			}
			record := processRecordFromSQLC(failed)
			if err := settleTerminalProcess(ctx, unit, record); err != nil {
				return err
			}
		}
	}
}

func completeProcessUnknownByMachineTx(
	ctx context.Context,
	unit *agentexecution.Unit,
	qtx *dbsqlc.Queries,
	orgID, machineID, processID uuid.UUID,
	reason, message string,
) (bool, error) {
	if _, found, err := lockProcessAgentByMachineTx(ctx, unit, orgID, machineID, processID); err != nil {
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
			StateReasonCode:    storeutil.TextFromEmpty(reason),
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
	if err := settleTerminalProcess(ctx, unit, record); err != nil {
		return false, err
	}
	if err := completeUnresolvedProcessActionsForClosedProcessTx(
		ctx,
		unit,
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
	unit *agentexecution.Unit,
	qtx *dbsqlc.Queries,
	orgID, processID uuid.UUID,
	reason string,
) error {
	if err := completeQueuedProcessActionsFailedTx(
		ctx,
		unit,
		qtx,
		orgID,
		processID,
		reason,
	); err != nil {
		return err
	}
	return completeAcceptedProcessActionsWithoutEvidenceTx(
		ctx,
		unit,
		qtx,
		orgID,
		processID,
		reason,
	)
}

func completeQueuedProcessActionsFailedTx(
	ctx context.Context,
	unit *agentexecution.Unit,
	qtx *dbsqlc.Queries,
	orgID, processID uuid.UUID,
	reason string,
) error {
	rows, err := qtx.MarkQueuedProcessActionsFailedForProcess(
		ctx,
		dbsqlc.MarkQueuedProcessActionsFailedForProcessParams{
			OrgID:              orgID,
			ProcessID:          processID,
			StateReasonCode:    storeutil.TextFromEmpty(reason),
			StateReasonMessage: "",
		},
	)
	if err != nil {
		return fmt.Errorf("mark queued process actions failed: %w", err)
	}
	if err := settleInterruptedActions(ctx, unit, rows); err != nil {
		return err
	}
	return nil
}

func completeQueuedProcessActionsForTerminalProcessTx(
	ctx context.Context,
	unit *agentexecution.Unit,
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
	if err := settleInterruptedActions(ctx, unit, failedRows); err != nil {
		return err
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
	if err := settleInterruptedActions(ctx, unit, resolvedRows); err != nil {
		return err
	}
	return nil
}

func completeAcceptedProcessActionsWithoutEvidenceTx(
	ctx context.Context,
	unit *agentexecution.Unit,
	qtx *dbsqlc.Queries,
	orgID, processID uuid.UUID,
	reason string,
) error {
	rows, err := qtx.ResolveAcceptedProcessActionsWithoutEvidence(
		ctx,
		dbsqlc.ResolveAcceptedProcessActionsWithoutEvidenceParams{
			OrgID:              orgID,
			ProcessID:          processID,
			StateReasonCode:    storeutil.TextFromEmpty(reason),
			StateReasonMessage: "",
		},
	)
	if err != nil {
		return fmt.Errorf("mark accepted process actions unknown: %w", err)
	}
	if len(rows) > 0 {
		if err := qtx.TouchProcessActivity(ctx, dbsqlc.TouchProcessActivityParams{
			ProjectID: rows[0].ProjectID,
			AgentID:   rows[0].AgentID,
			ProcessID: rows[0].ProcessID,
		}); err != nil {
			return fmt.Errorf("touch process activity: %w", err)
		}
	}
	if err := settleInterruptedActions(ctx, unit, rows); err != nil {
		return err
	}
	return nil
}

func settleTerminalProcess(ctx context.Context, unit *agentexecution.Unit, record ProcessRecord) error {
	if record.ToolCallID == uuid.Nil {
		return nil
	}
	h, err := unit.Handle(record.ProjectID, record.AgentID)
	if err != nil {
		return err
	}
	_, err = h.CompleteProcess(ctx, agentexecution.ProcessResult{ID: record.ID})
	return err
}
func settleInterruptedActions(ctx context.Context, unit *agentexecution.Unit, rows []dbsqlc.ProcessAction) error {
	for _, row := range rows {
		record := processActionRecordFromSQLC(row)
		if record.ToolCallID == uuid.Nil {
			continue
		}
		h, err := unit.Handle(record.ProjectID, record.AgentID)
		if err != nil {
			return err
		}
		if _, err = h.SettleInterruptedAction(ctx, record.ID); err != nil {
			return err
		}
	}
	return nil
}
