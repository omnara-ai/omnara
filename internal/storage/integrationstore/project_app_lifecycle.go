package integrationstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type DisconnectProjectAppInput struct {
	ProjectID uuid.UUID
	AppID     uuid.UUID
	// Provider revocation callbacks must not disconnect a newer verified setup.
	// An explicit user disconnect does not need an observed revision.
	ExpectedSetupRevision *int64
}

func (s *Store) DisconnectProjectApp(ctx context.Context, input DisconnectProjectAppInput) (bool, error) {
	if input.ProjectID == uuid.Nil || input.AppID == uuid.Nil {
		return false, storeerr.InvalidRequest(errors.New("project and app are required"))
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	app, err := getProjectApp(ctx, q, input.ProjectID, input.AppID)
	if err != nil {
		return false, err
	}
	if err := lifecyclelock.EnterActiveProject(ctx, tx, app.OrgID, input.ProjectID); err != nil {
		return false, err
	}
	if err := q.LockProjectAppLifecycleExclusive(
		ctx,
		dbsqlc.LockProjectAppLifecycleExclusiveParams{AppID: input.AppID},
	); err != nil {
		return false, err
	}
	rows, err := q.DisconnectProjectApp(
		ctx,
		dbsqlc.DisconnectProjectAppParams{
			ProjectID:             input.ProjectID,
			ID:                    input.AppID,
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

func (s *Store) DeleteProjectApp(ctx context.Context, orgID, projectID, id uuid.UUID) error {
	if orgID == uuid.Nil || projectID == uuid.Nil || id == uuid.Nil {
		return storeerr.InvalidRequest(errors.New("organization, project and app are required"))
	}
	_, err := storeutil.RetryTransaction(ctx, "delete_project_app", func() (struct{}, error) {
		return struct{}{}, s.deleteProjectAppOnce(ctx, orgID, projectID, id)
	})
	return err
}

func (s *Store) deleteProjectAppOnce(ctx context.Context, orgID, projectID, id uuid.UUID) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lifecyclelock.EnterActiveProject(ctx, tx, orgID, projectID); err != nil {
		return err
	}
	q := dbsqlc.New(tx)
	// Freeze admission before enumerating agents; target creation uses this gate.
	if err := q.LockProjectAppLifecycleExclusive(
		ctx,
		dbsqlc.LockProjectAppLifecycleExclusiveParams{AppID: id},
	); err != nil {
		return err
	}
	if _, err := getProjectApp(ctx, q, projectID, id); err != nil {
		return err
	}
	ids, err := q.ListProjectAppAgentIDsForLifecycle(
		ctx,
		dbsqlc.ListProjectAppAgentIDsForLifecycleParams{ProjectID: projectID, AppID: id},
	)
	if err != nil {
		return err
	}
	refs := make([]lifecyclelock.AgentRef, 0, len(ids))
	for _, agentID := range ids {
		refs = append(refs, lifecyclelock.AgentRef{ProjectID: projectID, AgentID: agentID})
	}
	if err := lifecyclelock.Agents(ctx, tx, refs); err != nil {
		return err
	}
	if err := q.DeleteProjectAppSubscriptions(ctx, dbsqlc.DeleteProjectAppSubscriptionsParams{
		ProjectID: projectID, AppID: id,
	}); err != nil {
		return err
	}
	if err := s.access.ClearAppTargetsFromAgents(ctx, tx, projectID, id); err != nil {
		return err
	}
	if err := q.DeleteIntegrationTargets(
		ctx,
		dbsqlc.DeleteIntegrationTargetsParams{ProjectID: projectID, AppID: id},
	); err != nil {
		return err
	}
	if _, err := q.DeleteCronTriggersForApp(ctx, dbsqlc.DeleteCronTriggersForAppParams{
		ProjectID: projectID, AppID: &id,
	}); err != nil {
		return fmt.Errorf("delete app cron triggers: %w", err)
	}
	rows, err := q.DeleteProjectApp(ctx, dbsqlc.DeleteProjectAppParams{ProjectID: projectID, ID: id})
	if err != nil {
		return fmt.Errorf("delete app: %w", err)
	}
	if rows == 0 {
		return storeerr.ErrNotFound
	}
	return tx.Commit(ctx)
}
