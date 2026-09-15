package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
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
	definitionID, definitionOK := parseOpenAPIPublicID(publicid.KindChannelDefinition, body.Target.DefinitionId)
	if !routeOK || !definitionOK {
		return input, uuid.Nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	input.Target.ChannelDefinitionID = definitionID
	if body.Target.ParentChannelId != nil {
		id, ok := parseOpenAPIPublicID(publicid.KindIntegrationTarget, *body.Target.ParentChannelId)
		if !ok {
			return input, routeID, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
		}
		input.Target.ParentChannelID = id
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
	for _, field := range []struct {
		name, value string
		maxBytes    int
	}{
		{"instance key", body.InstanceKey, 512},
		{"provider ref", body.Target.ProviderRef, 512},
		{"provider ref kind", body.Target.ProviderRefKind, 128},
	} {
		if strings.TrimSpace(field.value) == "" || len(field.value) > field.maxBytes {
			return input, routeID, apierror.FromCode(openapi.ErrorCodeInvalidRequest, field.name+" is empty or too large")
		}
		if err := dbsafe.Text(field.value); err != nil {
			return input, routeID, apierror.FromCode(openapi.ErrorCodeInvalidRequest, field.name+": "+err.Error())
		}
	}
	input.Target.ProviderRef, input.Target.ProviderRefKind = body.Target.ProviderRef, body.Target.ProviderRefKind
	if body.Target.DisplayName != nil {
		input.Target.DisplayName = *body.Target.DisplayName
	}
	if len(input.Target.DisplayName) > 512 {
		return input, routeID, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "target display name exceeds 512 bytes")
	}
	if err := dbsafe.Text(input.Target.DisplayName); err != nil {
		return input, routeID, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	if body.Target.ProviderMetadata != nil {
		input.Target.ProviderMetadata, err = channelconnector.NormalizeOpaqueObject(body.Target.ProviderMetadata)
		if err != nil {
			return input, routeID, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "target metadata: "+err.Error())
		}
	}
	input.ReadAllowed, input.SendAllowed = body.Grants.Read, body.Grants.Send
	return input, routeID, nil
}
