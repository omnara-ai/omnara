package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type interactionDestinationSelection struct {
	TargetID string `json:"target_id"`
	Resource string `json:"resource"`
}

type interactionDestinationChoice struct {
	interactionDestinationSelection
	ConnectionID      string                               `json:"connection_id"`
	HandlerDefinition string                               `json:"handler_definition"`
	Scope             integrationstore.ConversationAddress `json:"scope"`
	DisplayName       string                               `json:"display_name,omitempty"`
}

type interactionDestinationList struct {
	Current      *interactionDestinationSelection `json:"current"`
	Destinations []interactionDestinationChoice   `json:"destinations"`
}

type setInteractionDestinationRequest struct {
	Destination *interactionDestinationSelection `json:"destination"`
}

func interactionToolRegistrations() []toolRegistration {
	return []toolRegistration{
		{
			name:            toolcatalog.ToolNameListInteractionDestinations,
			handler:         toolHandler{Transactional: listInteractionDestinations},
			permissionModes: commonPermissionModeHandlers(genericPermissionChallenge),
		},
		{
			name:            toolcatalog.ToolNameSetInteractionDestination,
			handler:         toolHandler{Transactional: setInteractionDestination},
			permissionModes: commonPermissionModeHandlers(genericPermissionChallenge),
		},
	}
}

func listInteractionDestinations(ctx context.Context, call transactionalToolContext) (transactionalPhaseResult, error) {
	if err := authorizeToolExecution(ctx, call.Reader, call.Turn, call.Call, call.Call.Input); err != nil {
		return nil, err
	}
	destinations, err := call.Reader.ListInteractionDestinations(ctx)
	if err != nil {
		return nil, err
	}
	result, err := renderInteractionDestinations(destinations)
	if err != nil {
		return nil, err
	}
	content, err := structuredToolResultContent(result)
	if err != nil {
		return nil, err
	}
	return completeInTransaction(content), nil
}

func renderInteractionDestinations(input executionstore.InteractionDestinations) (interactionDestinationList, error) {
	result := interactionDestinationList{Destinations: []interactionDestinationChoice{}}
	for _, option := range input.Destinations {
		destination := option.Destination
		targetID, err := publicid.Encode(publicid.KindIntegrationTarget, destination.IntegrationTargetID)
		if err != nil {
			return result, err
		}
		connectionID, err := publicid.Encode(publicid.KindIntegrationConnection, destination.ConnectionID)
		if err != nil {
			return result, err
		}
		selection := interactionDestinationSelection{TargetID: targetID, Resource: destination.ResourceKey}
		result.Destinations = append(result.Destinations, interactionDestinationChoice{
			interactionDestinationSelection: selection,
			ConnectionID:                    connectionID, HandlerDefinition: destination.HandlerDefinition,
			Scope: destination.Address, DisplayName: option.DisplayName,
		})
		// Revocation can leave a stored choice behind until reconciliation.
		// Only an eligible option represents an effective current destination.
		if input.Current.IntegrationTargetID == destination.IntegrationTargetID &&
			input.Current.ResourceKey == destination.ResourceKey {
			result.Current = &selection
		}
	}
	return result, nil
}

func setInteractionDestination(ctx context.Context, call transactionalToolContext) (transactionalPhaseResult, error) {
	var input setInteractionDestinationRequest
	if err := decodeSingleStrictJSON(call.Call.Input, &input, "set_interaction_destination request"); err != nil {
		return nil, err
	}
	if err := authorizeToolExecution(ctx, call.Reader, call.Turn, call.Call, call.Call.Input); err != nil {
		return nil, err
	}
	var selection executionstore.InteractionSelection
	if input.Destination != nil {
		id, err := publicid.Decode(publicid.KindIntegrationTarget, input.Destination.TargetID)
		if err != nil {
			return nil, fmt.Errorf("invalid target_id: %w", err)
		}
		selection = executionstore.InteractionSelection{
			IntegrationTargetID: id,
			ResourceKey:         input.Destination.Resource,
		}
	}
	content, err := structuredToolResultContent(struct {
		Current *interactionDestinationSelection `json:"current"`
	}{Current: input.Destination})
	if err != nil {
		return nil, err
	}
	completion, err := successfulToolCallCompletion(content)
	if err != nil {
		return nil, err
	}
	return executeInTransaction(executionstore.SetInteractionDestinationForToolCall(selection, completion),
		func(err error) (transactionalPhaseResult, error) {
			if !errors.Is(err, storeerr.ErrNotFound) && !errors.Is(err, storeerr.ErrUnauthorized) &&
				!errors.Is(err, storeerr.ErrConflict) && !errors.Is(err, storeerr.ErrStateTransitionConflict) {
				return nil, err
			}
			content, contentErr := toolFailureContent("interaction_destination_unavailable",
				"Destination is no longer eligible. Call list_interaction_destinations to choose again.", true)
			if contentErr != nil {
				return nil, contentErr
			}
			return failInTransaction(content, err), nil
		}), nil
}
