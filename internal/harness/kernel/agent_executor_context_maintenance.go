package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/compaction"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/modelretry"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

type contextMaintenanceTrigger struct {
	Kind      model.ErrorKind
	Code      string
	Message   string
	Details   json.RawMessage
	RequestID string
	Cause     error
}

type normalCallFailureEvidence struct {
	APIFormat               modelprotocol.APIFormat
	APIVariant              modelprotocol.APIVariant
	ServedModelSlug         string
	ProviderRequestID       string
	ProviderResponseID      string
	Usage                   modelenvelope.Usage
	ProviderReportedCostUSD modelenvelope.ProviderReportedCostUSD
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
	return out
}

func localInputBudgetTrigger(
	assessment model.InputBudgetAssessment,
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
		Kind:    model.ErrorKindContextWindow,
		Code:    "configured_input_budget_exceeded",
		Message: message,
		Details: details,
		Cause:   errors.New(message),
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
	if !ok {
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
		trigger.Kind = model.ErrorKindContextWindow
		trigger.Code = "context_cannot_be_compacted"
		trigger.Message = "The current model input is too large and has no closed event prefix that can be compacted safely."
		trigger.Details = details
		return e.recordTerminalContextMaintenanceFailure(
			ctx,
			input,
			claim,
			resolved,
			trigger,
			providerRequestStarted,
			response,
		)
	}

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
	}
	handoff, err := e.Store.Execution().RecordModelCallFailureAndClaimCompaction(
		ctx,
		executionstore.RecordModelCallFailureAndClaimCompactionInput{
			ParentContextID:        claim.Context.ID,
			Failure:                failure,
			SourceEventSequenceEnd: plan.EventSequenceEnd,
		},
	)
	if err != nil {
		return modelStep{}, errors.Join(trigger.Cause, err)
	}
	if !handoff.BoundaryPreempted {
		_, err = e.compactionRunner(e.ModelResolver, e.contextBuilder()).RunClaimed(ctx, compaction.RunInput{
			Plan:                     plan,
			TurnID:                   input.TurnID,
			OpeningInputIDs:          input.InputIDs,
			OpeningEventSequence:     input.OpeningEventSequence,
			RuntimeLockID:            input.RuntimeLockID,
			ParentModelCallContextID: claim.Context.ID,
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
		},
	)
	if err != nil {
		return modelStep{}, errors.Join(trigger.Cause, err)
	}
	return modelStep{State: modelStepDone, Context: claim.Context, Resolved: resolved}, nil
}
