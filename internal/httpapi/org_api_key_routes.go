package httpapi

import (
	"context"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func orgAPIKeyResponse(record identitystore.OrgAPIKeyRecord) (openapi.OrgAPIKey, error) {
	id, err := publicID(publicid.KindOrgAPIKey, record.ID)
	if err != nil {
		return openapi.OrgAPIKey{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.OrgAPIKey{}, err
	}
	createdByUserID, err := publicID(publicid.KindUser, record.CreatedByUserID)
	if err != nil {
		return openapi.OrgAPIKey{}, err
	}
	return openapi.OrgAPIKey{
		Id:              id,
		OrgId:           orgID,
		Name:            record.Name,
		TokenId:         record.TokenID,
		OrgRole:         record.OrgRole,
		CreatedByUserId: createdByUserID,
		CreatedAt:       record.CreatedAt,
		UpdatedAt:       record.UpdatedAt,
		LastUsedAt:      nullableFromPtr(record.LastUsedAt),
		RevokedAt:       nullableFromPtr(record.RevokedAt),
	}, nil
}

func (s strictOpenAPIServer) CreateOrgAPIKey(
	ctx context.Context,
	request openapi.CreateOrgAPIKeyRequestObject,
) (openapi.CreateOrgAPIKeyResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	principal, ok := principalFromContext(ctx)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	created, err := s.server.store.Identity().CreateOrgAPIKeyWithPlaintext(ctx, identitystore.CreateOrgAPIKeyInput{
		OrgID:           org.ID,
		ActorPrincipal:  principal,
		CreatedByUserID: principal.ID,
		Name:            request.Body.Name,
		OrgRole:         string(request.Body.OrgRole),
	})
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	record, err := orgAPIKeyResponse(created.Record)
	if err != nil {
		return nil, err
	}
	return openapi.CreateOrgAPIKey201JSONResponse(openapi.CreateOrgAPIKeyResponse{
		Token:  created.Token,
		ApiKey: record,
	}), nil
}

func (s strictOpenAPIServer) ListOrgAPIKeys(
	ctx context.Context,
	request openapi.ListOrgAPIKeysRequestObject,
) (openapi.ListOrgAPIKeysResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	limit, after, err := parseOpenAPIPageParams(
		request.Params.Limit,
		request.Params.Cursor,
		publicid.KindOrgAPIKey,
	)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Identity().ListOrgAPIKeysForOrg(ctx, identitystore.ListOrgAPIKeysInput{
		OrgID: org.ID,
		Limit: limit,
		After: after,
	})
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	data := make([]openapi.OrgAPIKey, 0, len(page.Keys))
	var last identitystore.OrgAPIKeyRecord
	for _, key := range page.Keys {
		response, err := orgAPIKeyResponse(key)
		if err != nil {
			return nil, err
		}
		data = append(data, response)
		last = key
	}
	nextCursor, err := encodeNextCursor(page.HasMore, last.CreatedAt, publicid.KindOrgAPIKey, last.ID)
	if err != nil {
		return nil, err
	}
	return openapi.ListOrgAPIKeys200JSONResponse(openapi.ListOrgAPIKeysResponse{
		Data:       data,
		NextCursor: nullableFromPtr(nextCursor),
	}), nil
}

func (s strictOpenAPIServer) GetOrgAPIKey(
	ctx context.Context,
	request openapi.GetOrgAPIKeyRequestObject,
) (openapi.GetOrgAPIKeyResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	keyID, ok := parseOpenAPIPublicID(publicid.KindOrgAPIKey, request.KeyID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	record, err := s.server.store.Identity().GetOrgAPIKey(ctx, org.ID, keyID)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response, err := orgAPIKeyResponse(record)
	if err != nil {
		return nil, err
	}
	return openapi.GetOrgAPIKey200JSONResponse(response), nil
}

func (s strictOpenAPIServer) UpdateOrgAPIKey(
	ctx context.Context,
	request openapi.UpdateOrgAPIKeyRequestObject,
) (openapi.UpdateOrgAPIKeyResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	principal, ok := principalFromContext(ctx)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	keyID, ok := parseOpenAPIPublicID(publicid.KindOrgAPIKey, request.KeyID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	input := identitystore.UpdateOrgAPIKeyInput{
		OrgID:          org.ID,
		KeyID:          keyID,
		ActorPrincipal: principal,
	}
	if request.Body.Name != nil {
		input.Name = *request.Body.Name
	}
	if request.Body.OrgRole != nil {
		input.OrgRole = string(*request.Body.OrgRole)
	}
	record, err := s.server.store.Identity().UpdateOrgAPIKey(ctx, input)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response, err := orgAPIKeyResponse(record)
	if err != nil {
		return nil, err
	}
	return openapi.UpdateOrgAPIKey200JSONResponse(response), nil
}

func (s strictOpenAPIServer) RevokeOrgAPIKey(
	ctx context.Context,
	request openapi.RevokeOrgAPIKeyRequestObject,
) (openapi.RevokeOrgAPIKeyResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	principal, ok := principalFromContext(ctx)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
	}
	keyID, ok := parseOpenAPIPublicID(publicid.KindOrgAPIKey, request.KeyID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	record, err := s.server.store.Identity().RevokeOrgAPIKey(ctx, org.ID, keyID, principal)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response, err := orgAPIKeyResponse(record)
	if err != nil {
		return nil, err
	}
	return openapi.RevokeOrgAPIKey200JSONResponse(response), nil
}

func (s strictOpenAPIServer) ListOrgAPIKeyProjectAccess(
	ctx context.Context,
	request openapi.ListOrgAPIKeyProjectAccessRequestObject,
) (openapi.ListOrgAPIKeyProjectAccessResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	keyID, ok := parseOpenAPIPublicID(publicid.KindOrgAPIKey, request.KeyID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	grants, err := s.server.store.Identity().ListProjectMembershipGrantsForOrgAPIKey(ctx, org.ID, keyID)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	data := make([]openapi.ProjectMembershipGrant, 0, len(grants))
	for _, grant := range grants {
		response, err := projectMembershipGrantResponse(grant)
		if err != nil {
			return nil, err
		}
		data = append(data, response)
	}
	return openapi.ListOrgAPIKeyProjectAccess200JSONResponse(
		openapi.ListProjectMembershipGrantsResponse{Data: data},
	), nil
}

func (s strictOpenAPIServer) SetOrgAPIKeyProjectRole(
	ctx context.Context,
	request openapi.SetOrgAPIKeyProjectRoleRequestObject,
) (openapi.SetOrgAPIKeyProjectRoleResponseObject, error) {
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	principal, ok := principalFromContext(ctx)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
	}
	keyID, ok := parseOpenAPIPublicID(publicid.KindOrgAPIKey, request.KeyID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	membership, err := s.server.store.Identity().SetOrgAPIKeyProjectRole(ctx, identitystore.OrgAPIKeyProjectRoleInput{
		OrgID:          scope.org.ID,
		KeyID:          keyID,
		ProjectID:      scope.project.ID,
		ActorPrincipal: principal,
		Role:           request.Body.Role,
	})
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response, err := projectMembershipGrantResponse(identitystore.ProjectMembershipGrantRecord{
		ProjectID:   membership.ProjectID,
		ProjectName: scope.project.Name,
		Role:        membership.Role,
		CreatedAt:   membership.CreatedAt,
	})
	if err != nil {
		return nil, err
	}
	return openapi.SetOrgAPIKeyProjectRole200JSONResponse(response), nil
}

func (s strictOpenAPIServer) RemoveOrgAPIKeyProjectRole(
	ctx context.Context,
	request openapi.RemoveOrgAPIKeyProjectRoleRequestObject,
) (openapi.RemoveOrgAPIKeyProjectRoleResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	principal, ok := principalFromContext(ctx)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
	}
	keyID, ok := parseOpenAPIPublicID(publicid.KindOrgAPIKey, request.KeyID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if err := s.server.store.Identity().RemoveOrgAPIKeyProjectRole(ctx, identitystore.OrgAPIKeyProjectRoleInput{
		OrgID:          scope.org.ID,
		KeyID:          keyID,
		ProjectID:      scope.project.ID,
		ActorPrincipal: principal,
	}); err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	return openapi.RemoveOrgAPIKeyProjectRole204Response{}, nil
}
