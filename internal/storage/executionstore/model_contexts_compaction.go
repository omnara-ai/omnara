package executionstore

import (
	"context"

	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) RecordModelCallFailureAndClaimCompaction(
	ctx context.Context,
	input RecordModelCallFailureAndClaimCompactionInput,
) (TriggeredCompactionHandoff, error) {
	f := input.Failure
	if input.ParentContextID != f.ModelCallContextID || f.RecoveryKind != ModelCallRecoveryCompact {
		return TriggeredCompactionHandoff{}, storeerr.ErrStateTransitionConflict
	}
	unit, h, err := beginExecution(ctx, s, f.ProjectID, f.AgentID)
	if err != nil {
		return TriggeredCompactionHandoff{}, err
	}
	defer func() { _ = unit.Rollback(ctx) }()
	result, err := h.BeginCompaction(
		ctx,
		agentexecution.BeginCompactionInput{
			Failure:   executionFailure(f),
			SourceEnd: input.SourceEventSequenceEnd,
		},
	)
	if err != nil {
		return TriggeredCompactionHandoff{}, err
	}
	if err := applyAdmissionDestination(ctx,
		unit,
		input.Failure.ProjectID,
		input.Failure.AgentID,
		result.Admission); err != nil {
		return TriggeredCompactionHandoff{}, err
	}
	parent, err := loadModelCallContextByID(
		ctx,
		dbsqlc.New(unit.DB()),
		f.ProjectID,
		f.AgentID,
		input.ParentContextID,
	)
	if err != nil {
		return TriggeredCompactionHandoff{}, err
	}
	claim, err := executionClaim(ctx, unit, f.ProjectID, f.AgentID, result.Model)
	if err != nil {
		return TriggeredCompactionHandoff{}, err
	}
	if err = unit.Commit(ctx, "begin compaction"); err != nil {
		return TriggeredCompactionHandoff{}, err
	}
	return TriggeredCompactionHandoff{
		ParentContext:     parent,
		CompactionCall:    claim,
		BoundaryPreempted: result.Preempted,
	}, nil
}

func (s *Store) ReplaceCompactionSource(
	ctx context.Context,
	input ReplaceCompactionSourceInput,
) (ReplaceCompactionSourceResult, error) {
	unit, h, err := beginExecution(ctx, s, input.ProjectID, input.AgentID)
	if err != nil {
		return ReplaceCompactionSourceResult{}, err
	}
	defer func() { _ = unit.Rollback(ctx) }()
	failure := agentexecution.ModelFailure{
		ContextID:     input.ModelCallContextID,
		RuntimeLockID: input.RuntimeLockID,
		ErrorKind:     input.ErrorKind,
		ErrorCode:     input.ErrorCode,
		ErrorMessage:  input.ErrorMessage,
		ErrorDetails:  input.ErrorDetails,
		Evidence: agentexecution.ModelEvidence{APIFormat: input.APIFormat, APIVariant: input.APIVariant,
			RequestID: input.ProviderRequestID, ResponseID: input.ProviderResponseID, Usage: input.Usage,
			Cost: input.ProviderReportedCostUSD, Metadata: input.ProviderMetadata},
	}
	result, err := h.BeginCompaction(
		ctx,
		agentexecution.BeginCompactionInput{Failure: failure, SourceEnd: input.NextSourceEventSequenceEnd},
	)
	if err != nil {
		return ReplaceCompactionSourceResult{}, err
	}
	if err := applyAdmissionDestination(ctx,
		unit,
		input.ProjectID,
		input.AgentID,
		result.Admission); err != nil {
		return ReplaceCompactionSourceResult{}, err
	}
	claim, err := executionClaim(ctx, unit, input.ProjectID, input.AgentID, result.Model)
	if err != nil {
		return ReplaceCompactionSourceResult{}, err
	}
	if err = unit.Commit(ctx, "reduce compaction source"); err != nil {
		return ReplaceCompactionSourceResult{}, err
	}
	return ReplaceCompactionSourceResult{CompactionCall: claim, BoundaryPreempted: result.Preempted}, nil
}
