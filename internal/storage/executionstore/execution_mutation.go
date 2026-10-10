package executionstore

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
)

func beginExecution(
	ctx context.Context,
	s *Store,
	projectID, agentID uuid.UUID,
) (*agentexecution.Unit, *agentexecution.Handle, error) {
	unit, err := s.cell.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	plan, err := unit.PlanAgentFamily(ctx, projectID, agentID, agentexecution.LifecycleAuthority{})
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
