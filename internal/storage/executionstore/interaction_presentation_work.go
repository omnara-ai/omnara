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

// MaxPendingInteractionPresentations bounds each recovery batch, also in SQL.
const MaxPendingInteractionPresentations = 100

type InteractionPresentationReference struct {
	ProjectID uuid.UUID
	AgentID   uuid.UUID
	ID        uuid.UUID
}

// ListPendingInteractionPresentations discovers unattempted captured interactions
// on live scopes. The presenter supplies its supported app types. No
// connection/config authority filter is applied: claiming revoked destinations
// must remove them from pending work before presentation revalidates authority.
func (s *Store) ListPendingInteractionPresentations(
	ctx context.Context, appTypes []string, limit int,
) ([]InteractionPresentationReference, error) {
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

// ClaimInteractionPresentation commits a single best-effort attempt before any
// provider I/O or authority validation. True is returned only after a definite
// commit; an ambiguous commit must never authorize sending. A crash can lose a
// mirror, but neither a retry nor another worker can start another attempt.
// This transaction takes no connection locks and ends before provider checks.
func (s *Store) ClaimInteractionPresentation(
	ctx context.Context, projectID, agentID, id uuid.UUID,
) (bool, error) {
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
