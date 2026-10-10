package integrationstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type DisconnectIntegrationInput struct {
	ProjectID             uuid.UUID
	IntegrationID         uuid.UUID
	ExpectedSetupRevision *int64
}

func (s *Store) DisconnectIntegration(
	ctx context.Context,
	input DisconnectIntegrationInput,
) (bool, error) {
	if input.ProjectID == uuid.Nil || input.IntegrationID == uuid.Nil {
		return false, storeerr.InvalidRequest(errors.New("project and integration are required"))
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	integration, err := getIntegration(ctx, q, input.ProjectID, input.IntegrationID)
	if err != nil {
		return false, err
	}
	if err := lifecyclelock.EnterActiveProject(ctx, tx, integration.OrgID, input.ProjectID); err != nil {
		return false, err
	}
	if err := q.LockIntegrationLifecycleExclusive(
		ctx,
		dbsqlc.LockIntegrationLifecycleExclusiveParams{IntegrationID: input.IntegrationID},
	); err != nil {
		return false, err
	}
	rows, err := q.DisconnectIntegration(
		ctx,
		dbsqlc.DisconnectIntegrationParams{
			ProjectID:             input.ProjectID,
			ID:                    input.IntegrationID,
			ExpectedSetupRevision: input.ExpectedSetupRevision,
		},
	)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return rows == 1, nil
}

func (s *Store) DeleteIntegration(ctx context.Context, orgID, projectID, id uuid.UUID) error {
	if orgID == uuid.Nil || projectID == uuid.Nil || id == uuid.Nil {
		return storeerr.InvalidRequest(errors.New("organization, project and integration are required"))
	}
	_, err := storeutil.RetryTransaction(ctx, "delete_integration", func() (struct{}, error) {
		return struct{}{}, s.deleteIntegrationOnce(ctx, orgID, projectID, id)
	})
	return err
}

func (s *Store) deleteIntegrationOnce(ctx context.Context, orgID, projectID, id uuid.UUID) error {
	unit, err := s.cell.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = unit.Rollback(ctx) }()
	tx := unit.DB()
	if err := lifecyclelock.EnterActiveProject(ctx, tx, orgID, projectID); err != nil {
		return err
	}
	q := dbsqlc.New(tx)
	if err := q.LockIntegrationLifecycleExclusive(
		ctx,
		dbsqlc.LockIntegrationLifecycleExclusiveParams{IntegrationID: id},
	); err != nil {
		return err
	}
	if _, err := getIntegration(ctx, q, projectID, id); err != nil {
		return err
	}
	ids, err := q.ListIntegrationAgentIDsForLifecycle(
		ctx,
		dbsqlc.ListIntegrationAgentIDsForLifecycleParams{ProjectID: projectID, IntegrationID: id},
	)
	if err != nil {
		return err
	}
	refs := make([]lifecyclelock.AgentRef, 0, len(ids))
	for _, agentID := range ids {
		refs = append(refs, lifecyclelock.AgentRef{ProjectID: projectID, AgentID: agentID})
	}
	if err := unit.LockAgentRefs(ctx, refs, agentexecution.LifecycleAuthority{}); err != nil {
		return err
	}
	if err := q.DeleteIntegrationSubscriptions(ctx, dbsqlc.DeleteIntegrationSubscriptionsParams{
		ProjectID: projectID, IntegrationID: id,
	}); err != nil {
		return err
	}
	if err := s.access.ClearIntegrationTargetsFromAgents(ctx, unit, projectID, id); err != nil {
		return err
	}
	if err := q.DeleteIntegrationTargets(
		ctx,
		dbsqlc.DeleteIntegrationTargetsParams{ProjectID: projectID, IntegrationID: id},
	); err != nil {
		return err
	}
	if _, err := q.DeleteCronTriggersForIntegration(ctx, dbsqlc.DeleteCronTriggersForIntegrationParams{
		ProjectID: projectID, IntegrationID: &id,
	}); err != nil {
		return fmt.Errorf("delete integration cron triggers: %w", err)
	}
	rows, err := q.DeleteIntegration(ctx, dbsqlc.DeleteIntegrationParams{ProjectID: projectID, ID: id})
	if err != nil {
		return fmt.Errorf("delete integration: %w", err)
	}
	if rows == 0 {
		return storeerr.ErrNotFound
	}
	return unit.Commit(ctx, "deleteIntegrationOnce")
}
