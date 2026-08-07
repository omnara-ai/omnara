package kernel

import (
	"context"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/compaction"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelretry"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
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
	preSendErrorCodeBuildModelSelectionFailed = "build_model_selection_failed"
	preSendErrorCodeResolveModelFailed        = "resolve_model_failed"
	preSendErrorCodeBuildModelContextFailed   = "build_model_context_failed"
	preSendErrorCodeLoadReplayPolicyFailed    = "load_provider_replay_policy_failed"
	preSendErrorCodePrepareModelRequestFailed = "prepare_model_request_failed"
)

type modelStep struct {
	State               modelStepState
	Context             executionstore.ModelCallContextRecord
	Bundle              modelcontext.Bundle
	Envelope            modelenvelope.ResponseEnvelope
	Response            model.Response
	Resolved            model.ResolvedClient
	StreamedToolCallIDs map[string]storage.ID
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
		if !claim.Claimed {
			return modelStep{State: modelStepWaiting, Context: claim.Context}, nil
		}
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
		state := modelStepWaiting
		if claim.Context.State != executionstore.ModelCallContextStarted &&
			claim.Context.RecoveryKind != executionstore.ModelCallRecoveryRetry {
			state = modelStepDone
		}
		return modelStep{State: state, Context: claim.Context}, nil
	}
	contract, err := agentconfig.RuntimeContractFromCompiled(
		snapshot.AgentConfig.CompiledDefinition,
		snapshot.AgentConfig.CompilerVersion,
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
	mcpInitialization := mcpInitializationNone
	switch input.Kind {
	case executionstore.ModelWorkStart:
		mcpInitialization = mcpInitializationOpening
	case executionstore.ModelWorkResume:
		mcpInitialization = mcpInitializationResume
	case executionstore.ModelWorkContinue:
	}
	if mcpInitialization != mcpInitializationNone {
		if err := e.ensureMCPConnections(
			ctx,
			claim.Context.OrgID,
			input,
			contract,
			mcpInitialization,
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
	selection, err := modelSelectionForContext(claim.Context, contract.Model)
	if err != nil {
		return e.recordNormalPreSendFailure(
			ctx, input, claim, model.ResolvedClient{}, err,
			modelretry.PreSendFailure{
				Code:    preSendErrorCodeBuildModelSelectionFailed,
				Message: "Omnara could not construct the configured model selection.",
			},
		)
	}
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
	policy := model.RequestPolicyFromCapabilities(capabilities)
	if err := ensureModelSupportsContractTools(client, capabilities, contract); err != nil {
		cause := model.ProviderError{
			Kind:    model.ErrorKindInvalidRequest,
			Source:  "model_capabilities",
			Code:    "required_tools_unsupported",
			Message: err.Error(),
		}
		return e.recordNormalFailure(ctx, input, claim, resolved, cause, false, model.Response{})
	}
	bundle, err := builder.Build(ctx, modelcontext.BuildInput{
		ProjectID:           input.ProjectID,
		AgentID:             input.AgentID,
		TurnID:              input.TurnID,
		OpeningInputIDs:     input.InputIDs,
		Now:                 input.Now,
		AgentConfigSnapshot: &snapshot,
		MediaProjector:      model.MediaProjectorForClient(client),
		ModelWindow:         model.ModelWindowForRequest(capabilities, policy),
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

	policy.SuppressProviderReplay, err = e.Store.Execution().ModelCallOperationHasFailedWithErrorKind(
		ctx,
		input.ProjectID,
		input.AgentID,
		claim.Context.ID,
		model.ErrorKindReplayRejected,
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
	prepared, err := model.PrepareForSend(
		ctx,
		client,
		bundle,
		policy,
		modelErrorSourceForClient(client),
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
	request := model.Request{
		ProviderRequest: prepared.Body,
	}
	var streamSink *harnessStreamSink
	if claim.Context.AttemptNumber == 1 {
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
		return e.recordNormalFailure(ctx, input, claim, resolved, err, true, response)
	}
	response = model.WithoutToolCallsOnMaxTokens(response)
	if err := model.ValidateProviderResponse(response); err != nil {
		return e.recordNormalFailure(
			ctx,
			input,
			claim,
			resolved,
			model.MalformedProviderResponse(string(apiFormat), err),
			true,
			model.ResponseEvidenceForStorage(response),
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
			model.ResponseEvidenceForStorage(response),
		)
	}
	step := modelStep{
		Context:             claim.Context,
		Bundle:              bundle,
		Envelope:            envelope,
		Response:            response,
		Resolved:            resolved,
		StreamedToolCallIDs: streamSink.ToolCallIDs(),
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
	if cause, invalid := invalidModelToolCallResponse(
		errorSource,
		calls,
	); invalid {
		return e.recordNormalFailure(
			ctx,
			input,
			executionstore.ModelCallClaim{Context: step.Context, Claimed: true},
			step.Resolved,
			cause,
			true,
			step.Response,
		)
	}
	switch reason {
	case model.StopReasonToolUse:
		if len(calls) == 0 {
			cause := model.MalformedProviderSuccess(
				errorSource,
				string(reason),
				"The model stopped for tool use without returning a supported tool call.",
				nil,
			)
			return e.recordNormalFailure(
				ctx,
				input,
				executionstore.ModelCallClaim{Context: step.Context, Claimed: true},
				step.Resolved,
				cause,
				true,
				step.Response,
			)
		}
		step.State = modelStepToolUse
		return step, nil
	case model.StopReasonEndTurn:
		return e.recordSuccessfulModelOutput(ctx, input, step)
	case model.StopReasonRefusal, model.StopReasonContentFilter:
		if len(calls) > 0 {
			cause := model.MalformedProviderSuccess(
				errorSource,
				"contradictory_stop_reason",
				fmt.Sprintf("The model returned stop reason %q together with tool calls.", reason),
				nil,
			)
			return e.recordNormalFailure(
				ctx,
				input,
				executionstore.ModelCallClaim{Context: step.Context, Claimed: true},
				step.Resolved,
				cause,
				true,
				step.Response,
			)
		}
		return e.recordSuccessfulModelOutput(ctx, input, step)
	case model.StopReasonMaxTokens:
		return e.recordSuccessfulModelOutput(ctx, input, step)
	case model.StopReasonContextWindow:
		cause := model.ProviderError{
			Kind:    model.ErrorKindContextWindow,
			Source:  errorSource,
			Code:    string(reason),
			Message: "The model provider reported that the context window was exceeded.",
		}
		return e.recordNormalFailure(
			ctx,
			input,
			executionstore.ModelCallClaim{Context: step.Context, Claimed: true},
			step.Resolved,
			cause,
			true,
			step.Response,
		)
	case model.StopReasonPause:
		cause := model.ProviderError{
			Kind:    model.ErrorKindInvalidRequest,
			Source:  errorSource,
			Code:    string(reason),
			Message: fmt.Sprintf("The model returned unsupported stop reason %q.", reason),
		}
		return e.recordNormalFailure(
			ctx,
			input,
			executionstore.ModelCallClaim{Context: step.Context, Claimed: true},
			step.Resolved,
			cause,
			true,
			step.Response,
		)
	case model.StopReasonUnknown:
		cause := model.MalformedProviderSuccess(
			errorSource,
			string(reason),
			fmt.Sprintf("The model returned unsupported stop reason %q.", reason),
			nil,
		)
		return e.recordNormalFailure(
			ctx,
			input,
			executionstore.ModelCallClaim{Context: step.Context, Claimed: true},
			step.Resolved,
			cause,
			true,
			step.Response,
		)
	default:
		cause := model.MalformedProviderSuccess(
			errorSource,
			string(reason),
			fmt.Sprintf("The model returned unknown stop reason %q.", reason),
			nil,
		)
		return e.recordNormalFailure(
			ctx,
			input,
			executionstore.ModelCallClaim{Context: step.Context, Claimed: true},
			step.Resolved,
			cause,
			true,
			step.Response,
		)
	}
}

func (e AgentExecutor) recordSuccessfulModelOutput(
	ctx context.Context,
	input ModelWorkExecution,
	step modelStep,
) (modelStep, error) {
	_, err := e.Store.Execution().RecordModelOutputAndCompleteContext(
		ctx,
		executionstore.RecordModelOutputAndCompleteContextInput{
			ProjectID:          input.ProjectID,
			AgentID:            input.AgentID,
			RuntimeLockID:      input.RuntimeLockID,
			ModelCallContextID: step.Context.ID,
			ProviderRequestID:  step.Response.ProviderRequestID,
			ProviderResponse:   step.Envelope,
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
	response = model.ResponseEvidenceForStorage(response)
	now := e.now()
	evidence, decision := modelretry.Decide(
		cause,
		modelretry.Attempt{Number: claim.Context.AttemptNumber},
		claim.Context.ID.String(),
		now, modelretry.CompactOnInputOverflow)
	if decision.Action == modelretry.ActionRetry {
		decision.RetryDelay = e.modelRetryDelay(decision.RetryDelay)
	}

	var plan compaction.Plan
	if decision.Action == modelretry.ActionCompact {
		planned, ok, err := e.planCompactionForContext(
			ctx,
			claim.Context,
			resolved.Client,
			input,
		)
		if err != nil {
			return modelStep{}, err
		}
		if !ok {
			compactionTrigger, err := marshalJSON(map[string]any{
				"compaction_trigger": map[string]any{
					"kind":    evidence.Kind,
					"code":    evidence.Code,
					"message": evidence.Message,
					"details": evidence.Details,
				},
			})
			if err != nil {
				return modelStep{}, err
			}
			cause = model.ProviderError{
				Kind:      model.ErrorKindContextWindow,
				Source:    modelErrorSourceForClient(resolved.Client),
				Code:      "context_cannot_be_compacted",
				Message:   "The current model input is too large and has no closed event prefix that can be compacted safely.",
				RequestID: evidence.RequestID,
				Metadata:  compactionTrigger,
			}
			evidence = modelretry.EvidenceFor(cause)
			decision.Action = modelretry.ActionStop
		} else {
			plan = planned
		}
	}
	apiFormat, apiVariant, _ := model.APIIdentityForClient(resolved.Client)
	servedSlug := ""
	providerRequestID := ""
	providerResponseID := ""
	usage := modelenvelope.Usage{}
	if providerRequestStarted {
		servedSlug = response.ServedProviderModelSlug
		providerRequestID = evidence.RequestID
		if providerRequestID == "" {
			providerRequestID = response.ProviderRequestID
		}
		providerResponseID = response.ID
		usage = response.Usage
	}
	if decision.Action == modelretry.ActionRetry || decision.Action == modelretry.ActionCompact {
		recoveryKind := executionstore.ModelCallRecoveryRetry
		if decision.Action == modelretry.ActionCompact {
			recoveryKind = executionstore.ModelCallRecoveryCompact
		}
		failureInput := executionstore.RecordRecoverableModelCallFailureInput{
			ProjectID:          input.ProjectID,
			AgentID:            input.AgentID,
			ModelCallContextID: claim.Context.ID,
			RuntimeLockID:      input.RuntimeLockID,
			RecoveryKind:       recoveryKind,
			APIFormat:          apiFormat,
			APIVariant:         apiVariant,
			ProviderRequestID:  providerRequestID,
			ProviderResponseID: providerResponseID,
			ErrorKind:          evidence.Kind,
			ErrorCode:          evidence.Code,
			ErrorMessage:       evidence.Message,
			ErrorDetails:       evidence.Details,
			RetryDelay:         decision.RetryDelay,
			Usage:              usage,
		}
		var (
			contextRecord executionstore.ModelCallContextRecord
			err           error
		)
		compactionBoundaryPreempted := false
		var compactionClaim executionstore.ModelCallClaim
		if decision.Action == modelretry.ActionCompact {
			handoff, handoffErr := e.Store.Execution().RecordModelCallFailureAndClaimCompaction(
				ctx,
				executionstore.RecordModelCallFailureAndClaimCompactionInput{
					ParentContextID:        claim.Context.ID,
					Failure:                failureInput,
					SourceEventSequenceEnd: plan.EventSequenceEnd,
				},
			)
			contextRecord, err = handoff.ParentContext, handoffErr
			compactionBoundaryPreempted = handoff.BoundaryPreempted
			compactionClaim = handoff.CompactionCall
		} else {
			contextRecord, err = e.Store.Execution().RecordRetryableModelCallFailure(ctx, failureInput)
		}
		if err != nil {
			return modelStep{}, errors.Join(cause, err)
		}
		if decision.Action == modelretry.ActionCompact && !compactionBoundaryPreempted {
			if _, err := (compaction.Runner{
				Store:           compaction.NewStore(e.Store.Execution()),
				Resolver:        e.ModelResolver,
				ContextBuilder:  e.contextBuilder(),
				Now:             e.Now,
				ModelRetryDelay: e.ModelRetryDelay,
			}).RunClaimed(ctx, compaction.RunInput{
				Plan:                     plan,
				TurnID:                   input.TurnID,
				OpeningInputIDs:          input.InputIDs,
				OpeningEventSequence:     input.OpeningEventSequence,
				RuntimeLockID:            input.RuntimeLockID,
				ParentModelCallContextID: claim.Context.ID,
			}, compactionClaim); err != nil {
				return modelStep{}, err
			}
		}
		return modelStep{State: modelStepWaiting, Context: contextRecord, Resolved: resolved}, nil
	}
	_, err := e.Store.Execution().RecordModelCallErrorAndCompleteContext(
		ctx,
		executionstore.RecordModelCallErrorAndCompleteContextInput{
			ProjectID:               input.ProjectID,
			AgentID:                 input.AgentID,
			RuntimeLockID:           input.RuntimeLockID,
			ModelCallContextID:      claim.Context.ID,
			APIFormat:               apiFormat,
			APIVariant:              apiVariant,
			ServedProviderModelSlug: servedSlug,
			ProviderRequestID:       providerRequestID,
			ProviderResponseID:      providerResponseID,
			ErrorKind:               evidence.Kind,
			ErrorCode:               evidence.Code,
			ErrorMessage:            evidence.Message,
			ErrorDetails:            evidence.Details,
			Usage:                   usage,
		},
	)
	if err != nil {
		return modelStep{}, errors.Join(cause, err)
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
