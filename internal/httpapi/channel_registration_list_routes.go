package httpapi

import (
	"context"
	"time"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/listing"
)

const registeredChannelListKind = "registered_channels"

func (s strictOpenAPIServer) ListRegisteredChannels(
	ctx context.Context, request openapi.ListRegisteredChannelsRequestObject,
) (openapi.ListRegisteredChannelsResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	installID, ok := parseOpenAPIPublicID(publicid.KindIntegrationInstall, request.IntegrationInstallID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	limit, err := parseOpenAPIPageLimit(request.Params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	scopeKey := scope.project.ID.String() + "/" + installID.String()
	sort := "-created_at"
	options, err := parseResourceListQuery(resourceListQueryInput{
		Sort: &sort, Cursor: request.Params.Cursor, ListKind: registeredChannelListKind,
		Scope: scopeKey, IDKind: publicid.KindIntegrationTarget,
		AllowedSorts: map[string]struct{}{"created_at": {}},
	})
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	input := integrationstore.ListRegisteredChannelsInput{
		ProjectID: scope.project.ID, IntegrationInstallID: installID, Limit: limit,
	}
	if options.After.Set {
		createdAt, err := time.Parse(time.RFC3339Nano, options.After.Key)
		if err != nil || options.After.IsNull || createdAt.IsZero() {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid registered channel cursor")
		}
		input.After = listing.KeysetCursor{Set: true, CreatedAt: createdAt, ID: options.After.ID}
	}
	page, err := s.server.store.Integrations().ListRegisteredChannels(ctx, input)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	channels := make([]openapi.RegisteredChannel, 0, len(page.Channels))
	for _, channel := range page.Channels {
		item, err := publicRegisteredChannel(channel)
		if err != nil {
			return nil, err
		}
		channels = append(channels, item)
	}
	var after listing.Cursor
	if page.Next.Set {
		after = listing.Cursor{Set: true, Key: page.Next.CreatedAt.UTC().Format(time.RFC3339Nano), ID: page.Next.ID}
	}
	next, err := encodeResourceListNextCursor(page.Next.Set, after, options,
		registeredChannelListKind, scopeKey, publicid.KindIntegrationTarget, nil)
	if err != nil {
		return nil, err
	}
	return openapi.ListRegisteredChannels200JSONResponse{
		Channels: channels, NextCursor: nullableFromPtr(next),
	}, nil
}

func publicRegisteredChannel(channel integrationstore.RegisteredChannel) (openapi.RegisteredChannel, error) {
	response := openapi.RegisteredChannel{
		Name: channel.Name, ProviderRef: channel.ProviderRef, ProviderRefKind: channel.ProviderRefKind,
	}
	var err error
	response.ChannelId, err = publicID(publicid.KindIntegrationTarget, channel.ID)
	if err != nil {
		return response, err
	}
	response.DefinitionId, err = publicID(publicid.KindChannelDefinition, channel.ChannelDefinitionID)
	if err != nil {
		return response, err
	}
	response.ParentChannelId, err = idOrNil(publicid.KindIntegrationTarget, channel.ParentChannelID)
	return response, err
}
