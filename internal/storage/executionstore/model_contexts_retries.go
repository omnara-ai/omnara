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
	if projectID == uuid.Nil || agentID == uuid.Nil || contextID == uuid.Nil {
		return 0, errors.New("project, agent, and model context are required")
	}
	row, err := s.q.CountModelCallRetries(ctx, dbsqlc.CountModelCallRetriesParams{
		ProjectID: projectID, AgentID: agentID, ModelCallContextID: contextID,
	})
	if err != nil {
		return 0, fmt.Errorf("count model call retries: %w", err)
	}
	return int(row), nil
}

func (s *Store) CountModelCallRetriesWithErrorCode(
	ctx context.Context,
	projectID, agentID, contextID uuid.UUID,
	errorCode string,
) (int, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || contextID == uuid.Nil {
		return 0, errors.New("project, agent, and model context are required")
	}
	if errorCode == "" {
		return 0, errors.New("error code is required")
	}
	row, err := s.q.CountModelCallRetriesWithErrorCode(ctx, dbsqlc.CountModelCallRetriesWithErrorCodeParams{
		ProjectID: projectID, AgentID: agentID, ModelCallContextID: contextID, ErrorCode: errorCode,
	})
	if err != nil {
		return 0, fmt.Errorf("count model call retries with error code: %w", err)
	}
	return int(row), nil
}
