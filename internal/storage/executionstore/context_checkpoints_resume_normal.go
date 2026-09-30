package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type RecordCompactionFailureAndResumeNormalInput struct {
	Outcome                 OptionalCompactionOutcome
	ProjectID               uuid.UUID
	AgentID                 uuid.UUID
	RuntimeLockID           uuid.UUID
	ModelCallContextID      uuid.UUID
	APIFormat               modelprotocol.APIFormat
	APIVariant              modelprotocol.APIVariant
	ProviderRequestID       string
	ProviderResponseID      string
	ErrorKind               modelprotocol.ErrorKind
	ErrorCode               string
	ErrorMessage            string
	ErrorDetails            json.RawMessage
	Usage                   modelenvelope.Usage
	ProviderReportedCostUSD modelenvelope.ProviderReportedCostUSD
	ProviderMetadata        modelenvelope.ProviderMetadata
}

func (s *Store) RecordCompactionFailureAndResumeNormal(
	ctx context.Context,
	input RecordCompactionFailureAndResumeNormalInput,
) (ModelCallContextRecord, error) {
	if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil || input.RuntimeLockID == uuid.Nil ||
		input.ModelCallContextID == uuid.Nil || input.ErrorKind == "" || input.ErrorMessage == "" {
		return ModelCallContextRecord{}, errors.New(
			"project, agent, runtime, compaction context, and error are required",
		)
	}
	var err error
	input.ErrorDetails, err = normalizedJSONObject(input.ErrorDetails, "compaction error details")
	if err != nil {
		return ModelCallContextRecord{}, err
	}
	input.Usage = modelUsageForStorage(input.Usage)
	if err := validateModelCallFailureEvidence(
		input.APIFormat, input.APIVariant, "",
		input.ProviderRequestID, input.ProviderResponseID,
		input.Usage != (modelenvelope.Usage{}), input.ProviderReportedCostUSD,
	); err != nil {
		return ModelCallContextRecord{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ModelCallContextRecord{}, fmt.Errorf("begin compaction failure: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	if err := ensureRuntimeLockActiveTx(ctx, tx, input.ProjectID, input.AgentID, input.RuntimeLockID); err != nil {
		return ModelCallContextRecord{}, err
	}
	record, err := recordCompactionFailureAndResumeNormalTx(ctx, q, input, modelCallContextRuntimeOwned)
	if err != nil {
		return ModelCallContextRecord{}, err
	}
	if err := q.ReconcileAgentWakeup(ctx, dbsqlc.ReconcileAgentWakeupParams{
		ProjectID: input.ProjectID,
		AgentID:   input.AgentID,
		Metadata: json.RawMessage(
			`{"reason":"compaction_resumed_normal"}`,
		),
	}); err != nil {
		return ModelCallContextRecord{}, fmt.Errorf("reconcile compaction wakeup: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ModelCallContextRecord{}, fmt.Errorf("commit compaction failure: %w", err)
	}
	return record, nil
}

func recordCompactionFailureAndResumeNormalTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	input RecordCompactionFailureAndResumeNormalInput,
	authority modelCallContextRuntimeAuthority,
) (ModelCallContextRecord, error) {
	current, err := loadModelCallContextByID(ctx, q, input.ProjectID, input.AgentID, input.ModelCallContextID)
	if err != nil {
		return ModelCallContextRecord{}, err
	}
	if current.OperationKind != ModelCallOperationCompaction || current.RuntimeLockID != input.RuntimeLockID ||
		current.ParentNormalModelCallContextID == uuid.Nil {
		return ModelCallContextRecord{}, storeerr.ErrStateTransitionConflict
	}
	parent, err := loadModelCallContextByID(
		ctx,
		q,
		input.ProjectID,
		input.AgentID,
		current.ParentNormalModelCallContextID,
	)
	if err != nil {
		return ModelCallContextRecord{}, err
	}
	if parent.RecoveryKind == ModelCallRecoveryCompactOptional {
		if input.Outcome != OptionalCompactionInterrupted && input.Outcome != OptionalCompactionIneffective {
			return ModelCallContextRecord{}, errors.New("a valid optional compaction outcome is required")
		}
	} else if current.ReplacesCheckpointID == uuid.Nil || input.Outcome != "" {
		return ModelCallContextRecord{}, storeerr.ErrStateTransitionConflict
	}
	input.ErrorDetails = normalizedJSONOrObject(input.ErrorDetails)
	input.Usage = modelUsageForStorage(input.Usage)
	metadata, err := json.Marshal(input.ProviderMetadata)
	if err != nil {
		return ModelCallContextRecord{}, err
	}
	input.ProviderMetadata = providerMetadataFromSQLC(metadata)
	if current.State == ModelCallContextFailed && current.RecoveryKind == ModelCallRecoveryResumeNormal {
		if current.OptionalCompactionOutcome != input.Outcome ||
			current.APIFormat != input.APIFormat || current.APIVariant != input.APIVariant ||
			current.ProviderRequestID != input.ProviderRequestID || current.ProviderResponseID != input.ProviderResponseID ||
			current.ErrorKind != input.ErrorKind || current.ErrorCode != input.ErrorCode ||
			current.ErrorMessage != input.ErrorMessage ||
			!jsoncanonical.Equal(current.ErrorDetails, input.ErrorDetails) || current.Usage != input.Usage ||
			current.ProviderReportedCostUSD != input.ProviderReportedCostUSD ||
			current.ProviderMetadata != input.ProviderMetadata {
			return ModelCallContextRecord{}, storeerr.ErrIdempotencyConflict
		}
		return current, nil
	}
	if current.State != ModelCallContextStarted {
		return ModelCallContextRecord{}, storeerr.ErrStateTransitionConflict
	}
	return finishModelCallContextWithAuthorityTx(ctx, q, finishModelCallContextInput{
		ProjectID:                 input.ProjectID,
		AgentID:                   input.AgentID,
		ModelCallContextID:        input.ModelCallContextID,
		RuntimeLockID:             input.RuntimeLockID,
		ToState:                   ModelCallContextFailed,
		RecoveryKind:              ModelCallRecoveryResumeNormal,
		OptionalCompactionOutcome: input.Outcome,
		APIFormat:                 input.APIFormat,
		APIVariant:                input.APIVariant,
		ProviderRequestID:         input.ProviderRequestID,
		ProviderResponseID:        input.ProviderResponseID,
		ErrorKind:                 input.ErrorKind,
		ErrorCode:                 input.ErrorCode,
		ErrorMessage:              input.ErrorMessage,
		ErrorDetails:              input.ErrorDetails,
		Usage:                     input.Usage,
		ProviderReportedCostUSD:   input.ProviderReportedCostUSD,
		ProviderMetadata:          input.ProviderMetadata,
	}, authority)
}
