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
	return agentexecution.Retry(ctx,
		"publish_context_checkpoint",
		func(ctx context.Context) (ContextCheckpointRecord,
			error) {
			unit, h, err := beginExecution(ctx, s, input.ProjectID, input.AgentID, false, input.RuntimeLockID)
			if err != nil {
				return ContextCheckpointRecord{}, err
			}
			defer func() { _ = unit.Rollback(ctx) }()
			result, err := h.PublishCheckpoint(
				ctx,
				agentexecution.PublishCheckpointInput{RuntimeLockID: input.RuntimeLockID,
					ContextID: input.ModelCallContextID, Summary: input.Summary,
					Evidence: executionEvidence(input.APIFormat, input.APIVariant,
						input.ProviderRequestID, input.ProviderResponseID, input.Usage,
						input.ProviderReportedCostUSD, input.ProviderMetadata)},
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
		})
}
