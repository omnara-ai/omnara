package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func prepareChannelSend(ctx context.Context, call transactionalToolContext) (transactionalPhaseResult, error) {
	input, err := parseSendChannelMessageRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	channelID, err := publicid.Decode(publicid.KindIntegrationTarget, input.ChannelID)
	if err != nil {
		return nil, err
	}
	access, err := call.Reader.GetChannelAccess(ctx, channelID)
	if err != nil || !access.Active || !access.Capabilities.Send {
		return channelRequestFailure("The selected channel is unavailable or does not permit sending.")
	}
	if err := channelMessageCapabilities(input.Message, access); err != nil {
		return channelRequestFailure(err.Error())
	}
	params, err := channelconnector.ValidateSendParams(access.SendParamsSchema, input.Params)
	if err != nil {
		return channelRequestFailure(err.Error())
	}
	if access.IntegrationKind == integrationstore.IntegrationKindManaged {
		return continueAsync(), nil
	}
	if access.IntegrationKind != integrationstore.IntegrationKindExternal {
		return channelRequestFailure("The selected connection does not support channel operations.")
	}
	payload, err := json.Marshal(channelconnector.SendPayload{
		Destination: channelOperationDestination(access), Message: input.Message, Params: params,
	})
	if err != nil {
		return nil, err
	}
	return externalChannelRequestCommand(call, access, channelconnector.OperationSend, payload), nil
}

func prepareChannelRead(ctx context.Context, call transactionalToolContext) (transactionalPhaseResult, error) {
	input, err := resolveChannelHistoryRequest(call.Call.Input, call.Turn)
	if err != nil {
		return channelRequestFailure(err.Error())
	}
	access, err := call.Reader.GetChannelAccess(ctx, input.ChannelID)
	if err != nil || !access.Active || !access.Capabilities.Read {
		return channelRequestFailure("The selected channel is unavailable or does not permit reading history.")
	}
	if access.IntegrationKind == integrationstore.IntegrationKindManaged {
		return continueAsync(), nil
	}
	if access.IntegrationKind != integrationstore.IntegrationKindExternal {
		return channelRequestFailure("The selected connection does not support channel operations.")
	}
	payload, err := json.Marshal(channelconnector.ReadPayload{
		Destination: channelOperationDestination(access), Limit: input.Limit, Cursor: input.ProviderCursor,
	})
	if err != nil {
		return nil, err
	}
	return externalChannelRequestCommand(call, access, channelconnector.OperationRead, payload), nil
}

func externalChannelRequestCommand(
	call transactionalToolContext,
	access integrationstore.ChannelAccess,
	operation channelconnector.OperationKind,
	payload json.RawMessage,
) transactionalPhaseResult {
	return executeInTransaction(executionstore.CreateExternalChannelRequestForToolCall(
		executionstore.CreateExternalChannelRequestInput{
			TurnID: call.Turn.TurnID, ChannelID: access.ChannelID, Operation: operation, Payload: payload,
			CreatesReplyChannel: operation == channelconnector.OperationSend && access.Capabilities.CreatesReplyChannel,
			Timeout:             executionstore.ExternalChannelRequestTimeout,
		}), func(error) (transactionalPhaseResult, error) {
		return channelRequestFailure("The channel request could not be admitted with current authority.")
	})
}

func channelRequestFailure(detail string) (transactionalPhaseResult, error) {
	content, err := structuredToolResultContent(channelOperationToolResult{
		Status: channelconnector.OperationFailed, Code: "invalid_channel_request", Detail: detail,
	})
	if err != nil {
		return nil, err
	}
	return failInTransaction(content, errors.New("invalid_channel_request")), nil
}
