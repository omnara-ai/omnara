package identitystore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/authz"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) AuthorizeProject(ctx context.Context, input AuthorizeProjectInput) (bool, error) {
	if input.Principal.Type == "" || isNilID(input.Principal.ID) {
		return false, storeerr.ErrUnauthorized
	}
	if isNilID(input.OrgID) {
		return false, errors.New("org id is required")
	}
	if isNilID(input.ProjectID) {
		return false, errors.New("project id is required")
	}
	if input.Action == "" {
		return false, errors.New("action is required")
	}
	if !IsAccountPrincipal(input.Principal) {
		return false, nil
	}
	if !isNilID(input.Principal.OrgID) && input.Principal.OrgID != input.OrgID {
		return false, nil
	}
	userID, orgAPIKeyID := AccountPrincipalIDs(input.Principal)
	if userID == nil && orgAPIKeyID == nil {
		return false, nil
	}
	roles, err := s.q.ListProjectAuthorizationRolesForPrincipal(
		ctx,
		dbsqlc.ListProjectAuthorizationRolesForPrincipalParams{
			OrgID:       input.OrgID,
			ProjectID:   input.ProjectID,
			UserID:      userID,
			OrgApiKeyID: orgAPIKeyID,
		},
	)
	if err != nil {
		return false, fmt.Errorf("load project authorization roles: %w", err)
	}
	for _, role := range roles {
		if authz.ProjectRoleAllows(role, input.Action) {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) AuthorizeOrg(ctx context.Context, input AuthorizeOrgInput) (bool, error) {
	if input.Principal.Type == "" || isNilID(input.Principal.ID) {
		return false, storeerr.ErrUnauthorized
	}
	if isNilID(input.OrgID) {
		return false, errors.New("org id is required")
	}
	if input.Action == "" {
		return false, errors.New("action is required")
	}
	if !IsAccountPrincipal(input.Principal) {
		return false, nil
	}
	if !isNilID(input.Principal.OrgID) && input.Principal.OrgID != input.OrgID {
		return false, nil
	}
	userID, orgAPIKeyID := AccountPrincipalIDs(input.Principal)
	if userID == nil && orgAPIKeyID == nil {
		return false, nil
	}
	role, err := s.q.GetOrgAuthorizationRoleForPrincipal(
		ctx,
		dbsqlc.GetOrgAuthorizationRoleForPrincipalParams{
			OrgID:       input.OrgID,
			UserID:      userID,
			OrgApiKeyID: orgAPIKeyID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load org authorization role: %w", err)
	}
	return authz.OrgRoleAllows(role, input.Action), nil
}

func (s *Store) HasOrgMembership(ctx context.Context, principal PrincipalRecord, orgID ID) (bool, error) {
	if isNilID(orgID) || !IsAccountPrincipal(principal) {
		return false, nil
	}
	userID, orgAPIKeyID := AccountPrincipalIDs(principal)
	_, err := s.q.GetOrgAuthorizationRoleForPrincipal(
		ctx,
		dbsqlc.GetOrgAuthorizationRoleForPrincipalParams{
			OrgID:       orgID,
			UserID:      userID,
			OrgApiKeyID: orgAPIKeyID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load org membership: %w", err)
	}
	return true, nil
}

type orgMembershipRow struct {
	ID   ID
	Role string
}

func getOrgMembershipForPrincipalTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID ID,
	principal PrincipalRecord,
) (orgMembershipRow, error) {
	switch principal.Type {
	case authz.PrincipalUser:
		row, err := qtx.GetOrgMembershipForUser(
			ctx,
			dbsqlc.GetOrgMembershipForUserParams{OrgID: orgID, UserID: principal.ID},
		)
		if err != nil {
			return orgMembershipRow{}, err
		}
		return orgMembershipRow{ID: row.ID, Role: row.Role}, nil
	case authz.PrincipalOrgAPIKey:
		row, err := qtx.GetOrgMembershipForOrgAPIKey(
			ctx,
			dbsqlc.GetOrgMembershipForOrgAPIKeyParams{OrgID: orgID, OrgApiKeyID: principal.ID},
		)
		if err != nil {
			return orgMembershipRow{}, err
		}
		return orgMembershipRow{ID: row.ID, Role: row.Role}, nil
	default:
		return orgMembershipRow{}, pgx.ErrNoRows
	}
}
