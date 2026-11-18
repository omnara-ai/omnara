package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
)

type ReleaseDueScheduledAgentInputsResult struct {
	// Due is the number of due scheduled inputs found.
	Due      int
	Released int
}

// ReleaseDueScheduledAgentInputs moves every scheduled content input whose
// time has come into its agent's backlog, reading them in pages of pageSize.
// Each release commits on its own and the pass keeps going after a failure, so
// one failing or busy agent does not hold back the rest; failures are joined
// into the error, and failed or skipped inputs are retried on the next call.
func (s *Store) ReleaseDueScheduledAgentInputs(
	ctx context.Context,
	pageSize int,
) (ReleaseDueScheduledAgentInputsResult, error) {
	if pageSize <= 0 {
		return ReleaseDueScheduledAgentInputsResult{}, errors.New("page size must be positive")
	}
	var result ReleaseDueScheduledAgentInputsResult
	var failures []error
	params := dbsqlc.ListDueScheduledAgentInputsParams{RowLimit: int64(pageSize)}
	for {
		due, err := s.q.ListDueScheduledAgentInputs(ctx, params)
		if err != nil {
			return result, errors.Join(append(failures, fmt.Errorf("list due scheduled agent inputs: %w", err))...)
		}
		result.Due += len(due)
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
		if len(due) < pageSize {
			return result, errors.Join(failures...)
		}
		last := due[len(due)-1]
		params.AfterScheduledFor, params.AfterID = last.ScheduledFor, &last.ID
	}
}

// releaseScheduledAgentInput reports false without waiting when another
// transaction holds the agent, so one busy agent cannot stall the pass; the
// input stays scheduled for the next call.
func (s *Store) releaseScheduledAgentInput(ctx context.Context, projectID, agentID, inputID uuid.UUID) (bool, error) {
	return agentexecution.Retry(ctx,
		"release_scheduled_agent_input",
		func(ctx context.Context) (bool, error) {
			unit, err := s.cell.Begin(ctx)
			if err != nil {
				return false, err
			}
			defer func() { _ = unit.Rollback(ctx) }()
			plan, err := unit.PlanAgents(ctx, []lifecyclelock.AgentRef{{ProjectID: projectID, AgentID: agentID}}, nil)
			if err != nil {
				return false, err
			}
			locked, err := unit.TryLockAgents(ctx, plan)
			if err != nil || !locked {
				return false, err
			}
			h, err := unit.Handle(projectID, agentID)
			if err != nil {
				return false, err
			}
			released, err := h.ReleaseScheduledContent(ctx, inputID)
			if err != nil || !released {
				return false, err
			}
			return true, unit.Commit(ctx, "release scheduled agent input")
		})
}
