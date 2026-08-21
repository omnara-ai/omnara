package httpapi

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/defaultprovider"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func orgResponse(record identitystore.OrgRecord) (openapi.Organization, error) {
	id, err := publicID(publicid.KindOrganization, record.ID)
	if err != nil {
		return openapi.Organization{}, err
	}
	return openapi.Organization{
		Id:        id,
		Name:      record.Name,
		CreatedAt: record.CreatedAt,
		UpdatedAt: record.UpdatedAt,
	}, nil
}

func orgMembershipResponse(
	record identitystore.OrgMembershipRecord,
) (openapi.OrganizationMembership, error) {
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.OrganizationMembership{}, err
	}
	userID, err := publicID(publicid.KindUser, record.UserID)
	if err != nil {
		return openapi.OrganizationMembership{}, err
	}
	return openapi.OrganizationMembership{
		OrgId:     orgID,
		UserId:    userID,
		Role:      record.Role,
		CreatedAt: record.CreatedAt,
	}, nil
}

func (s strictOpenAPIServer) CreateOrganization(
	ctx context.Context,
	request openapi.CreateOrganizationRequestObject,
) (openapi.CreateOrganizationResponseObject, error) {
	if s.server.store == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "store unavailable")
	}
	principal, ok := principalFromContext(ctx)
	if !ok || principal.Type != identitystore.PrincipalTypeUser {
		return nil, apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	idempotencyKey := ""
	if request.Params.IdempotencyKey != nil {
		idempotencyKey = *request.Params.IdempotencyKey
		if strings.TrimSpace(idempotencyKey) == "" || idempotencyKey != strings.TrimSpace(idempotencyKey) {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest,
				"Idempotency-Key cannot be empty or have surrounding whitespace")
		}
		replay, found, err := s.server.store.Identity().GetOrgCreationReplay(ctx, identitystore.GetOrgCreationReplayInput{
			UserID:         principal.ID,
			Name:           request.Body.Name,
			IdempotencyKey: idempotencyKey,
		})
		if err != nil {
			return nil, s.createOrganizationStorageError("load completed organization request", err)
		}
		if found {
			return createOrganizationResponse(ctx, replay)
		}
	}

	orgID, err := newProposedOrganizationID(principal.ID, idempotencyKey)
	if err != nil {
		return nil, s.createOrganizationStorageError("generate organization id", err)
	}
	input := orglifecycle.CreateOrgForUserInput{
		OrgID:                         orgID,
		UserID:                        principal.ID,
		Name:                          request.Body.Name,
		IdempotencyKey:                idempotencyKey,
		DefaultMachinePools:           s.server.defaultPools,
		ProvisionDefaultModelProvider: s.server.defaultModelProvider != nil,
	}
	record, err := s.server.store.Organizations().CreateOrgForUser(ctx, input)
	if err != nil {
		return nil, s.createOrganizationStorageError("commit organization creation", err)
	}
	if record.Created && s.server.defaultModelProvider != nil && s.server.hostedCredentialProvisioner != nil {
		runner := defaultprovider.NewRunner(
			s.server.store.Organizations(),
			s.server.hostedCredentialProvisioner,
			*s.server.defaultModelProvider,
		)
		attempted, publicOrgID, provisionErr := runner.RunOrganization(ctx, record.Org.ID)
		if provisionErr != nil {
			s.server.log.Warn(
				"defer default model provider provisioning",
				"org_id", publicOrgID,
				"provider_name", s.server.defaultModelProvider.Name,
				"error", provisionErr,
			)
		} else if attempted {
			s.server.log.Info(
				"provisioned default model provider",
				"org_id", publicOrgID,
				"provider_name", s.server.defaultModelProvider.Name,
			)
		}
	}
	return createOrganizationResponse(ctx, record)
}

func createOrganizationResponse(
	ctx context.Context,
	record identitystore.CreateOrgForUserRecord,
) (openapi.CreateOrganizationResponseObject, error) {
	logent.Org(ctx, record.Org)
	logent.Project(ctx, record.Project)
	org, err := orgResponse(record.Org)
	if err != nil {
		return nil, err
	}
	project, err := projectResponse(record.Project)
	if err != nil {
		return nil, err
	}
	membership, err := orgMembershipResponse(record.Membership)
	if err != nil {
		return nil, err
	}
	response := openapi.CreateOrganizationResponse{Org: org, Project: project, Membership: membership}
	if record.Created {
		return openapi.CreateOrganization201JSONResponse(response), nil
	}
	return openapi.CreateOrganization200JSONResponse(response), nil
}

func (s strictOpenAPIServer) DeleteOrganization(
	ctx context.Context,
	_ openapi.DeleteOrganizationRequestObject,
) (openapi.DeleteOrganizationResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	principal, ok := principalFromContext(ctx)
	if !ok || principal.ID == storage.NilID {
		return nil, apierror.FromCode(openapi.ErrorCodeUnauthorized, "unauthorized")
	}
	machines, err := s.server.store.Organizations().DeleteOrganization(ctx, org.ID, principal)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	s.server.startPoolMachineDeletion(ctx, machines)
	return openapi.DeleteOrganization204Response{}, nil
}

func (s strictOpenAPIServer) createOrganizationStorageError(operation string, err error) error {
	switch {
	case errors.Is(err, storeerr.ErrIdempotencyConflict):
		return apierror.FromCode(
			openapi.ErrorCodeIdempotencyKeyConflict,
			"organization request conflicts with an earlier attempt")

	case errors.Is(err, storeerr.ErrConflict), errors.Is(err, storeerr.ErrStateTransitionConflict):
		return apierror.FromCode(openapi.ErrorCodeConflict, "organization creation state conflict")
	case errors.Is(err, storeerr.ErrUnauthorized):
		return apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
	case storeerr.IsNotFound(err):
		return apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	default:
		s.server.log.Error(operation, "error", err)
		return apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "organization creation temporarily unavailable")
	}
}

func newProposedOrganizationID(
	actorUserID storage.ID,
	orgCreationIdempotencyKey string,
) (uuid.UUID, error) {
	if orgCreationIdempotencyKey == "" {
		id, err := uuid.NewV7()
		if err != nil {
			return uuid.Nil, fmt.Errorf("generate UUIDv7: %w", err)
		}
		return id, nil
	}
	return uuid.NewHash(
		sha256.New(),
		uuid.NameSpaceURL,
		[]byte("https://omnara.com/org-creation/v1\x00"+
			actorUserID.String()+"\x00"+orgCreationIdempotencyKey),
		8,
	), nil
}
