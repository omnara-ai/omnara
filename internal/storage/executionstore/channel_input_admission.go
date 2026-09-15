package executionstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ChannelEventLease struct {
	ReceiptID       uuid.UUID
	LeaseToken      uuid.UUID
	LeaseGeneration int64
}

type ChannelInputResult struct {
	CreatedAgent           bool
	ChannelID              uuid.UUID
	BindingID              uuid.UUID
	AgentInput             AgentInputRecord
	ContentBlocks          json.RawMessage
	CreatedInput           bool
	CanceledInteractionIDs []uuid.UUID
}

// A replay reads the accepted immutable input; it neither recreates grants nor
// needs to reconstruct the agent's current configuration.
func channelInputReplayResult(
	ctx context.Context, q *dbsqlc.Queries, input AgentInputRecord,
) (ChannelInputResult, error) {
	blocks, err := agentInputContentBlocks(ctx, q, input.ProjectID, input.AgentID, []uuid.UUID{input.ID})
	if err != nil {
		return ChannelInputResult{}, err
	}
	return ChannelInputResult{
		AgentInput: input, ContentBlocks: blocks[input.ID],
		ChannelID: input.IntegrationTargetID, BindingID: input.IntegrationTargetBindingID,
	}, nil
}

func (s *Store) recordChannelInputOutcomeTx(
	ctx context.Context, tx pgx.Tx, q *dbsqlc.Queries,
	key integrationstore.IntegrationEventOutcomeKey, lease ChannelEventLease, result ChannelInputResult,
) error {
	if err := s.integrations.CreateIntegrationEventOutcomeTx(ctx, tx, key,
		integrationstore.IntegrationEventOutcome{AgentID: result.AgentInput.AgentID, AgentInputID: result.AgentInput.ID},
	); err != nil {
		return err
	}
	return checkChannelEventLease(ctx, q, key.ProjectID, key.IntegrationInstallID, lease)
}

func checkChannelEventLease(
	ctx context.Context, q *dbsqlc.Queries, projectID, installID uuid.UUID, lease ChannelEventLease,
) error {
	// A row lock fences replacement owners, but cannot stop time passing. Check
	// the lease again immediately before committing the admission outcome.
	_, err := q.LockIntegrationEventForExecution(ctx, dbsqlc.LockIntegrationEventForExecutionParams{
		ProjectID: projectID, IntegrationInstallID: installID,
		ID: lease.ReceiptID, LeaseToken: &lease.LeaseToken, LeaseGeneration: lease.LeaseGeneration,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrStateTransitionConflict
	}
	return err
}
