package httpapi

import (
	"context"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/resourcemeta"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func (s strictOpenAPIServer) ListActors(
	ctx context.Context,
	request openapi.ListActorsRequestObject,
) (openapi.ListActorsResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	limit, after, err := parseOpenAPIPageParams(request.Params.Limit, request.Params.Cursor, publicid.KindActor)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	provider := ""
	if request.Params.Provider != nil {
		provider = *request.Params.Provider
	}
	providerTenantID := ""
	if request.Params.ProviderTenantId != nil {
		providerTenantID = *request.Params.ProviderTenantId
	}
	providerUserID := ""
	if request.Params.ProviderUserId != nil {
		providerUserID = *request.Params.ProviderUserId
	}
	records, err := s.server.store.Execution().ListActors(ctx, executionstore.ListActorsInput{
		ProjectID:        scope.project.ID,
		Provider:         provider,
		ProviderTenantID: providerTenantID,
		ProviderUserID:   providerUserID,
		After:            after,
		Limit:            limit + 1,
	})
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	data := make([]openapi.Actor, 0, len(records))
	var last executionstore.ActorRecord
	for _, record := range records {
		response, err := publicActorFromRecord(scope.org.ID, record)
		if err != nil {
			return nil, err
		}
		data = append(data, response)
		last = record
	}
	nextCursor, err := encodeNextCursor(hasMore, last.CreatedAt, publicid.KindActor, last.ID)
	if err != nil {
		return nil, err
	}
	return openapi.ListActors200JSONResponse(openapi.ListActorsResponse{
		Data:       data,
		NextCursor: nullableFromPtr(nextCursor),
	}), nil
}

func (s strictOpenAPIServer) GetActor(
	ctx context.Context,
	request openapi.GetActorRequestObject,
) (openapi.GetActorResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	actorID, ok := parseOpenAPIPublicID(publicid.KindActor, request.ActorID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	record, err := s.server.store.Execution().GetActor(ctx, scope.project.ID, actorID)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := publicActorFromRecord(scope.org.ID, record)
	if err != nil {
		return nil, err
	}
	return openapi.GetActor200JSONResponse(response), nil
}

func (s strictOpenAPIServer) PutActor(
	ctx context.Context,
	request openapi.PutActorRequestObject,
) (openapi.PutActorResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	record, err := s.server.store.Execution().PutActor(
		ctx,
		putActorInputFromParams(scope.project.ID, *request.Body),
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := publicActorFromRecord(scope.org.ID, record)
	if err != nil {
		return nil, err
	}
	return openapi.PutActor200JSONResponse(response), nil
}

func putActorInputFromParams(
	projectID storage.ID,
	params openapi.ExternalActorParams,
) executionstore.PutActorInput {
	input := executionstore.PutActorInput{
		ProjectID:      projectID,
		ProviderUserID: params.ProviderUserId,
		DisplayName:    params.DisplayName,
		Metadata:       params.Metadata,
	}
	if params.ProviderTenantId != nil {
		input.ProviderTenantID = *params.ProviderTenantId
	}
	return input
}

func externalActorParamsFromRequest(params openapi.ExternalActorParams) *executionstore.ActorParams {
	actor := &executionstore.ActorParams{
		Provider:       identitystore.ActorProviderExternal,
		ProviderUserID: params.ProviderUserId,
		DisplayName:    params.DisplayName,
		Metadata:       params.Metadata,
	}
	if params.ProviderTenantId != nil {
		actor.ProviderTenantID = *params.ProviderTenantId
	}
	return actor
}

func requestActorParams(
	project identitystore.ProjectRecord,
	principal identitystore.PrincipalRecord,
	actor *openapi.ExternalActorParams,
) (*executionstore.ActorParams, error) {
	switch principal.Type {
	case identitystore.PrincipalTypeUser:
		if actor != nil {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest,
				"actor cannot be combined with user authentication")
		}
		return executionstore.OmnaraActorParams(project.OrgID, principal)
	case identitystore.PrincipalTypeOrgAPIKey:
		if actor != nil {
			return externalActorParamsFromRequest(*actor), nil
		}
		return executionstore.OmnaraActorParams(project.OrgID, principal)
	}
	if actor == nil {
		return nil, nil //nolint:nilnil // Nil params are the unattributed-request representation.
	}
	return externalActorParamsFromRequest(*actor), nil
}

func publicActorFromRecord(
	orgID storage.ID,
	record executionstore.ActorRecord,
) (openapi.Actor, error) {
	id, err := publicID(publicid.KindActor, record.ID)
	if err != nil {
		return openapi.Actor{}, err
	}
	publicOrgID, err := publicID(publicid.KindOrganization, orgID)
	if err != nil {
		return openapi.Actor{}, err
	}
	projectID, err := publicID(publicid.KindProject, record.ProjectID)
	if err != nil {
		return openapi.Actor{}, err
	}
	metadata, err := resourcemeta.FromJSON(record.Metadata)
	if err != nil {
		return openapi.Actor{}, err
	}
	response := openapi.Actor{
		Id:             id,
		OrgId:          publicOrgID,
		ProjectId:      projectID,
		Provider:       record.Provider,
		ProviderUserId: record.ProviderUserID,
		Metadata:       metadata,
		CreatedAt:      record.CreatedAt,
		UpdatedAt:      record.UpdatedAt,
	}
	if record.ProviderTenantID != "" {
		response.ProviderTenantId = &record.ProviderTenantID
	}
	if record.DisplayName != "" {
		response.DisplayName = &record.DisplayName
	}
	return response, nil
}
