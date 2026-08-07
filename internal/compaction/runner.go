package compaction

import (
	"context"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelretry"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (r Runner) Run(ctx context.Context, input RunInput) (RunResult, error) {
	return r.run(ctx, input, nil)
}

func (r Runner) RunClaimed(
	ctx context.Context,
	input RunInput,
	claim executionstore.ModelCallClaim,
) (RunResult, error) {
	return r.run(ctx, input, &claim)
}

func (r Runner) run(
	ctx context.Context,
	input RunInput,
	initialClaim *executionstore.ModelCallClaim,
) (RunResult, error) {
	if err := r.validate(input); err != nil {
		return RunResult{}, err
	}
	protectOpening, err := r.openingEventsRequireVerbatimRetention(ctx, input)
	if err != nil {
		return RunResult{}, err
	}
	if protectOpening && input.Plan.EventSequenceEnd >= input.OpeningEventSequence {
		return RunResult{}, errors.New("compaction must retain unanswered opening events")
	}

	prior, hasPrior, err := r.Store.GetLatestApplicableContextCheckpoint(
		ctx,
		input.Plan.ProjectID,
		input.Plan.AgentID,
		input.Plan.InputEventSequence,
	)
	if err != nil {
		return RunResult{}, err
	}
	priorSummary := ""
	wantStart := int64(1)
	if hasPrior {
		priorSummary = prior.Summary
		wantStart = prior.SummarizedThroughEventSequence + 1
	}
	if input.Plan.EventSequenceStart != wantStart {
		return RunResult{}, fmt.Errorf(
			"compaction source starts at %d, want cumulative boundary %d",
			input.Plan.EventSequenceStart,
			wantStart,
		)
	}

	snapshot, err := r.Store.CaptureAgentConfigForEventWatermark(
		ctx,
		input.Plan.ProjectID,
		input.Plan.AgentID,
		input.Plan.InputEventSequence,
	)
	if err != nil {
		return RunResult{}, err
	}
	var claim executionstore.ModelCallClaim
	if initialClaim == nil {
		claim, err = r.Store.ClaimCompactionModelCall(
			ctx,
			executionstore.ClaimCompactionModelCallInput{
				ProjectID:              input.Plan.ProjectID,
				AgentID:                input.Plan.AgentID,
				RuntimeLockID:          input.RuntimeLockID,
				InputEventSequence:     input.Plan.InputEventSequence,
				SourceEventSequenceEnd: input.Plan.EventSequenceEnd,
				ParentContextID:        input.ParentModelCallContextID,
			},
		)
		if err != nil {
			return RunResult{}, err
		}
	} else {
		claim = *initialClaim
		if err := validateInitialCompactionClaim(input, claim); err != nil {
			return RunResult{}, err
		}
	}
	if !claim.Claimed && claim.Context.State == executionstore.ModelCallContextFailed &&
		claim.Context.RecoveryKind == executionstore.ModelCallRecoveryRetry {
		claim, err = r.Store.ClaimNextModelCallContext(
			ctx,
			executionstore.ClaimNextModelCallContextInput{
				ProjectID:                     input.Plan.ProjectID,
				AgentID:                       input.Plan.AgentID,
				PredecessorModelCallContextID: claim.Context.ID,
				RuntimeLockID:                 input.RuntimeLockID,
			},
		)
		if err != nil {
			return RunResult{}, err
		}
	}
	if !claim.Claimed {
		return r.resultForUnclaimed(ctx, claim)
	}
	selection, err := compactionModelSelection(claim.Context, snapshot)
	if err != nil {
		return r.recordPreSendFailure(
			ctx, input, claim, err,
			modelretry.PreSendFailure{
				Code: compactionErrorCodeBuildModelSelectionFailed,
				Message: "Omnara could not construct the configured model selection " +
					"for compaction.",
			},
			providerAttemptEvidence{},
		)
	}
	resolved, err := r.Resolver.Resolve(ctx, selection)
	if err != nil {
		if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return RunResult{}, err
		}
		return r.recordPreSendFailure(
			ctx, input, claim, err,
			modelretry.PreSendFailure{
				Code:    compactionErrorCodeResolveModelFailed,
				Message: "Omnara could not resolve the configured model for compaction.",
			},
			providerAttemptEvidence{},
		)
	}

	if err := validateResolvedRevision(resolved, claim.Context.ConfiguredModelRevisionID); err != nil {
		return r.recordFailure(ctx, input, claim, err, providerAttemptEvidence{})
	}
	client := resolved.Client
	apiFormat, apiVariant, hasAPIIdentity := model.APIIdentityForClient(client)
	if !hasAPIIdentity {
		return r.recordFailure(
			ctx,
			input,
			claim,
			model.ProviderError{
				Kind:    model.ErrorKindInvalidRequest,
				Source:  "model",
				Code:    "missing_api_identity",
				Message: fmt.Sprintf("model client %T does not declare an API format and variant", client),
			},
			providerAttemptEvidence{},
		)
	}
	providerAttempt := providerAttemptEvidence{APIFormat: apiFormat, APIVariant: apiVariant}
	policy := compactionRequestPolicy(client)
	policy.SuppressProviderReplay, err = r.Store.ModelCallOperationHasFailedWithErrorKind(
		ctx,
		input.Plan.ProjectID,
		input.Plan.AgentID,
		claim.Context.ID,
		model.ErrorKindReplayRejected,
	)
	if err != nil {
		return r.recordPreSendFailure(
			ctx, input, claim, err,
			modelretry.PreSendFailure{
				Code: compactionErrorCodeLoadReplayPolicyFailed,
				Message: "Omnara could not determine whether provider replay is safe " +
					"for compaction.",
			},
			providerAttempt,
		)
	}
	boundaryWindow, err := r.loadCompactionBoundaryWindow(ctx, input.Plan)
	if err != nil {
		return r.recordPreSendFailure(
			ctx, input, claim, err,
			modelretry.PreSendFailure{
				Code:    compactionErrorCodeLoadSourceFailed,
				Message: "Omnara could not load the durable source events for compaction.",
			},
			providerAttempt,
		)
	}
	preparedRequest, err := largestFittingCompactionRequest(
		ctx,
		input,
		priorSummary,
		boundaryWindow.sourceEvents,
		boundaryWindow.witnessEvents,
		boundaryWindow.atomicGroups,
		client,
		policy,
		modelErrorSourceForAPIFormat(apiFormat),
	)
	if err != nil {
		return r.recordPreSendFailure(
			ctx, input, claim, err,
			modelretry.PreSendFailure{
				Code:    compactionErrorCodePrepareRequestFailed,
				Message: "Omnara could not prepare the compaction request.",
			},
			providerAttempt,
		)
	}
	if preparedRequest.sourceEnd == 0 {
		return r.recordFailure(
			ctx,
			input,
			claim,
			irreducibleCompactionError("no closed source prefix fits the configured model window"),
			providerAttempt,
		)
	}
	if preparedRequest.sourceEnd < input.Plan.EventSequenceEnd {
		cause := preparedRequest.adjustmentCause
		if cause == nil {
			cause = compactionSourceAdjustmentError(
				boundaryWindow.sourceEvents,
				boundaryWindow.witnessEvents,
				boundaryWindow.atomicGroups,
				input.Plan.EventSequenceEnd,
			)
		}
		return r.replaceCompactionSource(
			ctx,
			input,
			claim,
			cause,
			providerAttempt,
			preparedRequest.sourceEnd,
		)
	}
	prepared := preparedRequest.prepared
	sourceText := preparedRequest.sourceText

	response, err := client.Respond(
		ctx,
		model.Request{
			ProviderRequest: prepared.Body,
		},
	)
	providerAttempt.ProviderRequestStarted = true
	providerAttempt.Response = response
	if err != nil {
		if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return RunResult{}, err
		}
		if _, classified := model.ClassifyError(err); !classified {
			return RunResult{}, err
		}
		return r.recordFailure(ctx, input, claim, err, providerAttempt)
	}
	response = model.WithoutToolCallsOnMaxTokens(response)
	providerAttempt.Response = response
	errorSource := modelErrorSourceForAPIFormat(apiFormat)
	if err := model.ValidateProviderResponse(response); err != nil {
		return r.recordFailure(
			ctx,
			input,
			claim,
			model.MalformedProviderResponse(errorSource, err),
			providerAttempt,
		)
	}
	summary, err := validateCompactionResponse(errorSource, response)
	if err != nil {
		return r.recordFailure(ctx, input, claim, err, providerAttempt)
	}
	if err := validateSummaryReduction(priorSummary, sourceText, summary); err != nil {
		return r.recordFailure(ctx, input, claim, err, providerAttempt)
	}
	candidateBundle, candidateErr := r.ContextBuilder.Build(ctx, modelcontext.BuildInput{
		ProjectID:           input.Plan.ProjectID,
		AgentID:             input.Plan.AgentID,
		TurnID:              input.TurnID,
		OpeningInputIDs:     input.OpeningInputIDs,
		Now:                 r.now(),
		AgentConfigSnapshot: &snapshot,
		MediaProjector:      model.MediaProjectorForClient(client),
		ModelWindow: model.ModelWindowForRequest(
			model.CapabilitiesForClient(client),
			model.RequestPolicyFromCapabilities(model.CapabilitiesForClient(client)),
		),
		CheckpointOverride: &modelcontext.CheckpointRef{
			SummarizedThroughEventSequence: input.Plan.EventSequenceEnd,
			Summary:                        summary,
		},
	})
	if candidateErr == nil {
		_, candidateErr = model.PrepareForSend(
			ctx,
			client,
			candidateBundle,
			model.RequestPolicyFromCapabilities(model.CapabilitiesForClient(client)),
			modelErrorSourceForAPIFormat(apiFormat),
		)
	}
	if candidateErr != nil {
		if !requestSizeError(candidateErr) {
			if _, classified := model.ClassifyError(candidateErr); classified {
				return r.recordFailure(
					ctx,
					input,
					claim,
					candidateErr,
					providerAttempt,
				)
			}
			return RunResult{}, fmt.Errorf(
				"validate projected context with candidate checkpoint: %w",
				candidateErr,
			)
		}
		hasRemainingSource, err := r.hasRemainingSemanticSource(ctx, input, protectOpening)
		if err != nil {
			return RunResult{}, err
		}
		checkpointCount, err := r.Store.CountConsecutiveContextCheckpointLineage(
			ctx,
			input.Plan.ProjectID,
			input.Plan.AgentID,
			input.Plan.InputEventSequence,
		)
		if err != nil {
			return RunResult{}, err
		}
		progressionInput := ProgressiveCheckpointInput{
			CandidateSummarizedThroughSequence: input.Plan.EventSequenceEnd,
			HasRemainingSemanticSource:         hasRemainingSource,
			ConsecutiveCheckpointCount:         checkpointCount,
		}
		if hasPrior {
			progressionInput.PriorSummarizedThroughEventSequence = prior.SummarizedThroughEventSequence
		}
		decision, policyErr := r.progressivePolicy().Evaluate(progressionInput)
		if policyErr != nil {
			return RunResult{}, policyErr
		}
		if !decision.Allowed {
			return r.recordFailure(
				ctx,
				input,
				claim,
				irreducibleCompactionError(decision.Reason),
				providerAttempt,
			)
		}
	}
	checkpoint, err := r.Store.PublishContextCheckpoint(
		ctx,
		executionstore.PublishContextCheckpointInput{
			ProjectID:          input.Plan.ProjectID,
			AgentID:            input.Plan.AgentID,
			RuntimeLockID:      input.RuntimeLockID,
			ModelCallContextID: claim.Context.ID,
			Summary:            summary,
			APIFormat:          apiFormat,
			APIVariant:         apiVariant,
			ProviderRequestID:  response.ProviderRequestID,
			ProviderResponseID: response.ID,
			Usage:              response.Usage,
		},
	)
	if err != nil {
		if errors.Is(err, executionstore.ErrCheckpointBoundaryUnsafe) {
			nextEnd, adjustmentErr := r.nextSmallerSourceEnd(ctx, input.Plan)
			if adjustmentErr != nil {
				return RunResult{}, errors.Join(err, adjustmentErr)
			}
			cause := model.ProviderError{
				Kind:    model.ErrorKindInvalidRequest,
				Source:  "compaction",
				Code:    "compaction_source_boundary_adjustment",
				Message: "The planned compaction source does not end at a safe model-visible boundary.",
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
			return r.recordFailure(
				ctx,
				input,
				claim,
				irreducibleCompactionError("no safe closed source prefix remains"),
				providerAttempt,
			)
		}
		return RunResult{}, err
	}
	return RunResult{
		State:              RunCompleted,
		ModelCallContextID: claim.Context.ID,
		Checkpoint:         &checkpoint,
	}, nil
}

func validateInitialCompactionClaim(input RunInput, claim executionstore.ModelCallClaim) error {
	contextRow := claim.Context
	if !claim.Created || !claim.Claimed || contextRow.ID == storage.NilID ||
		contextRow.ProjectID != input.Plan.ProjectID ||
		contextRow.AgentID != input.Plan.AgentID ||
		contextRow.OperationKind != executionstore.ModelCallOperationCompaction ||
		contextRow.InputEventSequence != input.Plan.InputEventSequence ||
		contextRow.SourceEventSequenceEnd == nil ||
		*contextRow.SourceEventSequenceEnd != input.Plan.EventSequenceEnd ||
		contextRow.RuntimeLockID != input.RuntimeLockID ||
		contextRow.State != executionstore.ModelCallContextStarted {
		return fmt.Errorf(
			"initial compaction claim does not match the requested operation: %w",
			storeerr.ErrStateTransitionConflict,
		)
	}
	return nil
}

func (r Runner) recordPreSendFailure(
	ctx context.Context,
	input RunInput,
	claim executionstore.ModelCallClaim,
	cause error,
	failure modelretry.PreSendFailure,
	providerAttempt providerAttemptEvidence,
) (RunResult, error) {
	return r.recordFailure(
		ctx,
		input,
		claim,
		modelretry.NormalizePreSendFailure(cause, failure),
		providerAttempt,
	)
}

func (r Runner) validate(input RunInput) error {
	if r.Store == nil {
		return errors.New("compaction store is required")
	}
	if r.Resolver == nil {
		return errors.New("compaction model resolver is required")
	}
	if r.ContextBuilder == nil {
		return errors.New("normal model context builder is required")
	}
	if input.Plan.ProjectID == storage.NilID || input.Plan.AgentID == storage.NilID ||
		input.Plan.InputEventSequence <= 0 || input.Plan.EventSequenceStart <= 0 ||
		input.Plan.EventSequenceEnd < input.Plan.EventSequenceStart ||
		input.Plan.EventSequenceEnd > input.Plan.InputEventSequence {
		return errors.New("valid cumulative compaction plan is required")
	}
	if input.TurnID == storage.NilID || len(input.OpeningInputIDs) == 0 ||
		input.RuntimeLockID == storage.NilID ||
		input.ParentModelCallContextID == storage.NilID {
		return errors.New(
			"turn, opening input ids, runtime lock, and parent model context are required",
		)
	}
	if input.OpeningEventSequence <= 0 || input.OpeningEventSequence > input.Plan.InputEventSequence {
		return errors.New("compaction opening events exceed its model context frontier")
	}
	return nil
}

func (r Runner) resultForUnclaimed(ctx context.Context, claim executionstore.ModelCallClaim) (RunResult, error) {
	result := RunResult{
		ModelCallContextID: claim.Context.ID,
	}
	switch claim.Context.State {
	case executionstore.ModelCallContextSucceeded:
		checkpoint, found, err := r.Store.GetContextCheckpointByProducerContext(
			ctx,
			claim.Context.ProjectID,
			claim.Context.AgentID,
			claim.Context.ID,
		)
		if err != nil {
			return RunResult{}, err
		}
		if !found {
			return RunResult{}, errors.New("succeeded compaction context has no checkpoint")
		}
		result.State = RunCompleted
		result.Checkpoint = &checkpoint
		return result, nil
	case executionstore.ModelCallContextFailed:
		if claim.Context.RecoveryKind == executionstore.ModelCallRecoveryRetry {
			result.State = RunRetryScheduled
			result.RetryAt = claim.Context.RetryAt
			return result, nil
		}
		result.State = RunTerminal
		return result, nil
	case executionstore.ModelCallContextCanceled:
		result.State = RunTerminal
		return result, nil
	case executionstore.ModelCallContextStarted:
		return RunResult{}, storeerr.ErrAgentNotAdvanceable
	default:
		return RunResult{}, fmt.Errorf("unsupported compaction context state %q", claim.Context.State)
	}
}
