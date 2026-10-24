package agentexecution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/mcpauthorization"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

type OpenInteractionInput struct {
	ToolRef
	Permission    *toolpermission.Request
	Question      *interactionform.Form
	Authorization *mcpauthorization.Request
	Destination   json.RawMessage
}
type Interaction struct {
	ID         uuid.UUID
	ToolID     uuid.UUID
	Kind       string
	State      string
	Request    json.RawMessage
	Resolution json.RawMessage
	InputID    uuid.UUID
	Created    bool
}
type ResolveInteractionInput struct {
	ID         uuid.UUID
	ActorID    uuid.UUID
	Resolution interactionform.Resolution
}

func interactionRecord(row executiondb.ReadExecutionInteractionRow) Interaction {
	return Interaction{ID: row.ID, ToolID: row.ToolCallID, Kind: row.InteractionKind, State: row.State,
		Request: row.Request, Resolution: row.Resolution, InputID: storeutil.ValueOrZero(row.ResolvedByInputID)}
}

func interactionForm(kind string, request json.RawMessage) (interactionform.Form, error) {
	switch kind {
	case "permission":
		r, err := toolpermission.ParseRequest(request)
		return r.Form, err
	case "authorization":
		r, err := mcpauthorization.ParseRequest(request)
		return r.Form, err
	}
	return interactionform.Parse(request)
}

func (h *Handle) OpenInteraction(ctx context.Context, input OpenInteractionInput) (Interaction, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (Interaction, error) {
		kinds := 0
		for _, set := range []bool{input.Permission != nil, input.Question != nil, input.Authorization != nil} {
			if set {
				kinds++
			}
		}
		if kinds != 1 {
			return Interaction{}, errors.New("exactly one interaction kind is required")
		}
		if err := h.FenceRuntime(ctx, input.RuntimeLockID); err != nil {
			return Interaction{}, err
		}
		kind := "question"
		var request json.RawMessage
		var err error
		if input.Permission != nil {
			kind = "permission"
			if err := input.Permission.Validate(); err != nil {
				return Interaction{}, err
			}
			request, err = json.Marshal(input.Permission)
		} else if input.Authorization != nil {
			kind = "authorization"
			if err := input.Authorization.Validate(); err != nil {
				return Interaction{}, err
			}
			request, err = json.Marshal(input.Authorization)
		} else {
			if err := input.Question.Validate(); err != nil {
				return Interaction{}, err
			}
			request, err = json.Marshal(input.Question)
		}
		if err != nil {
			return Interaction{}, err
		}
		q := executiondb.New()
		existing, err := q.ReadExecutionInteraction(
			ctx,
			h.unit.DB(),
			executiondb.ReadExecutionInteractionParams{
				AgentID: h.route.AgentID, ToolID: &input.ID, Kind: &kind},
		)
		if err == nil {
			call, err := h.tool(ctx, input.ID)
			if err != nil {
				return Interaction{}, err
			}
			if kind == "authorization" {
				if existing.State != "open" || call.State != "waiting" || !equalJSON(existing.Request, request) {
					return Interaction{}, storeerr.ErrIdempotencyConflict
				}
				return interactionRecord(existing), nil
			}
			if kind == "permission" || call.Type != "built_in" ||
				call.State != "waiting" && call.State != "completed" {
				return Interaction{}, storeerr.ErrIdempotencyConflict
			}
			return interactionRecord(existing), nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Interaction{}, err
		}
		call, err := h.tool(ctx, input.ID)
		if err != nil {
			return Interaction{}, err
		}
		next := "waiting"
		switch kind {
		case "permission":
			next = "awaiting_permission"
			if call.State != "awaiting_authorization" {
				return Interaction{}, storeerr.ErrStateTransitionConflict
			}
		case "authorization":
			// The MCP call already ran and the server asked for authorization;
			// the runtime releases it to wait for the user.
			if call.State != "running" || call.Type != "mcp" ||
				storeutil.ValueOrZero(call.RuntimeLockID) != input.RuntimeLockID {
				return Interaction{}, storeerr.ErrStateTransitionConflict
			}
		default:
			if call.State != "ready" || call.Type != "built_in" {
				return Interaction{}, storeerr.ErrStateTransitionConflict
			}
		}
		var destination *json.RawMessage
		if len(input.Destination) > 0 {
			if !json.Valid(input.Destination) {
				return Interaction{}, errors.New("invalid interaction destination")
			}
			destination = &input.Destination
		}
		id, err := q.InsertExecutionInteraction(
			ctx,
			h.unit.DB(),
			executiondb.InsertExecutionInteractionParams{
				AgentID: h.route.AgentID, ToolID: call.ID, Kind: kind, Request: request, Destination: destination},
		)
		if err != nil {
			return Interaction{}, err
		}
		if err = h.transitionTool(ctx, m, call, next, uuid.Nil, input.RuntimeLockID); err != nil {
			return Interaction{}, err
		}
		result := Interaction{
			ID:         id,
			ToolID:     call.ID,
			Kind:       kind,
			State:      "open",
			Request:    request,
			Resolution: json.RawMessage(`{}`),
			Created:    true,
		}
		h.interactionNotification(result, next)
		if kind == "question" {
			if err = h.questionEffect(ctx, result, *input.Question); err != nil {
				return Interaction{}, err
			}
		}
		return result, nil
	})
}

func (h *Handle) interactionNotification(i Interaction, state string) {
	h.unit.Notifications().AddToolCallUpdate(h.route.AgentID, i.ToolID, state,
		&notifications.AgentInteractionUpdate{ID: i.ID, InteractionKind: i.Kind, State: i.State})
}

func (h *Handle) ResolveInteraction(ctx context.Context, input ResolveInteractionInput) (Interaction, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (Interaction, error) {
		q := executiondb.New()
		row, err := q.ReadExecutionInteraction(
			ctx,
			h.unit.DB(),
			executiondb.ReadExecutionInteractionParams{AgentID: h.route.AgentID, ID: input.ID},
		)
		if err != nil {
			return Interaction{}, err
		}
		form, err := interactionForm(row.InteractionKind, row.Request)
		if err != nil {
			return Interaction{}, err
		}
		normalized, err := interactionform.NormalizeResolution(form, input.Resolution)
		if err != nil {
			return Interaction{}, err
		}
		resolution, err := json.Marshal(normalized)
		if err != nil {
			return Interaction{}, err
		}
		if row.State != "open" {
			if row.State != "resolved" || !equalJSON(row.Resolution, resolution) {
				return Interaction{}, storeerr.ErrIdempotencyConflict
			}
			response, err := q.ReadExecutionInput(
				ctx,
				h.unit.DB(),
				executiondb.ReadExecutionInputParams{
					AgentID: h.route.AgentID,
					ID:      storeutil.ValueOrZero(row.ResolvedByInputID),
				},
			)
			if err != nil {
				return Interaction{}, err
			}
			if storeutil.ValueOrZero(response.ActorID) != input.ActorID {
				return Interaction{}, storeerr.ErrIdempotencyConflict
			}
			return interactionRecord(row), nil
		}
		call, err := h.tool(ctx, row.ToolCallID)
		if err != nil {
			return Interaction{}, err
		}
		if m.head.StopSequence > call.SourceSequence {
			return Interaction{}, storeerr.ErrStateTransitionConflict
		}
		id, err := q.InsertExecutionResponse(ctx, h.unit.DB(), executiondb.InsertExecutionResponseParams{
			ProjectID:     h.route.ProjectID,
			AgentID:       h.route.AgentID,
			ActorID:       storeutil.IDFromNil(input.ActorID),
			InteractionID: &row.ID})
		if err != nil {
			return Interaction{}, err
		}
		event, err := h.appendEvent(ctx, call.TurnID, "agent_input", id, uuid.Nil, uuid.Nil, false)
		if err != nil {
			return Interaction{}, err
		}
		if err = h.advanceTurn(ctx, event, true); err != nil {
			return Interaction{}, err
		}
		n, err := q.ResolveExecutionInteraction(
			ctx,
			h.unit.DB(),
			executiondb.ResolveExecutionInteractionParams{
				AgentID: h.route.AgentID, ID: row.ID, State: "resolved", Resolution: resolution, InputID: &id},
		)
		if err != nil {
			return Interaction{}, err
		}
		if n != 1 {
			return Interaction{}, storeerr.ErrStateTransitionConflict
		}
		n, err = q.ResolveExecutionInput(
			ctx,
			h.unit.DB(),
			executiondb.ResolveExecutionInputParams{AgentID: h.route.AgentID, ID: id, EventID: &event.ID},
		)
		if err != nil {
			return Interaction{}, err
		}
		if n != 1 {
			return Interaction{}, storeerr.ErrStateTransitionConflict
		}
		result := interactionRecord(row)
		result.State = "resolved"
		result.Resolution = resolution
		result.InputID = id
		m.changed()
		if err = h.applyInteraction(ctx, m, result, call); err != nil {
			return Interaction{}, err
		}
		return result, nil
	})
}

func (h *Handle) applyInteraction(
	ctx context.Context,
	m *executionMutation,
	i Interaction,
	call executiondb.ReadExecutionToolRow,
) error {
	if i.Kind == "authorization" {
		return h.applyAuthorizationInteraction(ctx, m, i, call)
	}
	outcome := "succeeded"
	var value any
	if i.Kind == "permission" {
		reason := "permission interaction canceled"
		if i.State == "resolved" {
			request, err := toolpermission.ParseRequest(i.Request)
			if err != nil {
				return err
			}
			answers, err := interactionform.ParseResolution(request.Form, i.Resolution)
			if err != nil {
				return err
			}
			decision, err := toolpermission.Resolve(request, answers)
			if err != nil {
				return err
			}
			if decision.Decision == toolpermission.DecisionAllow {
				if call.State != "awaiting_permission" {
					return storeerr.ErrStateTransitionConflict
				}
				if err = h.transitionTool(ctx, m, call, "ready", uuid.Nil, uuid.Nil); err != nil {
					return err
				}
				h.interactionNotification(i, "ready")
				return nil
			}
			reason = decision.Reason
		}
		outcome = "denied"
		value = map[string]string{"reason": reason}
	} else if i.State == "resolved" {
		form, err := interactionform.Parse(i.Request)
		if err != nil {
			return err
		}
		answers, err := interactionform.ParseResolution(form, i.Resolution)
		if err != nil {
			return err
		}
		value = interactionform.RenderAnswers(form, answers)
	} else {
		value = map[string]string{"reason": "question interaction canceled"}
	}
	if i.State == "canceled" {
		outcome = "canceled"
	}
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(
		[]map[string]any{{"type": "structured_data", "value": json.RawMessage(body)}},
	)
	if err != nil {
		return err
	}
	if len(encoded) > 50*1024 {
		outcome = "failed"
		body = json.RawMessage(
			`{"error":"Question response exceeded the size limit. Ask fewer or shorter questions, or request a shorter answer."}`,
		)
	}
	if _, err = h.completeTool(ctx,
		m,
		call,
		outcome,
		[]Content{{Kind: "structured_data",
			Data: body}},
		uuid.Nil,
		&notifications.AgentInteractionUpdate{ID: i.ID,
			InteractionKind: i.Kind,
			State:           i.State}); err != nil {
		return err
	}
	return nil
}

// applyAuthorizationInteraction returns a granted MCP call to ready so the
// runtime dispatches it again with the new credential; a declined or canceled
// authorization completes the call with an error the model can explain.
func (h *Handle) applyAuthorizationInteraction(
	ctx context.Context,
	m *executionMutation,
	i Interaction,
	call executiondb.ReadExecutionToolRow,
) error {
	outcome := "canceled"
	value := map[string]any{
		"error_code": "mcp_authorization_required",
		"error":      "The MCP authorization request was canceled before access was granted.",
	}
	if i.State == "resolved" {
		request, err := mcpauthorization.ParseRequest(i.Request)
		if err != nil {
			return err
		}
		answers, err := interactionform.ParseResolution(request.Form, i.Resolution)
		if err != nil {
			return err
		}
		granted, err := mcpauthorization.Granted(request, answers)
		if err != nil {
			return err
		}
		if granted {
			if call.State != "waiting" {
				return storeerr.ErrStateTransitionConflict
			}
			if err = h.transitionTool(ctx, m, call, "ready", uuid.Nil, uuid.Nil); err != nil {
				return err
			}
			h.interactionNotification(i, "ready")
			return nil
		}
		outcome = "denied"
		value = map[string]any{
			"error_code":     "mcp_authorization_required",
			"error":          fmt.Sprintf("The user declined to authorize %s, so %s was not run.", request.ServerKey, request.ToolName),
			"mcp_server_key": request.ServerKey,
			"mcp_tool_name":  request.ToolName,
		}
	}
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = h.completeTool(ctx, m, call, outcome, []Content{{Kind: "structured_data", Data: body}}, uuid.Nil,
		&notifications.AgentInteractionUpdate{ID: i.ID, InteractionKind: i.Kind, State: i.State})
	return err
}

func (h *Handle) SupersedeInteractions(ctx context.Context, inputID uuid.UUID) ([]uuid.UUID, error) {
	return executeCommand(ctx, h, func(m *executionMutation) ([]uuid.UUID, error) {
		row, err := executiondb.New().
			ReadExecutionInput(ctx,
				h.unit.DB(),
				executiondb.ReadExecutionInputParams{AgentID: h.route.AgentID,
					ID: inputID})
		if err != nil {
			return nil, err
		}
		if row.State != "received" || row.DeliveryMode != "steering" || row.InputKind != "content" {
			return nil, storeerr.ErrStateTransitionConflict
		}
		snapshot, err := h.LoadExecution(ctx)
		if err != nil {
			return nil, err
		}
		if !snapshot.Selection.TurnContinuable {
			return nil, nil
		}
		return h.cancelInteractions(ctx, m, m.head.CurrentTurnID, inputID, "superseded_by_input")
	})
}

func (h *Handle) cancelInteractions(
	ctx context.Context,
	m *executionMutation,
	turn, input uuid.UUID,
	reason string,
) ([]uuid.UUID, error) {
	interactions, err := h.cancelInteractionRows(ctx, turn, input, reason)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(interactions))
	for _, i := range interactions {
		call, err := h.tool(ctx, i.ToolID)
		if err != nil {
			return nil, err
		}
		if err = h.applyInteraction(ctx, m, i, call); err != nil {
			return nil, err
		}
		ids = append(ids, i.ID)
	}
	return ids, nil
}

func (h *Handle) cancelInteractionRows(
	ctx context.Context, turn, input uuid.UUID, reason string,
) ([]Interaction, error) {
	q := executiondb.New()
	ids, err := q.ListExecutionInteractions(
		ctx,
		h.unit.DB(),
		executiondb.ListExecutionInteractionsParams{AgentID: h.route.AgentID, TurnID: turn},
	)
	if err != nil {
		return nil, err
	}
	resolution, err := json.Marshal(map[string]string{"reason": reason})
	if err != nil {
		return nil, err
	}
	result := make([]Interaction, 0, len(ids))
	for _, id := range ids {
		row, err := q.ReadExecutionInteraction(
			ctx,
			h.unit.DB(),
			executiondb.ReadExecutionInteractionParams{AgentID: h.route.AgentID, ID: id},
		)
		if err != nil {
			return nil, err
		}
		n, err := q.ResolveExecutionInteraction(
			ctx,
			h.unit.DB(),
			executiondb.ResolveExecutionInteractionParams{
				AgentID: h.route.AgentID, ID: id, State: "canceled", Resolution: resolution, InputID: storeutil.IDFromNil(input)},
		)
		if err != nil {
			return nil, err
		}
		if n != 1 {
			return nil, storeerr.ErrStateTransitionConflict
		}
		i := interactionRecord(row)
		i.State = "canceled"
		i.Resolution = resolution
		i.InputID = input
		result = append(result, i)
	}
	return result, nil
}

func (h *Handle) RecordPresentation(
	ctx context.Context,
	id uuid.UUID,
	destination, receipt json.RawMessage,
) (bool, error) {
	if _, err := h.Route(); err != nil {
		return false, err
	}
	if _, err := objectJSON(receipt); err != nil {
		return false, err
	}
	q := executiondb.New()
	row, err := q.ReadExecutionInteraction(
		ctx,
		h.unit.DB(),
		executiondb.ReadExecutionInteractionParams{AgentID: h.route.AgentID, ID: id},
	)
	if err != nil {
		return false, err
	}
	if row.Destination == nil || !equalJSON(*row.Destination, destination) {
		return false, storeerr.ErrUnauthorized
	}
	if row.PresentationReceipt != nil {
		return false, nil
	}
	n, err := q.WriteExecutionPresentation(
		ctx,
		h.unit.DB(),
		executiondb.WriteExecutionPresentationParams{
			AgentID:     h.route.AgentID,
			ID:          id,
			Destination: destination,
			Receipt:     receipt,
		},
	)
	if err == nil && n != 1 {
		return false, storeerr.ErrIdempotencyConflict
	}
	return n > 0, err
}
