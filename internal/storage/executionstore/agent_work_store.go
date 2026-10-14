package executionstore

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type AdvanceOwnedAgentWorkInput struct {
	ProjectID      uuid.UUID
	AgentID        uuid.UUID
	RuntimeLockID  uuid.UUID
	AllowModelWork bool
	PrepareModel   bool
}

func (s *Store) AdvanceOwnedAgentWork(
	ctx context.Context,
	input AdvanceOwnedAgentWorkInput,
) (ClaimedAgentWork, bool, error) {
	type executionResult struct {
		work  ClaimedAgentWork
		found bool
	}
	result, err := agentexecution.Retry(ctx,
		"AdvanceOwnedAgentWork",
		func(ctx context.Context) (executionResult, error) {
			if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil || input.RuntimeLockID == uuid.Nil {
				return executionResult{ClaimedAgentWork{}, false}, errors.New("project, agent, and runtime lock ids are required")
			}
			unit, h, err := beginExecution(ctx, s, input.ProjectID, input.AgentID, false)
			if errors.Is(err, storeerr.ErrNotFound) {
				return executionResult{ClaimedAgentWork{}, false}, storeerr.ErrRuntimeLockInactive
			}
			if err != nil {
				return executionResult{ClaimedAgentWork{}, false}, err
			}
			defer func() { _ = unit.Rollback(ctx) }()
			work, err := h.Advance(ctx, input.RuntimeLockID, input.AllowModelWork, input.PrepareModel)
			if err != nil {
				return executionResult{ClaimedAgentWork{}, false}, err
			}
			if err := applyAdmissionDestination(ctx, unit, input.ProjectID, input.AgentID, work.Admission); err != nil {
				return executionResult{ClaimedAgentWork{}, false}, err
			}
			claim, err := shapeOwnedWork(ctx, unit, input.ProjectID, input.AgentID, work)
			if err != nil {
				return executionResult{ClaimedAgentWork{}, false}, err
			}
			if err := unit.Commit(ctx, "advance owned agent work"); err != nil {
				return executionResult{ClaimedAgentWork{}, false}, err
			}
			return executionResult{claim, !work.Released}, nil
		})
	return result.work, result.found, err
}
