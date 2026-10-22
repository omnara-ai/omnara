package executionstore

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
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
	Claim    ModelCallClaim
	Snapshot AgentConfigSnapshotRecord
}

func (s *Store) PrepareNormalModelCall(
	ctx context.Context,
	input PrepareNormalModelCallInput,
) (PreparedNormalModelCall, error) {
	if input.RuntimeLockID == uuid.Nil || len(input.OpeningInputIDs) == 0 {
		return PreparedNormalModelCall{}, errors.New("runtime and opening inputs are required")
	}
	return agentexecution.Retry(ctx,
		"prepare_normal_model_call",
		func(ctx context.Context) (PreparedNormalModelCall, error) {
			unit, _, err := beginExecution(ctx, s, input.ProjectID, input.AgentID, false, input.RuntimeLockID)
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
		})
}

func (s *Store) CheckPreparedModelCall(ctx context.Context, projectID, agentID, contextID,
	runtimeID uuid.UUID) (bool, error) {
	status, err := s.q.ReadModelCallEntry(ctx, dbsqlc.ReadModelCallEntryParams{
		ProjectID: projectID, AgentID: agentID, ContextID: contextID, RuntimeID: runtimeID,
	})
	if err != nil {
		return false, err
	}
	if !status.Executable {
		return false, storeerr.ErrAgentNotAdvanceable
	}
	return status.HasMcpConnections, nil
}
