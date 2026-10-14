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
	return agentexecution.Retry(ctx,
		"record_model_call_failure_and_claim_compaction",
		func(ctx context.Context) (TriggeredCompactionHandoff,
			error) {
			unit, h, err := beginExecution(ctx, s, f.ProjectID, f.AgentID, false, f.RuntimeLockID)
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
		})
}

func (s *Store) ReplaceCompactionSource(
	ctx context.Context,
	input ReplaceCompactionSourceInput,
) (ReplaceCompactionSourceResult, error) {
	return agentexecution.Retry(ctx,
		"replace_compaction_source",
		func(ctx context.Context) (ReplaceCompactionSourceResult,
			error) {
			unit, h, err := beginExecution(ctx, s, input.ProjectID, input.AgentID, false, input.RuntimeLockID)
			if err != nil {
				return ReplaceCompactionSourceResult{}, err
			}
			defer func() { _ = unit.Rollback(ctx) }()
			failure := executionFailureDetails(input.ModelCallContextID, input.RuntimeLockID,
				input.ErrorKind, input.ErrorCode, input.ErrorMessage, input.ErrorDetails,
				executionEvidence(input.APIFormat, input.APIVariant, input.ProviderRequestID, input.ProviderResponseID,
					input.Usage, input.ProviderReportedCostUSD, input.ProviderMetadata))

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
		})
}
