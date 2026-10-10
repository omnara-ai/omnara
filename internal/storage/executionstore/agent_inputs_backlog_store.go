package executionstore

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
)

type PromoteQueuedInputToSteeringInput struct {
	ProjectID              uuid.UUID
	AgentID                uuid.UUID
	InputID                uuid.UUID
	CancelOpenInteractions bool
}

type DemoteSteeringInputToQueuedInput struct {
	ProjectID uuid.UUID
	AgentID   uuid.UUID
	InputID   uuid.UUID
}

func (s *Store) PromoteQueuedInputToSteering(
	ctx context.Context,
	input PromoteQueuedInputToSteeringInput,
) error {
	return s.changeBacklog(
		ctx,
		input.ProjectID,
		input.AgentID,
		agentexecution.BacklogChange{
			ID:           input.InputID,
			DeliveryMode: "steering",
		},
		input.CancelOpenInteractions,
	)
}

func (s *Store) DemoteSteeringInputToQueued(
	ctx context.Context,
	input DemoteSteeringInputToQueuedInput,
) error {
	return s.changeBacklog(ctx, input.ProjectID, input.AgentID,
		agentexecution.BacklogChange{ID: input.InputID, DeliveryMode: "queued"}, false)
}

func (s *Store) changeBacklog(ctx context.Context, projectID, agentID uuid.UUID,
	change agentexecution.BacklogChange, cancelInteractions bool) error {
	if projectID == uuid.Nil || agentID == uuid.Nil || change.ID == uuid.Nil {
		return errors.New("project, agent and input are required")
	}
	unit, h, err := beginExecution(ctx, s, projectID, agentID)
	if err != nil {
		return err
	}
	defer func() { _ = unit.Rollback(ctx) }()
	changed, err := h.ChangeBacklog(ctx, change)
	if err != nil {
		return err
	}
	if changed && cancelInteractions {
		if _, err = h.SupersedeInteractions(ctx, change.ID); err != nil {
			return err
		}
	}
	return unit.Commit(ctx, "change input backlog")
}
