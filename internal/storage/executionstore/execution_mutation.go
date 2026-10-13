package executionstore

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
)

func beginExecution(
	ctx context.Context,
	s *Store,
	projectID, agentID uuid.UUID,
	includeParent bool,
) (*agentexecution.Unit, *agentexecution.Handle, error) {
	unit, err := s.cell.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	var plan agentexecution.MutationPlan
	if includeParent {
		plan, err = unit.PlanAgentFamily(ctx, projectID, agentID, nil)
	} else {
		plan, err = unit.PlanAgents(ctx, []lifecyclelock.AgentRef{{ProjectID: projectID, AgentID: agentID}}, nil)
	}
	if err == nil {
		err = unit.LockAgents(ctx, plan)
	}
	if err != nil {
		_ = unit.Rollback(ctx)
		return nil, nil, err
	}
	h, err := unit.Handle(projectID, agentID)
	if err != nil {
		_ = unit.Rollback(ctx)
		return nil, nil, err
	}
	return unit, h, nil
}
