package httpapi

import (
	"context"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func (s strictOpenAPIServer) PublishExternalChannelDefinition(
	ctx context.Context,
	request openapi.PublishExternalChannelDefinitionRequestObject,
) (openapi.PublishExternalChannelDefinitionResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	installID, ok := parseOpenAPIPublicID(publicid.KindIntegrationInstall, request.IntegrationInstallID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	definition, err := s.server.store.Integrations().PublishExternalChannelDefinition(ctx,
		integrationstore.PublishChannelDefinitionInput{
			ProjectID: scope.project.ID, IntegrationInstallID: installID,
			ImplementationKey: request.Body.ImplementationKey, Kind: integrationstore.ChannelKind(request.Body.Kind),
			Description: request.Body.Description, SendParamsSchema: request.Body.SendParamsSchema,
			Capabilities: storageChannelCapabilities(request.Body.Capabilities),
		})
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	id, err := publicID(publicid.KindChannelDefinition, definition.ID)
	if err != nil {
		return nil, err
	}
	return openapi.PublishExternalChannelDefinition200JSONResponse{
		Id: id, ImplementationKey: definition.ImplementationKey, Kind: openapi.ChannelKind(definition.Kind),
		Description: definition.Description, SendParamsSchema: definition.SendParamsSchema,
		Capabilities: publicChannelCapabilities(definition.Capabilities),
	}, nil
}

func (s strictOpenAPIServer) AttachAgentChannel(
	ctx context.Context,
	request openapi.AttachAgentChannelRequestObject,
) (openapi.AttachAgentChannelResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	channelID, ok := parseOpenAPIPublicID(publicid.KindIntegrationTarget, request.Body.ChannelId)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if !request.Body.Grants.Receive && !request.Body.Grants.Read && !request.Body.Grants.Send {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "at least one channel grant is required")
	}
	channel, err := s.server.store.Integrations().GetIntegrationTarget(ctx, scope.project.ID, channelID)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	input := integrationstore.CreateIntegrationTargetBindingInput{
		ProjectID: scope.project.ID, AgentID: scope.agent.ID,
		IntegrationInstallID: channel.IntegrationInstallID, IntegrationTargetID: channel.ID,
		ReceiveAllowed: request.Body.Grants.Receive, ReadAllowed: request.Body.Grants.Read,
		SendAllowed: request.Body.Grants.Send, Source: "api",
	}
	if grants := request.Body.ReplyChannelGrants; grants != nil {
		input.ReplyChannelGrants = &integrationstore.ChannelGrants{
			ReceiveAllowed: grants.Receive, ReadAllowed: grants.Read, SendAllowed: grants.Send,
		}
	}
	binding, err := s.server.store.Integrations().CreateIntegrationTargetBinding(ctx, input)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := publicAgentChannelBinding(binding)
	return openapi.AttachAgentChannel200JSONResponse(response), err
}

func (s strictOpenAPIServer) RevokeAgentChannelBinding(
	ctx context.Context,
	request openapi.RevokeAgentChannelBindingRequestObject,
) (openapi.RevokeAgentChannelBindingResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindIntegrationBinding, request.BindingID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if err := s.server.store.Integrations().RevokeAgentChannelBinding(
		ctx, scope.project.ID, scope.agent.ID, id,
	); err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	return openapi.RevokeAgentChannelBinding204Response{}, nil
}

func publicAgentChannelBinding(
	record integrationstore.IntegrationTargetBindingRecord,
) (openapi.AgentChannelBinding, error) {
	response := openapi.AgentChannelBinding{
		Grants: openapi.ChannelGrants{Receive: record.ReceiveAllowed, Read: record.ReadAllowed, Send: record.SendAllowed},
	}
	var err error
	response.Id, err = publicID(publicid.KindIntegrationBinding, record.ID)
	if err != nil {
		return response, err
	}
	response.ChannelId, err = publicID(publicid.KindIntegrationTarget, record.IntegrationTargetID)
	if grants := record.ReplyChannelGrants; grants != nil {
		response.ReplyChannelGrants = &openapi.ChannelGrants{
			Receive: grants.ReceiveAllowed, Read: grants.ReadAllowed, Send: grants.SendAllowed,
		}
	}
	return response, err
}
