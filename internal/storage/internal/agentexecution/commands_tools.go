package agentexecution

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ToolRef struct {
	ID            uuid.UUID
	RuntimeLockID uuid.UUID
}
type ToolOwner struct {
	InteractionID uuid.UUID
	ProcessID     uuid.UUID
	ActionID      uuid.UUID
}
type ToolCompletion struct {
	ToolRef
	Outcome string
	Content []Content
}
type CompletedTool struct {
	ID       uuid.UUID
	ResultID uuid.UUID
	Event    ExecutionEvent
	Changed  bool
}

func (h *Handle) tool(ctx context.Context, id uuid.UUID) (executiondb.ReadExecutionToolRow, error) {
	return executiondb.New().
		ReadExecutionTool(ctx, h.unit.DB(), executiondb.ReadExecutionToolParams{AgentID: h.route.AgentID, ID: id})
}

func (h *Handle) transitionTool(
	ctx context.Context,
	m *executionMutation,
	row executiondb.ReadExecutionToolRow,
	state string,
	owner, fence uuid.UUID,
) error {
	n, err := executiondb.New().
		TransitionExecutionTool(ctx, h.unit.DB(), executiondb.TransitionExecutionToolParams{
			AgentID: h.route.AgentID, ID: row.ID, PreviousState: row.State, PreviousRuntimeID: row.RuntimeLockID,
			NextState: state, NextRuntimeID: nullableID(owner), FenceID: nullableID(fence)})
	if err != nil {
		return err
	}
	if n != 1 {
		return storeerr.ErrStateTransitionConflict
	}
	m.changed()
	return nil
}

func (h *Handle) AuthorizeTool(ctx context.Context, ref ToolRef) (bool, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (bool, error) {
		if err := h.FenceRuntime(ctx, ref.RuntimeLockID); err != nil {
			return false, err
		}
		row, err := h.tool(ctx, ref.ID)
		if err != nil {
			return false, err
		}
		if row.State == "ready" {
			return false, nil
		}
		if row.State != "awaiting_authorization" {
			return false, storeerr.ErrStateTransitionConflict
		}
		if err = h.transitionTool(ctx, m, row, "ready", uuid.Nil, ref.RuntimeLockID); err != nil {
			return false, err
		}
		h.unit.Notifications().AddToolCallUpdate(h.route.AgentID, row.ID, "ready", nil)
		return true, nil
	})
}

func (h *Handle) RunTool(ctx context.Context, ref ToolRef) (bool, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (bool, error) {
		if err := h.FenceRuntime(ctx, ref.RuntimeLockID); err != nil {
			return false, err
		}
		row, err := h.tool(ctx, ref.ID)
		if err != nil {
			return false, err
		}
		if row.State == "running" {
			return false, storeerr.ErrToolCallInProgress
		}
		if row.State != "ready" || row.Type == "custom" {
			return false, storeerr.ErrStateTransitionConflict
		}
		if err = h.transitionTool(ctx, m, row, "running", ref.RuntimeLockID, ref.RuntimeLockID); err != nil {
			return false, err
		}
		h.unit.Notifications().AddToolCallUpdate(h.route.AgentID, row.ID, "running", nil)
		return true, nil
	})
}

func (h *Handle) RequeueTool(ctx context.Context, ref ToolRef) (bool, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (bool, error) {
		if err := h.FenceRuntime(ctx, ref.RuntimeLockID); err != nil {
			return false, err
		}
		row, err := h.tool(ctx, ref.ID)
		if err != nil {
			return false, err
		}
		if row.State == "ready" {
			return false, nil
		}
		if row.State != "running" || valueOrZero(row.RuntimeLockID) != ref.RuntimeLockID {
			return false, storeerr.ErrStateTransitionConflict
		}
		if err = h.transitionTool(ctx, m, row, "ready", uuid.Nil, ref.RuntimeLockID); err != nil {
			return false, err
		}
		h.unit.Notifications().AddToolCallUpdate(h.route.AgentID, row.ID, "ready", nil)
		return true, nil
	})
}

func (h *Handle) WaitForTool(ctx context.Context, ref ToolRef, owner ToolOwner) (bool, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (bool, error) {
		owners := 0
		for _, id := range []uuid.UUID{owner.InteractionID, owner.ProcessID, owner.ActionID} {
			if id != uuid.Nil {
				owners++
			}
		}
		if owners != 1 {
			return false, storeerr.ErrInvalidToolCallDisposition
		}

		if err := h.FenceRuntime(ctx, ref.RuntimeLockID); err != nil {
			return false, err
		}
		row, err := h.tool(ctx, ref.ID)
		if err != nil {
			return false, err
		}
		durable, err := executiondb.New().
			ExecutionToolOwner(ctx, h.unit.DB(), executiondb.ExecutionToolOwnerParams{
				AgentID: h.route.AgentID, ToolID: row.ID, InteractionID: nullableID(owner.InteractionID),
				ProcessID: nullableID(owner.ProcessID), ActionID: nullableID(owner.ActionID)})
		if err != nil {
			return false, err
		}
		if row.Type != "built_in" || !durable.Question && !durable.Process && !durable.Action {
			return false, storeerr.ErrInvalidToolCallDisposition
		}
		if row.State == "waiting" {
			return false, nil
		}
		if row.State != "ready" || row.Type != "built_in" {
			return false, storeerr.ErrStateTransitionConflict
		}
		if err = h.transitionTool(ctx, m, row, "waiting", uuid.Nil, ref.RuntimeLockID); err != nil {
			return false, err
		}
		h.unit.Notifications().AddToolCallUpdate(h.route.AgentID, row.ID, "waiting", nil)
		return true, nil
	})
}

func (h *Handle) CompleteTool(ctx context.Context, input ToolCompletion) (CompletedTool, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (CompletedTool, error) {
		if err := h.FenceRuntime(ctx, input.RuntimeLockID); err != nil {
			return CompletedTool{}, err
		}
		row, err := h.tool(ctx, input.ID)
		if err != nil {
			return CompletedTool{}, err
		}
		allowed := row.State == "completed" || (row.State == "ready" && row.Type != "custom") ||
			(row.State == "running" && valueOrZero(row.RuntimeLockID) == input.RuntimeLockID) ||
			((row.State == "awaiting_authorization" ||
				row.State == "awaiting_permission") &&
				input.Outcome != "succeeded")
		if !allowed {
			return CompletedTool{}, storeerr.ErrStateTransitionConflict
		}
		return h.completeTool(ctx, m, row, input.Outcome, input.Content, input.RuntimeLockID, nil)
	})
}

func (h *Handle) CompleteCustomTool(
	ctx context.Context,
	id uuid.UUID,
	outcome string,
	content []Content,
) (CompletedTool, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (CompletedTool, error) {
		row, err := h.tool(ctx, id)
		if err != nil {
			return CompletedTool{}, err
		}
		if row.Type != "custom" || (row.State != "ready" && row.State != "completed") ||
			(outcome != "succeeded" && outcome != "failed") {
			return CompletedTool{}, storeerr.ErrStateTransitionConflict
		}
		return h.completeTool(ctx, m, row, outcome, content, uuid.Nil, nil)
	})
}

func (h *Handle) completeTool(
	ctx context.Context,
	m *executionMutation,
	row executiondb.ReadExecutionToolRow,
	outcome string,
	content []Content,
	fence uuid.UUID,
	interaction *notifications.AgentInteractionUpdate,
) (CompletedTool, error) {
	switch outcome {
	case "succeeded", "failed", "denied", "canceled":
	default:
		return CompletedTool{}, errors.New("invalid tool outcome")
	}
	for _, part := range content {
		if part.Kind != "text" && part.Kind != "structured_data" && part.Kind != "artifact" {
			return CompletedTool{}, errors.New("invalid tool result content")
		}
	}
	if row.State == "completed" {
		if valueOrZero(row.Outcome) != outcome || row.ResultID == nil || row.ResultEventID == nil {
			return CompletedTool{}, storeerr.ErrIdempotencyConflict
		}
		err := h.matchContent(
			ctx,
			executiondb.ReadExecutionContentParams{AgentID: h.route.AgentID, ResultID: row.ResultID},
			content,
		)
		return CompletedTool{
			ID:       row.ID,
			ResultID: *row.ResultID,
			Event: ExecutionEvent{ID: *row.ResultEventID, TurnID: row.TurnID,
				EventBoundary: EventBoundary{
					Sequence: valueOrZero(row.ResultSequence),
					Time:     valueOrZero(row.CompletedAt),
				}},
		}, err
	}
	if err := h.transitionTool(ctx, m, row, "completed", uuid.Nil, fence); err != nil {
		return CompletedTool{}, err
	}
	q := executiondb.New()
	id, err := q.InsertExecutionToolResult(ctx, h.unit.DB(), executiondb.InsertExecutionToolResultParams{
		AgentID: h.route.AgentID, ID: row.ID, Outcome: outcome})
	if err != nil {
		return CompletedTool{}, err
	}
	if err = h.writeContent(ctx, "tool_call_result", id, content); err != nil {
		return CompletedTool{}, err
	}
	event, err := q.AppendExecutionEvent(ctx, h.unit.DB(), executiondb.AppendExecutionEventParams{
		ID:             uuid.New(),
		ProjectID:      h.route.ProjectID,
		AgentID:        h.route.AgentID,
		TurnID:         &row.TurnID,
		Kind:           "tool_result",
		ResultID:       &id,
		IdempotencyKey: optionalText("tool-call-result-authority:" + row.ID.String())})
	if err != nil {
		return CompletedTool{}, err
	}
	admitted := ExecutionEvent{
		ID:            event.ID,
		TurnID:        row.TurnID,
		EventBoundary: EventBoundary{Sequence: event.Sequence, Time: event.CreatedAt},
	}
	if err = h.advanceTurn(ctx, admitted, false); err != nil {
		return CompletedTool{}, err
	}
	closed, err := q.CloseExecutionToolInteractions(
		ctx,
		h.unit.DB(),
		executiondb.CloseExecutionToolInteractionsParams{
			AgentID: h.route.AgentID, ToolID: row.ID},
	)
	if err != nil {
		return CompletedTool{}, err
	}
	for _, i := range closed {
		interaction = &notifications.AgentInteractionUpdate{
			ID:              i.ID,
			InteractionKind: i.InteractionKind,
			State:           i.State,
		}
	}
	h.unit.Notifications().AddAgentEvent(h.route.AgentID, event.Sequence, "tool_result")
	h.unit.Notifications().AddToolCallUpdate(h.route.AgentID, row.ID, "completed", interaction)
	return CompletedTool{ID: row.ID, ResultID: id, Event: admitted, Changed: true}, nil
}

type ToolDispatch uint8

const (
	ToolDispatchReady ToolDispatch = iota + 1
	ToolDispatchWaiting
	ToolDispatchCompleted
)

func (h *Handle) DispatchTool(ctx context.Context, ref ToolRef, acceptExisting bool) (ToolDispatch, error) {
	if err := h.unit.LockAgentRefs(ctx,
		[]lifecyclelock.AgentRef{{ProjectID: h.route.ProjectID, AgentID: h.route.AgentID}},
		RuntimeAuthority{AgentID: h.route.AgentID, RuntimeLockID: ref.RuntimeLockID},
	); err != nil {
		return 0, err
	}
	state, err := executiondb.New().ReadExecutionToolDispatch(
		ctx, h.unit.DB(), executiondb.ReadExecutionToolDispatchParams{AgentID: h.route.AgentID, ID: ref.ID},
	)
	if err != nil {
		return 0, err
	}
	switch state {
	case "ready":
		return ToolDispatchReady, nil
	case "running":
		return 0, storeerr.ErrToolCallInProgress
	case "waiting":
		if acceptExisting {
			return ToolDispatchWaiting, nil
		}
	case "completed":
		if acceptExisting {
			return ToolDispatchCompleted, nil
		}
	}
	return 0, storeerr.ErrIdempotencyConflict
}
