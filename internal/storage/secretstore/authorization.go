package secretstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) AuthorizeSecretOwnerManage(
	ctx context.Context,
	orgID ID,
	owner SecretOwner,
	actor PrincipalRecord,
) error {
	switch owner.Kind {
	case SecretOwnerOrg:
		if !isNilID(owner.ProjectID) || !isNilID(owner.UserID) {
			return invalidSecretRequest("org-owned secret cannot set project or user owner")
		}
		if err := s.authorizeOrgSecretsManage(ctx, orgID, actor); err != nil {
			return err
		}
	case SecretOwnerProject:
		if isNilID(owner.ProjectID) || !isNilID(owner.UserID) {
			return invalidSecretRequest("project-owned secret requires only project owner")
		}
		if err := s.authorizeProjectSecretsManage(
			ctx,
			orgID,
			owner.ProjectID,
			actor,
		); err != nil {
			return err
		}
	case SecretOwnerUser:
		if !isNilID(owner.ProjectID) || isNilID(owner.UserID) {
			return invalidSecretRequest("user-owned secret requires only user owner")
		}
		if actor.Type != principalTypeUser || owner.UserID != actor.ID {
			return storeerr.ErrUnauthorized
		}
		if err := s.requireOrgMember(ctx, orgID, actor); err != nil {
			return err
		}
	default:
		return invalidSecretRequest("unsupported secret owner kind %q", owner.Kind)
	}
	return nil
}

func (s *Store) authorizeSecretManage(ctx context.Context, secret SecretRecord, actor PrincipalRecord) error {
	if isNilID(actor.ID) {
		return storeerr.ErrUnauthorized
	}
	switch secret.OwnerKind {
	case SecretOwnerOrg:
		return s.authorizeOrgSecretsManage(ctx, secret.OrgID, actor)
	case SecretOwnerProject:
		return s.authorizeProjectSecretsManage(ctx, secret.OrgID, secret.OwnerProjectID, actor)
	case SecretOwnerUser:
		if actor.Type != principalTypeUser || secret.OwnerUserID != actor.ID {
			return storeerr.ErrUnauthorized
		}
		return s.requireOrgMember(ctx, secret.OrgID, actor)
	default:
		return fmt.Errorf("unsupported secret owner kind %q", secret.OwnerKind)
	}
}

func (s *Store) authorizeSecretRead(ctx context.Context, secret SecretRecord, actor PrincipalRecord) error {
	if isNilID(actor.ID) {
		return storeerr.ErrUnauthorized
	}
	switch secret.OwnerKind {
	case SecretOwnerOrg:
		allowed, err := s.access.AuthorizeOrg(ctx, identitystore.AuthorizeOrgInput{
			Principal: actor,
			OrgID:     secret.OrgID,
			Action:    orgActionSecretsList,
		})
		if err != nil {
			return err
		}
		if !allowed {
			return storeerr.ErrUnauthorized
		}
		return nil
	case SecretOwnerProject:
		return s.authorizeProjectSecretsList(ctx, secret.OrgID, secret.OwnerProjectID, actor)
	case SecretOwnerUser:
		if actor.Type != principalTypeUser || secret.OwnerUserID != actor.ID {
			return storeerr.ErrUnauthorized
		}
		return s.requireOrgMember(ctx, secret.OrgID, actor)
	default:
		return fmt.Errorf("unsupported secret owner kind %q", secret.OwnerKind)
	}
}

func (s *Store) authorizeSecretGrantDelete(
	ctx context.Context,
	secret SecretRecord,
	targetProjectID ID,
	actor PrincipalRecord,
) error {
	if err := s.authorizeSecretManage(ctx, secret, actor); err == nil {
		return nil
	} else if !errors.Is(err, storeerr.ErrUnauthorized) {
		return err
	}
	if err := s.authorizeProjectSecretsManage(
		ctx,
		secret.OrgID,
		targetProjectID,
		actor,
	); err == nil {
		return nil
	} else if !errors.Is(
		err,
		storeerr.ErrUnauthorized,
	) {
		return err
	}
	return storeerr.ErrNotFound
}

func (s *Store) authorizeOrgSecretsManage(ctx context.Context, orgID ID, actor PrincipalRecord) error {
	allowed, err := s.access.AuthorizeOrg(ctx, identitystore.AuthorizeOrgInput{
		Principal: actor,
		OrgID:     orgID,
		Action:    orgActionSecretsManage,
	})
	if err != nil {
		return err
	}
	if !allowed {
		return storeerr.ErrUnauthorized
	}
	return nil
}

func (s *Store) authorizeOrgSecretsList(ctx context.Context, orgID ID, actor PrincipalRecord) error {
	allowed, err := s.access.AuthorizeOrg(ctx, identitystore.AuthorizeOrgInput{
		Principal: actor,
		OrgID:     orgID,
		Action:    orgActionSecretsList,
	})
	if err != nil {
		return err
	}
	if !allowed {
		return storeerr.ErrUnauthorized
	}
	return nil
}

func (s *Store) authorizeProjectSecretsManage(
	ctx context.Context,
	orgID, projectID ID,
	actor PrincipalRecord,
) error {
	allowed, err := s.access.AuthorizeProject(ctx, identitystore.AuthorizeProjectInput{
		Principal: actor,
		OrgID:     orgID,
		ProjectID: projectID,
		Action:    projectActionSecretsManage,
	})
	if err != nil {
		return err
	}
	if !allowed {
		return storeerr.ErrUnauthorized
	}
	return nil
}

func (s *Store) authorizeProjectSecretsList(
	ctx context.Context,
	orgID, projectID ID,
	actor PrincipalRecord,
) error {
	allowed, err := s.access.AuthorizeProject(ctx, identitystore.AuthorizeProjectInput{
		Principal: actor,
		OrgID:     orgID,
		ProjectID: projectID,
		Action:    projectActionSecretsList,
	})
	if err != nil {
		return err
	}
	if !allowed {
		return storeerr.ErrUnauthorized
	}
	return nil
}

func (s *Store) requireOrgMember(ctx context.Context, orgID ID, actor PrincipalRecord) error {
	allowed, err := s.access.HasOrgMembership(ctx, actor, orgID)
	if err != nil {
		return err
	}
	if !allowed {
		return storeerr.ErrUnauthorized
	}
	return nil
}

func lockActiveSecretOwnerMembershipTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID ID,
	ownerKind string,
	ownerUserID ID,
) error {
	if ownerKind != SecretOwnerUser {
		return nil
	}
	if _, err := qtx.LockUserForUpdate(
		ctx,
		dbsqlc.LockUserForUpdateParams{ID: ownerUserID},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrUnauthorized
		}
		return fmt.Errorf("lock secret owner: %w", err)
	}
	if _, err := qtx.GetOrgAuthorizationRole(
		ctx,
		dbsqlc.GetOrgAuthorizationRoleParams{OrgID: orgID, UserID: ownerUserID},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrUnauthorized
		}
		return fmt.Errorf("load owner org membership: %w", err)
	}
	return nil
}
