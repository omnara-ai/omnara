//go:build integration

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/compaction"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

func TestAgentExecutorRecoversInterruptedRetryCompaction(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgent(t, ctx, "openai/kernel-test", fixture.Now)

	firstModel := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		responses: []model.Response{{
			ID:         "resp_before_interrupted_compaction",
			Content:    []model.ResponsePart{{Type: "text", Text: "history before interrupted compaction"}},
			StopReason: model.StopReasonEndTurn,
		}},
	}
	firstTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"seed history for interrupted compaction",
		fixture.Now.Add(time.Second),
	)
	firstExecutor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, firstModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(2 * time.Second) },
	}
	if err := firstExecutor.ExecuteModelWork(ctx, firstTurn); err != nil {
		t.Fatalf("execute first turn: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		firstTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release first runtime lock: %v", err)
	}

	retryTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"continue after interrupted compaction",
		fixture.Now.Add(3*time.Second),
	)
	watermark, err := fixture.Store.Execution().MaxEventSequence(ctx, kernelTestProjectID, agentID)
	if err != nil {
		t.Fatalf("max event sequence before interrupted compaction: %v", err)
	}
	snapshot, err := fixture.Store.Execution().CaptureAgentConfigForEventWatermark(ctx, kernelTestProjectID, agentID, watermark)
	if err != nil {
		t.Fatalf("capture config for interrupted compaction: %v", err)
	}
	recoveryModel := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		capabilities: model.Capabilities{
			ContextWindowTokens: 128000,
			MaxOutputTokens:     128,
		},
		responses: []model.Response{
			{
				ID:         "resp_recovered_compaction_summary",
				Content:    []model.ResponsePart{{Type: "text", Text: "Recovered summary for interrupted compaction."}},
				StopReason: model.StopReasonEndTurn,
			},
			{
				ID:         "resp_after_recovered_compaction",
				Content:    []model.ResponsePart{{Type: "text", Text: "continued after recovering interrupted compaction"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	failedClaim, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID:          kernelTestProjectID,
		AgentID:            agentID,
		RuntimeLockID:      retryTurn.RuntimeLockID,
		OpeningInputIDs:    retryTurn.InputIDs,
		AgentConfigID:      snapshot.AgentConfig.ID,
		InputEventSequence: watermark,
	})
	if err != nil {
		t.Fatalf("claim context-window model call: %v", err)
	}
	plan, ok, err := compaction.PlanCheckpoint(compaction.PlanInput{
		ProjectID:               kernelTestProjectID,
		AgentID:                 agentID,
		InputEventSequence:      watermark,
		RetainFromEventSequence: watermark,
	})
	if err != nil || !ok {
		t.Fatalf("plan interrupted compaction = ok %v err %v", ok, err)
	}
	handoff, err := fixture.Store.Execution().RecordModelCallFailureAndClaimCompaction(
		ctx,
		executionstore.RecordModelCallFailureAndClaimCompactionInput{
			ParentContextID: failedClaim.Context.ID,
			Failure: executionstore.RecordRecoverableModelCallFailureInput{
				ProjectID:          kernelTestProjectID,
				AgentID:            agentID,
				ModelCallContextID: failedClaim.Context.ID,
				RuntimeLockID:      retryTurn.RuntimeLockID,
				RecoveryKind:       executionstore.ModelCallRecoveryCompact,
				ErrorKind:          model.ErrorKindContextWindow,
				ErrorCode:          "context_window",
				ErrorMessage:       "The model context exceeds the configured input budget.",
				ErrorDetails:       json.RawMessage(`{"code":"context_window"}`),
				RetryDelay:         0,
			},
			SourceEventSequenceEnd: plan.EventSequenceEnd,
		},
	)
	if err != nil {
		t.Fatalf("atomically hand off interrupted compaction: %v", err)
	}
	if handoff.ParentContext.AttemptNumber != 1 ||
		handoff.CompactionCall.Context.AttemptNumber != 1 {
		t.Fatalf("unexpected atomic compaction handoff: %+v", handoff)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		retryTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release interrupted compaction runtime: %v", err)
	}
	interrupted, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		kernelTestProjectID,
		agentID,
		handoff.CompactionCall.Context.ID,
	)
	if err != nil || !found || interrupted.RetryAt == nil {
		t.Fatalf("load interrupted compaction retry: attempt=%+v found=%v err=%v", interrupted, found, err)
	}
	recoveryNow := interrupted.RetryAt.Add(time.Second)
	if err := storagetest.DeleteAgentWakeup(ctx, fixture.Pool, kernelTestProjectID, agentID); err != nil {
		t.Fatalf("delete wakeup before interrupted compaction rebuild: %v", err)
	}
	rebuilt, err := fixture.Store.Execution().RebuildMissingAgentWakeups(ctx, kernelTestProjectID)
	if err != nil {
		t.Fatalf("rebuild interrupted compaction wakeup: %v", err)
	}
	if rebuilt != 1 {
		t.Fatalf("rebuilt wakeups = %d, want 1 for interrupted compaction", rebuilt)
	}
	claim, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		kernelTestClaimInput(recoveryNow),
	)
	if err != nil {
		t.Fatalf("claim interrupted compaction work: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkModel || claim.Model.TurnID != retryTurn.TurnID ||
		claim.Model.ModelCallContextID != handoff.CompactionCall.Context.ID {
		t.Fatalf(
			"claim interrupted compaction found=%v claim=%+v, want executable retry turn %s",
			found,
			claim,
			retryTurn.TurnID,
		)
	}

	recoveryTurn := modelWorkExecutionFromClaimForKernelTest(claim, recoveryNow)
	if err := fixture.Store.Execution().EnsureRuntimeLockActive(
		ctx,
		recoveryTurn.ProjectID,
		recoveryTurn.AgentID,
		recoveryTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("claimed recovery runtime is inactive before execution: %v", err)
	}
	recoveryExecutor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, recoveryModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return recoveryNow },
	}
	if err := recoveryExecutor.ExecuteModelWork(ctx, recoveryTurn); err != nil {
		t.Fatalf("execute recovered interrupted compaction: %v", err)
	}
	if recoveryModel.respondedCount() != 1 {
		t.Fatalf("recovery model prepared %d requests, want one compaction resend", recoveryModel.respondedCount())
	}
	parentContext, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		kernelTestProjectID,
		agentID,
		failedClaim.Context.ID,
	)
	if err != nil || !found {
		t.Fatalf("reload overflowing parent attempt: found=%v err=%v", found, err)
	}
	if parentContext.AttemptNumber != 1 {
		t.Fatalf(
			"overflowing parent attempt number = %d, want no retry after atomic handoff",
			parentContext.AttemptNumber,
		)
	}
	var recoveredCompactionContextID storage.ID
	if err := fixture.Pool.QueryRow(ctx, `
SELECT id
FROM model_call_contexts
WHERE project_id = $1
  AND agent_id = $2
  AND operation_kind = 'compaction'
  AND input_event_sequence = $3
ORDER BY attempt_number DESC
LIMIT 1
`, kernelTestProjectID, agentID, failedClaim.Context.InputEventSequence).Scan(&recoveredCompactionContextID); err != nil {
		t.Fatalf("load recovered compaction context id: %v", err)
	}
	reloadedCompaction, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		kernelTestProjectID,
		agentID,
		recoveredCompactionContextID,
	)
	if err != nil || !found {
		t.Fatalf("reload interrupted compaction context: found=%v err=%v", found, err)
	}
	if reloadedCompaction.State != executionstore.ModelCallContextSucceeded {
		t.Fatalf("interrupted compaction context state = %q, want succeeded", reloadedCompaction.State)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		recoveryTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release recovered compaction runtime: %v", err)
	}
	finalClaim, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		kernelTestClaimInput(recoveryNow.Add(2*time.Second)),
	)
	if err != nil {
		t.Fatalf("claim normal work after recovered compaction: %v", err)
	}
	if !found || finalClaim.Kind != executionstore.AgentWorkModel || finalClaim.Model.ModelCallContextID != storage.NilID {
		t.Fatalf("post-compaction work = %+v found=%v, want a fresh normal call", finalClaim, found)
	}
	finalTurn := modelWorkExecutionFromClaimForKernelTest(finalClaim, recoveryNow.Add(2*time.Second))
	if err := recoveryExecutor.ExecuteModelWork(ctx, finalTurn); err != nil {
		t.Fatalf("execute normal call after recovered compaction: %v", err)
	}
	if recoveryModel.respondedCount() != 2 {
		t.Fatalf("recovery model prepared %d requests, want compaction and final call", recoveryModel.respondedCount())
	}
	var finalOutputs int
	if err := fixture.Pool.QueryRow(ctx, `SELECT count(*) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.turn_id = $3 AND block.block_kind = 'text' AND block.text_content = 'continued after recovering interrupted compaction'`, kernelTestProjectID, agentID, retryTurn.TurnID).
		Scan(&finalOutputs); err != nil {
		t.Fatalf("count recovered interrupted compaction output: %v", err)
	}
	if finalOutputs != 1 {
		t.Fatalf("recovered interrupted compaction final outputs = %d, want 1", finalOutputs)
	}
}

func TestAgentExecutorSteeringPreemptsCompactionCreatedFromInFlightOverflow(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/overflow-steering-boundary", now)

	seedModel := &sequenceKernelModel{
		providerModelSlug: "overflow-steering-boundary",
		responses: []model.Response{{
			ID:         "resp_before_overflow_steering",
			Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "settled history"}},
			StopReason: model.StopReasonEndTurn,
		}},
	}
	seedTurn := fixture.admitContentInputTurn(t, ctx, agentID, userID, "seed history", now.Add(time.Second))
	seedExecutor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, seedModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return now.Add(2 * time.Second) },
	}
	if err := seedExecutor.ExecuteModelWork(ctx, seedTurn); err != nil {
		t.Fatalf("execute seed turn: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		seedTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release seed runtime: %v", err)
	}

	var steeringInput executionstore.AgentInputRecord
	var steeringErr error
	modelClient := &sequenceKernelModel{
		providerModelSlug: "overflow-steering-boundary",
		responses: []model.Response{
			{
				ID:         "resp_overflow_before_steering",
				StopReason: model.StopReasonContextWindow,
			},
			{
				ID:         "resp_after_inflight_steering",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "used the steered request"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
		afterRespond: func(response model.Response) {
			if response.ID != "resp_overflow_before_steering" {
				return
			}
			steeringInput, _, _, steeringErr = fixture.Store.Execution().CreateAgentContentInput(
				ctx,
				executionstore.CreateAgentContentInputInput{
					ProjectID:      kernelTestProjectID,
					AgentID:        agentID,
					Actor:          kernelTestOmnaraActorParams(t, userID),
					ContentBlocks:  mustKernelJSON([]map[string]string{{"type": "text", "text": "use the newer request"}}),
					DeliveryMode:   executionstore.DeliveryModeSteering,
					IdempotencyKey: "steering-during-overflow",
				},
			)
		},
	}
	overflowTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"request that overflows",
		now.Add(3*time.Second),
	)
	currentNow := now.Add(5 * time.Second)
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return currentNow },
	}
	if err := executor.ExecuteModelWork(ctx, overflowTurn); err != nil {
		t.Fatalf("execute overflowing turn: %v", err)
	}
	if steeringErr != nil {
		t.Fatalf("create in-flight steering input: %v", steeringErr)
	}
	if steeringInput.ID == storage.NilID || modelClient.respondedCount() != 1 {
		t.Fatalf(
			"in-flight steering id=%s provider requests=%d, want steering and no stale compaction request",
			steeringInput.ID,
			modelClient.respondedCount(),
		)
	}
	var compactionContexts int
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM model_call_contexts
		WHERE project_id = $1 AND agent_id = $2 AND operation_kind = 'compaction'`,
		kernelTestProjectID,
		agentID,
	).Scan(&compactionContexts); err != nil {
		t.Fatalf("count stale compaction contexts: %v", err)
	}
	if compactionContexts != 0 {
		t.Fatalf("compaction contexts = %d, want none after steering won the boundary", compactionContexts)
	}
	if err := fixture.Store.Execution().DemoteSteeringInputToQueued(
		ctx,
		executionstore.DemoteSteeringInputToQueuedInput{
			ProjectID: kernelTestProjectID,
			AgentID:   agentID,
			InputID:   steeringInput.ID,
		},
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("demote admitted steering error = %v, want state transition conflict", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		overflowTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release overflowing runtime: %v", err)
	}

	currentNow = now.Add(6 * time.Second)
	claimAt := currentNow
	if wallNow := time.Now().UTC(); claimAt.Before(wallNow) {
		claimAt = wallNow.Add(time.Second)
	}
	claim, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		kernelTestClaimInput(claimAt),
	)
	if err != nil {
		t.Fatalf("claim in-flight steering continuation: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkModel || claim.Model.ModelCallContextID != storage.NilID ||
		len(claim.Model.InputIDs) != 2 || claim.Model.InputIDs[0] != overflowTurn.InputIDs[0] ||
		claim.Model.InputIDs[1] != steeringInput.ID {
		t.Fatalf(
			"steering continuation = %+v found=%v, want unanswered overflow plus steering",
			claim,
			found,
		)
	}
	steeredTurn := modelWorkExecutionFromClaimForKernelTest(claim, currentNow)
	if err := executor.ExecuteModelWork(ctx, steeredTurn); err != nil {
		t.Fatalf("execute steered continuation: %v", err)
	}
	if modelClient.respondedCount() != 2 ||
		!strings.Contains(string(modelClient.responded[1].ProviderRequest), "use the newer request") {
		t.Fatalf("steered provider requests = %+v, want only the fresh request after overflow", modelClient.responded)
	}
	var oldState, freshState executionstore.ModelCallState
	var freshAttempt int
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT old_context.state, fresh_context.state, fresh_context.attempt_number
		FROM model_call_contexts old_context
		JOIN model_call_contexts fresh_context
		  ON fresh_context.project_id = old_context.project_id
		 AND fresh_context.agent_id = old_context.agent_id
		 AND fresh_context.input_event_sequence > old_context.input_event_sequence
		WHERE old_context.project_id = $1
		  AND old_context.agent_id = $2
		  AND old_context.operation_kind = 'normal'
		  AND old_context.input_event_sequence = $3
		  AND fresh_context.operation_kind = 'normal'`,
		kernelTestProjectID,
		agentID,
		overflowTurn.OpeningEventSequence,
	).Scan(&oldState, &freshState, &freshAttempt); err != nil {
		t.Fatalf("load overflow steering lineage: %v", err)
	}
	if oldState != executionstore.ModelCallContextFailed ||
		freshState != executionstore.ModelCallContextSucceeded || freshAttempt != 1 {
		t.Fatalf(
			"overflow steering lineage old=%s fresh=%s attempt=%d, want failed history and succeeded/1 fresh frontier",
			oldState,
			freshState,
			freshAttempt,
		)
	}
}

func TestAgentExecutorConfigChangePreemptsSmallerCompactionCreatedFromTruncatedSummary(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/truncated-compaction-config-boundary", now)

	seedModel := &sequenceKernelModel{
		providerModelSlug: "truncated-compaction-config-boundary",
		responses: []model.Response{{
			ID:         "resp_before_truncated_compaction_config",
			Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "settled history"}},
			StopReason: model.StopReasonEndTurn,
		}},
	}
	seedTurn := fixture.admitContentInputTurn(t, ctx, agentID, userID, "seed history", now.Add(time.Second))
	seedExecutor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, seedModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return now.Add(2 * time.Second) },
	}
	if err := seedExecutor.ExecuteModelWork(ctx, seedTurn); err != nil {
		t.Fatalf("execute seed turn: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		seedTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release seed runtime: %v", err)
	}

	nextConfig := fixture.kernelAgentConfigInput(t, ctx, "Kernel Test", "truncated-compaction-config-next")
	currentConfig := fixture.currentAgentConfig(t, ctx, agentID)
	currentRevisionID := currentRevisionIDForKernelConfig(t, ctx, fixture.Store, currentConfig)
	nextRevisionID := currentRevisionIDForKernelConfiguredModelID(t, ctx, fixture.Store, nextConfig.ConfiguredModelID)

	var changeResult executionstore.ChangeAgentConfigResult
	var changeErr error
	oldModel := &sequenceKernelModel{
		providerModelSlug: "truncated-compaction-config-boundary",
		capabilities: model.Capabilities{
			ContextWindowTokens: 128000,
			MaxOutputTokens:     128,
		},
		responses: []model.Response{
			{
				ID:         "resp_overflow_before_truncated_summary",
				StopReason: model.StopReasonContextWindow,
			},
			{
				ID:         "resp_truncated_summary_before_config",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "partial summary"}},
				StopReason: model.StopReasonMaxTokens,
			},
			{
				ID:         "resp_smaller_compaction_should_not_send",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "stale compaction"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
		afterRespond: func(response model.Response) {
			if response.ID != "resp_truncated_summary_before_config" {
				return
			}
			changeResult, changeErr = fixture.Store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
				CreateAgentConfigInput: nextConfig,
				AgentID:                agentID,
				ActorType:              identitystore.PrincipalTypeSystem,
				Reason:                 "test_inflight_compaction_change",
				IdempotencyKey:         "config-during-truncated-compaction",
			})
		},
	}
	newModel := &sequenceKernelModel{
		providerModelSlug: "truncated-compaction-config-next",
		responses: []model.Response{{
			ID:         "resp_after_truncated_compaction_config",
			Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "used the changed config"}},
			StopReason: model.StopReasonEndTurn,
		}},
	}
	currentNow := now.Add(5 * time.Second)
	executor := AgentExecutor{
		Store: fixture.Store,
		ModelResolver: staticTestModelResolver{Clients: []model.ResolvedClient{
			{
				Client:                    oldModel,
				ConfiguredModelRevisionID: currentRevisionID.String(),
			},
			{
				Client:                    newModel,
				ConfiguredModelRevisionID: nextRevisionID.String(),
			},
		}},
		ToolExecutor: tools.Executor{Store: fixture.Store},
		Now:          func() time.Time { return currentNow },
	}
	overflowTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"overflow before changing config",
		now.Add(3*time.Second),
	)
	if err := executor.ExecuteModelWork(ctx, overflowTurn); err != nil {
		t.Fatalf("execute overflowing turn with truncated compaction: %v", err)
	}
	if changeErr != nil {
		t.Fatalf("change config during truncated compaction: %v", changeErr)
	}
	if changeResult.AgentConfig.ID == storage.NilID || oldModel.respondedCount() != 2 {
		t.Fatalf(
			"changed config=%s old provider requests=%d, want config change after one compaction request",
			changeResult.AgentConfig.ID,
			oldModel.respondedCount(),
		)
	}
	oldModel.mu.Lock()
	remainingOldResponses := append([]model.Response(nil), oldModel.responses...)
	oldModel.mu.Unlock()
	if len(remainingOldResponses) != 1 || remainingOldResponses[0].ID != "resp_smaller_compaction_should_not_send" {
		t.Fatalf("smaller stale compaction consumed a provider response: %+v", remainingOldResponses)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		overflowTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release truncated compaction runtime: %v", err)
	}

	currentNow = now.Add(6 * time.Second)
	claimAt := currentNow
	if wallNow := time.Now().UTC(); claimAt.Before(wallNow) {
		claimAt = wallNow.Add(time.Second)
	}
	claim, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		kernelTestClaimInput(claimAt),
	)
	if err != nil {
		t.Fatalf("claim changed-config continuation: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkModel || claim.Model.ModelCallContextID != storage.NilID {
		t.Fatalf("changed-config continuation = %+v found=%v, want fresh model work", claim, found)
	}
	changedTurn := modelWorkExecutionFromClaimForKernelTest(claim, currentNow)
	if err := executor.ExecuteModelWork(ctx, changedTurn); err != nil {
		t.Fatalf("execute changed-config continuation: %v", err)
	}
	if oldModel.respondedCount() != 2 || newModel.respondedCount() != 1 {
		t.Fatalf(
			"provider requests old/new=%d/%d, want 2/1 with no stale smaller compaction",
			oldModel.respondedCount(),
			newModel.respondedCount(),
		)
	}
	var compactionCount, sourceAdjustedCompactions int
	var freshConfigID storage.ID
	var freshAttempt int
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (
		         WHERE state = 'failed'
		           AND recovery_kind = 'reduce_compaction_source'
		       )
		FROM model_call_contexts
		WHERE project_id = $1 AND agent_id = $2 AND operation_kind = 'compaction'`,
		kernelTestProjectID,
		agentID,
	).Scan(&compactionCount, &sourceAdjustedCompactions); err != nil {
		t.Fatalf("load truncated compaction lineage: %v", err)
	}
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT context.agent_config_id, context.attempt_number
		FROM model_call_contexts context
		WHERE context.project_id = $1
		  AND context.agent_id = $2
		  AND context.operation_kind = 'normal'
		  AND context.agent_config_id = $3
		  AND context.state = 'succeeded'`,
		kernelTestProjectID,
		agentID,
		changeResult.AgentConfig.ID,
	).Scan(&freshConfigID, &freshAttempt); err != nil {
		t.Fatalf("load changed-config model context: %v", err)
	}
	if compactionCount != 1 || sourceAdjustedCompactions != 1 ||
		freshConfigID != changeResult.AgentConfig.ID || freshAttempt != 1 {
		t.Fatalf(
			"compactions=%d source_adjusted=%d fresh_config=%s attempt=%d, want 1/1/%s/1",
			compactionCount,
			sourceAdjustedCompactions,
			freshConfigID,
			freshAttempt,
			changeResult.AgentConfig.ID,
		)
	}
}

func TestAgentExecutorSteeringStartsFreshFrontierDuringCompactionRetry(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/compaction-retry-supersession", now)

	seedModel := &sequenceKernelModel{
		providerModelSlug: "compaction-retry-supersession",
		responses: []model.Response{{
			ID:         "resp_before_compaction_retry_supersession",
			Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "settled history"}},
			StopReason: model.StopReasonEndTurn,
		}},
	}
	seedTurn := fixture.admitContentInputTurn(t, ctx, agentID, userID, "seed history", now.Add(time.Second))
	seedExecutor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, seedModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return now.Add(2 * time.Second) },
	}
	if err := seedExecutor.ExecuteModelWork(ctx, seedTurn); err != nil {
		t.Fatalf("execute seed turn: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		seedTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release seed runtime: %v", err)
	}

	retryAfterSeconds := int64(3600)
	retryModel := &sequenceKernelModel{
		providerModelSlug: "compaction-retry-supersession",
		errs: []error{
			nil,
			model.ProviderError{
				Kind:    model.ErrorKindRateLimit,
				Source:  "test-provider",
				Code:    "rate_limited",
				Message: "retry compaction later",
				RetryAfter: &model.RetryAfter{
					DeltaSeconds: &retryAfterSeconds,
				},
			},
		},
		responses: []model.Response{
			{
				ID:         "resp_context_window_before_new_input",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "context window exceeded"}},
				StopReason: model.StopReasonContextWindow,
			},
			{
				ID:         "resp_after_compaction_retry_supersession",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "handled the newer input"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	currentNow := now.Add(4 * time.Second)
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, retryModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return currentNow },
	}
	overflowTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"trigger compaction retry",
		now.Add(3*time.Second),
	)
	if err := executor.ExecuteModelWork(ctx, overflowTurn); err != nil {
		t.Fatalf("execute overflow and retrying compaction: %v", err)
	}
	if retryModel.respondedCount() != 2 {
		t.Fatalf("prepared requests = %d, want normal overflow and compaction retry", retryModel.respondedCount())
	}
	var blockedContextID, compactionContextID storage.ID
	var compactionRetryAt time.Time
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT parent.id, compaction.id, compaction.retry_at
		FROM model_call_contexts parent
		JOIN model_call_contexts compaction
		  ON compaction.project_id = parent.project_id
		 AND compaction.agent_id = parent.agent_id
		 AND compaction.input_event_sequence = parent.input_event_sequence
		WHERE parent.project_id = $1
		  AND parent.agent_id = $2
		  AND parent.input_event_sequence = $3
		  AND parent.state = 'failed'
		  AND parent.recovery_kind = 'compact'
		  AND compaction.state = 'failed'
		  AND compaction.recovery_kind = 'retry'`,
		kernelTestProjectID,
		agentID,
		overflowTurn.OpeningEventSequence,
	).Scan(&blockedContextID, &compactionContextID, &compactionRetryAt); err != nil {
		t.Fatalf("load retrying compaction lineage: %v", err)
	}
	if !compactionRetryAt.After(now.Add(30 * time.Minute)) {
		t.Fatalf("compaction retry_at = %s, want provider backoff well after new input", compactionRetryAt)
	}
	retryingCompactionContext, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		kernelTestProjectID,
		agentID,
		compactionContextID,
	)
	if err != nil || !found {
		t.Fatalf("load retrying compaction context: found=%v err=%v", found, err)
	}
	if retryingCompactionContext.SourceEventSequenceEnd == nil {
		t.Fatalf("retrying compaction context has no source range: %+v", retryingCompactionContext)
	}
	replayedRetry, err := (compaction.Runner{
		Store:          compaction.NewStore(fixture.Store.Execution()),
		Resolver:       executor.ModelResolver,
		ContextBuilder: executor.contextBuilder(),
		Now:            executor.Now,
	}).Run(ctx, compaction.RunInput{
		Plan: compaction.Plan{
			ProjectID:          kernelTestProjectID,
			AgentID:            agentID,
			InputEventSequence: retryingCompactionContext.InputEventSequence,
			EventSequenceStart: compactionSourceStartForKernelTest(t, ctx, fixture.Store, retryingCompactionContext),
			EventSequenceEnd:   *retryingCompactionContext.SourceEventSequenceEnd,
		},
		TurnID:                   overflowTurn.TurnID,
		OpeningInputIDs:          overflowTurn.InputIDs,
		OpeningEventSequence:     overflowTurn.OpeningEventSequence,
		RuntimeLockID:            overflowTurn.RuntimeLockID,
		ParentModelCallContextID: blockedContextID,
	})
	if err != nil {
		t.Fatalf("replay compaction before retry deadline: %v", err)
	}
	if replayedRetry.State != compaction.RunRetryScheduled || replayedRetry.RetryAt == nil ||
		!replayedRetry.RetryAt.Equal(compactionRetryAt) {
		t.Fatalf("replayed compaction retry = %+v, want deadline %s", replayedRetry, compactionRetryAt)
	}
	if retryModel.respondedCount() != 2 {
		t.Fatalf("early compaction replay made a provider request; prepared=%d", retryModel.respondedCount())
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		overflowTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release compaction retry runtime: %v", err)
	}

	newInput, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      kernelTestProjectID,
		AgentID:        agentID,
		Actor:          kernelTestOmnaraActorParams(t, userID),
		ContentBlocks:  mustKernelJSON([]map[string]string{{"type": "text", "text": "newer input"}}),
		DeliveryMode:   executionstore.DeliveryModeSteering,
		IdempotencyKey: "steering-supersedes-compaction-retry",
	})
	if err != nil {
		t.Fatalf("create steering input during compaction retry: %v", err)
	}
	currentNow = now.Add(6 * time.Second)
	claim, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		kernelTestClaimInput(currentNow),
	)
	if err != nil {
		t.Fatalf("claim steering input during compaction retry: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkModel || claim.Model.ModelCallContextID != storage.NilID ||
		len(claim.Model.InputIDs) != 2 || claim.Model.InputIDs[0] != overflowTurn.InputIDs[0] ||
		claim.Model.InputIDs[1] != newInput.ID {
		t.Fatalf(
			"steering input claim = %+v found=%v, want unanswered overflow plus steering",
			claim,
			found,
		)
	}
	freshTurn := modelWorkExecutionFromClaimForKernelTest(claim, currentNow)
	if err := executor.ExecuteModelWork(ctx, freshTurn); err != nil {
		t.Fatalf("execute fresh input after compaction retry: %v", err)
	}

	var blockedState, compactionState executionstore.ModelCallState
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT
		  (SELECT state FROM model_call_contexts
		   WHERE project_id = $1 AND agent_id = $2 AND id = $3),
		  (SELECT state FROM model_call_contexts
		   WHERE project_id = $1 AND agent_id = $2 AND id = $4)`,
		kernelTestProjectID,
		agentID,
		blockedContextID,
		compactionContextID,
	).Scan(&blockedState, &compactionState); err != nil {
		t.Fatalf("load prior compaction lineage: %v", err)
	}
	var freshState executionstore.ModelCallState
	var freshAttemptNumber int
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT context.state, context.attempt_number
		FROM model_call_contexts context
		WHERE context.project_id = $1
		  AND context.agent_id = $2
		  AND context.operation_kind = 'normal'
		  AND context.input_event_sequence = (
			    SELECT event.sequence
			    FROM agent_events event
			    JOIN agents event_agent ON event_agent.id = event.agent_id
			    WHERE event_agent.project_id = $1
			      AND event.agent_id = $2
		      AND event.agent_input_id = $3
		  )`,
		kernelTestProjectID,
		agentID,
		newInput.ID,
	).Scan(&freshState, &freshAttemptNumber); err != nil {
		t.Fatalf("load fresh context after compaction retry: %v", err)
	}
	if blockedState != executionstore.ModelCallContextFailed ||
		compactionState != executionstore.ModelCallContextFailed ||
		freshState != executionstore.ModelCallContextSucceeded || freshAttemptNumber != 1 {
		t.Fatalf(
			"contexts blocked=%s compaction=%s fresh=%s attempt=%d, want failed/failed immutable history and succeeded/1 fresh frontier",
			blockedState,
			compactionState,
			freshState,
			freshAttemptNumber,
		)
	}
}
