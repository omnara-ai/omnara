package executionstore

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type CancelAgentResult struct {
	Event                  events.Event
	RuntimeCancelRequested bool
	Affected               bool
	ActorID                uuid.UUID
}

type CancelAgentInput struct {
	ProjectID uuid.UUID
	AgentID   uuid.UUID
	Actor     *ActorParams
}

const cancelReasonAgentCanceled = "agent_canceled"

func (s *Store) CancelAgent(ctx context.Context, input CancelAgentInput) (CancelAgentResult, error) {
	unit, h, err := beginExecution(ctx, s, input.ProjectID, input.AgentID)
	if err != nil {
		return CancelAgentResult{}, err
	}
	defer func() { _ = unit.Rollback(ctx) }()
	actor, err := resolveActorTx(ctx, dbsqlc.New(unit.DB()), input.ProjectID, input.Actor)
	if err != nil {
		return CancelAgentResult{}, err
	}
	result, err := h.Cancel(ctx, agentexecution.CancelInput{ActorID: actor, Reason: cancelReasonAgentCanceled,
		Message: "The model call was canceled by an explicit agent cancellation."})
	if err != nil {
		return CancelAgentResult{}, err
	}
	if err = unit.Commit(ctx, "cancel agent"); err != nil {
		return CancelAgentResult{}, err
	}
	return CancelAgentResult{Event: executionEvent(input.AgentID, result.Event, events.KindAgentInput),
		Affected: result.Changed, RuntimeCancelRequested: result.RuntimeID != uuid.Nil, ActorID: actor}, nil
}
