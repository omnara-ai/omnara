package httpapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func (s strictOpenAPIServer) DeliverChannelConnectorInput(
	ctx context.Context,
	request openapi.DeliverChannelConnectorInputRequestObject,
) (openapi.DeliverChannelConnectorInputResponseObject, error) {
	scope, err := channelConnectorScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	install, err := s.channelConnectorInstallationByPublicID(
		ctx, scope, request.IntegrationAppID, request.IntegrationInstallID,
	)
	if err != nil {
		return nil, err
	}
	body := *request.Body
	bindingID, ok := parseOpenAPIPublicID(publicid.KindIntegrationBinding, body.BindingId)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	fields, err := channelDeliveryFields(body.Receipt, body.Author, body.Metadata,
		body.DeliveryMode, body.CancelOpenInteractions)
	if err != nil {
		return nil, err
	}
	plan, err := channelInputContentPlan(body.ContentBlocks)
	if err != nil {
		return nil, err
	}
	prepared, err := s.server.store.Execution().PrepareBoundChannelInput(ctx, executionstore.ChannelBindingInputIdentity{
		ProjectID: install.ProjectID, IntegrationInstallID: install.ID, BindingID: bindingID,
		Capabilities: scope.Capabilities,
	})
	if err != nil {
		return nil, apierror.FromError(err)
	}
	content, err := s.server.prepareChannelInputMedia(ctx, mediaIngestContext{
		ProjectID: install.ProjectID, AgentID: prepared.AgentID(),
		IdempotencyKey: fmt.Sprintf("channel-receipt:%s:binding:%s", fields.Receipt.ReceiptID, bindingID),
	}, plan)
	if err != nil {
		return nil, mediaIngestAPIError(err)
	}
	input := executionstore.DeliverBoundChannelInput{
		Prepared: prepared, Receipt: fields.Receipt, InputKey: body.InputKey, Content: content,
		ProviderUserID: fields.ProviderUserID, ActorDisplayName: fields.ActorDisplayName,
		Metadata: fields.Metadata, DeliveryMode: fields.DeliveryMode, CancelOpenInteractions: fields.CancelOpenInteractions,
	}
	if body.InputPrecondition != nil {
		input.InputPrecondition = &executionstore.ChannelInputPrecondition{
			InputKey: body.InputPrecondition.InputKey, Exists: body.InputPrecondition.Exists,
		}
	}
	// Admission owns upload compensation for every outcome, including replay.
	result, err := s.server.store.Execution().DeliverBoundChannelInput(ctx, input)
	if errors.Is(err, executionstore.ErrChannelInputPreconditionChanged) {
		return nil, apierror.FromCode(openapi.ErrorCodeStateTransitionConflict, err.Error())
	}
	if err != nil {
		return nil, apierror.FromError(err)
	}
	response, err := publicChannelInputResult(result)
	if err != nil {
		return nil, err
	}
	return openapi.DeliverChannelConnectorInput200JSONResponse(response), nil
}
