package httpapi

import (
	"context"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/listing"
)

const agentChannelListKind = "agent_channels"

func (s strictOpenAPIServer) ListAgentChannels(
	ctx context.Context,
	request openapi.ListAgentChannelsRequestObject,
) (openapi.ListAgentChannelsResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	limit, err := parseOpenAPIPageLimit(request.Params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	input := integrationstore.ListAgentChannelTargetsInput{Limit: limit}
	parent := ""
	if request.Params.ParentChannelId != nil {
		parent = *request.Params.ParentChannelId
		var ok bool
		input.ParentChannelID, ok = parseOpenAPIPublicID(publicid.KindIntegrationTarget, parent)
		if !ok {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid parent channel")
		}
	}
	scopeKey := scope.project.ID.String() + "/" + scope.agent.ID.String()
	sort := "-created_at"
	options, err := parseResourceListQuery(resourceListQueryInput{
		Sort: &sort, Cursor: request.Params.Cursor, ListKind: agentChannelListKind,
		Scope: scopeKey, IDKind: publicid.KindIntegrationTarget, Extra: parent,
		AllowedSorts: map[string]struct{}{"created_at": {}},
	})
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	if options.After.Set {
		createdAt, err := time.Parse(time.RFC3339Nano, options.After.Key)
		if err != nil || options.After.IsNull || createdAt.IsZero() {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid channel cursor")
		}
		input.After = &integrationstore.AgentChannelTargetCursor{CreatedAt: createdAt, ID: options.After.ID}
	}
	page, err := s.server.store.Integrations().ListAgentChannelTargets(ctx, scope.project.ID, scope.agent.ID, input)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	channels := make([]openapi.AgentChannelSummary, 0, len(page.Targets))
	for _, target := range page.Targets {
		item, err := publicAgentChannelSummary(target)
		if err != nil {
			return nil, err
		}
		channels = append(channels, item)
	}
	var after listing.Cursor
	if page.Next != nil {
		after = listing.Cursor{Set: true, Key: page.Next.CreatedAt.UTC().Format(time.RFC3339Nano), ID: page.Next.ID}
	}
	next, err := encodeResourceListNextCursor(page.Next != nil, after, options,
		agentChannelListKind, scopeKey, publicid.KindIntegrationTarget, parent)
	if err != nil {
		return nil, err
	}
	currentID, err := s.server.store.Execution().GetAgentCurrentChannelID(ctx, scope.project.ID, scope.agent.ID)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	current, err := idOrNil(publicid.KindIntegrationTarget, currentID)
	if err != nil {
		return nil, err
	}
	return openapi.ListAgentChannels200JSONResponse{
		Channels: channels, CurrentChannelId: nullableFromPtr(current), NextCursor: nullableFromPtr(next),
	}, nil
}

func (s strictOpenAPIServer) GetAgentChannel(
	ctx context.Context,
	request openapi.GetAgentChannelRequestObject,
) (openapi.GetAgentChannelResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindIntegrationTarget, request.ChannelID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	access, err := s.server.store.Integrations().GetAgentChannelAccess(ctx, scope.project.ID, scope.agent.ID, id)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	currentID, err := s.server.store.Execution().GetAgentCurrentChannelID(ctx, scope.project.ID, scope.agent.ID)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response := openapi.GetAgentChannel200JSONResponse{
		IsCurrent: currentID == access.ChannelID, Kind: openapi.ChannelKind(access.Kind),
		Name: access.Name, Description: access.Description, Active: access.Active, CanReceive: access.ReceiveAllowed,
		Capabilities: publicChannelCapabilities(access.Capabilities), SendParamsSchema: access.SendParamsSchema,
	}
	response.ChannelId, err = publicID(publicid.KindIntegrationTarget, access.ChannelID)
	if err != nil {
		return nil, err
	}
	response.ParentChannelId, err = idOrNil(publicid.KindIntegrationTarget, access.ParentChannelID)
	return response, err
}

func publicAgentChannelSummary(target integrationstore.AgentChannelTarget) (openapi.AgentChannelSummary, error) {
	active := target.InstallState == integrationstore.IntegrationInstallStateActive &&
		(target.IntegrationKind == integrationstore.IntegrationKindExternal ||
			target.AppState == integrationstore.IntegrationAppStateActive)
	state := openapi.AgentChannelStateDisabled
	if active {
		state = openapi.AgentChannelStateActive
	}
	name := strings.TrimSpace(target.DisplayName)
	if name == "" {
		name = target.Provider + " " + target.ProviderRefKind
	}
	response := openapi.AgentChannelSummary{
		Provider: target.Provider, AddressKind: target.ProviderRefKind, Name: name, State: state,
		CanReceive: active && target.ReceiveAllowed,
		CanRead:    active && target.ReadAllowed, CanSend: active && target.SendAllowed,
	}
	var err error
	response.ChannelId, err = publicID(publicid.KindIntegrationTarget, target.ID)
	if err != nil {
		return response, err
	}
	response.ParentChannelId, err = idOrNil(publicid.KindIntegrationTarget, target.ParentChannelID)
	return response, err
}
