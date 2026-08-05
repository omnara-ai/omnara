package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const processToolMachineUnreachableBatchSize int32 = 500

func (s *Store) ExpireMachineUnreachableProcessToolCallsForAllProjects(
	ctx context.Context,
	grace time.Duration,
) (int64, error) {
	graceSeconds := int32(grace / time.Second)
	total := int64(0)
	for {
		candidates, err := s.q.ListMachineUnreachableMachineCandidates(
			ctx,
			dbsqlc.ListMachineUnreachableMachineCandidatesParams{
				MachineUnreachableGraceSeconds: graceSeconds,
				LimitCount:                     processToolMachineUnreachableBatchSize,
			},
		)
		if err != nil {
			return total, fmt.Errorf("list machine-unreachable machine candidates: %w", err)
		}
		if len(candidates) == 0 {
			return total, nil
		}
		processed := int64(0)
		for _, candidate := range candidates {
			expired, err := s.expireMachineUnreachableProcessToolCallsForMachine(
				ctx,
				candidate.OrgID,
				candidate.MachineID,
				graceSeconds,
			)
			if err != nil {
				return total, err
			}
			total += expired
			processed += expired
		}
		if processed == 0 {
			return total, nil
		}
	}
}

func (s *Store) expireMachineUnreachableProcessToolCallsForMachine(
	ctx context.Context,
	orgID, machineID ID,
	graceSeconds int32,
) (int64, error) {
	total := int64(0)
	for {
		queuedProcesses, err := s.q.ListMachineUnreachableQueuedProcessToolCallsForMachine(
			ctx,
			dbsqlc.ListMachineUnreachableQueuedProcessToolCallsForMachineParams{
				OrgID:                          orgID,
				MachineID:                      machineID,
				MachineUnreachableGraceSeconds: graceSeconds,
				LimitCount:                     processToolMachineUnreachableBatchSize,
			},
		)
		if err != nil {
			return total, fmt.Errorf("list machine-unreachable queued process tool calls for machine: %w", err)
		}
		acceptedProcesses, err := s.q.ListMachineUnreachableAcceptedProcessToolCallsForMachine(
			ctx,
			dbsqlc.ListMachineUnreachableAcceptedProcessToolCallsForMachineParams{
				OrgID:                          orgID,
				MachineID:                      machineID,
				MachineUnreachableGraceSeconds: graceSeconds,
				LimitCount:                     processToolMachineUnreachableBatchSize,
			},
		)
		if err != nil {
			return total, fmt.Errorf("list machine-unreachable accepted process tool calls for machine: %w", err)
		}
		queuedActions, err := s.q.ListMachineUnreachableQueuedProcessActionToolCallsForMachine(
			ctx,
			dbsqlc.ListMachineUnreachableQueuedProcessActionToolCallsForMachineParams{
				OrgID:                          orgID,
				MachineID:                      machineID,
				MachineUnreachableGraceSeconds: graceSeconds,
				LimitCount:                     processToolMachineUnreachableBatchSize,
			},
		)
		if err != nil {
			return total, fmt.Errorf("list machine-unreachable queued process action tool calls for machine: %w", err)
		}
		acceptedActions, err := s.q.ListMachineUnreachableAcceptedProcessActionToolCallsForMachine(
			ctx,
			dbsqlc.ListMachineUnreachableAcceptedProcessActionToolCallsForMachineParams{
				OrgID:                          orgID,
				MachineID:                      machineID,
				MachineUnreachableGraceSeconds: graceSeconds,
				LimitCount:                     processToolMachineUnreachableBatchSize,
			},
		)
		if err != nil {
			return total, fmt.Errorf("list machine-unreachable accepted process action tool calls for machine: %w", err)
		}
		if len(queuedProcesses) == 0 && len(acceptedProcesses) == 0 && len(queuedActions) == 0 &&
			len(acceptedActions) == 0 {
			return total, nil
		}
		for _, row := range queuedProcesses {
			process := processRecordFromSQLC(row)
			expired, err := s.failMachineUnreachableQueuedProcess(ctx, process, graceSeconds)
			if err != nil {
				return total, err
			}
			if expired {
				total++
			}
		}
		for _, row := range acceptedProcesses {
			process := processRecordFromSQLC(row)
			completed, err := s.completeMachineUnreachableProcessToolCall(ctx, process, graceSeconds)
			if err != nil {
				return total, err
			}
			if completed {
				total++
			}
		}
		for _, row := range queuedActions {
			action := processActionRecordFromSQLC(row)
			expired, err := s.failMachineUnreachableQueuedProcessActions(
				ctx,
				orgID,
				machineID,
				action,
				graceSeconds,
			)
			if err != nil {
				return total, err
			}
			total += expired
		}
		for _, row := range acceptedActions {
			action := processActionRecordFromSQLC(row)
			completed, err := s.completeMachineUnreachableProcessActionToolCall(
				ctx,
				orgID,
				machineID,
				action,
				graceSeconds,
			)
			if err != nil {
				return total, err
			}
			if completed {
				total++
			}
		}
		if len(queuedProcesses) < int(processToolMachineUnreachableBatchSize) &&
			len(acceptedProcesses) < int(processToolMachineUnreachableBatchSize) &&
			len(queuedActions) < int(processToolMachineUnreachableBatchSize) &&
			len(acceptedActions) < int(processToolMachineUnreachableBatchSize) {
			return total, nil
		}
	}
}

func (s *Store) failMachineUnreachableQueuedProcess(
	ctx context.Context,
	process ProcessRecord,
	graceSeconds int32,
) (bool, error) {
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin machine-unreachable queued process expiry: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	unreachable, err := machineStillUnreachableForToolExpiryTx(
		ctx,
		qtx,
		process.OrgID,
		process.MachineID,
		process.CreatedAt,
		graceSeconds,
	)
	if err != nil {
		return false, err
	}
	if !unreachable {
		if err := s.commitTxWithNotifications(
			ctx,
			tx,
			txNotifications,
			"skipped machine-unreachable queued process expiry",
		); err != nil {
			return false, err
		}
		return false, nil
	}
	if _, err := qtx.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{
			ProjectID: process.ProjectID,
			ID:        process.AgentID,
		},
	); err != nil {
		return false, fmt.Errorf("lock agent for machine-unreachable queued process expiry: %w", err)
	}
	row, err := qtx.MarkQueuedProcessFailedByMachine(
		ctx,
		dbsqlc.MarkQueuedProcessFailedByMachineParams{
			ProjectID:       process.ProjectID,
			AgentID:         process.AgentID,
			ID:              process.ID,
			OrgID:           process.OrgID,
			MachineID:       process.MachineID,
			StateReasonCode: sqlcTextFromEmpty(ProcessToolReasonMachineUnreachable),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := s.commitTxWithNotifications(
			ctx,
			tx,
			txNotifications,
			"missed machine-unreachable queued process expiry",
		); err != nil {
			return false, err
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("mark machine-unreachable queued process failed: %w", err)
	}
	record := processRecordFromSQLC(row)
	if err := completeProcessToolCallFromRecordTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		record,
		nil,
		ProcessToolReasonMachineUnreachable,
	); err != nil {
		return false, err
	}
	if err := s.commitTxWithNotifications(
		ctx,
		tx,
		txNotifications,
		"machine-unreachable queued process expiry",
	); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) FailQueuedProcessAfterWakeFailure(
	ctx context.Context,
	process ProcessRecord,
) (bool, error) {
	if isNilID(process.ID) || isNilID(process.OrgID) || isNilID(process.ProjectID) ||
		isNilID(process.AgentID) || isNilID(process.MachineID) {
		return false, errors.New("process, org, project, agent, and machine are required")
	}
	return s.failMachineUnreachableQueuedProcess(ctx, process, 0)
}

func (s *Store) FailQueuedProcessActionsAfterWakeFailure(
	ctx context.Context,
	process ProcessRecord,
	action ProcessActionRecord,
) (bool, error) {
	if action.OrgID != process.OrgID || action.ProjectID != process.ProjectID ||
		action.AgentID != process.AgentID || action.ProcessID != process.ID {
		return false, errors.New("process action does not belong to process")
	}
	failed, err := s.failMachineUnreachableQueuedProcessActions(
		ctx,
		process.OrgID,
		process.MachineID,
		action,
		0,
	)
	return failed > 0, err
}

func (s *Store) failMachineUnreachableQueuedProcessActions(
	ctx context.Context,
	orgID, machineID ID,
	action ProcessActionRecord,
	graceSeconds int32,
) (int64, error) {
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("begin machine-unreachable queued action expiry: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	unreachable, err := machineStillUnreachableForToolExpiryTx(
		ctx,
		qtx,
		orgID,
		machineID,
		action.CreatedAt,
		graceSeconds,
	)
	if err != nil {
		return 0, err
	}
	if !unreachable {
		if err := s.commitTxWithNotifications(
			ctx,
			tx,
			txNotifications,
			"skipped machine-unreachable queued action expiry",
		); err != nil {
			return 0, err
		}
		return 0, nil
	}
	if _, err := qtx.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{
			ProjectID: action.ProjectID,
			ID:        action.AgentID,
		},
	); err != nil {
		return 0, fmt.Errorf("lock agent for machine-unreachable queued action expiry: %w", err)
	}
	rows, err := qtx.MarkQueuedProcessActionsFailedForProcess(
		ctx,
		dbsqlc.MarkQueuedProcessActionsFailedForProcessParams{
			OrgID:              action.OrgID,
			ProcessID:          action.ProcessID,
			ActionID:           &action.ID,
			StateReasonCode:    sqlcTextFromEmpty(ProcessToolReasonMachineUnreachable),
			StateReasonMessage: "",
		},
	)
	if err != nil {
		return 0, fmt.Errorf("mark machine-unreachable queued actions failed: %w", err)
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
			ProcessToolReasonMachineUnreachable,
		); err != nil {
			return 0, err
		}
	}
	if err := s.commitTxWithNotifications(
		ctx,
		tx,
		txNotifications,
		"machine-unreachable queued action expiry",
	); err != nil {
		return 0, err
	}
	return int64(len(rows)), nil
}

func (s *Store) completeMachineUnreachableProcessToolCall(
	ctx context.Context,
	process ProcessRecord,
	graceSeconds int32,
) (bool, error) {
	result, err := MachineUnreachableToolResult(map[string]any{
		"process_id": publicResourceID(publicid.KindProcess, process.ID),
		"state":      process.State,
	})
	if err != nil {
		return false, err
	}
	return s.completeMachineUnreachableToolCall(
		ctx,
		process.OrgID,
		process.MachineID,
		process.CreatedAt,
		process.ProjectID,
		process.AgentID,
		process.ToolCallID,
		result,
		graceSeconds,
	)
}

func (s *Store) completeMachineUnreachableProcessActionToolCall(
	ctx context.Context,
	orgID, machineID ID,
	action ProcessActionRecord,
	graceSeconds int32,
) (bool, error) {
	result, err := MachineUnreachableToolResult(map[string]any{
		"process_id":        publicResourceID(publicid.KindProcess, action.ProcessID),
		"process_action_id": publicResourceID(publicid.KindProcessAction, action.ID),
		"action_kind":       action.ActionKind,
		"state":             ProcessActionStateUnknown,
	})
	if err != nil {
		return false, err
	}
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf(
			"begin machine-unreachable process action completion: %w",
			err,
		)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	unreachable, err := machineStillUnreachableForToolExpiryTx(
		ctx,
		qtx,
		orgID,
		machineID,
		action.CreatedAt,
		graceSeconds,
	)
	if err != nil {
		return false, err
	}
	if !unreachable {
		if err := s.commitTxWithNotifications(
			ctx,
			tx,
			txNotifications,
			"skipped machine-unreachable process action completion",
		); err != nil {
			return false, err
		}
		return false, nil
	}
	state := ProcessActionStateUnknown
	if action.ActionKind == ProcessActionKindRead {
		state = ProcessActionStateFailed
	}
	_, err = completeDaemonProcessActionTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		processActionCompletionInput{
			ProjectID:       action.ProjectID,
			AgentID:         action.AgentID,
			ProcessID:       action.ProcessID,
			ID:              action.ID,
			StateReasonCode: ProcessToolReasonMachineUnreachable,
			Result:          result,
		},
		state,
	)
	if errors.Is(err, storeerr.ErrDaemonRuntimeUnregistered) {
		if err := s.commitTxWithNotifications(
			ctx,
			tx,
			txNotifications,
			"missed machine-unreachable process action completion",
		); err != nil {
			return false, err
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf(
			"resolve machine-unreachable process action: %w",
			err,
		)
	}
	if err := s.commitTxWithNotifications(
		ctx,
		tx,
		txNotifications,
		"machine-unreachable process action completion",
	); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) completeMachineUnreachableToolCall(
	ctx context.Context,
	orgID, machineID ID,
	fallbackAt time.Time,
	projectID, agentID, toolCallID ID,
	result json.RawMessage,
	graceSeconds int32,
) (bool, error) {
	parts, err := ToolResultContentParts(result)
	if err != nil {
		return false, err
	}
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	unreachable, err := machineStillUnreachableForToolExpiryTx(
		ctx,
		qtx,
		orgID,
		machineID,
		fallbackAt,
		graceSeconds,
	)
	if err != nil {
		return false, err
	}
	if !unreachable {
		if err := s.commitTxWithNotifications(
			ctx,
			tx,
			txNotifications,
			"skipped machine-unreachable tool call completion",
		); err != nil {
			return false, err
		}
		return false, nil
	}
	row, err := qtx.CompleteMachineUnreachableToolCall(
		ctx,
		dbsqlc.CompleteMachineUnreachableToolCallParams{
			ProjectID: projectID,
			AgentID:   agentID,
			ID:        toolCallID,
			Outcome:   string(ToolResultOutcomeFailed),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := finishCompletedToolCallTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		toolCallRecordFromMachineUnreachableCompleteSQLC(row),
		toolCallResultInput{
			Outcome:            ToolResultOutcomeFailed,
			ResultContentParts: parts,
		},
	); err != nil {
		return false, err
	}
	if err := s.commitTxWithNotifications(
		ctx,
		tx,
		txNotifications,
		"machine-unreachable tool call completion",
	); err != nil {
		return false, err
	}
	return true, nil
}

func machineStillUnreachableForToolExpiryTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID, machineID ID,
	fallbackAt time.Time,
	graceSeconds int32,
) (bool, error) {
	if _, err := qtx.LockMachineForLifecycle(
		ctx,
		dbsqlc.LockMachineForLifecycleParams{
			OrgID: orgID,
			ID:    machineID,
		},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("lock machine for machine-unreachable tool expiry: %w", err)
	}
	unreachable, err := qtx.CheckMachineUnreachableForToolExpiry(ctx, dbsqlc.CheckMachineUnreachableForToolExpiryParams{
		OrgID:                          orgID,
		MachineID:                      machineID,
		FallbackAt:                     fallbackAt,
		MachineUnreachableGraceSeconds: graceSeconds,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check machine unreachable for tool expiry: %w", err)
	}
	return unreachable != nil && *unreachable, nil
}

func MachineUnreachableToolResult(fields map[string]any) (json.RawMessage, error) {
	body := map[string]any{
		"error_code":        ProcessToolReasonMachineUnreachable,
		"state_reason_code": ProcessToolReasonMachineUnreachable,
		"error":             "machine is offline or unreachable",
		"retryable":         true,
	}
	for key, value := range fields {
		if value != nil && value != "" {
			body[key] = value
		}
	}
	result, err := marshalJSON(body)
	if err != nil {
		return nil, fmt.Errorf("marshal machine-unreachable tool result: %w", err)
	}
	return result, nil
}
