package httpapi

import (
	"context"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func (s strictOpenAPIServer) LookupChannelConnectorWorkflow(
	ctx context.Context,
	request openapi.LookupChannelConnectorWorkflowRequestObject,
) (openapi.LookupChannelConnectorWorkflowResponseObject, error) {
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
	routeID, ok := parseOpenAPIPublicID(publicid.KindIntegrationRoute, body.RouteId)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	result, err := s.server.store.Execution().LookupChannelWorkflow(ctx,
		executionstore.ChannelWorkflowIdentity{
			ProjectID: install.ProjectID, IntegrationInstallID: install.ID, IntegrationRouteID: routeID,
			InstanceKey: body.InstanceKey, Capabilities: scope.Capabilities,
		}, body.InputKeys,
	)
	if err != nil {
		return nil, apierror.FromError(err)
	}
	response := openapi.LookupChannelConnectorWorkflowResponse{Exists: result.Exists, InputKeys: result.InputKeys}
	if result.Exists {
		state := openapi.AgentState(result.AgentState)
		response.AgentState = &state
	}
	return openapi.LookupChannelConnectorWorkflow200JSONResponse(response), nil
}
