package httpapi

import (
	"context"

	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func orgInvitationResponse(record identitystore.OrgInvitationRecord) (openapi.OrgInvitation, error) {
	id, err := publicID(publicid.KindOrgInvitation, record.ID)
	if err != nil {
		return openapi.OrgInvitation{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.OrgInvitation{}, err
	}
	return openapi.OrgInvitation{
		Id:        id,
		OrgId:     orgID,
		Email:     openapi_types.Email(record.Email),
		OrgRole:   record.OrgRole,
		CreatedAt: record.CreatedAt,
	}, nil
}

func invitationListResponse(
	invitations []identitystore.OrgInvitationRecord,
	hasMore bool,
) (openapi.ListOrgInvitationsResponse, error) {
	data := make([]openapi.OrgInvitation, 0, len(invitations))
	var last identitystore.OrgInvitationRecord
	for _, invitation := range invitations {
		response, err := orgInvitationResponse(invitation)
		if err != nil {
			return openapi.ListOrgInvitationsResponse{}, err
		}
		data = append(data, response)
		last = invitation
	}
	nextCursor, err := encodeNextCursor(hasMore, last.CreatedAt, publicid.KindOrgInvitation, last.ID)
	if err != nil {
		return openapi.ListOrgInvitationsResponse{}, err
	}
	return openapi.ListOrgInvitationsResponse{Data: data, NextCursor: nullableFromPtr(nextCursor)}, nil
}

func (s strictOpenAPIServer) ListPendingInvitations(
	ctx context.Context,
	request openapi.ListPendingInvitationsRequestObject,
) (openapi.ListPendingInvitationsResponseObject, error) {
	principal, ok := principalFromContext(ctx)
	if !ok || principal.Type != identitystore.PrincipalTypeUser {
		return nil, apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
	}
	if s.server.store == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "store unavailable")
	}
	limit, after, err := parseOpenAPIPageParams(request.Params.Limit, request.Params.Cursor, publicid.KindOrgInvitation)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Identity().ListPendingOrgInvitationsForUser(
		ctx,
		identitystore.ListPendingOrgInvitationsForUserInput{
			UserID: principal.ID,
			Limit:  limit,
			After:  after,
		},
	)
	if err != nil {
		return nil, apierror.UserScoped(err)
	}
	response, err := invitationListResponse(page.Invitations, page.HasMore)
	if err != nil {
		return nil, err
	}
	return openapi.ListPendingInvitations200JSONResponse(response), nil
}

func (s strictOpenAPIServer) AcceptInvitation(
	ctx context.Context,
	request openapi.AcceptInvitationRequestObject,
) (openapi.AcceptInvitationResponseObject, error) {
	invitation, errResponse := s.answerInvitation(ctx, request.InvitationID, true)
	if errResponse != nil {
		return nil, *errResponse
	}
	response, err := orgInvitationResponse(invitation)
	if err != nil {
		return nil, err
	}
	return openapi.AcceptInvitation200JSONResponse(response), nil
}

func (s strictOpenAPIServer) DeclineInvitation(
	ctx context.Context,
	request openapi.DeclineInvitationRequestObject,
) (openapi.DeclineInvitationResponseObject, error) {
	invitation, errResponse := s.answerInvitation(ctx, request.InvitationID, false)
	if errResponse != nil {
		return nil, *errResponse
	}
	response, err := orgInvitationResponse(invitation)
	if err != nil {
		return nil, err
	}
	return openapi.DeclineInvitation200JSONResponse(response), nil
}

func (s strictOpenAPIServer) answerInvitation(
	ctx context.Context,
	invitationIDRaw string,
	accept bool,
) (identitystore.OrgInvitationRecord, *apierror.ResponseError) {
	principal, ok := principalFromContext(ctx)
	if !ok || principal.Type != identitystore.PrincipalTypeUser {
		err := apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
		return identitystore.OrgInvitationRecord{}, &err
	}
	if s.server.store == nil {
		err := apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "store unavailable")
		return identitystore.OrgInvitationRecord{}, &err
	}
	invitationID, ok := parseOpenAPIPublicID(publicid.KindOrgInvitation, invitationIDRaw)
	if !ok {
		err := apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
		return identitystore.OrgInvitationRecord{}, &err
	}
	var invitation identitystore.OrgInvitationRecord
	var err error
	if accept {
		invitation, err = s.server.store.Identity().AcceptOrgInvitation(
			ctx,
			identitystore.AcceptOrgInvitationInput{ID: invitationID, UserID: principal.ID},
		)
	} else {
		invitation, err = s.server.store.Identity().DeclineOrgInvitation(
			ctx,
			identitystore.DeclineOrgInvitationInput{ID: invitationID, UserID: principal.ID},
		)
	}
	if err != nil {
		apiErr := apierror.UserScoped(err)
		return identitystore.OrgInvitationRecord{}, &apiErr
	}
	return invitation, nil
}

func (s strictOpenAPIServer) ListOrgInvitations(
	ctx context.Context,
	request openapi.ListOrgInvitationsRequestObject,
) (openapi.ListOrgInvitationsResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.listOrgInvitations(ctx, request.Params, org)
}

func (s strictOpenAPIServer) listOrgInvitations(
	ctx context.Context,
	params openapi.ListOrgInvitationsParams,
	org identitystore.OrgRecord,
) (openapi.ListOrgInvitationsResponseObject, error) {
	limit, after, err := parseOpenAPIPageParams(params.Limit, params.Cursor, publicid.KindOrgInvitation)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Identity().ListOrgInvitations(ctx, identitystore.ListOrgInvitationsInput{
		OrgID: org.ID,
		Limit: limit,
		After: after,
	})
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response, err := invitationListResponse(page.Invitations, page.HasMore)
	if err != nil {
		return nil, err
	}
	return openapi.ListOrgInvitations200JSONResponse(response), nil
}

func (s strictOpenAPIServer) CreateOrgInvitation(
	ctx context.Context,
	request openapi.CreateOrgInvitationRequestObject,
) (openapi.CreateOrgInvitationResponseObject, error) {
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.createOrgInvitation(ctx, org, *request.Body)
}

func (s strictOpenAPIServer) createOrgInvitation(
	ctx context.Context,
	org identitystore.OrgRecord,
	body openapi.CreateOrgInvitationJSONRequestBody,
) (openapi.CreateOrgInvitationResponseObject, error) {
	invitation, err := s.server.store.Identity().CreateOrgInvitation(ctx, identitystore.CreateOrgInvitationInput{
		OrgID: org.ID,
		Email: string(body.Email),
		Role:  body.Role,
	})
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	if s.server.email != nil {
		if err := s.server.email.SendInvite(ctx, invitation.Email, org.Name); err != nil {
			logent.OrgInvitationEmailFailed(ctx, err)
		}
	}
	response, err := orgInvitationResponse(invitation)
	if err != nil {
		return nil, err
	}
	return openapi.CreateOrgInvitation201JSONResponse(response), nil
}

func (s strictOpenAPIServer) DeleteOrgInvitation(
	ctx context.Context,
	request openapi.DeleteOrgInvitationRequestObject,
) (openapi.DeleteOrgInvitationResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	invitationID, ok := parseOpenAPIPublicID(publicid.KindOrgInvitation, request.InvitationID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if _, err := s.server.store.Identity().DeleteOrgInvitation(ctx, org.ID, invitationID); err != nil {
		return nil, apierror.OrgScoped(err)
	}
	return openapi.DeleteOrgInvitation204Response{}, nil
}
