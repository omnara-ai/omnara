package httpapi

import (
	"context"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func (s strictOpenAPIServer) ListOrgMembers(
	ctx context.Context,
	request openapi.ListOrgMembersRequestObject,
) (openapi.ListOrgMembersResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.listOrgMembers(ctx, request.Params, org)
}

func (s strictOpenAPIServer) listOrgMembers(
	ctx context.Context,
	params openapi.ListOrgMembersParams,
	org identitystore.OrgRecord,
) (openapi.ListOrgMembersResponseObject, error) {
	limit, err := parseOpenAPIPageLimit(params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	list, err := parseResourceListQuery(resourceListQueryInput{
		Name: params.Name, Sort: optionalString(params.Sort),
		Cursor: params.Cursor, ListKind: "org_members",
		Scope: org.ID.String(), IDKind: publicid.KindUser,
		AllowedSorts: sortSet("name", "created_at"),
	})
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Identity().ListOrgMembers(ctx, identitystore.ListOrgMembersInput{
		OrgID: org.ID,
		Limit: limit,
		List:  list,
	})
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	data := make([]openapi.OrgMember, 0, len(page.Members))
	for _, member := range page.Members {
		response, err := orgMemberResponse(member)
		if err != nil {
			return nil, err
		}
		data = append(data, response)
	}
	nextCursor, err := encodeResourceListNextCursor(
		page.HasMore, page.Next, list, "org_members",
		org.ID.String(), publicid.KindUser, nil,
	)
	if err != nil {
		return nil, err
	}
	return openapi.ListOrgMembers200JSONResponse(openapi.ListOrgMembersResponse{
		Data:       data,
		NextCursor: nullableFromPtr(nextCursor),
	}), nil
}

func orgAndMemberUserID(ctx context.Context, userIDRaw string) (identitystore.OrgRecord, storage.ID, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return identitystore.OrgRecord{}, storage.NilID, err
	}
	userID, ok := parseOpenAPIPublicID(publicid.KindUser, userIDRaw)
	if !ok {
		return identitystore.OrgRecord{}, storage.NilID, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	return org, userID, nil
}

func orgMemberResponse(member identitystore.OrgMemberRecord) (openapi.OrgMember, error) {
	userID, err := publicID(publicid.KindUser, member.UserID)
	if err != nil {
		return openapi.OrgMember{}, err
	}
	return openapi.OrgMember{
		UserId:      userID,
		Email:       member.Email,
		DisplayName: member.DisplayName,
		Role:        member.Role,
		CreatedAt:   member.CreatedAt,
	}, nil
}

func (s strictOpenAPIServer) UpdateOrgMember(
	ctx context.Context,
	request openapi.UpdateOrgMemberRequestObject,
) (openapi.UpdateOrgMemberResponseObject, error) {
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	org, userID, err := orgAndMemberUserID(ctx, request.UserID)
	if err != nil {
		return nil, err
	}
	membership, err := s.server.store.Identity().UpdateOrgMemberRole(ctx, identitystore.UpdateOrgMemberRoleInput{
		OrgID:  org.ID,
		UserID: userID,
		Role:   request.Body.Role,
	})
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response, err := orgMembershipResponse(membership)
	if err != nil {
		return nil, err
	}
	return openapi.UpdateOrgMember200JSONResponse(response), nil
}

func (s strictOpenAPIServer) RemoveOrgMember(
	ctx context.Context,
	request openapi.RemoveOrgMemberRequestObject,
) (openapi.RemoveOrgMemberResponseObject, error) {
	org, userID, err := orgAndMemberUserID(ctx, request.UserID)
	if err != nil {
		return nil, err
	}
	if err := s.server.store.Identity().RemoveOrgMember(ctx, identitystore.RemoveOrgMemberInput{
		OrgID:  org.ID,
		UserID: userID,
	}); err != nil {
		return nil, apierror.OrgScoped(err)
	}
	return openapi.RemoveOrgMember204Response{}, nil
}

func projectMembershipGrantResponse(
	grant identitystore.ProjectMembershipGrantRecord,
) (openapi.ProjectMembershipGrant, error) {
	projectID, err := publicID(publicid.KindProject, grant.ProjectID)
	if err != nil {
		return openapi.ProjectMembershipGrant{}, err
	}
	return openapi.ProjectMembershipGrant{
		ProjectId:   projectID,
		ProjectName: grant.ProjectName,
		Role:        grant.Role,
		CreatedAt:   grant.CreatedAt,
	}, nil
}

func (s strictOpenAPIServer) ListMemberProjectAccess(
	ctx context.Context,
	request openapi.ListMemberProjectAccessRequestObject,
) (openapi.ListMemberProjectAccessResponseObject, error) {
	org, userID, err := orgAndMemberUserID(ctx, request.UserID)
	if err != nil {
		return nil, err
	}
	grants, err := s.server.store.Identity().ListProjectMembershipGrantsForUser(ctx, org.ID, userID)
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
	return openapi.ListMemberProjectAccess200JSONResponse(
		openapi.ListProjectMembershipGrantsResponse{Data: data},
	), nil
}

func (s strictOpenAPIServer) SetMemberProjectAccess(
	ctx context.Context,
	request openapi.SetMemberProjectAccessRequestObject,
) (openapi.SetMemberProjectAccessResponseObject, error) {
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	userID, ok := parseOpenAPIPublicID(publicid.KindUser, request.UserID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	membership, err := s.server.store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
		OrgID:     scope.org.ID,
		ProjectID: scope.project.ID,
		UserID:    userID,
		Role:      request.Body.Role,
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
	return openapi.SetMemberProjectAccess200JSONResponse(response), nil
}

func (s strictOpenAPIServer) RemoveMemberProjectAccess(
	ctx context.Context,
	request openapi.RemoveMemberProjectAccessRequestObject,
) (openapi.RemoveMemberProjectAccessResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	userID, ok := parseOpenAPIPublicID(publicid.KindUser, request.UserID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if err := s.server.store.Identity().RemoveProjectMembership(ctx, identitystore.RemoveProjectMembershipInput{
		OrgID:     scope.org.ID,
		ProjectID: scope.project.ID,
		UserID:    userID,
	}); err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	return openapi.RemoveMemberProjectAccess204Response{}, nil
}
