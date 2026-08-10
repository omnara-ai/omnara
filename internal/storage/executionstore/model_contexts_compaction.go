package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) RecordModelCallFailureAndClaimCompaction(
	ctx context.Context,
	input RecordModelCallFailureAndClaimCompactionInput,
) (TriggeredCompactionHandoff, error) {
	failure := input.Failure
	if isNilID(input.ParentContextID) || input.SourceEventSequenceEnd <= 0 {
		return TriggeredCompactionHandoff{}, errors.New(
			"parent model context and a valid compaction source range are required",
		)
	}
	if failure.RecoveryKind != ModelCallRecoveryCompact {
		return TriggeredCompactionHandoff{}, errors.New("compaction recovery is required")
	}
	if err := validateRecoverableModelCallFailure(failure); err != nil {
		return TriggeredCompactionHandoff{}, err
	}
	var err error
	failure.ErrorDetails, err = normalizedJSONObject(failure.ErrorDetails, "model call error details")
	if err != nil {
		return TriggeredCompactionHandoff{}, err
	}
	if err := validateModelCallFailureEvidence(
		failure.APIFormat,
		failure.APIVariant,
		"",
		failure.ProviderRequestID,
		failure.ProviderResponseID,
		modelenvelope.NormalizeUsage(failure.Usage) != (modelenvelope.Usage{}),
		failure.ProviderReportedCostUSD,
	); err != nil {
		return TriggeredCompactionHandoff{}, err
	}

	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return TriggeredCompactionHandoff{}, fmt.Errorf("begin triggered compaction handoff: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	if err := ensureRuntimeLockActiveTx(
		ctx,
		tx,
		failure.ProjectID,
		failure.AgentID,
		failure.RuntimeLockID,
	); err != nil {
		return TriggeredCompactionHandoff{}, err
	}

	parent, err := loadModelCallContextByID(
		ctx,
		q,
		failure.ProjectID,
		failure.AgentID,
		input.ParentContextID,
	)
	if err != nil {
		return TriggeredCompactionHandoff{}, fmt.Errorf("load overflowing model context: %w", err)
	}
	if parent.OperationKind != ModelCallOperationNormal || parent.State != ModelCallContextStarted ||
		parent.RuntimeLockID != failure.RuntimeLockID ||
		input.SourceEventSequenceEnd > parent.InputEventSequence {
		return TriggeredCompactionHandoff{}, storeerr.ErrStateTransitionConflict
	}
	if failure.ModelCallContextID != parent.ID {
		return TriggeredCompactionHandoff{}, storeerr.ErrStateTransitionConflict
	}

	summarizedThrough := int64(0)
	checkpoint, checkpointErr := q.GetLatestApplicableContextCheckpoint(
		ctx,
		dbsqlc.GetLatestApplicableContextCheckpointParams{
			ProjectID:        failure.ProjectID,
			AgentID:          failure.AgentID,
			MaxEventSequence: parent.InputEventSequence,
		},
	)
	if checkpointErr == nil {
		summarizedThrough = checkpoint.SummarizedThroughEventSequence
	} else if !errors.Is(checkpointErr, pgx.ErrNoRows) {
		return TriggeredCompactionHandoff{}, fmt.Errorf(
			"load prior checkpoint for triggered compaction: %w",
			checkpointErr,
		)
	}
	if input.SourceEventSequenceEnd <= summarizedThrough {
		return TriggeredCompactionHandoff{}, storeerr.ErrStateTransitionConflict
	}

	parentContext, err := finishModelCallContextTx(ctx, q, finishModelCallContextInput{
		ProjectID:               failure.ProjectID,
		AgentID:                 failure.AgentID,
		ModelCallContextID:      failure.ModelCallContextID,
		RuntimeLockID:           failure.RuntimeLockID,
		ToState:                 ModelCallContextFailed,
		RecoveryKind:            ModelCallRecoveryCompact,
		APIFormat:               failure.APIFormat,
		APIVariant:              failure.APIVariant,
		ProviderRequestID:       failure.ProviderRequestID,
		ProviderResponseID:      failure.ProviderResponseID,
		ErrorKind:               failure.ErrorKind,
		ErrorCode:               failure.ErrorCode,
		ErrorMessage:            failure.ErrorMessage,
		ErrorDetails:            failure.ErrorDetails,
		Usage:                   failure.Usage,
		ProviderReportedCostUSD: failure.ProviderReportedCostUSD,
	})
	if err != nil {
		return TriggeredCompactionHandoff{}, err
	}
	boundaryPreempted, err := supersedeUnacceptedAtLaterModelReadyBoundaryTx(
		ctx,
		txNotifications,
		tx,
		q,
		failure.ProjectID,
		failure.AgentID,
		parent.ID,
		"triggered_compaction_preempted",
	)
	if err != nil {
		return TriggeredCompactionHandoff{}, err
	}
	if boundaryPreempted {
		if err := s.commitTxWithNotifications(
			ctx,
			tx,
			txNotifications,
			"preempted triggered compaction handoff",
		); err != nil {
			return TriggeredCompactionHandoff{}, err
		}
		return TriggeredCompactionHandoff{
			ParentContext:     parentContext,
			BoundaryPreempted: true,
		}, nil
	}
	compactionContext, created, err := claimCompactionContextTx(ctx, q, ClaimCompactionModelCallInput{
		ProjectID:              failure.ProjectID,
		AgentID:                failure.AgentID,
		RuntimeLockID:          failure.RuntimeLockID,
		InputEventSequence:     parent.InputEventSequence,
		SourceEventSequenceEnd: input.SourceEventSequenceEnd,
		ParentContextID:        parent.ID,
	})
	if err != nil {
		return TriggeredCompactionHandoff{}, err
	}
	if !created {
		return TriggeredCompactionHandoff{}, fmt.Errorf(
			"triggered compaction context already exists: %w",
			storeerr.ErrStateTransitionConflict,
		)
	}
	if compactionContext.State != ModelCallContextStarted ||
		compactionContext.RuntimeLockID != failure.RuntimeLockID {
		return TriggeredCompactionHandoff{}, errors.New("triggered compaction context was not runtime-owned")
	}
	if err := q.ReconcileAgentWakeup(ctx, dbsqlc.ReconcileAgentWakeupParams{
		ProjectID: failure.ProjectID,
		AgentID:   failure.AgentID,
		Metadata:  json.RawMessage(`{"reason":"triggered_compaction"}`),
	}); err != nil {
		return TriggeredCompactionHandoff{}, fmt.Errorf(
			"reconcile wakeup after triggered compaction: %w",
			err,
		)
	}
	if err := s.commitTxWithNotifications(
		ctx,
		tx,
		txNotifications,
		"triggered compaction handoff",
	); err != nil {
		return TriggeredCompactionHandoff{}, err
	}
	return TriggeredCompactionHandoff{
		ParentContext: parentContext,
		CompactionCall: ModelCallClaim{
			Context: compactionContext,
			Created: true,
			Claimed: true,
		},
	}, nil
}

func (s *Store) ReplaceCompactionSource(
	ctx context.Context,
	input ReplaceCompactionSourceInput,
) (ReplaceCompactionSourceResult, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.RuntimeLockID) ||
		isNilID(input.ModelCallContextID) ||
		input.ErrorKind == "" || input.ErrorMessage == "" || input.NextSourceEventSequenceEnd <= 0 {
		return ReplaceCompactionSourceResult{}, errors.New(
			"project, agent, runtime, compaction context, error, and replacement source end are required",
		)
	}
	errorDetails, err := normalizedJSONObject(input.ErrorDetails, "compaction replacement error details")
	if err != nil {
		return ReplaceCompactionSourceResult{}, err
	}
	input.ErrorDetails = errorDetails
	input.Usage = modelUsageForStorage(input.Usage)
	if err := validateModelCallFailureEvidence(
		input.APIFormat,
		input.APIVariant,
		"",
		input.ProviderRequestID,
		input.ProviderResponseID,
		input.Usage != (modelenvelope.Usage{}),
		input.ProviderReportedCostUSD,
	); err != nil {
		return ReplaceCompactionSourceResult{}, err
	}

	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ReplaceCompactionSourceResult{}, fmt.Errorf("begin compaction source replacement: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	if err := ensureRuntimeLockActiveTx(
		ctx,
		tx,
		input.ProjectID,
		input.AgentID,
		input.RuntimeLockID,
	); err != nil {
		return ReplaceCompactionSourceResult{}, err
	}
	contextRow, err := loadModelCallContextByID(
		ctx,
		q,
		input.ProjectID,
		input.AgentID,
		input.ModelCallContextID,
	)
	if err != nil {
		return ReplaceCompactionSourceResult{}, fmt.Errorf("load compaction context for smaller source: %w", err)
	}
	if contextRow.OperationKind != ModelCallOperationCompaction ||
		contextRow.State != ModelCallContextStarted ||
		contextRow.RuntimeLockID != input.RuntimeLockID ||
		contextRow.SourceEventSequenceEnd == nil ||
		input.NextSourceEventSequenceEnd >= *contextRow.SourceEventSequenceEnd {
		return ReplaceCompactionSourceResult{}, storeerr.ErrStateTransitionConflict
	}
	sourceStart, err := compactionSourceStartTx(ctx, q, contextRow)
	if err != nil {
		return ReplaceCompactionSourceResult{}, err
	}
	if input.NextSourceEventSequenceEnd < sourceStart {
		return ReplaceCompactionSourceResult{}, storeerr.ErrStateTransitionConflict
	}
	if _, err := finishModelCallContextTx(ctx, q, finishModelCallContextInput{
		ProjectID:               input.ProjectID,
		AgentID:                 input.AgentID,
		ModelCallContextID:      input.ModelCallContextID,
		RuntimeLockID:           input.RuntimeLockID,
		ToState:                 ModelCallContextFailed,
		RecoveryKind:            ModelCallRecoveryReduceCompactionSource,
		APIFormat:               input.APIFormat,
		APIVariant:              input.APIVariant,
		ProviderRequestID:       input.ProviderRequestID,
		ProviderResponseID:      input.ProviderResponseID,
		ErrorKind:               input.ErrorKind,
		ErrorCode:               input.ErrorCode,
		ErrorMessage:            input.ErrorMessage,
		ErrorDetails:            input.ErrorDetails,
		Usage:                   input.Usage,
		ProviderReportedCostUSD: input.ProviderReportedCostUSD,
	}); err != nil {
		return ReplaceCompactionSourceResult{}, err
	}
	boundaryPreempted, err := supersedeUnacceptedAtLaterModelReadyBoundaryTx(
		ctx,
		txNotifications,
		tx,
		q,
		input.ProjectID,
		input.AgentID,
		contextRow.ID,
		"compaction_source_preempted",
	)
	if err != nil {
		return ReplaceCompactionSourceResult{}, err
	}
	if boundaryPreempted {
		if err := s.commitTxWithNotifications(
			ctx,
			tx,
			txNotifications,
			"preempted compaction source replacement",
		); err != nil {
			return ReplaceCompactionSourceResult{}, err
		}
		return ReplaceCompactionSourceResult{BoundaryPreempted: true}, nil
	}

	parentID, err := q.GetNormalModelCallContextByIdentity(
		ctx,
		dbsqlc.GetNormalModelCallContextByIdentityParams{
			ProjectID:          contextRow.ProjectID,
			AgentID:            contextRow.AgentID,
			InputEventSequence: contextRow.InputEventSequence,
		},
	)
	if err != nil {
		return ReplaceCompactionSourceResult{}, fmt.Errorf("load parent normal context for replacement compaction: %w", err)
	}
	nextContext, created, err := claimCompactionContextTx(ctx, q, ClaimCompactionModelCallInput{
		ProjectID:              contextRow.ProjectID,
		AgentID:                contextRow.AgentID,
		RuntimeLockID:          input.RuntimeLockID,
		InputEventSequence:     contextRow.InputEventSequence,
		SourceEventSequenceEnd: input.NextSourceEventSequenceEnd,
		ParentContextID:        parentID,
	})
	if err != nil {
		return ReplaceCompactionSourceResult{}, err
	}
	if !created {
		return ReplaceCompactionSourceResult{}, fmt.Errorf(
			"replacement compaction context already exists: %w",
			storeerr.ErrStateTransitionConflict,
		)
	}
	if nextContext.State != ModelCallContextStarted ||
		nextContext.RuntimeLockID != input.RuntimeLockID {
		return ReplaceCompactionSourceResult{}, errors.New("replacement compaction context was not runtime-owned")
	}
	if err := s.commitTxWithNotifications(
		ctx,
		tx,
		txNotifications,
		"compaction source replacement",
	); err != nil {
		return ReplaceCompactionSourceResult{}, err
	}
	return ReplaceCompactionSourceResult{CompactionCall: ModelCallClaim{
		Context: nextContext,
		Created: true,
		Claimed: true,
	}}, nil
}
