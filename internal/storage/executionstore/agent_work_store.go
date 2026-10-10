package executionstore

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type AdvanceOwnedAgentWorkInput struct {
	ProjectID      uuid.UUID
	AgentID        uuid.UUID
	RuntimeLockID  uuid.UUID
	AllowModelWork bool
	PrepareModel   bool
}

// AdvanceOwnedAgentWork returns continued work or releases the runtime atomically.
// On error, the caller must still release the runtime through the recovery path.
func (s *Store) AdvanceOwnedAgentWork(
	ctx context.Context,
	input AdvanceOwnedAgentWorkInput,
) (ClaimedAgentWork, bool, error) {
	if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil || input.RuntimeLockID == uuid.Nil {
		return ClaimedAgentWork{}, false, errors.New("project, agent, and runtime lock ids are required")
	}
	unit, h, err := beginExecution(ctx, s, input.ProjectID, input.AgentID)
	if errors.Is(err, storeerr.ErrNotFound) {
		return ClaimedAgentWork{}, false, storeerr.ErrRuntimeLockInactive
	}
	if err != nil {
		return ClaimedAgentWork{}, false, err
	}
	defer func() { _ = unit.Rollback(ctx) }()
	work, err := h.Advance(ctx, input.RuntimeLockID, input.AllowModelWork, input.PrepareModel)
	if err != nil {
		return ClaimedAgentWork{}, false, err
	}
	if err := applyAdmissionDestination(ctx, unit, input.ProjectID, input.AgentID, work.Admission); err != nil {
		return ClaimedAgentWork{}, false, err
	}
	claim, err := shapeOwnedWork(ctx, unit, input.ProjectID, input.AgentID, work)
	if err != nil {
		return ClaimedAgentWork{}, false, err
	}
	if err := unit.Commit(ctx, "advance owned agent work"); err != nil {
		return ClaimedAgentWork{}, false, err
	}
	return claim, !work.Released, nil
}
