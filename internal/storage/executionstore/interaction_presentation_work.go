package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const MaxPendingInteractionPresentations = 100

type InteractionPresentationReference struct {
	ProjectID uuid.UUID
	AgentID   uuid.UUID
	ID        uuid.UUID
}

func (s *Store) ListPendingInteractionPresentations(
	ctx context.Context, appTypes []string, limit int,
) ([]InteractionPresentationReference, error) {
	// Revoked destinations must still be claimed to drain pending work;
	// presentation checks live authority afterward.
	if limit <= 0 {
		return nil, storeerr.InvalidRequest(errors.New("presentation limit must be positive"))
	}
	limit = min(limit, MaxPendingInteractionPresentations)
	rows, err := s.q.ListPendingInteractionPresentations(ctx, dbsqlc.ListPendingInteractionPresentationsParams{
		AppTypes: appTypes, BatchLimit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list pending interaction presentations: %w", err)
	}
	result := make([]InteractionPresentationReference, 0, len(rows))
	for _, row := range rows {
		result = append(result, InteractionPresentationReference{
			ProjectID: row.ProjectID, AgentID: row.AgentID, ID: row.ID,
		})
	}
	return result, nil
}

func (s *Store) ClaimInteractionPresentation(
	ctx context.Context, projectID, agentID, id uuid.UUID,
) (bool, error) {
	// Commit before provider I/O to prevent duplicate sends after an ambiguous
	// outcome. A crash between commit and send can lose the mirror.
	if projectID == uuid.Nil || agentID == uuid.Nil || id == uuid.Nil {
		return false, storeerr.InvalidRequest(errors.New("project, agent and interaction are required"))
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	project, err := loadProjectTx(ctx, q, projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := lifecyclelock.EnterActiveProject(ctx, tx, project.OrgID, projectID); err != nil {
		if errors.Is(err, storeerr.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	if _, err := q.LockAgentInProject(ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: projectID, ID: agentID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	rows, err := q.ClaimInteractionPresentation(ctx, dbsqlc.ClaimInteractionPresentationParams{
		ProjectID: projectID, AgentID: agentID, ID: id,
	})
	if err != nil {
		return false, fmt.Errorf("claim interaction presentation: %w", err)
	}
	if rows == 0 {
		return false, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit interaction presentation attempt: %w", err)
	}
	return true, nil
}
