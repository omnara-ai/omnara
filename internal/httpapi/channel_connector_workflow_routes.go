package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

// One concurrent winner can change the provisional agent. Reprepare once for
// that durable winner; no other error (especially COMMIT failure) is retried here.
const channelWorkflowPreparationAttempts = 2

func (s strictOpenAPIServer) DeliverChannelConnectorWorkflow(
	ctx context.Context,
	request openapi.DeliverChannelConnectorWorkflowRequestObject,
) (openapi.DeliverChannelConnectorWorkflowResponseObject, error) {
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
	input, routeID, err := channelWorkflowDeliveryInput(body)
	if err != nil {
		return nil, err
	}
	plan, err := channelInputContentPlan(body.ContentBlocks)
	if err != nil {
		return nil, err
	}
	identity := executionstore.ChannelWorkflowIdentity{
		ProjectID: install.ProjectID, IntegrationInstallID: install.ID, IntegrationRouteID: routeID,
		InstanceKey: body.InstanceKey, Capabilities: scope.Capabilities,
	}
	for range channelWorkflowPreparationAttempts {
		prepared, err := s.server.store.Execution().PrepareChannelWorkflow(ctx, identity)
		if err != nil {
			return nil, apierror.FromError(err)
		}
		input.Prepared = prepared
		input.Content, err = s.server.prepareChannelInputMedia(ctx, mediaIngestContext{
			ProjectID: install.ProjectID, AgentID: prepared.AgentID(),
			IdempotencyKey: fmt.Sprintf("channel-receipt:%s:route:%s", input.Receipt.ReceiptID, routeID),
		}, plan)
		if err != nil {
			return nil, mediaIngestAPIError(err)
		}
		// Delivery owns the single outer compensation outcome, including replay
		// and winner races. The HTTP layer must never settle these uploads again.
		result, err := s.server.store.Execution().DeliverChannelWorkflow(ctx, input)
		if errors.Is(err, executionstore.ErrChannelWorkflowAgentChanged) {
			continue
		}
		if errors.Is(err, executionstore.ErrChannelInputPreconditionChanged) ||
			errors.Is(err, executionstore.ErrChannelRecipientsChanged) {
			return nil, apierror.FromCode(openapi.ErrorCodeStateTransitionConflict, err.Error())
		}
		if err != nil {
			return nil, apierror.FromError(err)
		}
		response, err := publicChannelInputResult(result)
		if err != nil {
			return nil, err
		}
		return openapi.DeliverChannelConnectorWorkflow200JSONResponse(response), nil
	}
	return nil, apierror.FromCode(openapi.ErrorCodeStateTransitionConflict, "workflow changed during preparation; retry")
}

func channelWorkflowDeliveryInput(
	body openapi.DeliverChannelConnectorWorkflowRequest,
) (executionstore.DeliverChannelWorkflowInput, uuid.UUID, error) {
	input := executionstore.DeliverChannelWorkflowInput{}
	input.InputKey = body.InputKey
	input.OnlyIfUnbound = body.OnlyIfUnbound != nil && *body.OnlyIfUnbound
	if body.InputPrecondition != nil {
		input.InputPrecondition = &executionstore.ChannelInputPrecondition{
			InputKey: body.InputPrecondition.InputKey, Exists: body.InputPrecondition.Exists,
		}
	}
	routeID, routeOK := parseOpenAPIPublicID(publicid.KindIntegrationRoute, body.RouteId)
	if !routeOK {
		return input, uuid.Nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	var err error
	input.Target, input.ParentTarget, err = channelRegistrationInput(body.Target)
	if err != nil {
		return input, routeID, err
	}
	if strings.TrimSpace(body.InstanceKey) == "" || len(body.InstanceKey) > 512 {
		return input, routeID, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "instance key is empty or too large")
	}
	if err := dbsafe.Text(body.InstanceKey); err != nil {
		return input, routeID, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "instance key: "+err.Error())
	}
	fields, err := channelDeliveryFields(body.Receipt, body.Author, body.Metadata,
		body.DeliveryMode, body.CancelOpenInteractions)
	if err != nil {
		return input, routeID, err
	}
	input.Receipt = fields.Receipt
	input.ProviderUserID, input.ActorDisplayName = fields.ProviderUserID, fields.ActorDisplayName
	input.Metadata, input.DeliveryMode = fields.Metadata, fields.DeliveryMode
	input.CancelOpenInteractions = fields.CancelOpenInteractions
	input.ReadAllowed, input.SendAllowed = body.Grants.Read, body.Grants.Send
	return input, routeID, nil
}
