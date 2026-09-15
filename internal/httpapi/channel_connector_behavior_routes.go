package httpapi

import (
	"context"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
)

func (s strictOpenAPIServer) ListChannelConnectorRoutes(
	ctx context.Context, request openapi.ListChannelConnectorRoutesRequestObject,
) (openapi.ListChannelConnectorRoutesResponseObject, error) {
	scope, err := channelConnectorScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	install, err := s.channelConnectorInstallationByPublicID(
		ctx, scope, request.IntegrationAppID, request.IntegrationInstallID,
	)
	if err != nil {
		return nil, err
	}
	routes, err := s.server.store.Integrations().ListActiveIntegrationRoutes(ctx, install.ProjectID, install.ID)
	if err != nil {
		return nil, apierror.FromError(err)
	}
	response := openapi.ListChannelConnectorRoutesResponse{Routes: make([]openapi.ChannelConnectorRoute, 0, len(routes))}
	for _, route := range routes {
		id, err := publicID(publicid.KindIntegrationRoute, route.ID)
		if err != nil {
			return nil, err
		}
		response.Routes = append(response.Routes, openapi.ChannelConnectorRoute{
			Id: id, BehaviorKey: route.BehaviorKey, Configuration: route.Configuration,
		})
	}
	return openapi.ListChannelConnectorRoutes200JSONResponse(response), nil
}
