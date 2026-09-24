package httpapi

import (
	"context"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func (s strictOpenAPIServer) ListIntegrationSubscriptions(
	ctx context.Context,
	request openapi.ListIntegrationSubscriptionsRequestObject,
) (openapi.ListIntegrationSubscriptionsResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	integrationID, ok := parseOpenAPIPublicID(publicid.KindProjectIntegration, request.IntegrationID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid integration id")
	}
	limit, after, err := parseOpenAPIPageParams(
		request.Params.Limit,
		request.Params.Cursor,
		publicid.KindIntegrationSubscription,
	)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Integrations().ListIntegrationSubscriptions(
		ctx,
		integrationstore.ListIntegrationSubscriptionsInput{
			ProjectID: scope.project.ID, IntegrationID: integrationID, Limit: limit, After: after,
		},
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	data := make([]openapi.IntegrationSubscription, 0, len(page.Subscriptions))
	for _, subscription := range page.Subscriptions {
		response, err := integrationSubscriptionResponse(subscription)
		if err != nil {
			return nil, err
		}
		data = append(data, response)
	}
	next, err := encodeNextCursor(page.HasMore, page.Next.CreatedAt, publicid.KindIntegrationSubscription, page.Next.ID)
	if err != nil {
		return nil, err
	}
	return openapi.ListIntegrationSubscriptions200JSONResponse{Data: data, NextCursor: nullableFromPtr(next)}, nil
}

func (s strictOpenAPIServer) CreateIntegrationSubscription(
	ctx context.Context,
	request openapi.CreateIntegrationSubscriptionRequestObject,
) (openapi.CreateIntegrationSubscriptionResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	integrationID, ok := parseOpenAPIPublicID(publicid.KindProjectIntegration, request.IntegrationID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid integration id")
	}
	agentID, ok := parseOpenAPIPublicID(publicid.KindAgent, request.Body.AgentId)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid agent_id")
	}
	input := integrationstore.CreateIntegrationSubscriptionInput{
		OrgID: scope.project.OrgID, ProjectID: scope.project.ID, IntegrationID: integrationID, AgentID: agentID,
		Type: request.Body.Type, Conversation: request.Body.Conversation,
	}
	if request.Body.Events != nil {
		input.Events = *request.Body.Events
	}
	subscription, err := s.server.store.Integrations().CreateIntegrationSubscription(ctx, input)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := integrationSubscriptionResponse(subscription)
	if err != nil {
		return nil, err
	}
	return openapi.CreateIntegrationSubscription201JSONResponse(response), nil
}

func (s strictOpenAPIServer) DeleteIntegrationSubscription(
	ctx context.Context,
	request openapi.DeleteIntegrationSubscriptionRequestObject,
) (openapi.DeleteIntegrationSubscriptionResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	integrationID, ok := parseOpenAPIPublicID(publicid.KindProjectIntegration, request.IntegrationID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid integration id")
	}
	id, ok := parseOpenAPIPublicID(publicid.KindIntegrationSubscription, request.SubscriptionID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid subscription id")
	}
	if err := s.server.store.Integrations().DeleteIntegrationSubscription(
		ctx, scope.project.OrgID, scope.project.ID, integrationID, id,
	); err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	return openapi.DeleteIntegrationSubscription204Response{}, nil
}

func integrationSubscriptionResponse(
	subscription integrationstore.IntegrationSubscriptionRecord,
) (openapi.IntegrationSubscription, error) {
	id, err := publicID(publicid.KindIntegrationSubscription, subscription.ID)
	if err != nil {
		return openapi.IntegrationSubscription{}, err
	}
	projectID, err := publicID(publicid.KindProject, subscription.ProjectID)
	if err != nil {
		return openapi.IntegrationSubscription{}, err
	}
	integrationID, err := publicID(publicid.KindProjectIntegration, subscription.IntegrationID)
	if err != nil {
		return openapi.IntegrationSubscription{}, err
	}
	agentID, err := publicID(publicid.KindAgent, subscription.AgentID)
	if err != nil {
		return openapi.IntegrationSubscription{}, err
	}
	response := openapi.IntegrationSubscription{
		Id: id, ProjectId: projectID, IntegrationId: integrationID, AgentId: agentID, AgentName: subscription.AgentName,
		Type: subscription.Type, Events: subscription.Events, CreatedAt: subscription.CreatedAt,
		Conversation: subscription.Conversation,
	}
	return response, nil
}
