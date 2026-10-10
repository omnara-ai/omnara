package executionstore

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type PublishContextCheckpointInput struct {
	ProjectID               uuid.UUID
	AgentID                 uuid.UUID
	RuntimeLockID           uuid.UUID
	ModelCallContextID      uuid.UUID
	Summary                 string
	APIFormat               modelprotocol.APIFormat
	APIVariant              modelprotocol.APIVariant
	ProviderRequestID       string
	ProviderResponseID      string
	Usage                   modelenvelope.Usage
	ProviderReportedCostUSD modelenvelope.ProviderReportedCostUSD
	ProviderMetadata        modelenvelope.ProviderMetadata
}

func (s *Store) PublishContextCheckpoint(
	ctx context.Context,
	input PublishContextCheckpointInput,
) (ContextCheckpointRecord, error) {
	unit, h, err := beginExecution(ctx, s, input.ProjectID, input.AgentID)
	if err != nil {
		return ContextCheckpointRecord{}, err
	}
	defer func() { _ = unit.Rollback(ctx) }()
	result, err := h.PublishCheckpoint(
		ctx,
		agentexecution.PublishCheckpointInput{RuntimeLockID: input.RuntimeLockID,
			ContextID: input.ModelCallContextID, Summary: input.Summary, Evidence: agentexecution.ModelEvidence{
				APIFormat: input.APIFormat, APIVariant: input.APIVariant, RequestID: input.ProviderRequestID,
				ResponseID: input.ProviderResponseID,
				Usage:      input.Usage,
				Cost:       input.ProviderReportedCostUSD,
				Metadata:   input.ProviderMetadata}},
	)
	if err != nil {
		return ContextCheckpointRecord{}, err
	}
	if !result.Created {
		return ContextCheckpointRecord{}, storeerr.ErrStateTransitionConflict
	}
	row, err := dbsqlc.New(unit.DB()).
		GetContextCheckpoint(ctx,
			dbsqlc.GetContextCheckpointParams{ProjectID: input.ProjectID,
				AgentID: input.AgentID,
				ID:      result.ID})
	if err != nil {
		return ContextCheckpointRecord{}, err
	}
	if err = unit.Commit(ctx, "publish context checkpoint"); err != nil {
		return ContextCheckpointRecord{}, err
	}
	return contextCheckpointRecordFromGetSQLC(row), nil
}
