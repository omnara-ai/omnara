package executionstore

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func beginExecution(
	ctx context.Context,
	s *Store,
	projectID, agentID uuid.UUID,
) (*agentexecution.Unit, *agentexecution.Handle, error) {
	unit, err := s.cell.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	if err = lockAgentWithParentTx(ctx, unit, dbsqlc.New(unit.DB()), projectID, agentID); err != nil {
		_ = unit.Rollback(ctx)
		return nil, nil, err
	}
	h, err := unit.Handle(projectID, agentID)
	if err != nil {
		_ = unit.Rollback(ctx)
		return nil, nil, err
	}
	return unit, h, nil
}
