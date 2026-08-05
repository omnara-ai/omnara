package skillstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) validateCreateSkillInput(ctx context.Context, input CreateSkillInput) error {
	if isNilUUID(input.OrgID) || isNilUUID(input.Actor.ID) {
		return invalidSkillRequest("org and actor are required")
	}
	if err := s.AuthorizeSkillOwnerManage(ctx, input.OrgID, SkillOwner{
		Kind: input.OwnerKind, ProjectID: input.OwnerProjectID, UserID: input.OwnerUserID,
	}, input.Actor); err != nil {
		return err
	}
	if input.Name == "" {
		return invalidSkillRequest("skill name is required")
	}
	if input.Description == "" {
		return invalidSkillRequest("skill description is required")
	}
	if input.SkillMd == "" {
		return invalidSkillRequest("skill body is required")
	}
	if len(input.ArchiveBytes) == 0 {
		return invalidSkillRequest("skill archive bytes are required")
	}
	return nil
}

type SkillOwner struct {
	Kind      string
	ProjectID uuid.UUID
	UserID    uuid.UUID
}

func (s *Store) AuthorizeSkillOwnerManage(
	ctx context.Context,
	orgID uuid.UUID,
	owner SkillOwner,
	actor identitystore.PrincipalRecord,
) error {
	switch owner.Kind {
	case SkillOwnerOrg:
		if !isNilUUID(owner.ProjectID) || !isNilUUID(owner.UserID) {
			return invalidSkillRequest("org-owned skill cannot set project or user owner")
		}
		return s.authorizeOrgSkillManage(ctx, orgID, actor)
	case SkillOwnerProject:
		if isNilUUID(owner.ProjectID) || !isNilUUID(owner.UserID) {
			return invalidSkillRequest("project-owned skill requires only project owner")
		}
		return s.authorizeProjectSkillManage(ctx, orgID, owner.ProjectID, actor)
	case SkillOwnerUser:
		if !isNilUUID(owner.ProjectID) || isNilUUID(owner.UserID) {
			return invalidSkillRequest("user-owned skill requires only user owner")
		}
		if actor.Type != identitystore.PrincipalTypeUser || owner.UserID != actor.ID {
			return storeerr.ErrUnauthorized
		}
		return s.requireOrgMember(ctx, orgID, actor)
	default:
		return invalidSkillRequest("unsupported owner kind %q", owner.Kind)
	}
}

func (s *Store) authorizeSkillManage(
	ctx context.Context,
	skill SkillRecord,
	actor identitystore.PrincipalRecord,
) error {
	return s.AuthorizeSkillOwnerManage(ctx, skill.OrgID, SkillOwner{
		Kind: skill.OwnerKind, ProjectID: skill.OwnerProjectID, UserID: skill.OwnerUserID,
	}, actor)
}

func (s *Store) authorizeSkillRead(
	ctx context.Context,
	skill SkillRecord,
	actor identitystore.PrincipalRecord,
) error {
	if isNilUUID(actor.ID) {
		return storeerr.ErrUnauthorized
	}
	switch skill.OwnerKind {
	case SkillOwnerOrg:
		allowed, err := s.access.AuthorizeOrg(ctx, identitystore.AuthorizeOrgInput{
			Principal: actor,
			OrgID:     skill.OrgID,
			Action:    identitystore.OrgActionRead,
		})
		if err != nil {
			return err
		}
		if !allowed {
			return storeerr.ErrUnauthorized
		}
		return nil
	case SkillOwnerProject:
		allowed, err := s.access.AuthorizeProject(ctx, identitystore.AuthorizeProjectInput{
			Principal: actor,
			OrgID:     skill.OrgID,
			ProjectID: skill.OwnerProjectID,
			Action:    identitystore.ProjectActionRead,
		})
		if err != nil {
			return err
		}
		if !allowed {
			return storeerr.ErrUnauthorized
		}
		return nil
	case SkillOwnerUser:
		if actor.Type != identitystore.PrincipalTypeUser || skill.OwnerUserID != actor.ID {
			return storeerr.ErrUnauthorized
		}
		return s.requireOrgMember(ctx, skill.OrgID, actor)
	default:
		return fmt.Errorf("unsupported skill owner kind %q", skill.OwnerKind)
	}
}

func (s *Store) authorizeOrgSkillManage(
	ctx context.Context,
	orgID uuid.UUID,
	actor identitystore.PrincipalRecord,
) error {
	allowed, err := s.access.AuthorizeOrg(ctx, identitystore.AuthorizeOrgInput{
		Principal: actor,
		OrgID:     orgID,
		Action:    identitystore.OrgActionManage,
	})
	if err != nil {
		return err
	}
	if !allowed {
		return storeerr.ErrUnauthorized
	}
	return nil
}

func (s *Store) authorizeProjectSkillManage(
	ctx context.Context,
	orgID, projectID uuid.UUID,
	actor identitystore.PrincipalRecord,
) error {
	allowed, err := s.access.AuthorizeProject(ctx, identitystore.AuthorizeProjectInput{
		Principal: actor,
		OrgID:     orgID,
		ProjectID: projectID,
		Action:    identitystore.ProjectActionManage,
	})
	if err != nil {
		return err
	}
	if !allowed {
		return storeerr.ErrUnauthorized
	}
	return nil
}

func (s *Store) requireOrgMember(
	ctx context.Context,
	orgID uuid.UUID,
	actor identitystore.PrincipalRecord,
) error {
	allowed, err := s.access.HasOrgMembership(ctx, actor, orgID)
	if err != nil {
		return err
	}
	if !allowed {
		return storeerr.ErrUnauthorized
	}
	return nil
}

func (s *Store) skillVisibleForCompile(
	ctx context.Context,
	rec SkillRecord,
	input GetSkillsByIDsInput,
) (bool, error) {
	if rec.OrgID != input.OrgID || isNilUUID(input.ProjectID) {
		return false, nil
	}
	if rec.OwnerKind == SkillOwnerProject && rec.OwnerProjectID == input.ProjectID {
		return true, nil
	}
	available, err := s.q.SkillAvailableToProject(ctx, dbsqlc.SkillAvailableToProjectParams{
		OrgID: input.OrgID, SkillID: rec.ID, ProjectID: input.ProjectID,
	})
	if err != nil {
		return false, fmt.Errorf("check skill project grant: %w", err)
	}
	return available, nil
}

func (s *Store) authorizeSkillGrantDelete(
	ctx context.Context,
	skill SkillRecord,
	targetProjectID uuid.UUID,
	actor identitystore.PrincipalRecord,
) error {
	if err := s.authorizeSkillManage(ctx, skill, actor); err == nil {
		return nil
	} else if !errors.Is(err, storeerr.ErrUnauthorized) {
		return err
	}
	if err := s.authorizeProjectSkillManage(ctx, skill.OrgID, targetProjectID, actor); err == nil {
		return nil
	} else if !errors.Is(err, storeerr.ErrUnauthorized) {
		return err
	}
	return storeerr.ErrNotFound
}
