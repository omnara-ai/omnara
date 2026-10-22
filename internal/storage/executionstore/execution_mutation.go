package executionstore

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func beginExecution(
	ctx context.Context,
	s *Store,
	projectID, agentID uuid.UUID,
	includeParent bool,
	runtimeID uuid.UUID,
) (*agentexecution.Unit, *agentexecution.Handle, error) {
	unit, err := s.cell.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	h, err := lockExecution(ctx, unit, projectID, agentID, includeParent, runtimeID)
	if err != nil {
		_ = unit.Rollback(ctx)
		return nil, nil, err
	}
	return unit, h, nil
}

func lockExecution(ctx context.Context, unit *agentexecution.Unit, projectID, agentID uuid.UUID,
	includeParent bool, runtimeID uuid.UUID) (*agentexecution.Handle, error) {
	var plan agentexecution.MutationPlan
	var err error
	if includeParent {
		plan, err = unit.PlanAgentFamily(ctx, projectID, agentID, nil)
	} else {
		plan, err = unit.PlanAgents(ctx, []lifecyclelock.AgentRef{{ProjectID: projectID, AgentID: agentID}}, nil)
	}
	if err == nil {
		err = unit.LockAgents(ctx, plan)
	}
	if err != nil {
		if runtimeID != uuid.Nil && errors.Is(err, storeerr.ErrNotFound) {
			return nil, storeerr.ErrRuntimeLockInactive
		}
		return nil, err
	}
	return unit.Handle(projectID, agentID)
}

func beginStepExecution(
	ctx context.Context, s *Store, projectID, agentID, rootID uuid.UUID, includeParent bool, runtimeID uuid.UUID,
) (*agentexecution.Unit, *agentexecution.Handle, error) {
	if rootID == uuid.Nil || includeParent {
		return beginExecution(ctx, s, projectID, agentID, includeParent, runtimeID)
	}
	unit, err := s.cell.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	h, err := unit.LockStepAgent(ctx, agentexecution.AgentRoute{ProjectID: projectID, AgentID: agentID,
		RootAgentID: rootID}, runtimeID)
	if err != nil {
		_ = unit.Rollback(ctx)
		return nil, nil, err
	}
	return unit, h, nil
}
