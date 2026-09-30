package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/compaction"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/modelretry"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	contextMaintenanceErrorCodeInputBudgetExceeded = "configured_input_budget_exceeded"
	contextMaintenanceErrorCodeCannotCompact       = "context_cannot_be_compacted"
	optionalCompactionRearmHeadroomPercent         = 75
)

type contextMaintenanceTrigger struct {
	Optional                  bool
	OptionalInputTargetTokens int
	Kind                      model.ErrorKind
	Code                      string
	Message                   string
	Details                   json.RawMessage
	RequestID                 string
	Cause                     error
}

type normalCallFailureEvidence struct {
	APIFormat               modelprotocol.APIFormat
	APIVariant              modelprotocol.APIVariant
	ServedModelSlug         string
	ProviderRequestID       string
	ProviderResponseID      string
	Usage                   modelenvelope.Usage
	ProviderReportedCostUSD modelenvelope.ProviderReportedCostUSD
	ProviderMetadata        modelenvelope.ProviderMetadata
}

func collectNormalCallFailureEvidence(
	resolved model.ResolvedClient,
	requestID string,
	providerRequestStarted bool,
	response model.Response,
) normalCallFailureEvidence {
	apiFormat, apiVariant, _ := model.APIIdentityForClient(resolved.Client)
	out := normalCallFailureEvidence{APIFormat: apiFormat, APIVariant: apiVariant}
	if !providerRequestStarted {
		return out
	}
	response = model.ResponseEvidenceForStorage(response)
	out.ServedModelSlug = response.ServedProviderModelSlug
	out.ProviderRequestID = requestID
	if out.ProviderRequestID == "" {
		out.ProviderRequestID = response.ProviderRequestID
	}
	out.ProviderResponseID = response.ID
	out.Usage = response.Usage
	out.ProviderReportedCostUSD = response.ProviderReportedCostUSD
	out.ProviderMetadata = response.ProviderMetadata
	return out
}

func localInputBudgetTrigger(
	assessment model.InputBudgetAssessment,
	workingInputTarget int,
	source string,
) (contextMaintenanceTrigger, error) {
	details, err := marshalJSON(map[string]any{
		"source":            source,
		"request_admission": assessment,
	})
	if err != nil {
		return contextMaintenanceTrigger{}, err
	}
	message := fmt.Sprintf(
		"The prepared model request is estimated at %d input tokens, exceeding the configured budget of %d.",
		assessment.EstimatedInputTokens,
		assessment.UsableInputTokens,
	)
	return contextMaintenanceTrigger{
		OptionalInputTargetTokens: workingInputTarget,
		Kind:                      model.ErrorKindContextWindow,
		Code:                      contextMaintenanceErrorCodeInputBudgetExceeded,
		Message:                   message,
		Details:                   details,
		Cause:                     errors.New(message),
	}, nil
}

func providerInputFailureTrigger(cause error) (contextMaintenanceTrigger, bool) {
	evidence := modelretry.EvidenceFor(cause)
	if evidence.Kind != model.ErrorKindContextWindow &&
		evidence.Kind != model.ErrorKindPayloadTooLarge {
		return contextMaintenanceTrigger{}, false
	}
	return contextMaintenanceTrigger{
		Kind:      evidence.Kind,
		Code:      evidence.Code,
		Message:   evidence.Message,
		Details:   evidence.Details,
		RequestID: evidence.RequestID,
		Cause:     cause,
	}, true
}

func (e AgentExecutor) enterContextMaintenance(
	ctx context.Context,
	input ModelWorkExecution,
	claim executionstore.ModelCallClaim,
	resolved model.ResolvedClient,
	trigger contextMaintenanceTrigger,
	providerRequestStarted bool,
	response model.Response,
	maxOutputTokens int,
) (modelStep, error) {
	plan, ok, err := e.planCompactionForContext(
		ctx,
		claim.Context,
		resolved.Client,
		input,
	)
	if err != nil {
		return modelStep{}, errors.Join(trigger.Cause, err)
	}
	if ok && plan.ReplacesCheckpointID == uuid.Nil {
		return e.enterPlannedContextMaintenance(
			ctx, input, claim, resolved, trigger, providerRequestStarted, response, plan,
		)
	}
	recovery, err := e.Store.Execution().GetModelCallRecoveryState(
		ctx, input.ProjectID, input.AgentID, claim.Context.ID,
	)
	if err != nil {
		return modelStep{}, err
	}
	if trigger.Kind == model.ErrorKindContextWindow && !recovery.OutputAllowanceRestored {
		if step, reduced, reduceErr := e.retryWithSmallerOutputAllowance(
			ctx, input, claim, resolved, trigger, response, maxOutputTokens,
		); reduceErr != nil || reduced {
			return step, reduceErr
		}
	}
	if ok {
		if !recovery.CheckpointRecompressionAttempted && recovery.RecoveryCheckpointRetainedBytes == nil {
			return e.enterPlannedContextMaintenance(
				ctx, input, claim, resolved, trigger, providerRequestStarted, response, plan,
			)
		}
		if step, reduced, reduceErr := e.retryWithCheckpointExcerpt(
			ctx, input, claim, resolved, trigger, response, plan.ReplacesCheckpointID, recovery,
		); reduceErr != nil || reduced {
			return step, reduceErr
		}
	}
	details, marshalErr := marshalJSON(map[string]any{
		"source": modelErrorSourceForClient(resolved.Client),
		"compaction_trigger": map[string]any{
			"kind":    trigger.Kind,
			"code":    trigger.Code,
			"message": trigger.Message,
			"details": trigger.Details,
		},
	})
	if marshalErr != nil {
		return modelStep{}, errors.Join(trigger.Cause, marshalErr)
	}
	trigger.Code = contextMaintenanceErrorCodeCannotCompact
	trigger.Message = "The current model input remains too large after the available safe context reductions."
	trigger.Details = details
	return e.recordTerminalContextMaintenanceFailure(
		ctx, input, claim, resolved, trigger, providerRequestStarted, response,
	)
}

func (e AgentExecutor) restoreOutputAllowance(
	ctx context.Context,
	input ModelWorkExecution,
	step modelStep,
	cause error,
) (modelStep, error) {
	evidence := modelretry.EvidenceFor(cause)
	failure := collectNormalCallFailureEvidence(step.Resolved, evidence.RequestID, true, step.Response)
	record, err := e.Store.Execution().RecordRetryableModelCallFailure(ctx,
		executionstore.RecordRecoverableModelCallFailureInput{
			ProjectID: input.ProjectID, AgentID: input.AgentID,
			ModelCallContextID: step.Context.ID, RuntimeLockID: input.RuntimeLockID,
			RecoveryKind: executionstore.ModelCallRecoveryRestoreOutput,
			APIFormat:    failure.APIFormat, APIVariant: failure.APIVariant,
			ProviderRequestID: failure.ProviderRequestID, ProviderResponseID: failure.ProviderResponseID,
			ErrorKind: evidence.Kind, ErrorCode: evidence.Code, ErrorMessage: evidence.Message,
			ErrorDetails: evidence.Details, Usage: failure.Usage,
			ProviderReportedCostUSD: failure.ProviderReportedCostUSD, ProviderMetadata: failure.ProviderMetadata,
		},
	)
	if err != nil {
		return modelStep{}, errors.Join(cause, err)
	}
	return modelStep{State: modelStepWaiting, Context: record, Resolved: step.Resolved}, nil
}

func (e AgentExecutor) retryWithCheckpointExcerpt(
	ctx context.Context,
	input ModelWorkExecution,
	claim executionstore.ModelCallClaim,
	resolved model.ResolvedClient,
	trigger contextMaintenanceTrigger,
	response model.Response,
	checkpointID uuid.UUID,
	recovery executionstore.ModelCallRecoveryState,
) (modelStep, bool, error) {
	checkpoint, found, err := e.Store.Execution().GetContextCheckpoint(
		ctx, input.ProjectID, input.AgentID, checkpointID,
	)
	if err != nil || !found {
		return modelStep{}, false, err
	}
	if checkpoint.SummarizedThroughEventSequence >= input.OpeningEventSequence {
		return modelStep{}, false, nil
	}
	next, reduced, err := modelcontext.NextCheckpointExcerptBytes(
		checkpoint.Summary, recovery.RecoveryCheckpointRetainedBytes,
	)
	if err != nil || !reduced {
		return modelStep{}, false, err
	}
	evidence := collectNormalCallFailureEvidence(resolved, trigger.RequestID, true, response)
	record, err := e.Store.Execution().RecordRetryableModelCallFailure(
		ctx,
		executionstore.RecordRecoverableModelCallFailureInput{
			ProjectID: input.ProjectID, AgentID: input.AgentID,
			ModelCallContextID: claim.Context.ID, RuntimeLockID: input.RuntimeLockID,
			RecoveryKind: executionstore.ModelCallRecoveryRetry,
			APIFormat:    evidence.APIFormat, APIVariant: evidence.APIVariant,
			ProviderRequestID: evidence.ProviderRequestID, ProviderResponseID: evidence.ProviderResponseID,
			ErrorKind: trigger.Kind, ErrorCode: trigger.Code,
			ErrorMessage: trigger.Message, ErrorDetails: trigger.Details,
			Usage: evidence.Usage, ProviderReportedCostUSD: evidence.ProviderReportedCostUSD,
			ProviderMetadata:                evidence.ProviderMetadata,
			RecoveryCheckpointRetainedBytes: &next,
		},
	)
	if err != nil {
		return modelStep{}, false, errors.Join(trigger.Cause, err)
	}
	return modelStep{State: modelStepWaiting, Context: record, Resolved: resolved}, true, nil
}

func (e AgentExecutor) enterPlannedContextMaintenance(
	ctx context.Context,
	input ModelWorkExecution,
	claim executionstore.ModelCallClaim,
	resolved model.ResolvedClient,
	trigger contextMaintenanceTrigger,
	providerRequestStarted bool,
	response model.Response,
	plan compaction.Plan,
) (modelStep, error) {

	evidence := collectNormalCallFailureEvidence(
		resolved,
		trigger.RequestID,
		providerRequestStarted,
		response,
	)
	failure := executionstore.RecordRecoverableModelCallFailureInput{
		ProjectID:               input.ProjectID,
		AgentID:                 input.AgentID,
		ModelCallContextID:      claim.Context.ID,
		RuntimeLockID:           input.RuntimeLockID,
		RecoveryKind:            executionstore.ModelCallRecoveryCompact,
		APIFormat:               evidence.APIFormat,
		APIVariant:              evidence.APIVariant,
		ProviderRequestID:       evidence.ProviderRequestID,
		ProviderResponseID:      evidence.ProviderResponseID,
		ErrorKind:               trigger.Kind,
		ErrorCode:               trigger.Code,
		ErrorMessage:            trigger.Message,
		ErrorDetails:            trigger.Details,
		Usage:                   evidence.Usage,
		ProviderReportedCostUSD: evidence.ProviderReportedCostUSD,
		ProviderMetadata:        evidence.ProviderMetadata,
	}
	if trigger.Optional {
		failure.RecoveryKind = executionstore.ModelCallRecoveryCompactOptional
		failure.OptionalInputTargetTokens = new(trigger.OptionalInputTargetTokens)
	}
	handoff, err := e.Store.Execution().RecordModelCallFailureAndClaimCompaction(
		ctx,
		executionstore.RecordModelCallFailureAndClaimCompactionInput{
			ParentContextID:        claim.Context.ID,
			Failure:                failure,
			SourceEventSequenceEnd: plan.EventSequenceEnd,
			ReplacesCheckpointID:   plan.ReplacesCheckpointID,
		},
	)
	if err != nil {
		return modelStep{}, errors.Join(trigger.Cause, err)
	}
	if !handoff.BoundaryPreempted {
		_, err = e.compactionRunner(e.ModelResolver).RunClaimed(ctx, compaction.RunInput{
			Plan:                 plan,
			TurnID:               input.TurnID,
			OpeningInputIDs:      input.InputIDs,
			OpeningEventSequence: input.OpeningEventSequence,
			RuntimeLockID:        input.RuntimeLockID,
		}, handoff.CompactionCall)
		if err != nil {
			return modelStep{}, err
		}
	}
	return modelStep{
		State:    modelStepWaiting,
		Context:  handoff.ParentContext,
		Resolved: resolved,
	}, nil
}

func shouldAttemptOptionalCompaction(
	prepared model.PreparedRequest,
	workingInputTarget int,
	recovery executionstore.ModelCallRecoveryState,
) bool {
	if !prepared.InputBudget.OverBudget() || workingInputTarget <= 0 ||
		recovery.OptionalCompactionAttemptedAtFrontier ||
		recovery.CheckpointNeedsNormalAttempt || recovery.HasPriorNormalAttempt {
		return false
	}
	if recovery.LastOptionalContextID == uuid.Nil ||
		recovery.LastOptionalInputTargetTokens != workingInputTarget {
		return true
	}
	hasMeasuredPressure := prepared.HasMeasuredInputPrefix && prepared.RequestInputIdentity != nil
	if !hasMeasuredPressure && recovery.LatestObservedNormalInputTokens < workingInputTarget {
		return false
	}
	return !recovery.LastOptionalCompactionNeedsHeadroom ||
		(recovery.MinimumObservedNormalInputTokens > 0 &&
			recovery.MinimumObservedNormalInputTokens <= workingInputTarget*optionalCompactionRearmHeadroomPercent/100)
}

func (e AgentExecutor) retryWithSmallerOutputAllowance(
	ctx context.Context,
	input ModelWorkExecution,
	claim executionstore.ModelCallClaim,
	resolved model.ResolvedClient,
	trigger contextMaintenanceTrigger,
	response model.Response,
	maxOutputTokens int,
) (modelStep, bool, error) {
	limits, err := model.OutputTokenLimitsForClient(resolved.Client, modelErrorSourceForClient(resolved.Client))
	if err != nil {
		return modelStep{}, false, err
	}
	minimum := max(1, limits.Minimum)
	if maxOutputTokens <= minimum {
		return modelStep{}, false, nil
	}
	nextAllowance := max(minimum, maxOutputTokens/2)
	evidence := collectNormalCallFailureEvidence(resolved, trigger.RequestID, true, response)
	contextRecord, err := e.Store.Execution().RecordRetryableModelCallFailure(
		ctx,
		executionstore.RecordRecoverableModelCallFailureInput{
			ProjectID: input.ProjectID, AgentID: input.AgentID,
			ModelCallContextID: claim.Context.ID, RuntimeLockID: input.RuntimeLockID,
			RecoveryKind: executionstore.ModelCallRecoveryRetry,
			APIFormat:    evidence.APIFormat, APIVariant: evidence.APIVariant,
			ProviderRequestID: evidence.ProviderRequestID, ProviderResponseID: evidence.ProviderResponseID,
			ErrorKind: trigger.Kind, ErrorCode: trigger.Code,
			ErrorMessage: trigger.Message, ErrorDetails: trigger.Details,
			Usage: evidence.Usage, ProviderReportedCostUSD: evidence.ProviderReportedCostUSD,
			ProviderMetadata:        evidence.ProviderMetadata,
			RecoveryMaxOutputTokens: &nextAllowance,
		},
	)
	if err != nil {
		return modelStep{}, false, errors.Join(trigger.Cause, err)
	}
	return modelStep{State: modelStepWaiting, Context: contextRecord, Resolved: resolved}, true, nil
}

func (e AgentExecutor) recordTerminalContextMaintenanceFailure(
	ctx context.Context,
	input ModelWorkExecution,
	claim executionstore.ModelCallClaim,
	resolved model.ResolvedClient,
	trigger contextMaintenanceTrigger,
	providerRequestStarted bool,
	response model.Response,
) (modelStep, error) {
	evidence := collectNormalCallFailureEvidence(
		resolved,
		trigger.RequestID,
		providerRequestStarted,
		response,
	)
	_, err := e.Store.Execution().RecordModelCallErrorAndCompleteContext(
		ctx,
		executionstore.RecordModelCallErrorAndCompleteContextInput{
			ProjectID:               input.ProjectID,
			AgentID:                 input.AgentID,
			RuntimeLockID:           input.RuntimeLockID,
			ModelCallContextID:      claim.Context.ID,
			APIFormat:               evidence.APIFormat,
			APIVariant:              evidence.APIVariant,
			ServedProviderModelSlug: evidence.ServedModelSlug,
			ProviderRequestID:       evidence.ProviderRequestID,
			ProviderResponseID:      evidence.ProviderResponseID,
			ErrorKind:               trigger.Kind,
			ErrorCode:               trigger.Code,
			ErrorMessage:            trigger.Message,
			ErrorDetails:            trigger.Details,
			Usage:                   evidence.Usage,
			ProviderReportedCostUSD: evidence.ProviderReportedCostUSD,
			ProviderMetadata:        evidence.ProviderMetadata,
		},
	)
	if err != nil {
		return modelStep{}, errors.Join(trigger.Cause, err)
	}
	if ctx.Err() == nil {
		e.postIntegrationRuntimeError(ctx, input)
	}
	return modelStep{State: modelStepDone, Context: claim.Context, Resolved: resolved}, nil
}
