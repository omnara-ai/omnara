package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

const (
	ToolCallExpiryBatchSize    = 100
	toolCallTimeoutErrorCode   = "timeout"
	toolCallTimeoutErrorText   = "tool call timed out before it completed"
	toolCallTimeoutWakeupScope = "tool call expiry"
)

func (s *Store) ExpireToolCalls(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = ToolCallExpiryBatchSize
	}
	candidates, err := s.q.ListExpiredToolCalls(ctx, dbsqlc.ListExpiredToolCallsParams{RowLimit: int32(limit)})
	if err != nil {
		return 0, fmt.Errorf("list expired tool calls: %w", err)
	}
	expired := 0
	for _, candidate := range candidates {
		completed, err := s.expireToolCall(ctx, candidate.ProjectID, candidate.AgentID, candidate.ID)
		if err != nil {
			return expired, err
		}
		if completed {
			expired++
		}
	}
	return expired, nil
}

func (s *Store) expireToolCall(ctx context.Context, projectID, agentID, toolCallID ID) (bool, error) {
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin tool call expiry: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if _, err := qtx.LockAgentInProject(
		ctx, dbsqlc.LockAgentInProjectParams{ProjectID: projectID, ID: agentID},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("lock agent for tool call expiry: %w", err)
	}
	completed, err := expireToolCallTx(ctx, txNotifications, tx, qtx, projectID, agentID, toolCallID)
	if err != nil {
		return false, err
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, toolCallTimeoutWakeupScope); err != nil {
		return false, err
	}
	return completed, nil
}

func expireToolCallTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	projectID, agentID, toolCallID ID,
) (bool, error) {
	waitRow, err := qtx.GetAgentWaitByToolCall(ctx, dbsqlc.GetAgentWaitByToolCallParams{
		ProjectID:  projectID,
		AgentID:    agentID,
		ToolCallID: toolCallID,
	})
	if err == nil && waitRow.State == "open" {
		wait := agentWaitRecordFromSQLC(
			waitRow.ID, waitRow.OrgID, waitRow.ProjectID, waitRow.AgentID,
			waitRow.ToolCallID, waitRow.Mode, waitRow.State,
		)
		if err := timeOutAgentWaitTx(ctx, txNotifications, tx, qtx, wait); err != nil {
			return false, err
		}
		return true, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("load agent wait for tool call expiry: %w", err)
	}
	result, err := marshalJSON(map[string]any{
		"error_code": toolCallTimeoutErrorCode,
		"error":      toolCallTimeoutErrorText,
	})
	if err != nil {
		return false, fmt.Errorf("marshal tool call timeout result: %w", err)
	}
	parts, err := ToolResultContentParts(result)
	if err != nil {
		return false, err
	}
	row, err := qtx.CompleteExpiredToolCall(ctx, dbsqlc.CompleteExpiredToolCallParams{
		ProjectID: projectID,
		AgentID:   agentID,
		ID:        toolCallID,
		Outcome:   string(ToolResultOutcomeFailed),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("complete expired tool call: %w", err)
	}
	if _, err := finishCompletedToolCallTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		toolCallRecordFromSQLC(
			row.ID, row.ProjectID, row.AgentID, row.TurnID,
			row.SourceEventID, row.ModelCallContextID, row.ProviderCallID,
			row.Name, row.Input, row.Type,
			row.State, row.Outcome, row.RuntimeLockID,
			row.ResultContentParts, row.CreatedAt, nil,
		),
		toolCallResultInput{Outcome: ToolResultOutcomeFailed, ResultContentParts: parts},
	); err != nil {
		return false, err
	}
	return true, nil
}
