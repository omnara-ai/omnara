package compaction

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/modelretry"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type compactionFailureReason uint8

const (
	compactionFailureSummaryTruncated compactionFailureReason = iota + 1
	compactionFailureSummaryNotReduced
	compactionFailureSourceIrreducible
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
	providerAttempt.Response = model.ResponseEvidenceForStorage(providerAttempt.Response)
	if shrinkableCompactionFailure(cause) {
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
		cause = irreducibleCompactionError(irreducibleCompactionFailureDetail(cause))
	}
	now := r.now()
	evidence, decision := modelretry.Decide(
		cause,
		modelretry.Attempt{Number: claim.Context.AttemptNumber},
		claim.Context.ID.String(),
		now, modelretry.StopOnInputOverflow)
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
		},
	); err != nil {
		return RunResult{}, errors.Join(cause, err)
	}
	return RunResult{
		State:              RunTerminal,
		ModelCallContextID: claim.Context.ID,
	}, nil
}

func (r Runner) replaceCompactionSource(
	ctx context.Context,
	input RunInput,
	claim executionstore.ModelCallClaim,
	cause error,
	providerAttempt providerAttemptEvidence,
	nextEnd int64,
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
			NextSourceEventSequenceEnd: nextEnd,
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
		switch reason {
		case compactionFailureSummaryTruncated, compactionFailureSummaryNotReduced:
			return true
		case compactionFailureSourceIrreducible:
			return false
		}
		return false
	}
	providerErr, ok := model.ClassifyError(err)
	if !ok {
		return false
	}
	return providerErr.Kind == model.ErrorKindContextWindow ||
		providerErr.Kind == model.ErrorKindPayloadTooLarge
}

func irreducibleCompactionFailureDetail(err error) string {
	reason, _ := reasonForCompactionFailure(err)
	switch reason {
	case compactionFailureSummaryTruncated:
		return "the smallest closed source prefix produced a truncated summary"
	case compactionFailureSummaryNotReduced:
		return "the smallest closed source prefix did not produce a smaller summary"
	default:
		return "the smallest closed source prefix still exceeded the configured model window"
	}
}

func irreducibleCompactionError(detail string) error {
	return withCompactionFailureReason(compactionFailureSourceIrreducible, model.ProviderError{
		Kind:    model.ErrorKindContextWindow,
		Source:  "compaction",
		Code:    compactionErrorCodeSourceIrreducible,
		Message: "The conversation prefix cannot be compacted within the configured model window: " + detail + ".",
	})
}

func compactionRequestPolicy(client model.Client) model.RequestPolicy {
	capabilities := model.CapabilitiesForClient(client)
	policy := model.RequestPolicyFromCapabilities(capabilities)
	// The prompt controls summary length; this ceiling prevents truncation.
	policy.MaxOutputTokens = capabilities.MaxOutputTokens
	return policy
}

func validateCompactionResponse(errorSource string, response model.Response) (string, error) {
	if response.HasToolCalls() {
		return "", model.ProviderError{
			Kind:    model.ErrorKindTransient,
			Source:  errorSource,
			Code:    "tool_use",
			Message: "compaction model returned tool calls",
		}
	}
	stopReason := model.NormalizeStopReason(response.StopReason, false)
	if stopReason == model.StopReasonMaxTokens {
		return "", withCompactionFailureReason(compactionFailureSummaryTruncated, model.ProviderError{
			Kind:    model.ErrorKindTransient,
			Source:  errorSource,
			Code:    compactionErrorCodeSummaryTruncated,
			Message: "compaction summary was truncated before completion",
		})
	}
	if stopReason == model.StopReasonContextWindow {
		return "", model.ProviderError{
			Kind:    model.ErrorKindContextWindow,
			Source:  errorSource,
			Code:    string(stopReason),
			Message: "compaction request exceeded the configured model context window",
		}
	}
	if stopReason == model.StopReasonToolUse ||
		stopReason == model.StopReasonError ||
		stopReason == model.StopReasonUnknown {
		return "", model.MalformedProviderSuccess(
			errorSource,
			string(stopReason),
			fmt.Sprintf("compaction model returned unsupported stop reason %q", stopReason),
			nil,
		)
	}
	if stopReason != model.StopReasonEndTurn {
		return "", model.ProviderError{
			Kind:    model.ErrorKindInvalidRequest,
			Source:  errorSource,
			Code:    string(stopReason),
			Message: fmt.Sprintf("compaction model returned unsupported stop reason %q", stopReason),
		}
	}
	summary := strings.TrimSpace(response.Text())
	if summary == "" {
		return "", model.ProviderError{
			Kind:    model.ErrorKindTransient,
			Source:  errorSource,
			Code:    "empty_summary",
			Message: "compaction model returned an empty summary",
		}
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
	return withCompactionFailureReason(compactionFailureSummaryNotReduced, model.ProviderError{
		Kind:    model.ErrorKindTransient,
		Source:  "compaction",
		Code:    compactionErrorCodeSummaryNotReduced,
		Message: "compaction summary did not reduce the source context",
	})
}

func validateResolvedRevision(resolved model.ResolvedClient, revisionID storage.ID) error {
	resolvedRevisionID, err := storage.ParseID(resolved.ConfiguredModelRevisionID)
	if err != nil || resolvedRevisionID == storage.NilID {
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
		snapshot.AgentConfig.CompilerVersion,
		snapshot.AgentConfig.EffectiveDefinitionHash,
	)
	if err != nil {
		return model.Selection{}, err
	}
	reasoningEffort := ""
	if contract.Model.Reasoning != nil {
		reasoningEffort = contract.Model.Reasoning.Effort
	}
	return model.Selection{
		OrgID:                     contextRow.OrgID.String(),
		ProjectID:                 contextRow.ProjectID.String(),
		ConfiguredModelRevisionID: contextRow.ConfiguredModelRevisionID.String(),
		Options: model.SelectionOptions{
			ContextWindowTokens:    contract.Model.ContextWindowTokens,
			DefaultMaxOutputTokens: contract.Model.DefaultMaxOutputTokens,
			CacheRetention:         model.CacheRetention(contract.Model.CacheRetention),
			ReasoningEffort:        reasoningEffort,
		},
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
