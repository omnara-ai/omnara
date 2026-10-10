package agentexecution

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (h *Handle) child(ctx context.Context, route AgentRoute) (*Handle, error) {
	child, err := h.unit.Agent(route)
	if err != nil {
		return nil, err
	}
	if route.ProjectID != h.route.ProjectID || route.RootAgentID != h.route.RootAgentID {
		return nil, storeerr.ErrNotFound
	}
	identity, err := executiondb.New().
		ReadExecutionAgentIdentity(ctx,
			h.unit.DB(),
			executiondb.ReadExecutionAgentIdentityParams{ProjectID: route.ProjectID,
				ID: route.AgentID})
	if err != nil {
		return nil, err
	}
	if valueOrZero(identity.ParentAgentID) != h.route.AgentID {
		return nil, storeerr.ErrNotFound
	}
	if identity.State != "active" {
		return nil, storeerr.ErrStateTransitionConflict
	}
	return child, nil
}

func (h *Handle) childCommand(
	ctx context.Context,
	route AgentRoute,
	completion ToolCompletion,
	apply func(*Handle) error,
) (CompletedTool, error) {
	return executeCommand(ctx, h, func(_ *executionMutation) (CompletedTool, error) {
		if err := h.FenceRuntime(ctx, completion.RuntimeLockID); err != nil {
			return CompletedTool{}, err
		}
		call, err := h.tool(ctx, completion.ID)
		if err != nil {
			return CompletedTool{}, err
		}
		if call.State == "completed" {
			return h.CompleteTool(ctx, completion)
		}
		if call.State != "ready" || call.Type != "built_in" {
			return CompletedTool{}, storeerr.ErrInvalidToolCallDisposition
		}
		child, err := h.child(ctx, route)
		if err != nil {
			return CompletedTool{}, err
		}
		if err = apply(child); err != nil {
			return CompletedTool{}, err
		}
		return h.CompleteTool(ctx, completion)
	})
}

func (h *Handle) SendToChild(
	ctx context.Context,
	route AgentRoute,
	actorID uuid.UUID,
	message string,
	completion ToolCompletion,
) (CompletedTool, error) {
	return h.childCommand(ctx, route, completion, func(child *Handle) error {
		if message == "" {
			return errors.New("child message is required")
		}
		id, err := publicid.Encode(publicid.KindAgent, h.route.AgentID)
		if err != nil {
			return err
		}
		metadata, err := json.Marshal(map[string]any{"parent_message": map[string]string{"agent_id": id}})
		if err != nil {
			return err
		}
		received, err := child.ReceiveContent(
			ctx,
			ReceiveContentInput{
				ActorID:          actorID,
				DeliveryMode:     "steering",
				IdempotencyScope: "subagent_message",
				IdempotencyKey:   "tool_call:" + completion.ID.String(),
				Metadata:         metadata,
				Content:          []Content{{Kind: "text", Text: message}},
			},
		)
		if err != nil {
			return err
		}
		if received.Created {
			_, err = child.SupersedeInteractions(ctx, received.ID)
		}
		return err
	})
}

func (h *Handle) CancelChild(
	ctx context.Context,
	route AgentRoute,
	actorID uuid.UUID,
	completion ToolCompletion,
) (CompletedTool, error) {
	return h.childCommand(ctx, route, completion, func(child *Handle) error {
		_, err := child.Cancel(
			ctx,
			CancelInput{
				ActorID: actorID,
				Reason:  "agent_canceled",
				Message: "The model call was canceled by the parent agent.",
			},
		)
		return err
	})
}
