package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/authz"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	MachineActionRead   = authz.MachineRead
	MachineActionManage = authz.MachineManage
)

type AuthorizeMachineInput struct {
	Principal identitystore.PrincipalRecord
	OrgID     uuid.UUID
	MachineID uuid.UUID
	Action    string
}

func (s *Store) AuthorizeMachine(ctx context.Context, input AuthorizeMachineInput) (bool, error) {
	if input.Principal.Type == "" || input.Principal.ID == uuid.Nil {
		return false, storeerr.ErrUnauthorized
	}
	if input.OrgID == uuid.Nil {
		return false, errors.New("org id is required")
	}
	if input.MachineID == uuid.Nil {
		return false, errors.New("machine id is required")
	}
	if input.Action == "" {
		return false, errors.New("action is required")
	}
	if !identitystore.IsAccountPrincipal(input.Principal) {
		return false, nil
	}
	if input.Principal.OrgID != uuid.Nil && input.Principal.OrgID != input.OrgID {
		return false, nil
	}
	userID, orgAPIKeyID := identitystore.AccountPrincipalIDs(input.Principal)
	if userID == nil && orgAPIKeyID == nil {
		return false, nil
	}
	if _, err := s.GetMachine(ctx, input.OrgID, input.MachineID); errors.Is(err, storeerr.ErrNotFound) ||
		errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("load machine authorization facts: %w", err)
	}
	if allowed, err := s.identity.AuthorizeOrg(
		ctx,
		identitystore.AuthorizeOrgInput{
			Principal: input.Principal,
			OrgID:     input.OrgID,
			Action:    identitystore.OrgActionManage,
		},
	); err != nil {
		return false, err
	} else if allowed &&
		authz.MachineRoleAllows(input.Action) {
		return true, nil
	}
	if input.Action == MachineActionRead {
		visible, err := s.q.MachineProjectVisibleToPrincipal(
			ctx,
			dbsqlc.MachineProjectVisibleToPrincipalParams{
				OrgID:       input.OrgID,
				MachineID:   input.MachineID,
				UserID:      userID,
				OrgApiKeyID: orgAPIKeyID,
			},
		)
		if err != nil {
			return false, fmt.Errorf("load machine project visibility: %w", err)
		}
		if visible {
			return true, nil
		}
	}
	return false, nil
}
