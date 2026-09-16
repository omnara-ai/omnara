package httpapi

import (
	"context"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func (s strictOpenAPIServer) PublishChannelConnectorDefinition(
	ctx context.Context,
	request openapi.PublishChannelConnectorDefinitionRequestObject,
) (openapi.PublishChannelConnectorDefinitionResponseObject, error) {
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
	if !integrationstore.ChannelKind(request.Body.Kind).MatchesProvider(install.Provider) {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "channel kind does not match the connection provider")
	}
	definition, err := s.server.store.Integrations().PublishConnectorChannelDefinition(
		ctx, integrationstore.PublishChannelDefinitionInput{
			ProjectID: install.ProjectID, IntegrationInstallID: install.ID,
			ImplementationKey: request.Body.ImplementationKey, Kind: integrationstore.ChannelKind(request.Body.Kind),
			Description: request.Body.Description, SendParamsSchema: request.Body.SendParamsSchema,
			Capabilities: storageChannelCapabilities(request.Body.Capabilities), ConnectorCapabilities: scope.Capabilities,
		},
	)
	if err != nil {
		return nil, apierror.FromError(err)
	}
	id, err := publicID(publicid.KindChannelDefinition, definition.ID)
	if err != nil {
		return nil, err
	}
	return openapi.PublishChannelConnectorDefinition200JSONResponse{
		Id: id, ImplementationKey: definition.ImplementationKey, Kind: openapi.ChannelKind(definition.Kind),
		Description: definition.Description, SendParamsSchema: definition.SendParamsSchema,
		Capabilities: publicChannelCapabilities(definition.Capabilities),
	}, nil
}

func (s strictOpenAPIServer) channelConnectorInstallationByPublicID(
	ctx context.Context,
	scope channelConnectorScope,
	appPublicID, installPublicID string,
) (integrationstore.IntegrationInstallRecord, error) {
	appID, appOK := parseOpenAPIPublicID(publicid.KindIntegrationApp, appPublicID)
	installID, installOK := parseOpenAPIPublicID(publicid.KindIntegrationInstall, installPublicID)
	if !appOK || !installOK {
		return integrationstore.IntegrationInstallRecord{}, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	return s.channelConnectorEventInstallation(ctx, scope, appID, installID)
}

func storageChannelCapabilities(value openapi.ChannelCapabilities) integrationstore.ChannelCapabilities {
	return integrationstore.ChannelCapabilities{
		Read: value.Read, Send: value.Send, Text: value.Text, Artifacts: value.Artifacts,
		Permissions: value.Permissions, Questions: value.Questions, CreatesReplyChannel: value.CreatesReplyChannel,
	}
}

func publicChannelCapabilities(value integrationstore.ChannelCapabilities) openapi.ChannelCapabilities {
	return openapi.ChannelCapabilities{
		Read: value.Read, Send: value.Send, Text: value.Text, Artifacts: value.Artifacts,
		Permissions: value.Permissions, Questions: value.Questions, CreatesReplyChannel: value.CreatesReplyChannel,
	}
}
