package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func runManagedChannelSendAsync(ctx context.Context, call asyncToolContext) (asyncPhaseResult, error) {
	input, err := parseSendChannelMessageRequest(call.Call.Input)
	if err != nil {
		return channelOperationFailure("", "invalid_channel_request", err.Error(), channelconnector.OperationFailed)
	}
	channelID, err := publicid.Decode(publicid.KindIntegrationTarget, input.ChannelID)
	if err != nil {
		return nil, err
	}
	e := call.Executor
	prepared, err := e.prepareChannelOperation(ctx, call, channelID, channelconnector.OperationSend)
	if err != nil {
		return channelOperationFailure("", "channel_unavailable",
			"The channel is unavailable or does not permit this managed operation.", channelconnector.OperationFailed)
	}
	if err := channelMessageCapabilities(input.Message, prepared.access); err != nil {
		return channelOperationFailure(prepared.request.RequestID, "unsupported_channel_message",
			err.Error(), channelconnector.OperationFailed)
	}
	prepared.request.Artifacts, err = e.channelOperationArtifacts(ctx, call.Turn, input.Message.ArtifactIDs)
	if err != nil {
		return channelOperationFailure(prepared.request.RequestID, "artifact_unavailable",
			"A requested artifact is not available to this agent.", channelconnector.OperationFailed)
	}
	access, params, err := e.Store.Execution().PrepareChannelSend(ctx, prepared.owner)
	if err != nil {
		return channelSendPreparationFailure(prepared.request.RequestID, err)
	}
	if err := channelMessageCapabilities(input.Message, access); err != nil {
		return channelOperationFailure(prepared.request.RequestID, "unsupported_channel_message",
			err.Error(), channelconnector.OperationFailed)
	}
	payload := channelconnector.SendPayload{
		Destination: channelOperationDestination(access), Message: input.Message, Params: params,
	}
	if access.Capabilities.CreatesReplyChannel && prepared.binding.ReplyChannelGrants != nil {
		grants := prepared.binding.ReplyChannelGrants
		payload.ReplyChannelGrants = &channelconnector.ChannelGrants{
			Receive: grants.ReceiveAllowed, Read: grants.ReadAllowed, Send: grants.SendAllowed,
		}
	}
	prepared.request.Payload, err = json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	result, err := e.ChannelOperations.Execute(ctx, prepared.request)
	if err != nil || result.Outcome != channelconnector.OperationCompleted {
		return channelTransportFailure(prepared.request.RequestID, result, err)
	}
	if result.RequestID != prepared.request.RequestID {
		return channelOperationFailure(prepared.request.RequestID, "channel_operation_unknown",
			"The response could not be correlated with this send; publication is unknown.", channelconnector.OperationUnknown)
	}
	sent, err := channelconnector.DecodeSendResult(result.Payload)
	if err != nil {
		return channelOperationFailure(prepared.request.RequestID, "channel_operation_unknown",
			"The provider response did not establish a valid send result; publication is unknown.", channelconnector.OperationUnknown)
	}
	if _, err := e.Store.Execution().CompleteChannelOperation(ctx, executionstore.CompleteChannelOperationInput{
		Prepared: prepared.owner, Payload: prepared.request.Payload, Result: result,
	}); err == nil {
		// Completion already committed the canonical result and any child binding.
		// The existing release path accepts a completed tool without another write.
		return awaitDurableAsynchronously(), nil
	}
	return channelSendCompletionFailure(prepared, input.Message, sent)
}

func channelSendPreparationFailure(requestID string, err error) (asyncPhaseResult, error) {
	if errors.Is(err, storeerr.ErrInvalidRequest) {
		return channelOperationFailure(requestID, "invalid_send_params", err.Error(), channelconnector.OperationFailed)
	}
	return channelOperationFailure(requestID, "channel_authority_changed",
		"Channel access or operation ownership is no longer valid.", channelconnector.OperationFailed)
}

func channelMessageCapabilities(message channelconnector.Message, access integrationstore.ChannelAccess) error {
	if message.Text != "" && !access.Capabilities.Text {
		return errors.New("this channel does not support message text")
	}
	if len(message.ArtifactIDs) != 0 && !access.Capabilities.Artifacts {
		return errors.New("this channel does not support artifact attachments")
	}
	return nil
}

func channelSendCompletionFailure(
	prepared preparedChannelOperation,
	message channelconnector.Message,
	sent channelconnector.SendResult,
) (asyncPhaseResult, error) {
	observation := channelconnector.MessageObservation{
		Content: message, Publication: sent.Publication, MessageID: sent.MessageID,
		CreatedAt: sent.CreatedAt, Metadata: sent.Metadata,
	}
	if sent.MessageChannel == channelconnector.MessageAtDestination {
		observation.ChannelID = prepared.request.Scope.ChannelID
	}
	result := channelOperationToolResult{
		RequestID: prepared.request.RequestID, Message: &observation,
		Status: channelconnector.OperationFailed, Code: "channel_operation_completion_failed",
		Detail: "The provider reported publication or staging, but local completion could not be confirmed. " +
			"Do not resend the message.",
	}
	// A failed/ambiguous local commit cannot establish a registered child. Retain
	// known publication, including its actual containing location when known;
	// the ordinary tool owner will reject overwriting an already committed result.
	if sent.ReplyChannel != nil {
		result.Detail += " Reply channel registration could not be confirmed."
	}
	content, err := structuredToolResultContent(result)
	if err != nil {
		return nil, err
	}
	return failAsynchronously(content, errors.New("channel_operation_completion_failed")), nil
}

func runManagedChannelReadAsync(ctx context.Context, call asyncToolContext) (asyncPhaseResult, error) {
	input, err := resolveChannelHistoryRequest(call.Call.Input, call.Turn)
	if err != nil {
		return channelOperationFailure("", "invalid_channel_request", err.Error(), channelconnector.OperationFailed)
	}
	e := call.Executor
	prepared, err := e.prepareChannelOperation(ctx, call, input.ChannelID, channelconnector.OperationRead)
	if err != nil {
		return channelOperationFailure("", "channel_unavailable",
			"The channel is unavailable or does not permit history reads.", channelconnector.OperationFailed)
	}
	// Recheck the owning store's exact tool/runtime and binding pin before dispatch.
	access, err := e.Store.Execution().RecheckChannelOperation(ctx, prepared.owner)
	if err != nil {
		return channelOperationFailure(prepared.request.RequestID, "channel_authority_changed",
			"Channel read access or operation ownership is no longer valid.", channelconnector.OperationFailed)
	}
	prepared.request.Payload, err = json.Marshal(channelconnector.ReadPayload{
		Destination: channelOperationDestination(access), Limit: input.Limit, Cursor: input.ProviderCursor,
	})
	if err != nil {
		return nil, err
	}
	result, err := e.ChannelOperations.Execute(ctx, prepared.request)
	if err != nil || result.Outcome != channelconnector.OperationCompleted {
		return channelTransportFailure(prepared.request.RequestID, result, err)
	}
	if result.RequestID != prepared.request.RequestID {
		return channelOperationFailure(prepared.request.RequestID, "invalid_history_result",
			"The history response did not match this request.", channelconnector.OperationFailed)
	}
	if _, err := e.Store.Execution().CompleteChannelOperation(ctx, executionstore.CompleteChannelOperationInput{
		Prepared: prepared.owner, Payload: prepared.request.Payload, Result: result,
	}); err != nil {
		return channelOperationFailure(prepared.request.RequestID, "invalid_history_result",
			"The history page could not be validated and recorded with current channel authority.", channelconnector.OperationFailed)
	}
	return awaitDurableAsynchronously(), nil
}
