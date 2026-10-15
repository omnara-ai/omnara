package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func (s *Store) CountModelCallRetries(
	ctx context.Context,
	projectID, agentID, contextID uuid.UUID,
) (int, error) {
	return countModelCallRetriesTx(ctx, s.q, projectID, agentID, contextID)
}

func countModelCallRetriesTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID, contextID uuid.UUID,
) (int, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || contextID == uuid.Nil {
		return 0, errors.New("project, agent, and model context are required")
	}
	row, err := q.CountModelCallRetries(ctx, dbsqlc.CountModelCallRetriesParams{
		ProjectID: projectID, AgentID: agentID, ModelCallContextID: contextID,
	})
	if err != nil {
		return 0, fmt.Errorf("count model call retries: %w", err)
	}
	return int(row), nil
}
