package httpapi

import (
	"context"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
)

func (s strictOpenAPIServer) ListAppSubscriptions(
	ctx context.Context,
	request openapi.ListAppSubscriptionsRequestObject,
) (openapi.ListAppSubscriptionsResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	appID, ok := parseOpenAPIPublicID(publicid.KindProjectApp, request.AppID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid app id")
	}
	limit, after, err := parseOpenAPIPageParams(request.Params.Limit, request.Params.Cursor, publicid.KindAppSubscription)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Apps().ListAppSubscriptions(ctx, appstore.ListAppSubscriptionsInput{
		ProjectID: scope.project.ID, AppID: appID, Limit: limit, After: after,
	})
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	data := make([]openapi.AppSubscription, 0, len(page.Subscriptions))
	for _, subscription := range page.Subscriptions {
		response, err := appSubscriptionResponse(subscription)
		if err != nil {
			return nil, err
		}
		data = append(data, response)
	}
	next, err := encodeNextCursor(page.HasMore, page.Next.CreatedAt, publicid.KindAppSubscription, page.Next.ID)
	if err != nil {
		return nil, err
	}
	return openapi.ListAppSubscriptions200JSONResponse{Data: data, NextCursor: nullableFromPtr(next)}, nil
}

func (s strictOpenAPIServer) CreateAppSubscription(
	ctx context.Context,
	request openapi.CreateAppSubscriptionRequestObject,
) (openapi.CreateAppSubscriptionResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	appID, ok := parseOpenAPIPublicID(publicid.KindProjectApp, request.AppID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid app id")
	}
	agentID, ok := parseOpenAPIPublicID(publicid.KindAgent, request.Body.AgentId)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid agent_id")
	}
	input := appstore.CreateAppSubscriptionInput{
		OrgID: scope.project.OrgID, ProjectID: scope.project.ID, AppID: appID, AgentID: agentID,
		Type: request.Body.Type, Conversation: request.Body.Conversation,
	}
	if request.Body.Events != nil {
		input.Events = *request.Body.Events
	}
	subscription, err := s.server.store.Apps().CreateAppSubscription(ctx, input)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := appSubscriptionResponse(subscription)
	if err != nil {
		return nil, err
	}
	return openapi.CreateAppSubscription201JSONResponse(response), nil
}

func (s strictOpenAPIServer) DeleteAppSubscription(
	ctx context.Context,
	request openapi.DeleteAppSubscriptionRequestObject,
) (openapi.DeleteAppSubscriptionResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	appID, ok := parseOpenAPIPublicID(publicid.KindProjectApp, request.AppID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid app id")
	}
	id, ok := parseOpenAPIPublicID(publicid.KindAppSubscription, request.SubscriptionID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid subscription id")
	}
	if err := s.server.store.Apps().DeleteAppSubscription(
		ctx, scope.project.OrgID, scope.project.ID, appID, id,
	); err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	return openapi.DeleteAppSubscription204Response{}, nil
}

func appSubscriptionResponse(subscription appstore.AppSubscriptionRecord) (openapi.AppSubscription, error) {
	id, err := publicID(publicid.KindAppSubscription, subscription.ID)
	if err != nil {
		return openapi.AppSubscription{}, err
	}
	projectID, err := publicID(publicid.KindProject, subscription.ProjectID)
	if err != nil {
		return openapi.AppSubscription{}, err
	}
	appID, err := publicID(publicid.KindProjectApp, subscription.AppID)
	if err != nil {
		return openapi.AppSubscription{}, err
	}
	agentID, err := publicID(publicid.KindAgent, subscription.AgentID)
	if err != nil {
		return openapi.AppSubscription{}, err
	}
	response := openapi.AppSubscription{
		Id: id, ProjectId: projectID, AppId: appID, AgentId: agentID, AgentName: subscription.AgentName,
		Type: subscription.Type, Events: subscription.Events, CreatedAt: subscription.CreatedAt,
		Conversation: subscription.Conversation,
	}
	return response, nil
}
