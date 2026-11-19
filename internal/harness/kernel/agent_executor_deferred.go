package kernel

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelbatch"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelretry"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

// ErrModelWorkParked reports that the model call was handed to a deferred
// provider job. The runtime lock is still held and must not be released: it
// stays parked, with no goroutine attached, until the job is resumed.
var ErrModelWorkParked = errors.New("model work is parked on a deferred provider job")

const (
	defaultDeferredPollInterval = 2 * time.Minute
	defaultDeferredWindow       = 24 * time.Hour
	// maxDeferredWindow caps provider-reported deadlines; Vertex batches may
	// run for up to 96 hours.
	maxDeferredWindow = 7 * 24 * time.Hour
	// deferredDeadlineGrace keeps the parked lock past the provider deadline so
	// a final result that lands at the deadline can still be collected.
	deferredDeadlineGrace = time.Hour
)

func (e AgentExecutor) deferredPollInterval() time.Duration {
	if e.DeferredPollInterval > 0 {
		return e.DeferredPollInterval
	}
	return defaultDeferredPollInterval
}

// submitDeferredModelCall submits the prepared request as a deferred provider
// job and parks the runtime lock. It returns ErrModelWorkParked on success.
func (e AgentExecutor) submitDeferredModelCall(
	ctx context.Context,
	input ModelWorkExecution,
	claim executionstore.ModelCallClaim,
	resolved model.ResolvedClient,
	client model.DeferredClient,
	policy model.RequestPolicy,
	prepared model.PreparedRequest,
) (modelStep, error) {
	requestID, err := uuid.NewV7()
	if err != nil {
		return modelStep{}, err
	}
	token, tokenHash, err := modelbatch.NewCallbackToken()
	if err != nil {
		return modelStep{}, err
	}
	customID := claim.Context.ID.String()
	// The request row exists before the provider can call back, so an early
	// callback is remembered and honored when the lock is parked.
	if err := e.Store.Execution().CreateModelBatchRequest(ctx, executionstore.CreateModelBatchRequestInput{
		ID:                   requestID,
		ProjectID:            input.ProjectID,
		AgentID:              input.AgentID,
		RuntimeLockID:        input.RuntimeLockID,
		ModelCallContextID:   claim.Context.ID,
		TurnID:               input.TurnID,
		OpeningInputIDs:      input.InputIDs,
		OpeningEventSequence: input.OpeningEventSequence,
		CallbackTokenHash:    tokenHash,
		CustomID:             customID,
		InputFingerprint:     prepared.InputFingerprint,
	}); err != nil {
		return modelStep{}, err
	}
	callbackURL := ""
	if e.DeferredCallbackURL != nil {
		callbackURL = e.DeferredCallbackURL(requestID, token)
	}
	callCtx := model.WithCallScope(ctx, model.CallScope{
		Purpose:            model.CallPurposeAgentTurn,
		ModelCallContextID: claim.Context.ID.String(),
		Attempt:            claim.Context.AttemptNumber,
	})
	submission, err := client.SubmitDeferred(callCtx, model.DeferredRequest{
		ProviderRequest: prepared.Body,
		CustomID:        customID,
		CallbackURL:     callbackURL,
	})
	if err != nil {
		e.forgetBatchRequest(ctx, input.RuntimeLockID, requestID)
		return e.completeModelCall(
			ctx, input, claim, resolved, modelcontext.Bundle{}, policy, prepared.InputFingerprint,
			model.Response{}, err, nil,
		)
	}
	holdFor := defaultDeferredWindow
	if !submission.ExpiresAt.IsZero() {
		holdFor = min(max(submission.ExpiresAt.Sub(e.now()), time.Minute), maxDeferredWindow)
	}
	if _, err := e.Store.Execution().ParkModelBatchRuntime(ctx, executionstore.ParkModelBatchRuntimeInput{
		ID:              requestID,
		ProjectID:       input.ProjectID,
		AgentID:         input.AgentID,
		RuntimeLockID:   input.RuntimeLockID,
		ProviderBatchID: submission.ProviderHandle,
		HoldFor:         holdFor + deferredDeadlineGrace,
		PollDelay:       e.deferredPollInterval(),
	}); err != nil {
		e.forgetBatchRequest(ctx, input.RuntimeLockID, requestID)
		return modelStep{}, err
	}
	return modelStep{}, ErrModelWorkParked
}

// forgetBatchRequest drops a batch request that will not be resumed. Failures
// are harmless: a request without a live runtime lock is never claimed and is
// purged by the runtime lock reaper.
func (e AgentExecutor) forgetBatchRequest(ctx context.Context, runtimeLockID, requestID uuid.UUID) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = e.Store.Execution().DeleteModelBatchRequest(cleanupCtx, runtimeLockID, requestID)
}

// collectDeferredModelWork picks a parked model call back up: it reads the
// deferred job and either records its outcome or parks the lock again.
func (e AgentExecutor) collectDeferredModelWork(
	ctx context.Context,
	input ModelWorkExecution,
	builder modelcontext.Builder,
	resolver model.Resolver,
) (modelStep, error) {
	store := e.Store.Execution()
	request, err := store.GetModelBatchRequest(
		ctx, input.ProjectID, input.AgentID, input.RuntimeLockID, input.BatchRequestID,
	)
	if err != nil {
		return modelStep{}, err
	}
	contextRow, found, err := store.GetModelCallContext(ctx, input.ProjectID, input.AgentID, request.ModelCallContextID)
	if err != nil {
		return modelStep{}, err
	}
	if !found || contextRow.State != executionstore.ModelCallContextStarted ||
		contextRow.RuntimeLockID != input.RuntimeLockID {
		// Canceled or torn down while parked; releasing the lock is all that is left.
		e.forgetBatchRequest(ctx, input.RuntimeLockID, request.ID)
		return modelStep{State: modelStepDone, Context: contextRow}, nil
	}
	claim := executionstore.ModelCallClaim{Context: contextRow, Claimed: true}
	snapshot, err := store.CaptureAgentConfigForEventWatermark(
		ctx, input.ProjectID, input.AgentID, contextRow.InputEventSequence,
	)
	if err != nil {
		return modelStep{}, err
	}
	contract, err := agentconfig.RuntimeContractFromCompiled(
		snapshot.AgentConfig.CompiledDefinition,
		snapshot.AgentConfig.EffectiveDefinitionHash,
	)
	if err != nil {
		return modelStep{}, err
	}
	resolved, err := resolver.Resolve(ctx, modelSelectionForContext(contextRow, contract.Model))
	if err != nil {
		if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return modelStep{}, err
		}
		return e.recordNormalFailure(ctx, input, claim, model.ResolvedClient{}, modelretry.NormalizePreSendFailure(
			err, modelretry.PreSendFailure{
				Code:    preSendErrorCodeResolveModelFailed,
				Message: "Omnara could not resolve the configured model to collect its batch result.",
			}), true, model.Response{})
	}
	if err := validateResolvedModelContext(resolved, contextRow); err != nil {
		return e.recordNormalFailure(ctx, input, claim, resolved, err, true, model.Response{})
	}
	client, ok := resolved.Client.(model.DeferredClient)
	if !ok {
		cause := model.ProviderError{
			Kind:    model.ErrorKindInvalidRequest,
			Source:  "model",
			Code:    "deferred_client_unavailable",
			Message: "The configured model no longer supports collecting a deferred response.",
		}
		return e.recordNormalFailure(ctx, input, claim, resolved, cause, true, model.Response{})
	}
	callCtx := model.WithCallScope(ctx, model.CallScope{
		Purpose:            model.CallPurposeAgentTurn,
		ModelCallContextID: contextRow.ID.String(),
		Attempt:            contextRow.AttemptNumber,
	})
	response, respErr := client.CollectDeferred(callCtx, model.DeferredHandle{
		ProviderHandle: request.ProviderBatchID,
		CustomID:       request.CustomID,
	})
	if errors.Is(respErr, model.ErrDeferredPending) {
		if _, err := store.ParkModelBatchRuntime(ctx, executionstore.ParkModelBatchRuntimeInput{
			ID:            request.ID,
			ProjectID:     input.ProjectID,
			AgentID:       input.AgentID,
			RuntimeLockID: input.RuntimeLockID,
			// Only used if the first park never recorded a deadline.
			HoldFor:   defaultDeferredWindow,
			PollDelay: e.deferredPollInterval(),
		}); err != nil {
			return modelStep{}, err
		}
		return modelStep{}, ErrModelWorkParked
	}
	// The batch has a final outcome; from here the request is never resumed.
	defer e.forgetBatchRequest(ctx, input.RuntimeLockID, request.ID)
	capabilities := model.CapabilitiesForClient(client)
	policy := model.RequestPolicyFromCapabilities(capabilities)
	if loaded, policyErr := modelretry.RequestPolicyForModelCall(
		ctx, store, input.ProjectID, input.AgentID, contextRow.ID, policy,
	); policyErr == nil {
		policy = loaded
	}
	var bundle modelcontext.Bundle
	if respErr == nil {
		// The bundle is only needed to bind tool calls to their tool specs.
		bundle, err = builder.Build(ctx, modelcontext.BuildInput{
			ProjectID:                  input.ProjectID,
			AgentID:                    input.AgentID,
			TurnID:                     input.TurnID,
			OpeningInputIDs:            input.InputIDs,
			Now:                        input.Now,
			AgentConfigSnapshot:        &snapshot,
			MediaProjector:             model.MediaProjectorForClient(client),
			TextAttachmentBudgetTokens: model.TextAttachmentBudgetTokens(client),
		})
		if err != nil {
			return modelStep{}, err
		}
	}
	return e.completeModelCall(
		ctx, input, claim, resolved, bundle, policy, request.InputFingerprint, response, respErr, nil,
	)
}
