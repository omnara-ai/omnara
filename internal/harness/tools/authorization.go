package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

var ErrToolAuthorizationInvalidated = errors.New("tool_authorization_invalidated")

func authorizeToolExecution(
	ctx context.Context,
	reader *executionstore.ToolCallReader,
	turn Turn,
	call model.ToolCall,
	authorizationInput json.RawMessage,
) error {
	toolCall, err := reader.GetToolCall(ctx)
	if err != nil {
		return err
	}
	if existing, found, err := reader.GetAgentInteractionByToolCallKind(
		ctx,
		executionstore.AgentInteractionKindPermission,
	); err != nil {
		return err
	} else if found {
		if existing.State != executionstore.AgentInteractionStateResolved {
			return storeerr.ErrIdempotencyConflict
		}
		selection := turn.Tools[call.Name].Permission
		if toolCallAuthorizationMatches(
			existing,
			call,
			toolCall.ID,
			selection,
			authorizationInput,
		) {
			return nil
		}
		return fmt.Errorf(
			"%w: the approved tool request no longer matches the executable request",
			ErrToolAuthorizationInvalidated,
		)
	}
	return authorizeDirectToolExecution(turn, call)
}

func authorizeDirectToolExecution(
	turn Turn,
	call model.ToolCall,
) error {
	spec, ok := turn.Tools[call.Name]
	if !ok {
		return fmt.Errorf("tool %q has no runtime permission", call.Name)
	}
	permission := spec.Permission
	switch permission.Mode {
	case toolpermission.ModeAlwaysAllow:
		return nil
	case toolpermission.ModeAlwaysDeny:
		return fmt.Errorf(
			"denied tool %q reached execution after permission gate",
			call.Name,
		)
	case toolpermission.ModeAlwaysAsk:
		return fmt.Errorf(
			"permission-required tool %q reached execution without blocked preflight",
			call.Name,
		)
	default:
		return fmt.Errorf(
			"permission mode %q reached execution without an interaction",
			permission.Mode,
		)
	}
}

func toolCallAuthorizationMatches(
	action executionstore.AgentInteractionRecord,
	call model.ToolCall,
	toolCallID storage.ID,
	selection toolpermission.Selection,
	authorizationInput json.RawMessage,
) bool {
	request, ok := matchingToolPermissionRequest(action, call, toolCallID, selection)
	return ok && jsoncanonical.Equal(request.Authorization.Input, authorizationInput)
}

func toolCallPermissionMatches(
	action executionstore.AgentInteractionRecord,
	call model.ToolCall,
	toolCallID storage.ID,
	selection toolpermission.Selection,
) bool {
	_, ok := matchingToolPermissionRequest(action, call, toolCallID, selection)
	return ok
}

func matchingToolPermissionRequest(
	action executionstore.AgentInteractionRecord,
	call model.ToolCall,
	toolCallID storage.ID,
	selection toolpermission.Selection,
) (toolpermission.Request, bool) {
	if action.ToolCallID != toolCallID || action.ProviderCallID != call.ID ||
		action.InteractionKind != executionstore.AgentInteractionKindPermission {
		return toolpermission.Request{}, false
	}
	request, err := toolpermission.ParseRequest(action.Request)
	if err != nil {
		return toolpermission.Request{}, false
	}
	return request, request.Authorization.ToolName == call.Name &&
		request.Permission.Mode == selection.Mode &&
		jsoncanonical.Equal(request.Permission.Parameters, selection.Parameters)
}
