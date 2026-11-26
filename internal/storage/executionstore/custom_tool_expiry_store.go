package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

// ExpireCustomToolCalls times out up to limit ready calls whose deadline has passed. It
// reports how many it timed out and whether the batch was full, which is what says more may
// be waiting: a call in the batch can already have been timed out by a late result.
func (s *Store) ExpireCustomToolCalls(ctx context.Context, limit int32) (int64, bool, error) {
	candidates, err := s.q.ListExpiredCustomToolCalls(ctx, dbsqlc.ListExpiredCustomToolCallsParams{LimitCount: limit})
	if err != nil {
		return 0, false, fmt.Errorf("list expired custom tool calls: %w", err)
	}
	var expired int64
	var failures []error
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			break
		}
		done, err := s.expireCustomToolCall(ctx, candidate.ProjectID, candidate.AgentID, candidate.ID)
		if err != nil {
			failures = append(failures, fmt.Errorf("expire custom tool call %s: %w", candidate.ID, err))
			continue
		}
		if done {
			expired++
		}
	}
	return expired, len(candidates) >= int(limit), errors.Join(failures...)
}

func (s *Store) expireCustomToolCall(ctx context.Context, projectID, agentID, id uuid.UUID) (bool, error) {
	return agentexecution.Retry(ctx,
		"expire_custom_tool_call",
		func(ctx context.Context) (bool, error) {
			unit, err := s.cell.Begin(ctx)
			if err != nil {
				return false, err
			}
			defer func() { _ = unit.Rollback(ctx) }()
			h, err := lockExecution(ctx, unit, projectID, agentID, false, uuid.Nil)
			if err != nil {
				return false, err
			}
			expired, err := h.ExpireCustomTool(ctx, id)
			if err != nil {
				return false, err
			}
			if err := unit.Commit(ctx, "expire custom tool call"); err != nil {
				return false, err
			}
			return expired, nil
		})
}
