package httpapi

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/httpapi/publicevents"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/listing"
)

const externalChannelRequestListKind = "external_channel_requests"

func (s strictOpenAPIServer) ListExternalChannelRequests(
	ctx context.Context,
	request openapi.ListExternalChannelRequestsRequestObject,
) (openapi.ListExternalChannelRequestsResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	installID, ok := parseOpenAPIPublicID(publicid.KindIntegrationInstall, request.IntegrationInstallID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	install, err := s.server.store.Integrations().GetIntegrationInstall(ctx, scope.project.ID, installID)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	if install.IntegrationKind != integrationstore.IntegrationKindExternal {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	limit, err := parseOpenAPIPageLimit(request.Params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	scopeKey := scope.project.ID.String() + "/" + install.ID.String()
	sort := "created_at"
	options, err := parseResourceListQuery(resourceListQueryInput{
		Sort: &sort, Cursor: request.Params.Cursor, ListKind: externalChannelRequestListKind,
		Scope: scopeKey, IDKind: publicid.KindExternalChannelRequest,
		AllowedSorts: map[string]struct{}{"created_at": {}},
	})
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	input := executionstore.ListExternalChannelRequestsInput{
		ProjectID: scope.project.ID, IntegrationInstallID: install.ID, Limit: int32(limit),
	}
	if options.After.Set {
		createdAt, err := time.Parse(time.RFC3339Nano, options.After.Key)
		if err != nil || options.After.IsNull || createdAt.IsZero() {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid request cursor")
		}
		input.After = &executionstore.ExternalChannelRequestCursor{CreatedAt: createdAt, ID: options.After.ID}
	}
	page, err := s.server.store.Execution().ListPendingExternalChannelRequests(ctx, input)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	data := make([]openapi.ExternalChannelRequest, 0, len(page.Requests))
	var after listing.Cursor
	for _, record := range page.Requests {
		item, err := publicExternalChannelRequest(record)
		if err != nil {
			return nil, err
		}
		data = append(data, item)
		after = listing.Cursor{Set: true, Key: record.CreatedAt.UTC().Format(time.RFC3339Nano), ID: record.ID}
	}
	next, err := encodeResourceListNextCursor(page.HasMore, after, options,
		externalChannelRequestListKind, scopeKey, publicid.KindExternalChannelRequest, nil)
	if err != nil {
		return nil, err
	}
	return openapi.ListExternalChannelRequests200JSONResponse{
		Data: data, NextCursor: nullableFromPtr(next),
	}, nil
}

func (s strictOpenAPIServer) CompleteExternalChannelRequest(
	ctx context.Context,
	request openapi.CompleteExternalChannelRequestRequestObject,
) (openapi.CompleteExternalChannelRequestResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	installID, installOK := parseOpenAPIPublicID(publicid.KindIntegrationInstall, request.IntegrationInstallID)
	requestID, requestOK := parseOpenAPIPublicID(publicid.KindExternalChannelRequest, request.RequestID)
	if !installOK || !requestOK {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	// Project authorization applies to every attempt. The owning store then
	// resolves terminal replay before mutable connection/binding authorization;
	// a revoked grant must not prevent acknowledgement of an already saved result.
	result, err := s.server.store.Execution().CompleteExternalChannelRequest(ctx,
		executionstore.CompleteExternalChannelRequestInput{
			ProjectID: scope.project.ID, IntegrationInstallID: installID, ID: requestID,
			Result: channelconnector.OperationResult{
				RequestID: request.RequestID, Outcome: channelconnector.OperationOutcome(request.Body.Outcome),
				Payload: request.Body.Payload,
			},
		},
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response := openapi.CompleteExternalChannelRequestResponse{}
	response.Request, err = publicExternalChannelRequest(result.Request)
	if err != nil {
		return nil, err
	}
	if result.ToolCall != nil {
		call, err := publicToolCallFromRecord(*result.ToolCall)
		if err != nil {
			return nil, err
		}
		blocks, err := publicevents.ToolResultContentBlocks(result.ToolCall.ResultContentParts)
		if err != nil {
			return nil, err
		}
		response.ToolCall, response.ToolResultContentBlocks = &call, &blocks
	}
	return openapi.CompleteExternalChannelRequest200JSONResponse(response), nil
}

func publicExternalChannelRequest(
	record executionstore.ExternalChannelRequestRecord,
) (openapi.ExternalChannelRequest, error) {
	response := openapi.ExternalChannelRequest{
		Operation: openapi.ChannelOperationKind(record.Operation), State: openapi.ExternalChannelRequestState(record.State),
		CreatedAt: record.CreatedAt, DeadlineAt: record.Deadline, TerminalAt: record.TerminalAt,
		StateReasonCode: ptrFromNonEmpty(record.StateReasonCode),
	}
	for _, field := range []struct {
		kind publicid.Kind
		id   uuid.UUID
		out  *string
	}{
		{publicid.KindExternalChannelRequest, record.ID, &response.Id},
		{publicid.KindAgent, record.AgentID, &response.AgentId},
		{publicid.KindIntegrationTarget, record.IntegrationTargetID, &response.ChannelId},
	} {
		id, err := publicID(field.kind, field.id)
		if err != nil {
			return response, err
		}
		*field.out = id
	}
	var err error
	response.ToolCallId, err = idOrNil(publicid.KindToolCall, record.ToolCallID)
	if err != nil {
		return response, err
	}
	response.InteractionId, err = idOrNil(publicid.KindAgentInteraction, record.InteractionID)
	if err != nil {
		return response, err
	}
	if err := response.Payload.UnmarshalJSON(record.Payload); err != nil {
		return response, err
	}
	return response, nil
}
