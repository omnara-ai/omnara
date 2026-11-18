package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type ReleaseDueScheduledAgentInputsResult struct {
	// Due is the number of due scheduled inputs found, which equals the limit when more may remain.
	Due      int
	Released int
}

// ReleaseDueScheduledAgentInputs moves scheduled content inputs whose time has
// come into their agents' backlogs. Each release commits on its own, so one
// failing agent does not hold back the rest; failures are joined into the error
// and retried on the next call.
func (s *Store) ReleaseDueScheduledAgentInputs(
	ctx context.Context,
	limit int,
) (ReleaseDueScheduledAgentInputsResult, error) {
	if limit <= 0 {
		return ReleaseDueScheduledAgentInputsResult{}, errors.New("limit must be positive")
	}
	due, err := s.q.ListDueScheduledAgentInputs(ctx, dbsqlc.ListDueScheduledAgentInputsParams{RowLimit: int64(limit)})
	if err != nil {
		return ReleaseDueScheduledAgentInputsResult{}, fmt.Errorf("list due scheduled agent inputs: %w", err)
	}
	result := ReleaseDueScheduledAgentInputsResult{Due: len(due)}
	var failures []error
	for _, input := range due {
		released, err := s.releaseScheduledAgentInput(ctx, input.ProjectID, input.AgentID, input.ID)
		if err != nil {
			if ctx.Err() != nil {
				return result, errors.Join(append(failures, ctx.Err())...)
			}
			failures = append(failures, fmt.Errorf("release scheduled agent input %s: %w", input.ID, err))
			continue
		}
		if released {
			result.Released++
		}
	}
	return result, errors.Join(failures...)
}

func (s *Store) releaseScheduledAgentInput(ctx context.Context, projectID, agentID, inputID uuid.UUID) (bool, error) {
	return agentexecution.Retry(ctx,
		"release_scheduled_agent_input",
		func(ctx context.Context) (bool, error) {
			unit, h, err := beginExecution(ctx, s, projectID, agentID, false, uuid.Nil)
			if err != nil {
				return false, err
			}
			defer func() { _ = unit.Rollback(ctx) }()
			released, err := h.ReleaseScheduledContent(ctx, inputID)
			if err != nil || !released {
				return false, err
			}
			return true, unit.Commit(ctx, "release scheduled agent input")
		})
}
