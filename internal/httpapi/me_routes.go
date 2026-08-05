package httpapi

import (
	"context"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func (s strictOpenAPIServer) DeleteCurrentUser(
	ctx context.Context,
	_ openapi.DeleteCurrentUserRequestObject,
) (openapi.DeleteCurrentUserResponseObject, error) {
	principal, ok := principalFromContext(ctx)
	if !ok || principal.ID == storage.NilID {
		return nil, apierror.FromCode(openapi.ErrorCodeUnauthorized, "unauthorized")
	}
	if err := s.server.store.Identity().DeleteUserAccount(ctx, principal.ID); err != nil {
		return nil, apierror.UserScoped(err)
	}
	return openapi.DeleteCurrentUser204Response{}, nil
}

func (s strictOpenAPIServer) GetCurrentUser(
	ctx context.Context,
	_ openapi.GetCurrentUserRequestObject,
) (openapi.GetCurrentUserResponseObject, error) {
	if s.server.store == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "store unavailable")
	}
	principal, ok := principalFromContext(ctx)
	if !ok || principal.Type != identitystore.PrincipalTypeUser || principal.ID == storage.NilID {
		return nil, apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
	}
	user, err := s.server.store.Identity().GetUser(ctx, principal.ID)
	if err != nil {
		return nil, apierror.UserScoped(err)
	}
	email, err := s.server.store.Identity().PrimaryVerifiedUserEmail(ctx, principal.ID)
	if err != nil {
		return nil, apierror.UserScoped(err)
	}
	memberships, err := s.server.store.Identity().ListOrgMembershipsForUser(ctx, principal.ID)
	if err != nil {
		return nil, apierror.UserScoped(err)
	}
	response, err := currentUserResponse(user, email, memberships)
	if err != nil {
		return nil, err
	}
	return openapi.GetCurrentUser200JSONResponse(response), nil
}

func currentUserResponse(
	user identitystore.UserRecord,
	email string,
	memberships []identitystore.UserOrgMembershipRecord,
) (openapi.CurrentUser, error) {
	userID, err := publicID(publicid.KindUser, user.ID)
	if err != nil {
		return openapi.CurrentUser{}, err
	}
	orgs := make([]openapi.CurrentUserOrg, 0, len(memberships))
	for _, membership := range memberships {
		orgID, err := publicID(publicid.KindOrganization, membership.OrgID)
		if err != nil {
			return openapi.CurrentUser{}, err
		}
		orgs = append(orgs, openapi.CurrentUserOrg{
			Id:        orgID,
			Name:      membership.OrgName,
			Role:      membership.Role,
			CreatedAt: membership.CreatedAt,
		})
	}
	return openapi.CurrentUser{
		User: openapi.CurrentUserIdentity{
			Id:          userID,
			Email:       email,
			DisplayName: user.DisplayName,
		},
		Orgs: orgs,
	}, nil
}
