package executionstore

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

type PrepareNormalModelCallInput struct {
	ProjectID                uuid.UUID
	AgentID                  uuid.UUID
	RuntimeLockID            uuid.UUID
	OpeningInputIDs          []uuid.UUID
	SourceModelCallContextID uuid.UUID
	SourceModelOutputID      uuid.UUID
}

type PreparedNormalModelCall struct {
	Claim       ModelCallClaim
	Snapshot    AgentConfigSnapshotRecord
	ContextData *ModelContextData
}

func (s *Store) PrepareNormalModelCall(
	ctx context.Context,
	input PrepareNormalModelCallInput,
) (PreparedNormalModelCall, error) {
	if input.RuntimeLockID == uuid.Nil || len(input.OpeningInputIDs) == 0 {
		return PreparedNormalModelCall{}, errors.New("runtime and opening inputs are required")
	}
	unit, _, err := beginExecution(ctx, s, input.ProjectID, input.AgentID)
	if err != nil {
		return PreparedNormalModelCall{}, err
	}
	defer func() { _ = unit.Rollback(ctx) }()
	result, err := prepareExecutionModel(ctx, unit, input)
	if err != nil {
		return PreparedNormalModelCall{}, err
	}
	if err = unit.Commit(ctx, "prepare model call"); err != nil {
		return PreparedNormalModelCall{}, err
	}
	return result, nil
}
