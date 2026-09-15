package tools

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (e Executor) postIntegrationPromptToPinnedChannel(
	ctx context.Context, turn Turn, interaction executionstore.AgentInteractionRecord,
	beforeAttempt func(context.Context) error,
) error {
	ctx, cancel := context.WithTimeout(ctx, integrationPermissionPromptCopyTimeout)
	defer cancel()
	// Callers cannot replace the canonical target, kind or form with a stale copy.
	current, found, err := e.Store.Execution().GetAgentInteraction(ctx, turn.ProjectID, turn.AgentID, interaction.ID)
	if err != nil {
		return err
	}
	if !found || current.State != executionstore.AgentInteractionStateOpen ||
		current.IntegrationTargetID == storage.NilID {
		return nil
	}
	if current.TurnID != turn.TurnID {
		return storeerr.ErrUnauthorized
	}
	access, err := e.Store.Integrations().GetAgentChannelAccess(
		ctx, turn.ProjectID, turn.AgentID, current.IntegrationTargetID)
	if err != nil {
		return availableChannelOutputError(err)
	}
	if !access.Active || !access.Capabilities.Send ||
		(current.InteractionKind == executionstore.AgentInteractionKindPermission && !access.Capabilities.Permissions) ||
		(current.InteractionKind == executionstore.AgentInteractionKindQuestion && !access.Capabilities.Questions) {
		return nil
	}
	if beforeAttempt != nil {
		if err := beforeAttempt(ctx); err != nil {
			return err
		}
	}
	var prepared executionstore.PreparedChannelOutput
	if access.IntegrationKind == integrationstore.IntegrationKindManaged {
		prepared, err = e.Store.Execution().PrepareChannelPresentation(ctx, turn.ProjectID, turn.AgentID, current.ID)
		if err != nil {
			return availableChannelOutputError(err)
		}
		access, err = e.Store.Execution().RecheckChannelOutput(ctx, prepared)
		if err != nil {
			return availableChannelOutputError(err)
		}
	}
	payload, err := channelInteractionPayload(access, current)
	if err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	switch access.IntegrationKind {
	case integrationstore.IntegrationKindExternal:
		_, err = e.Store.Execution().CreateExternalChannelPresentation(ctx,
			executionstore.CreateExternalChannelPresentationInput{
				ProjectID: turn.ProjectID, AgentID: turn.AgentID, InteractionID: current.ID,
				Payload: body, Timeout: executionstore.ExternalChannelRequestTimeout,
			})
		return availableChannelOutputError(err)
	case integrationstore.IntegrationKindManaged:
		request, err := newChannelOperationRequest(access, payload.InteractionID, channelconnector.OperationInteraction)
		if err != nil {
			return err
		}
		request.Payload = body
		result, err := e.executeChannelOutput(ctx, request)
		if err != nil {
			return err
		}
		if _, err := channelconnector.DecodeInteractionResult(result.Payload); err != nil {
			return errors.New("channel presentation outcome is unknown: invalid result")
		}
		return nil
	default:
		return storeerr.ErrUnauthorized
	}
}

func channelInteractionPayload(
	access integrationstore.ChannelAccess, interaction executionstore.AgentInteractionRecord,
) (channelconnector.InteractionPayload, error) {
	payload := channelconnector.InteractionPayload{
		Destination: channelOperationDestination(access), Kind: string(interaction.InteractionKind),
	}
	var err error
	payload.AgentID, err = publicid.Encode(publicid.KindAgent, interaction.AgentID)
	if err != nil {
		return payload, err
	}
	payload.ChannelID, err = publicid.Encode(publicid.KindIntegrationTarget, interaction.IntegrationTargetID)
	if err != nil {
		return payload, err
	}
	payload.InteractionID, err = publicid.Encode(publicid.KindAgentInteraction, interaction.ID)
	if err != nil {
		return payload, err
	}
	payload.Form, err = interaction.Form()
	if err != nil {
		return payload, err
	}
	return payload, payload.Validate()
}

// PostIntegrationRuntimeMessage uses the selected channel under the runtime's
// authority. Managed I/O is bounded; external notices have the store's finite
// acceptance deadline. Neither path creates a canonical tool or interaction.
func (e Executor) PostIntegrationRuntimeMessage(ctx context.Context, turn Turn, text string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	channelID, err := e.Store.Execution().GetAgentCurrentChannelID(ctx, turn.ProjectID, turn.AgentID)
	if err != nil {
		return err
	}
	if channelID == storage.NilID {
		return nil
	}
	access, err := e.Store.Integrations().GetAgentChannelAccess(ctx, turn.ProjectID, turn.AgentID, channelID)
	if err != nil {
		return availableChannelOutputError(err)
	}
	if !access.Active || !access.Capabilities.Send {
		return nil
	}
	if access.IntegrationKind == integrationstore.IntegrationKindManaged {
		prepared, err := e.Store.Execution().PrepareChannelNotice(ctx, executionstore.PrepareChannelNoticeInput{
			ProjectID: turn.ProjectID, AgentID: turn.AgentID, TurnID: turn.TurnID, RuntimeLockID: turn.RuntimeLockID,
		})
		if err != nil {
			return availableChannelOutputError(err)
		}
		access, err = e.Store.Execution().RecheckChannelOutput(ctx, prepared)
		if err != nil {
			return availableChannelOutputError(err)
		}
	}
	message := channelconnector.Message{Text: text}
	if err := message.Validate(); err != nil {
		return err
	}
	if err := channelMessageCapabilities(message, access); err != nil {
		return err
	}
	params, err := channelconnector.ValidateSendParams(access.SendParamsSchema, nil)
	if err != nil {
		return errors.New("channel does not accept runtime notice parameters")
	}
	payload, err := json.Marshal(channelconnector.SendPayload{
		Destination: channelOperationDestination(access), Message: message, Params: params,
	})
	if err != nil {
		return err
	}
	switch access.IntegrationKind {
	case integrationstore.IntegrationKindExternal:
		_, err := e.Store.Execution().CreateExternalChannelNotice(ctx, executionstore.CreateExternalChannelNoticeInput{
			ProjectID: turn.ProjectID, AgentID: turn.AgentID, TurnID: turn.TurnID, RuntimeLockID: turn.RuntimeLockID,
			ChannelID: channelID, NoticeKey: "runtime_error:" + turn.RuntimeLockID.String(),
			Payload: payload, Timeout: executionstore.ExternalChannelRequestTimeout,
		})
		return err
	case integrationstore.IntegrationKindManaged:
		request, err := newChannelOperationRequest(access,
			"runtime_error:"+turn.TurnID.String()+":"+turn.RuntimeLockID.String(), channelconnector.OperationSend)
		if err != nil {
			return err
		}
		request.Payload = payload
		result, err := e.executeChannelOutput(ctx, request)
		if err != nil {
			return err
		}
		if _, err := channelconnector.DecodeSendResult(result.Payload); err != nil {
			return errors.New("channel runtime notice outcome is unknown: invalid result")
		}
		return nil
	default:
		return storeerr.ErrUnauthorized
	}
}

func availableChannelOutputError(err error) error {
	if errors.Is(err, storeerr.ErrNotFound) || errors.Is(err, storeerr.ErrUnauthorized) {
		return nil
	}
	return err
}

func (e Executor) executeChannelOutput(
	ctx context.Context, request channelconnector.OperationRequest,
) (channelconnector.OperationResult, error) {
	if e.ChannelOperations == nil {
		return channelconnector.OperationResult{}, errors.New("managed channel operations are not configured")
	}
	result, err := e.ChannelOperations.Execute(ctx, request)
	if err == nil && result.RequestID == request.RequestID && result.Outcome == channelconnector.OperationCompleted {
		return result, nil
	}
	var operationError *channelconnector.OperationError
	if (errors.As(err, &operationError) && operationError.Outcome == channelconnector.OperationFailed) ||
		(err == nil && result.RequestID == request.RequestID && result.Outcome == channelconnector.OperationFailed) {
		return channelconnector.OperationResult{}, errors.New("channel output failed without publication")
	}
	// Do not wrap provider errors or repeat a mutation whose outcome is unknown.
	return channelconnector.OperationResult{}, errors.New("channel output outcome is unknown; do not resend blindly")
}
