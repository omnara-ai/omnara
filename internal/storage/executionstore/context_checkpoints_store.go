package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type PublishContextCheckpointInput struct {
	ProjectID               ID
	AgentID                 ID
	RuntimeLockID           ID
	ModelCallContextID      ID
	Summary                 string
	APIFormat               modelprotocol.APIFormat
	APIVariant              modelprotocol.APIVariant
	ProviderRequestID       string
	ProviderResponseID      string
	Usage                   modelenvelope.Usage
	ProviderReportedCostUSD modelenvelope.ProviderReportedCostUSD
}

func (s *Store) PublishContextCheckpoint(
	ctx context.Context,
	input PublishContextCheckpointInput,
) (ContextCheckpointRecord, error) {
	if err := validatePublishContextCheckpointInput(input); err != nil {
		return ContextCheckpointRecord{}, err
	}
	input.Usage = modelUsageForStorage(input.Usage)
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ContextCheckpointRecord{}, fmt.Errorf("begin publish context checkpoint: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	if _, err := q.LockAgentInProject(ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: input.ProjectID,
		ID:        input.AgentID,
	}); err != nil {
		return ContextCheckpointRecord{}, fmt.Errorf("lock agent for context checkpoint: %w", err)
	}
	if err := ensureRuntimeLockActiveTx(
		ctx,
		tx,
		input.ProjectID,
		input.AgentID,
		input.RuntimeLockID,
	); err != nil {
		return ContextCheckpointRecord{}, err
	}
	contextRow, err := loadModelCallContextByID(
		ctx,
		q,
		input.ProjectID,
		input.AgentID,
		input.ModelCallContextID,
	)
	if err != nil {
		return ContextCheckpointRecord{}, fmt.Errorf("load compaction context: %w", err)
	}
	if contextRow.OperationKind != ModelCallOperationCompaction ||
		contextRow.State != ModelCallContextStarted ||
		contextRow.RuntimeLockID != input.RuntimeLockID ||
		contextRow.SourceEventSequenceEnd == nil {
		return ContextCheckpointRecord{}, storeerr.ErrStateTransitionConflict
	}
	sourceStart, err := compactionSourceStartTx(ctx, q, contextRow)
	if err != nil {
		return ContextCheckpointRecord{}, err
	}
	if err := validateClosedCheckpointRangeTx(
		ctx,
		tx,
		input.ProjectID,
		input.AgentID,
		sourceStart,
		*contextRow.SourceEventSequenceEnd,
	); err != nil {
		return ContextCheckpointRecord{}, err
	}
	if err := validateCheckpointDoesNotCutOpenAuthoritiesTx(
		ctx,
		tx,
		input.ProjectID,
		input.AgentID,
		sourceStart,
		*contextRow.SourceEventSequenceEnd,
	); err != nil {
		return ContextCheckpointRecord{}, err
	}

	checkpointID, err := uuid.NewV7()
	if err != nil {
		return ContextCheckpointRecord{}, fmt.Errorf("generate checkpoint id: %w", err)
	}
	checkpointEventID, err := uuid.NewV7()
	if err != nil {
		return ContextCheckpointRecord{}, fmt.Errorf("generate checkpoint event id: %w", err)
	}
	turnID, err := q.GetModelCallContextTurnID(ctx, dbsqlc.GetModelCallContextTurnIDParams{
		ProjectID:          input.ProjectID,
		AgentID:            input.AgentID,
		ModelCallContextID: input.ModelCallContextID,
	})
	if err != nil {
		return ContextCheckpointRecord{}, fmt.Errorf("load checkpoint turn: %w", err)
	}
	_, err = q.InsertContextCheckpoint(ctx, dbsqlc.InsertContextCheckpointParams{
		ID:                         checkpointID,
		Summary:                    input.Summary,
		ProducerModelCallContextID: input.ModelCallContextID,
		ProjectID:                  input.ProjectID,
		AgentID:                    input.AgentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ContextCheckpointRecord{}, storeerr.ErrRuntimeLockInactive
	}
	if err != nil {
		return ContextCheckpointRecord{}, fmt.Errorf("insert context checkpoint: %w", err)
	}
	eventRecord, err := appendTypedAgentEventTx(
		ctx,
		txNotifications,
		tx,
		AppendTypedAgentEventInput{
			ID:                  checkpointEventID,
			ProjectID:           input.ProjectID,
			AgentID:             input.AgentID,
			TurnID:              turnID,
			Kind:                events.KindContextCheckpoint,
			ContextCheckpointID: checkpointID,
		},
	)
	if err != nil {
		return ContextCheckpointRecord{}, err
	}
	if err := updateAgentTurnLatestEventTx(
		ctx,
		tx,
		input.ProjectID,
		input.AgentID,
		turnID,
		eventRecord.Event.ID,
		NilID,
	); err != nil {
		return ContextCheckpointRecord{}, err
	}
	if _, err := finishModelCallContextTx(ctx, q, finishModelCallContextInput{
		ProjectID:               input.ProjectID,
		AgentID:                 input.AgentID,
		ModelCallContextID:      input.ModelCallContextID,
		RuntimeLockID:           input.RuntimeLockID,
		ToState:                 ModelCallContextSucceeded,
		APIFormat:               input.APIFormat,
		APIVariant:              input.APIVariant,
		ProviderRequestID:       input.ProviderRequestID,
		ProviderResponseID:      input.ProviderResponseID,
		Usage:                   input.Usage,
		ProviderReportedCostUSD: input.ProviderReportedCostUSD,
	}); err != nil {
		return ContextCheckpointRecord{}, err
	}
	if err := q.ReconcileAgentWakeup(ctx, dbsqlc.ReconcileAgentWakeupParams{
		Metadata:  json.RawMessage(`{"reason":"context_checkpoint"}`),
		ProjectID: input.ProjectID,
		AgentID:   input.AgentID,
	}); err != nil {
		return ContextCheckpointRecord{}, fmt.Errorf("reconcile wakeup after context checkpoint: %w", err)
	}
	record, found, err := getContextCheckpointByProducerContextTx(
		ctx,
		q,
		input.ProjectID,
		input.AgentID,
		input.ModelCallContextID,
	)
	if err != nil {
		return ContextCheckpointRecord{}, err
	}
	if !found {
		return ContextCheckpointRecord{}, storeerr.ErrStateTransitionConflict
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "publish context checkpoint"); err != nil {
		return ContextCheckpointRecord{}, err
	}
	return record, nil
}

func compactionSourceStartTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	contextRow ModelCallContextRecord,
) (int64, error) {
	if contextRow.OperationKind != ModelCallOperationCompaction {
		return 0, storeerr.ErrStateTransitionConflict
	}
	checkpoint, err := q.GetLatestApplicableContextCheckpoint(
		ctx,
		dbsqlc.GetLatestApplicableContextCheckpointParams{
			ProjectID:        contextRow.ProjectID,
			AgentID:          contextRow.AgentID,
			MaxEventSequence: contextRow.InputEventSequence,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return 1, nil
	}
	if err != nil {
		return 0, fmt.Errorf("load prior compaction checkpoint: %w", err)
	}
	return checkpoint.SummarizedThroughEventSequence + 1, nil
}

func validatePublishContextCheckpointInput(input PublishContextCheckpointInput) error {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.RuntimeLockID) ||
		isNilID(input.ModelCallContextID) || input.Summary == "" {
		return errors.New("project, agent, runtime, context, and summary are required")
	}
	if err := modelenvelope.ValidateProviderReportedCostUSD(input.ProviderReportedCostUSD); err != nil {
		return fmt.Errorf("provider-reported cost: %w", err)
	}
	return nil
}
