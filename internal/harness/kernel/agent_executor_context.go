package kernel

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/mcp"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelretry"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type modelStepState string

const (
	modelStepWaiting modelStepState = "waiting"
	modelStepDone    modelStepState = "done"
	modelStepToolUse modelStepState = "tool_use"
)

const (
	preSendErrorCodeCaptureAgentConfigFailed  = "capture_agent_config_failed"
	preSendErrorCodeCompileAgentConfigFailed  = "compile_agent_config_failed"
	preSendErrorCodeInitializeMCPFailed       = "initialize_mcp_connections_failed"
	preSendErrorCodeResolveModelFailed        = "resolve_model_failed"
	preSendErrorCodeBuildModelContextFailed   = "build_model_context_failed"
	preSendErrorCodeLoadReplayPolicyFailed    = "load_provider_replay_policy_failed"
	preSendErrorCodePrepareModelRequestFailed = "prepare_model_request_failed"
)

type modelStep struct {
	State                   modelStepState
	Context                 executionstore.ModelCallContextRecord
	Bundle                  modelcontext.Bundle
	Envelope                modelenvelope.ResponseEnvelope
	Response                model.Response
	Resolved                model.ResolvedClient
	StreamedToolCallIDs     map[string]uuid.UUID
	RequestInputIdentity    *modelenvelope.RequestInputIdentity
	MaxOutputTokens         int
	ReducedOutputAllowance  bool
	OutputAllowanceRestored bool
}

func (e AgentExecutor) executeModelStep(
	ctx context.Context,
	input ModelWorkExecution,
	builder modelcontext.Builder,
	resolver model.Resolver,
) (modelStep, error) {
	var snapshot executionstore.AgentConfigSnapshotRecord
	var claim executionstore.ModelCallClaim
	var err error
	if input.Kind == executionstore.ModelWorkResume {
		claim, err = e.Store.Execution().ClaimNextModelCallContext(ctx, executionstore.ClaimNextModelCallContextInput{
			ProjectID:                     input.ProjectID,
			AgentID:                       input.AgentID,
			PredecessorModelCallContextID: input.ModelCallContextID,
			RuntimeLockID:                 input.RuntimeLockID,
		})
		if err != nil {
			return modelStep{}, err
		}
		if claim.Claimed {
			snapshot, err = e.Store.Execution().CaptureAgentConfigForEventWatermark(
				ctx,
				input.ProjectID,
				input.AgentID,
				claim.Context.InputEventSequence,
			)
			if err != nil {
				return e.recordNormalPreSendFailure(
					ctx, input, claim, model.ResolvedClient{}, err,
					modelretry.PreSendFailure{
						Code:    preSendErrorCodeCaptureAgentConfigFailed,
						Message: "Omnara could not load the agent configuration for this model attempt.",
					},
				)
			}
		}
	} else {
		snapshot, err = e.Store.Execution().CaptureAgentConfigForModelContext(ctx, input.ProjectID, input.AgentID)
		if err == nil {
			claim, err = e.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
				ProjectID:                input.ProjectID,
				AgentID:                  input.AgentID,
				RuntimeLockID:            input.RuntimeLockID,
				OpeningInputIDs:          input.InputIDs,
				AgentConfigID:            snapshot.AgentConfig.ID,
				InputEventSequence:       snapshot.InputEventSequence,
				SourceModelCallContextID: input.SourceModelCallContextID,
				SourceModelOutputID:      input.SourceModelOutputID,
			})
		}
	}
	if err != nil {
		return modelStep{}, err
	}
	if !claim.Claimed {
		if claim.Created &&
			claim.Context.ErrorCode == storeerr.ManagedWorkAdmissionDeniedCode &&
			shouldPostIntegrationRuntimeError(ctx, storeerr.ErrManagedWorkAdmissionDenied) {
			e.postIntegrationRuntimeError(ctx, input)
		}
		state := modelStepWaiting
		if claim.Context.State != executionstore.ModelCallContextStarted &&
			claim.Context.RecoveryKind != executionstore.ModelCallRecoveryRetry &&
			claim.Context.RecoveryKind != executionstore.ModelCallRecoveryRestoreOutput {
			state = modelStepDone
		}
		return modelStep{State: state, Context: claim.Context}, nil
	}
	contract, err := agentconfig.RuntimeContractFromCompiled(
		snapshot.AgentConfig.CompiledDefinition,
		snapshot.AgentConfig.EffectiveDefinitionHash,
	)
	if err != nil {
		return e.recordNormalPreSendFailure(
			ctx, input, claim, model.ResolvedClient{}, err,
			modelretry.PreSendFailure{
				Code: preSendErrorCodeCompileAgentConfigFailed,
				Message: "Omnara could not load the compiled agent configuration " +
					"for this model attempt.",
			},
		)
	}
	var mcpTrigger mcp.ConnectionTrigger
	switch input.Kind {
	case executionstore.ModelWorkStart:
		mcpTrigger = mcp.TriggerTurnStart
	case executionstore.ModelWorkResume:
		mcpTrigger = mcp.TriggerTurnResume
	case executionstore.ModelWorkContinue:
		mcpTrigger = mcp.TriggerTurnContinue
	}
	if mcpTrigger != 0 {
		if err := e.ensureMCPConnections(
			ctx,
			claim.Context.OrgID,
			input,
			contract,
			mcpTrigger,
		); err != nil {
			return e.recordNormalPreSendFailure(
				ctx, input, claim, model.ResolvedClient{}, err,
				modelretry.PreSendFailure{
					Code:    preSendErrorCodeInitializeMCPFailed,
					Message: "Omnara could not initialize the configured MCP connections.",
				},
			)
		}
	}
	selection := modelSelectionForContext(claim.Context, contract.Model)
	resolved, err := resolver.Resolve(ctx, selection)
	if err != nil {
		if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return modelStep{}, err
		}
		return e.recordNormalPreSendFailure(
			ctx, input, claim, model.ResolvedClient{}, err,
			modelretry.PreSendFailure{
				Code:    preSendErrorCodeResolveModelFailed,
				Message: "Omnara could not resolve the configured model for this attempt.",
			},
		)
	}
	if err := validateResolvedModelContext(resolved, claim.Context); err != nil {
		return e.recordNormalFailure(ctx, input, claim, resolved, err, false, model.Response{})
	}
	client := resolved.Client
	apiFormat, apiVariant, hasAPIIdentity := model.APIIdentityForClient(client)
	if !hasAPIIdentity {
		cause := model.ProviderError{
			Kind:    model.ErrorKindInvalidRequest,
			Source:  "model",
			Code:    "missing_api_identity",
			Message: "The configured model client does not declare its API format and variant.",
		}
		return e.recordNormalFailure(ctx, input, claim, resolved, cause, false, model.Response{})
	}
	capabilities := model.CapabilitiesForClient(client)
	bundle, err := builder.Build(ctx, modelcontext.BuildInput{
		ProjectID:           input.ProjectID,
		AgentID:             input.AgentID,
		TurnID:              input.TurnID,
		OpeningInputIDs:     input.InputIDs,
		Now:                 input.Now,
		AgentConfigSnapshot: &snapshot,
		MediaProjector:      model.MediaProjectorForClient(client),
	})
	if errors.Is(err, modelcontext.ErrOpeningMediaBudgetExceeded) {
		cause := model.ProviderError{
			Kind:    model.ErrorKindInvalidRequest,
			Source:  modelErrorSourceForClient(client),
			Code:    "opening_media_too_large",
			Message: "The media attached to the current inputs is too large for one model request.",
			Cause:   err,
		}
		return e.recordNormalFailure(ctx, input, claim, resolved, cause, false, model.Response{})
	}
	if err != nil {
		return e.recordNormalPreSendFailure(
			ctx, input, claim, resolved, err,
			modelretry.PreSendFailure{
				Code:    preSendErrorCodeBuildModelContextFailed,
				Message: "Omnara could not construct the model context for this attempt.",
			},
		)
	}
	if err := ensureModelSupportsTools(
		client,
		capabilities,
		contract.RequiresModelToolSupport() || len(bundle.ToolSpecs) > 0,
	); err != nil {
		cause := model.ProviderError{
			Kind:    model.ErrorKindInvalidRequest,
			Source:  "model_capabilities",
			Code:    "required_tools_unsupported",
			Message: err.Error(),
		}
		return e.recordNormalFailure(ctx, input, claim, resolved, cause, false, model.Response{})
	}

	policy, err := modelretry.RequestPolicyForModelCall(
		ctx,
		e.Store.Execution(),
		input.ProjectID,
		input.AgentID,
		claim.Context.ID,
		model.RequestPolicyFromCapabilities(capabilities),
	)
	if err != nil {
		return e.recordNormalPreSendFailure(
			ctx, input, claim, resolved, err,
			modelretry.PreSendFailure{
				Code: preSendErrorCodeLoadReplayPolicyFailed,
				Message: "Omnara could not determine whether provider replay is safe " +
					"for this attempt.",
			},
		)
	}
	recovery, err := e.Store.Execution().GetModelCallRecoveryState(
		ctx, input.ProjectID, input.AgentID, claim.Context.ID,
	)
	if err != nil {
		return modelStep{}, err
	}
	if recovery.RecoveryCheckpointRetainedBytes != nil {
		bundle, err = modelcontext.ApplyCheckpointExcerpt(
			bundle, recovery.RecoveryCheckpointID, *recovery.RecoveryCheckpointRetainedBytes,
		)
		if err != nil {
			return e.recordNormalPreSendFailure(ctx, input, claim, resolved, err, modelretry.PreSendFailure{
				Code:    preSendErrorCodeBuildModelContextFailed,
				Message: "Omnara could not apply the saved checkpoint recovery projection.",
			})
		}
	}
	workingInputTarget, err := model.WorkingInputTargetTokens(client, policy, modelErrorSourceForClient(client))
	if err != nil {
		return e.recordNormalPreSendFailure(ctx, input, claim, resolved, err, modelretry.PreSendFailure{
			Code: preSendErrorCodePrepareModelRequestFailed, Message: "Omnara could not determine the model input target.",
		})
	}
	configuredOutputAllowance := policy.MaxOutputTokens
	if recovery.RecoveryMaxOutputTokens != nil && !recovery.OutputAllowanceRestored {
		if policy.MaxOutputTokens == 0 || *recovery.RecoveryMaxOutputTokens < policy.MaxOutputTokens {
			policy.MaxOutputTokens = *recovery.RecoveryMaxOutputTokens
		}
	}
	prepared, err := model.PrepareForSend(
		ctx,
		client,
		model.PrepareForSendInput{
			Context:                 bundle,
			Policy:                  policy,
			ErrorSource:             modelErrorSourceForClient(client),
			AllowUncertainInput:     true,
			PreserveOutputAllowance: recovery.OutputAllowanceRestored,
		},
	)
	if err != nil {
		return e.recordNormalPreSendFailure(
			ctx, input, claim, resolved, err,
			modelretry.PreSendFailure{
				Code:    preSendErrorCodePrepareModelRequestFailed,
				Message: "Omnara could not prepare the provider request for this attempt.",
			},
		)
	}
	optionalMaintenance, err := e.shouldAttemptOptionalCompaction(
		ctx, claim.Context, prepared, workingInputTarget, recovery,
	)
	if err != nil {
		return modelStep{}, err
	}
	if optionalMaintenance {
		trigger, triggerErr := localInputBudgetTrigger(
			prepared.InputBudget,
			workingInputTarget,
			modelErrorSourceForClient(client),
		)
		if triggerErr != nil {
			return modelStep{}, triggerErr
		}
		plan, ok, planErr := e.planCompactionForContext(ctx, claim.Context, client, input)
		if planErr != nil {
			return modelStep{}, planErr
		}
		if ok && plan.ReplacesCheckpointID == uuid.Nil {
			trigger.Optional = true
			return e.enterPlannedContextMaintenance(ctx, input, claim, resolved, trigger, false, model.Response{}, plan)
		}
	}
	request := model.Request{
		ProviderRequest: prepared.Body,
	}
	var streamSink *harnessStreamSink
	if recovery.ProviderAttemptCount == 0 {
		if streamSink = e.streamSinkForCall(
			context.WithoutCancel(ctx),
			input.AgentID,
			input.TurnID,
			claim.Context.ID,
		); streamSink != nil {
			request.DeltaSink = streamSink
			defer streamSink.Close()
		}
	}
	response, err := client.Respond(ctx, request)
	if err != nil {
		if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return modelStep{}, err
		}
		if _, classified := model.ClassifyError(err); !classified {
			return modelStep{}, err
		}
		return e.recordNormalProviderFailure(
			ctx,
			input,
			claim,
			resolved,
			policy,
			err,
			response,
			prepared.MaxOutputTokens,
		)
	}
	envelope, err := model.NewResponseEnvelopeForStorage(
		client.RequestedProviderModelSlug(),
		apiFormat,
		apiVariant,
		response,
	)
	if err != nil {
		return e.recordNormalFailure(
			ctx,
			input,
			claim,
			resolved,
			model.MalformedProviderResponse(string(apiFormat), err),
			true,
			response,
		)
	}
	for _, part := range response.Content {
		if part.ToolCallError != "" {
			prepared.RequestInputIdentity = nil
			break
		}
	}
	step := modelStep{
		Context:                 claim.Context,
		Bundle:                  bundle,
		Envelope:                envelope,
		Response:                response,
		Resolved:                resolved,
		StreamedToolCallIDs:     streamSink.ToolCallIDs(),
		RequestInputIdentity:    prepared.RequestInputIdentity,
		MaxOutputTokens:         prepared.MaxOutputTokens,
		ReducedOutputAllowance:  prepared.MaxOutputTokens > 0 && configuredOutputAllowance > prepared.MaxOutputTokens,
		OutputAllowanceRestored: recovery.OutputAllowanceRestored,
	}
	return e.finishModelResponse(ctx, input, step)
}

func (e AgentExecutor) finishModelResponse(
	ctx context.Context,
	input ModelWorkExecution,
	step modelStep,
) (modelStep, error) {
	client := step.Resolved.Client
	errorSource := modelErrorSourceForClient(client)
	calls := model.ToolCallsFromEnvelope(step.Envelope)
	reason := step.Envelope.Normalized.StopReason
	cause := invalidModelResponse(errorSource, reason, calls)
	if cause == nil && (step.ReducedOutputAllowance || step.OutputAllowanceRestored) &&
		reason == model.StopReasonMaxTokens && len(calls) == 0 {
		visibleProgress := false
		for _, part := range step.Envelope.Normalized.Content {
			if part.Type == modelenvelope.ResponsePartTypeText && strings.TrimSpace(part.Text) != "" {
				visibleProgress = true
				break
			}
		}
		if !visibleProgress {
			cause = model.ProviderError{
				Kind: model.ErrorKindTransient, Source: errorSource, Code: "output_recovery_no_progress",
				Message: "The model exhausted its output allowance without producing text or a tool call.",
			}
			if !step.OutputAllowanceRestored {
				return e.restoreOutputAllowance(ctx, input, step, cause)
			}
		}
	}
	if cause != nil {
		return e.recordNormalFailureForAttempt(
			ctx,
			input,
			executionstore.ModelCallClaim{Context: step.Context, Claimed: true},
			step.Resolved,
			cause,
			true,
			step.Response,
			modelretry.Attempt{Number: step.Context.AttemptNumber},
			step.MaxOutputTokens,
		)
	}
	if len(calls) > 0 {
		step.State = modelStepToolUse
		return step, nil
	}
	return e.recordSuccessfulModelOutput(ctx, input, step)
}

func (e AgentExecutor) recordSuccessfulModelOutput(
	ctx context.Context,
	input ModelWorkExecution,
	step modelStep,
) (modelStep, error) {
	_, err := e.Store.Execution().RecordModelOutputAndCompleteContext(
		ctx,
		executionstore.RecordModelOutputAndCompleteContextInput{
			ProjectID:            input.ProjectID,
			AgentID:              input.AgentID,
			RuntimeLockID:        input.RuntimeLockID,
			ModelCallContextID:   step.Context.ID,
			ProviderRequestID:    step.Response.ProviderRequestID,
			ProviderResponse:     step.Envelope,
			RequestInputIdentity: step.RequestInputIdentity,
		},
	)
	if err != nil {
		return modelStep{}, err
	}
	step.State = modelStepDone
	return step, nil
}

func (e AgentExecutor) recordNormalFailure(
	ctx context.Context,
	input ModelWorkExecution,
	claim executionstore.ModelCallClaim,
	resolved model.ResolvedClient,
	cause error,
	providerRequestStarted bool,
	response model.Response,
) (modelStep, error) {
	return e.recordNormalFailureForAttempt(
		ctx,
		input,
		claim,
		resolved,
		cause,
		providerRequestStarted,
		response,
		modelretry.Attempt{Number: claim.Context.AttemptNumber},
		0,
	)
}

func (e AgentExecutor) recordNormalProviderFailure(
	ctx context.Context,
	input ModelWorkExecution,
	claim executionstore.ModelCallClaim,
	resolved model.ResolvedClient,
	policy model.RequestPolicy,
	cause error,
	response model.Response,
	maxOutputTokens int,
) (modelStep, error) {
	return e.recordNormalFailureForAttempt(
		ctx,
		input,
		claim,
		resolved,
		cause,
		true,
		response,
		modelretry.Attempt{
			Number: claim.Context.AttemptNumber,
			ProviderReplayCutoffCanAdvance: policy.ProviderReplayCutoffEventSequence <
				claim.Context.InputEventSequence,
		},
		maxOutputTokens,
	)
}

func (e AgentExecutor) recordNormalFailureForAttempt(
	ctx context.Context,
	input ModelWorkExecution,
	claim executionstore.ModelCallClaim,
	resolved model.ResolvedClient,
	cause error,
	providerRequestStarted bool,
	response model.Response,
	attempt modelretry.Attempt,
	maxOutputTokens int,
) (modelStep, error) {
	if trigger, ok := providerInputFailureTrigger(cause); ok {
		return e.enterContextMaintenance(
			ctx,
			input,
			claim,
			resolved,
			trigger,
			providerRequestStarted,
			response,
			maxOutputTokens,
		)
	}
	recovery, err := e.Store.Execution().GetModelCallRecoveryState(
		ctx, input.ProjectID, input.AgentID, claim.Context.ID,
	)
	if err != nil {
		return modelStep{}, err
	}
	attempt.Number = recovery.NormalRetryCount + 1
	now := e.now()
	evidence, decision := modelretry.Decide(
		cause,
		attempt,
		claim.Context.ID.String(),
		now,
	)
	if decision.Action == modelretry.ActionRetry {
		decision.RetryDelay = e.modelRetryDelay(decision.RetryDelay)
	}

	failureEvidence := collectNormalCallFailureEvidence(
		resolved,
		evidence.RequestID,
		providerRequestStarted,
		response,
	)
	if decision.Action == modelretry.ActionRetry {
		failureInput := executionstore.RecordRecoverableModelCallFailureInput{
			ProjectID:               input.ProjectID,
			AgentID:                 input.AgentID,
			ModelCallContextID:      claim.Context.ID,
			RuntimeLockID:           input.RuntimeLockID,
			RecoveryKind:            executionstore.ModelCallRecoveryRetry,
			APIFormat:               failureEvidence.APIFormat,
			APIVariant:              failureEvidence.APIVariant,
			ProviderRequestID:       failureEvidence.ProviderRequestID,
			ProviderResponseID:      failureEvidence.ProviderResponseID,
			ErrorKind:               evidence.Kind,
			ErrorCode:               evidence.Code,
			ErrorMessage:            evidence.Message,
			ErrorDetails:            evidence.Details,
			RetryDelay:              decision.RetryDelay,
			Usage:                   failureEvidence.Usage,
			ProviderReportedCostUSD: failureEvidence.ProviderReportedCostUSD,
			ProviderMetadata:        failureEvidence.ProviderMetadata,
		}
		contextRecord, err := e.Store.Execution().RecordRetryableModelCallFailure(ctx, failureInput)
		if err != nil {
			return modelStep{}, errors.Join(cause, err)
		}
		return modelStep{State: modelStepWaiting, Context: contextRecord, Resolved: resolved}, nil
	}
	_, err = e.Store.Execution().RecordModelCallErrorAndCompleteContext(
		ctx,
		executionstore.RecordModelCallErrorAndCompleteContextInput{
			ProjectID:               input.ProjectID,
			AgentID:                 input.AgentID,
			RuntimeLockID:           input.RuntimeLockID,
			ModelCallContextID:      claim.Context.ID,
			APIFormat:               failureEvidence.APIFormat,
			APIVariant:              failureEvidence.APIVariant,
			ServedProviderModelSlug: failureEvidence.ServedModelSlug,
			ProviderRequestID:       failureEvidence.ProviderRequestID,
			ProviderResponseID:      failureEvidence.ProviderResponseID,
			ErrorKind:               evidence.Kind,
			ErrorCode:               evidence.Code,
			ErrorMessage:            evidence.Message,
			ErrorDetails:            evidence.Details,
			Usage:                   failureEvidence.Usage,
			ProviderReportedCostUSD: failureEvidence.ProviderReportedCostUSD,
			ProviderMetadata:        failureEvidence.ProviderMetadata,
		},
	)
	if err != nil {
		return modelStep{}, errors.Join(cause, err)
	}
	if ctx.Err() == nil {
		e.postIntegrationRuntimeError(ctx, input)
	}
	return modelStep{State: modelStepDone, Context: claim.Context, Resolved: resolved}, nil
}

func (e AgentExecutor) recordNormalPreSendFailure(
	ctx context.Context,
	input ModelWorkExecution,
	claim executionstore.ModelCallClaim,
	resolved model.ResolvedClient,
	cause error,
	failure modelretry.PreSendFailure,
) (modelStep, error) {
	return e.recordNormalFailure(
		ctx,
		input,
		claim,
		resolved,
		modelretry.NormalizePreSendFailure(cause, failure),
		false,
		model.Response{},
	)
}
