package integrationstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// RegisterExternalChannel fences public project-management registration to a
// live external connection. Managed addresses use their connector-owned path.
func (s *Store) RegisterExternalChannel(
	ctx context.Context,
	input CreateIntegrationTargetInput,
) (IntegrationTargetRecord, error) {
	// Kind and project ownership are immutable. The creator rechecks the live
	// installation and definition under its own transaction's lifecycle locks.
	install, err := s.GetIntegrationInstall(ctx, input.ProjectID, input.IntegrationInstallID)
	if err != nil {
		return IntegrationTargetRecord{}, err
	}
	if install.IntegrationKind != IntegrationKindExternal {
		return IntegrationTargetRecord{}, storeerr.ErrNotFound
	}
	return s.CreateIntegrationTarget(ctx, input)
}

// RegisterManagedChannel atomically registers a resolved managed address and its
// optional parent. The caller resolves provider addresses before this transaction
// and authorizes project management. Registration creates no agents or grants.
func (s *Store) RegisterManagedChannel(
	ctx context.Context,
	input CreateIntegrationTargetInput,
	parent *CreateIntegrationTargetInput,
) (IntegrationTargetRecord, error) {
	if input.ProjectID == uuid.Nil || input.IntegrationInstallID == uuid.Nil {
		return IntegrationTargetRecord{}, storeerr.InvalidRequest(
			errors.New("project and integration install are required"))
	}
	if parent != nil {
		if input.ParentChannelID != uuid.Nil || parent.ParentChannelID != uuid.Nil {
			return IntegrationTargetRecord{}, storeerr.InvalidRequest(
				errors.New("inline parent requires no existing child parent ID or nested parent ID"))
		}
		if strings.TrimSpace(parent.ProviderRef) == strings.TrimSpace(input.ProviderRef) {
			return IntegrationTargetRecord{}, storeerr.InvalidRequest(
				errors.New("parent and child channel addresses must differ"))
		}
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationTargetRecord{}, fmt.Errorf("begin managed channel registration: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	install, err := lockIntegrationInstallLifecycleShared(ctx, tx, input.ProjectID, input.IntegrationInstallID)
	if err != nil {
		return IntegrationTargetRecord{}, err
	}
	if install.IntegrationKind != IntegrationKindManaged {
		return IntegrationTargetRecord{}, storeerr.ErrNotFound
	}
	if parent != nil || input.ParentChannelID != uuid.Nil {
		existing, err := s.GetIntegrationTargetByProviderRefTx(
			ctx, tx, input.ProjectID, input.IntegrationInstallID, strings.TrimSpace(input.ProviderRef),
		)
		if err != nil && !errors.Is(err, storeerr.ErrNotFound) {
			return IntegrationTargetRecord{}, err
		}
		if err == nil && existing.ParentChannelID == uuid.Nil {
			if input.ParentChannelID != uuid.Nil {
				if _, err := dbsqlc.New(tx).LockIntegrationChannelParent(ctx, dbsqlc.LockIntegrationChannelParentParams{
					ProjectID: input.ProjectID, IntegrationInstallID: input.IntegrationInstallID,
					ParentChannelID: input.ParentChannelID,
				}); err != nil {
					return IntegrationTargetRecord{}, integrationChannelReadError("lock channel parent", err)
				}
			}
			// Resolving an existing address is not a reparent operation. Older
			// inbound/migrated threads can legitimately have no recorded parent.
			// Preserve that identity, without creating an unused parent or grants.
			parent = nil
			input.ParentChannelID = uuid.Nil
		}
	}
	// Each target creator rechecks and locks the live installation, app and
	// definition. The parent's scope always comes from the outer authority.
	if parent != nil {
		parentInput := *parent
		parentInput.ProjectID, parentInput.IntegrationInstallID = input.ProjectID, input.IntegrationInstallID
		registeredParent, err := s.CreateIntegrationTargetTx(ctx, tx, parentInput)
		if err != nil {
			return IntegrationTargetRecord{}, err
		}
		input.ParentChannelID = registeredParent.ID
	}
	record, err := s.CreateIntegrationTargetTx(ctx, tx, input)
	if err != nil {
		return IntegrationTargetRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IntegrationTargetRecord{}, fmt.Errorf("commit managed channel registration: %w", err)
	}
	return record, nil
}

// RevokeAgentChannelBinding checks immutable ownership, including revoked rows.
// Deleting an old binding again never affects a replacement for the same channel.
func (s *Store) RevokeAgentChannelBinding(ctx context.Context, projectID, agentID, id uuid.UUID) error {
	if projectID == uuid.Nil || agentID == uuid.Nil || id == uuid.Nil {
		return storeerr.InvalidRequest(errors.New("project, agent and binding are required"))
	}
	belongs, err := s.q.IntegrationTargetBindingBelongsToAgent(ctx,
		dbsqlc.IntegrationTargetBindingBelongsToAgentParams{ProjectID: projectID, AgentID: agentID, ID: id})
	if err != nil {
		return fmt.Errorf("check agent channel binding owner: %w", err)
	}
	if !belongs {
		return storeerr.ErrNotFound
	}
	return s.RevokeIntegrationTargetBinding(ctx, projectID, id)
}
