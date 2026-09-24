package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type setInteractionHandlerRequest struct {
	Handler *string         `json:"handler"`
	Args    json.RawMessage `json:"args"`
}

func interactionToolRegistrations() []toolRegistration {
	return []toolRegistration{
		{
			name:            toolcatalog.ToolNameListInteractionHandlers,
			handler:         toolHandler{Transactional: listInteractionHandlers},
			permissionModes: commonPermissionModeHandlers(genericPermissionChallenge),
		},
		{
			name:            toolcatalog.ToolNameSetInteractionHandler,
			handler:         toolHandler{Transactional: setInteractionHandler},
			permissionModes: commonPermissionModeHandlers(genericPermissionChallenge),
		},
	}
}

func listInteractionHandlers(
	ctx context.Context,
	call transactionalToolContext,
) (transactionalPhaseResult, error) {
	var input struct {
		Cursor string `json:"cursor"`
		Limit  int    `json:"limit"`
	}
	if err := decodeSingleStrictJSON(
		call.Call.Input,
		&input,
		"list_interaction_handlers request",
	); err != nil {
		return nil, err
	}
	if err := authorizeToolExecution(
		ctx,
		call.Reader,
		call.Turn,
		call.Call,
		call.Call.Input,
	); err != nil {
		return nil, err
	}
	page, err := call.Reader.ListInteractionHandlers(ctx, input.Cursor, input.Limit)
	if err != nil {
		return nil, err
	}
	content, err := structuredToolResultContent(page)
	if err != nil {
		return nil, err
	}
	return completeInTransaction(content), nil
}

func setInteractionHandler(
	ctx context.Context,
	call transactionalToolContext,
) (transactionalPhaseResult, error) {
	var input setInteractionHandlerRequest
	if err := decodeSingleStrictJSON(
		call.Call.Input,
		&input,
		"set_interaction_handler request",
	); err != nil {
		return nil, err
	}
	if err := authorizeToolExecution(
		ctx,
		call.Reader,
		call.Turn,
		call.Call,
		call.Call.Input,
	); err != nil {
		return nil, err
	}
	selection := executionstore.SelectInteractionHandlerInput{Args: input.Args}
	var current *setInteractionHandlerRequest
	if input.Handler != nil {
		selection.HandlerKey = *input.Handler
		current = &input
	}
	content, err := structuredToolResultContent(struct {
		Selection *setInteractionHandlerRequest `json:"selection"`
	}{Selection: current})
	if err != nil {
		return nil, err
	}
	completion, err := successfulToolCallCompletion(content)
	if err != nil {
		return nil, err
	}
	return executeInTransaction(
		executionstore.SetInteractionHandlerForToolCall(selection, completion),
		func(err error) (transactionalPhaseResult, error) {
			if !errors.Is(err, storeerr.ErrNotFound) && !errors.Is(err, storeerr.ErrUnauthorized) &&
				!errors.Is(
					err,
					storeerr.ErrConflict,
				) && !errors.Is(err, storeerr.ErrStateTransitionConflict) && !errors.Is(err, storeerr.ErrInvalidRequest) {
				return nil, err
			}
			content, contentErr := toolFailureContent(
				"interaction_handler_unavailable",
				"Handler or arguments are no longer eligible. Call list_interaction_handlers and select a handler using its argument schema.",
				true,
			)
			if contentErr != nil {
				return nil, contentErr
			}
			return failInTransaction(content, err), nil
		},
	), nil
}
