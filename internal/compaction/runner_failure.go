package compaction

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/modelretry"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type compactionFailureReason uint8

const (
	compactionFailureSummaryNotReduced compactionFailureReason = iota + 1
	compactionFailureSourceIrreducible
	compactionFailureSummaryTruncated
	compactionFailureSummaryInvalid
)

const (
	compactionErrorCodeBuildModelSelectionFailed = "build_compaction_model_selection_failed"
	compactionErrorCodeResolveModelFailed        = "resolve_compaction_model_failed"
	compactionErrorCodeLoadReplayPolicyFailed    = "load_compaction_replay_policy_failed"
	compactionErrorCodeLoadSourceFailed          = "load_compaction_source_failed"
	compactionErrorCodePrepareRequestFailed      = "prepare_compaction_request_failed"
	compactionErrorCodeSummaryTruncated          = "summary_truncated"
	compactionErrorCodeSummaryNotReduced         = "summary_not_reduced"
	compactionErrorCodeSourceIrreducible         = "compaction_source_irreducible"
)

// Reserve up to this much output so summaries can preserve long-running agent state.
const preferredSummaryOutputTokens = 16_384

type compactionFailureError struct {
	reason compactionFailureReason
	cause  error
}

func (e *compactionFailureError) Error() string {
	return e.cause.Error()
}

func (e *compactionFailureError) Unwrap() error {
	return e.cause
}

func withCompactionFailureReason(reason compactionFailureReason, cause error) error {
	return &compactionFailureError{reason: reason, cause: cause}
}

func reasonForCompactionFailure(err error) (compactionFailureReason, bool) {
	var failure *compactionFailureError
	if !errors.As(err, &failure) {
		return 0, false
	}
	return failure.reason, true
}

func isIrreducibleCompactionFailure(err error) bool {
	reason, ok := reasonForCompactionFailure(err)
	return ok && reason == compactionFailureSourceIrreducible
}

type providerAttemptEvidence struct {
	APIFormat              modelprotocol.APIFormat
	APIVariant             modelprotocol.APIVariant
	ProviderRequestStarted bool
	Response               model.Response
}

func (r Runner) recordFailure(
	ctx context.Context,
	input RunInput,
	claim executionstore.ModelCallClaim,
	cause error,
	providerAttempt providerAttemptEvidence,
) (RunResult, error) {
	recovery, err := r.Store.GetModelCallRecoveryState(ctx, input.Plan.ProjectID, input.Plan.AgentID, claim.Context.ID)
	if err != nil {
		return RunResult{}, errors.Join(cause, err)
	}
	providerAttempt.Response = model.ResponseEvidenceForStorage(providerAttempt.Response)
	if recovery.ParentRecoveryKind == executionstore.ModelCallRecoveryCompactOptional {
		return r.recordFailureAndResumeNormal(ctx, input, claim, cause, providerAttempt,
			optionalCompactionOutcome(cause, providerAttempt.ProviderRequestStarted))
	}
	_, semanticFailure := reasonForCompactionFailure(cause)
	if input.Plan.ReplacesCheckpointID != uuid.Nil && semanticFailure {
		return r.recordFailureAndResumeNormal(ctx, input, claim, cause, providerAttempt, "")
	}
	truncated := isTruncatedSummaryFailure(cause)
	if shrinkableCompactionFailure(cause) || truncated {
		nextEnd, err := r.nextSmallerSourceEnd(ctx, input.Plan)
		if err != nil {
			return RunResult{}, errors.Join(cause, err)
		}
		if nextEnd > 0 {
			return r.replaceCompactionSource(
				ctx,
				input,
				claim,
				cause,
				providerAttempt,
				nextEnd,
			)
		}
		if providerAttempt.ProviderRequestStarted && isProviderSourceOverflow(cause) {
			excerptBytes, err := r.nextSourceExcerptBytes(ctx, input, claim)
			if err != nil {
				return RunResult{}, errors.Join(cause, err)
			}
			if excerptBytes > 0 {
				return r.replaceCompactionProjection(
					ctx, input, claim, cause, providerAttempt, input.Plan.EventSequenceEnd, &excerptBytes,
				)
			}
		}
		if !truncated {
			cause = irreducibleCompactionError(irreducibleCompactionFailureDetail(cause))
		}
	}
	now := r.now()
	evidence, decision := modelretry.Decide(
		cause,
		modelretry.Attempt{Number: recovery.CompactionRetryCount + 1},
		claim.Context.ID.String(),
		now,
	)
	if decision.Action == modelretry.ActionRetry {
		decision.RetryDelay = r.modelRetryDelay(decision.RetryDelay)
	}

	if isIrreducibleCompactionFailure(cause) {
		decision.Action = modelretry.ActionStop
	}
	servedSlug := servedModelSlugAfter(providerAttempt)
	providerRequestID := providerRequestIDAfter(providerAttempt, evidence.RequestID)
	if decision.Action == modelretry.ActionRetry {
		failedContext, err := r.Store.RecordRetryableModelCallFailure(
			ctx,
			executionstore.RecordRecoverableModelCallFailureInput{
				ProjectID:               input.Plan.ProjectID,
				AgentID:                 input.Plan.AgentID,
				ModelCallContextID:      claim.Context.ID,
				RuntimeLockID:           input.RuntimeLockID,
				RecoveryKind:            executionstore.ModelCallRecoveryRetry,
				APIFormat:               providerAttempt.APIFormat,
				APIVariant:              providerAttempt.APIVariant,
				ProviderRequestID:       providerRequestID,
				ProviderResponseID:      providerResponseIDAfter(providerAttempt),
				ErrorKind:               evidence.Kind,
				ErrorCode:               evidence.Code,
				ErrorMessage:            evidence.Message,
				ErrorDetails:            evidence.Details,
				RetryDelay:              decision.RetryDelay,
				Usage:                   usageAfter(providerAttempt),
				ProviderReportedCostUSD: providerReportedCostUSDAfter(providerAttempt),
				ProviderMetadata:        providerMetadataAfter(providerAttempt),
			},
		)
		if err != nil {
			return RunResult{}, errors.Join(cause, err)
		}
		if failedContext.RetryAt == nil {
			return RunResult{}, errors.New("retryable compaction failure has no durable retry time")
		}
		return RunResult{
			State:              RunRetryScheduled,
			ModelCallContextID: failedContext.ID,
			RetryAt:            failedContext.RetryAt,
		}, nil
	}
	if input.Plan.ReplacesCheckpointID != uuid.Nil {
		return r.recordFailureAndResumeNormal(ctx, input, claim, cause, providerAttempt, "")
	}
	if err := r.Store.RecordTerminalCompactionFailure(
		ctx,
		executionstore.RecordTerminalCompactionFailureInput{
			ProjectID:               input.Plan.ProjectID,
			AgentID:                 input.Plan.AgentID,
			RuntimeLockID:           input.RuntimeLockID,
			ModelCallContextID:      claim.Context.ID,
			APIFormat:               providerAttempt.APIFormat,
			APIVariant:              providerAttempt.APIVariant,
			ServedProviderModelSlug: servedSlug,
			ProviderRequestID:       providerRequestID,
			ProviderResponseID:      providerResponseIDAfter(providerAttempt),
			ErrorKind:               evidence.Kind,
			ErrorCode:               evidence.Code,
			ErrorMessage:            evidence.Message,
			ErrorDetails:            evidence.Details,
			Usage:                   usageAfter(providerAttempt),
			ProviderReportedCostUSD: providerReportedCostUSDAfter(providerAttempt),
			ProviderMetadata:        providerMetadataAfter(providerAttempt),
		},
	); err != nil {
		return RunResult{}, errors.Join(cause, err)
	}
	return RunResult{
		State:              RunTerminal,
		ModelCallContextID: claim.Context.ID,
	}, nil
}

func (r Runner) recordFailureAndResumeNormal(
	ctx context.Context,
	input RunInput,
	claim executionstore.ModelCallClaim,
	cause error,
	providerAttempt providerAttemptEvidence,
	outcome executionstore.OptionalCompactionOutcome,
) (RunResult, error) {
	evidence := modelretry.EvidenceFor(cause)
	failed, err := r.Store.RecordCompactionFailureAndResumeNormal(
		ctx, executionstore.RecordCompactionFailureAndResumeNormalInput{
			Outcome:                 outcome,
			ProjectID:               input.Plan.ProjectID,
			AgentID:                 input.Plan.AgentID,
			RuntimeLockID:           input.RuntimeLockID,
			ModelCallContextID:      claim.Context.ID,
			APIFormat:               providerAttempt.APIFormat,
			APIVariant:              providerAttempt.APIVariant,
			ProviderRequestID:       providerRequestIDAfter(providerAttempt, evidence.RequestID),
			ProviderResponseID:      providerResponseIDAfter(providerAttempt),
			ErrorKind:               evidence.Kind,
			ErrorCode:               evidence.Code,
			ErrorMessage:            evidence.Message,
			ErrorDetails:            evidence.Details,
			Usage:                   usageAfter(providerAttempt),
			ProviderReportedCostUSD: providerReportedCostUSDAfter(providerAttempt),
			ProviderMetadata:        providerMetadataAfter(providerAttempt),
		})
	if err != nil {
		return RunResult{}, errors.Join(cause, err)
	}
	return RunResult{State: RunResumeNormal, ModelCallContextID: failed.ID}, nil
}

func optionalCompactionOutcome(cause error, requestStarted bool) executionstore.OptionalCompactionOutcome {
	if _, ok := reasonForCompactionFailure(cause); ok {
		return executionstore.OptionalCompactionIneffective
	}
	if !requestStarted {
		return executionstore.OptionalCompactionInterrupted
	}
	providerError, _ := model.ClassifyError(cause)
	switch providerError.Kind {
	case model.ErrorKindContextWindow, model.ErrorKindPayloadTooLarge, model.ErrorKindInvalidRequest:
		return executionstore.OptionalCompactionIneffective
	default:
		return executionstore.OptionalCompactionInterrupted
	}
}

func (r Runner) replaceCompactionSource(
	ctx context.Context,
	input RunInput,
	claim executionstore.ModelCallClaim,
	cause error,
	providerAttempt providerAttemptEvidence,
	nextEnd int64,
) (RunResult, error) {
	if providerAttempt.ProviderRequestStarted {
		recovery, err := r.Store.GetModelCallRecoveryState(ctx, input.Plan.ProjectID, input.Plan.AgentID, claim.Context.ID)
		if err != nil {
			return RunResult{}, errors.Join(cause, err)
		}
		if recovery.ParentRecoveryKind == executionstore.ModelCallRecoveryCompactOptional {
			return r.recordFailure(ctx, input, claim, cause, providerAttempt)
		}
	}
	return r.replaceCompactionProjection(
		ctx, input, claim, cause, providerAttempt, nextEnd, claim.Context.SourceExcerptBytes,
	)
}

func (r Runner) replaceCompactionProjection(
	ctx context.Context,
	input RunInput,
	claim executionstore.ModelCallClaim,
	cause error,
	providerAttempt providerAttemptEvidence,
	nextEnd int64,
	excerptBytes *int,
) (RunResult, error) {
	evidence := modelretry.EvidenceFor(cause)
	providerAttempt.Response = model.ResponseEvidenceForStorage(providerAttempt.Response)
	providerRequestID := providerRequestIDAfter(providerAttempt, evidence.RequestID)
	replacement, err := r.Store.ReplaceCompactionSource(
		ctx,
		executionstore.ReplaceCompactionSourceInput{
			ProjectID:                  input.Plan.ProjectID,
			AgentID:                    input.Plan.AgentID,
			RuntimeLockID:              input.RuntimeLockID,
			ModelCallContextID:         claim.Context.ID,
			APIFormat:                  providerAttempt.APIFormat,
			APIVariant:                 providerAttempt.APIVariant,
			ProviderRequestID:          providerRequestID,
			ProviderResponseID:         providerResponseIDAfter(providerAttempt),
			ErrorKind:                  evidence.Kind,
			ErrorCode:                  evidence.Code,
			ErrorMessage:               evidence.Message,
			ErrorDetails:               evidence.Details,
			Usage:                      usageAfter(providerAttempt),
			ProviderReportedCostUSD:    providerReportedCostUSDAfter(providerAttempt),
			ProviderMetadata:           providerMetadataAfter(providerAttempt),
			NextSourceEventSequenceEnd: nextEnd,
			NextSourceExcerptBytes:     excerptBytes,
		},
	)
	if err != nil {
		return RunResult{}, errors.Join(cause, err)
	}
	if replacement.BoundaryPreempted {
		return RunResult{
			State:              RunTerminal,
			ModelCallContextID: claim.Context.ID,
		}, nil
	}
	input.Plan.EventSequenceEnd = nextEnd
	return r.RunClaimed(ctx, input, replacement.CompactionCall)
}

func shrinkableCompactionFailure(err error) bool {
	reason, hasReason := reasonForCompactionFailure(err)
	if hasReason {
		return reason == compactionFailureSummaryNotReduced
	}
	return isProviderSourceOverflow(err)
}

func isProviderSourceOverflow(err error) bool {
	providerErr, ok := model.ClassifyError(err)
	if !ok {
		return false
	}
	return providerErr.Kind == model.ErrorKindContextWindow ||
		providerErr.Kind == model.ErrorKindPayloadTooLarge
}

func isTruncatedSummaryFailure(err error) bool {
	reason, ok := reasonForCompactionFailure(err)
	return ok && reason == compactionFailureSummaryTruncated
}

func irreducibleCompactionFailureDetail(err error) string {
	reason, _ := reasonForCompactionFailure(err)
	switch reason {
	case compactionFailureSummaryNotReduced:
		return "the smallest closed source prefix did not produce a smaller summary"
	default:
		return "the provider rejected the smallest closed source after bounded source reductions"
	}
}

func irreducibleCompactionError(detail string) error {
	return withCompactionFailureReason(compactionFailureSourceIrreducible, model.ProviderError{
		Kind:    model.ErrorKindContextWindow,
		Source:  "compaction",
		Code:    compactionErrorCodeSourceIrreducible,
		Message: "The conversation prefix could not be compacted: " + detail + ".",
	})
}

func compactionModel(
	client model.Client,
	errorSource string,
) (model.Client, model.RequestPolicy, error) {
	capabilities := model.CapabilitiesForClient(client)
	normalPolicy := model.RequestPolicyFromCapabilities(capabilities)
	limits, err := model.OutputTokenLimitsForClient(client, errorSource)
	if err != nil {
		return nil, model.RequestPolicy{}, err
	}
	if normalPolicy.MaxOutputTokens > 0 {
		if err := limits.Validate(normalPolicy.MaxOutputTokens, errorSource); err != nil {
			return nil, model.RequestPolicy{}, err
		}
	}
	if provider, ok := client.(interface {
		WithoutManualThinking() (model.Client, error)
	}); ok {
		client, err = provider.WithoutManualThinking()
		if err != nil {
			return nil, model.RequestPolicy{}, err
		}
	}
	policy := normalPolicy
	policy.CacheRetention = model.CacheRetentionNone
	policy.MaxOutputTokens = min(preferredSummaryOutputTokens, capabilities.ContextWindowTokens/2)
	if normalPolicy.MaxOutputTokens > 0 {
		policy.MaxOutputTokens = min(policy.MaxOutputTokens, normalPolicy.MaxOutputTokens)
	}
	return client, policy, nil
}

func validateCompactionResponse(errorSource string, response model.Response) (string, error) {
	stopReason := model.NormalizeStopReason(response.StopReason, false)
	switch stopReason {
	case model.StopReasonContextWindow:
		return "", model.ProviderError{
			Kind:    model.ErrorKindContextWindow,
			Source:  errorSource,
			Code:    string(stopReason),
			Message: "compaction request exceeded the configured model context window",
		}
	case model.StopReasonToolUse, model.StopReasonError, model.StopReasonUnknown:
		if stopReason != model.StopReasonToolUse || !response.HasToolCalls() {
			return "", withCompactionFailureReason(compactionFailureSummaryInvalid, model.MalformedProviderSuccess(
				errorSource,
				string(stopReason),
				fmt.Sprintf("compaction model returned unsupported stop reason %q", stopReason),
				nil,
			))
		}
	case model.StopReasonMaxTokens:
		return "", withCompactionFailureReason(compactionFailureSummaryTruncated, model.ProviderError{
			Kind: model.ErrorKindTransient, Source: errorSource, Code: compactionErrorCodeSummaryTruncated,
			Message: "compaction summary was truncated before completion",
		})
	case model.StopReasonEndTurn:
	default:
		return "", withCompactionFailureReason(compactionFailureSummaryInvalid, model.ProviderError{
			Kind:    model.ErrorKindInvalidRequest,
			Source:  errorSource,
			Code:    string(stopReason),
			Message: fmt.Sprintf("compaction model returned unsupported stop reason %q", stopReason),
		})
	}
	if response.HasToolCalls() {
		return "", withCompactionFailureReason(compactionFailureSummaryInvalid, model.ProviderError{
			Kind:    model.ErrorKindTransient,
			Source:  errorSource,
			Code:    "tool_use",
			Message: "compaction model returned tool calls",
		})
	}
	summary := strings.TrimSpace(response.Text())
	if summary == "" {
		return "", withCompactionFailureReason(compactionFailureSummaryInvalid, model.ProviderError{
			Kind:    model.ErrorKindTransient,
			Source:  errorSource,
			Code:    "empty_summary",
			Message: "compaction model returned an empty summary",
		})
	}
	return summary, nil
}

func validateSummaryReduction(priorSummary, sourceText, summary string) error {
	priorTokens := estimateTokens(priorSummary)
	sourceTokens := estimateTokens(sourceText)
	uncompactedTokens := priorTokens + sourceTokens
	summaryTokens := estimateTokens(summary)
	requiredSourceSavings := sourceTokens / 10
	if requiredSourceSavings < 1 {
		requiredSourceSavings = 1
	}
	if uncompactedTokens > 0 &&
		summaryTokens < uncompactedTokens &&
		uncompactedTokens-summaryTokens >= requiredSourceSavings {
		return nil
	}
	return summaryNotReducedError()
}

func validateCheckpointSummaryReduction(plan Plan, priorSummary, sourceText, summary string) error {
	if plan.ReplacesCheckpointID != uuid.Nil {
		if len(summary) > len(priorSummary)-max(1, (len(priorSummary)+9)/10) {
			return summaryNotReducedError()
		}
		return nil
	}
	return validateSummaryReduction(priorSummary, sourceText, summary)
}

func summaryNotReducedError() error {
	return withCompactionFailureReason(compactionFailureSummaryNotReduced, model.ProviderError{
		Kind:    model.ErrorKindTransient,
		Source:  "compaction",
		Code:    compactionErrorCodeSummaryNotReduced,
		Message: "compaction summary did not reduce the source context",
	})
}

func validateResolvedRevision(resolved model.ResolvedClient, revisionID uuid.UUID) error {
	resolvedRevisionID, err := uuid.Parse(resolved.ConfiguredModelRevisionID)
	if err != nil || resolvedRevisionID == uuid.Nil {
		return model.ProviderError{
			Kind:    model.ErrorKindInvalidRequest,
			Source:  "model_resolver",
			Code:    "missing_configured_model_revision",
			Message: "compaction model was not resolved from a configured model revision",
		}
	}
	if resolvedRevisionID != revisionID {
		return model.ProviderError{
			Kind:    model.ErrorKindInvalidRequest,
			Source:  "model_resolver",
			Code:    "configured_model_revision_mismatch",
			Message: "resolved model revision does not match the durable compaction context",
		}
	}
	return nil
}

func compactionModelSelection(
	contextRow executionstore.ModelCallContextRecord,
	snapshot executionstore.AgentConfigSnapshotRecord,
) (model.Selection, error) {
	if snapshot.AgentConfig.ID != contextRow.AgentConfigID ||
		snapshot.InputEventSequence != contextRow.InputEventSequence {
		return model.Selection{}, fmt.Errorf(
			"compaction config snapshot does not match durable context: %w",
			storeerr.ErrStateTransitionConflict,
		)
	}
	contract, err := agentconfig.RuntimeContractFromCompiled(
		snapshot.AgentConfig.CompiledDefinition,
		snapshot.AgentConfig.EffectiveDefinitionHash,
	)
	if err != nil {
		return model.Selection{}, err
	}
	return model.Selection{
		OrgID:                     contextRow.OrgID.String(),
		ProjectID:                 contextRow.ProjectID.String(),
		ConfiguredModelRevisionID: contextRow.ConfiguredModelRevisionID.String(),
		Overrides:                 contract.Model.Overrides(),
	}, nil
}

func modelErrorSourceForAPIFormat(apiFormat modelprotocol.APIFormat) string {
	if apiFormat == "" {
		return "model"
	}
	return string(apiFormat)
}

func servedModelSlugAfter(providerAttempt providerAttemptEvidence) string {
	if !providerAttempt.ProviderRequestStarted {
		return ""
	}
	return providerAttempt.Response.ServedProviderModelSlug
}

func providerRequestIDAfter(
	providerAttempt providerAttemptEvidence,
	errorRequestID string,
) string {
	if !providerAttempt.ProviderRequestStarted {
		return ""
	}
	if errorRequestID != "" {
		return errorRequestID
	}
	return providerAttempt.Response.ProviderRequestID
}

func providerResponseIDAfter(providerAttempt providerAttemptEvidence) string {
	if !providerAttempt.ProviderRequestStarted {
		return ""
	}
	return providerAttempt.Response.ID
}

func usageAfter(providerAttempt providerAttemptEvidence) modelenvelope.Usage {
	if !providerAttempt.ProviderRequestStarted {
		return modelenvelope.Usage{}
	}
	return providerAttempt.Response.Usage
}

func providerReportedCostUSDAfter(
	providerAttempt providerAttemptEvidence,
) modelenvelope.ProviderReportedCostUSD {
	if !providerAttempt.ProviderRequestStarted {
		return ""
	}
	return providerAttempt.Response.ProviderReportedCostUSD
}

func providerMetadataAfter(providerAttempt providerAttemptEvidence) modelenvelope.ProviderMetadata {
	if !providerAttempt.ProviderRequestStarted {
		return modelenvelope.ProviderMetadata{}
	}
	return providerAttempt.Response.ProviderMetadata
}
