//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func claimReplacementAfterReapedNormalModelCall(
	t *testing.T,
	ctx context.Context,
	fixtureName string,
) (processDaemonFixture, executionstore.ClaimedAgentWork, executionstore.ModelCallClaim, executionstore.ModelCallClaim) {
	t.Helper()
	fixture, oldWork, oldClaim, _ :=
		claimReplacementAfterReapedNormalModelCallSetup(t, ctx, fixtureName)
	recoveryAt := reapRuntimeLockForReplacementTest(t, ctx, fixture, oldWork, oldClaim.Context.ID)
	_, replacementClaim := claimReplacementModelCallContext(t, ctx, fixture, oldClaim.Context.ID, recoveryAt)
	return fixture, oldWork, oldClaim, replacementClaim
}

func claimReplacementAfterReapedNormalModelCallSetup(
	t *testing.T,
	ctx context.Context,
	fixtureName string,
) (processDaemonFixture, executionstore.ClaimedAgentWork, executionstore.ModelCallClaim, time.Time) {
	t.Helper()
	fixture := newProcessDaemonFixture(t, ctx, fixtureName)
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	); err != nil {
		t.Fatalf("release initial runtime lock: %v", err)
	}
	now := fixture.Now.Add(time.Minute)
	input, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        fixture.AgentID,
		Actor:          mustOmnaraActorParams(t, fixture.UserID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"exercise stale worker fencing"}]`),
		IdempotencyKey: fixtureName,
	})
	if err != nil {
		t.Fatalf("create stale-worker input: %v", err)
	}
	oldWork, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		testClaimNextAgentWorkInput(),
	)
	if err != nil || !found || oldWork.Kind != executionstore.AgentWorkModel || !claimedOpeningInputIDsEqual(oldWork, input.ID) {
		t.Fatalf("claim stale-worker input = %+v found=%v err=%v", oldWork, found, err)
	}
	oldClaim := claimTestNormalModelCallForWork(t, ctx, fixture, oldWork, now.Add(2*time.Second))
	return fixture, oldWork, oldClaim, now
}

func claimReplacementAfterReapedCompaction(
	t *testing.T,
	ctx context.Context,
	fixtureName string,
) (processDaemonFixture, executionstore.ClaimedAgentWork, executionstore.ModelCallClaim, executionstore.ClaimedAgentWork, executionstore.ModelCallClaim) {
	t.Helper()
	fixture, oldWork, parentClaim, _ :=
		claimReplacementAfterReapedNormalModelCallSetup(t, ctx, fixtureName)
	handoff, err := fixture.Store.Execution().RecordModelCallFailureAndClaimCompaction(
		ctx,
		executionstore.RecordModelCallFailureAndClaimCompactionInput{
			ParentContextID: parentClaim.Context.ID,
			Failure: executionstore.RecordRecoverableModelCallFailureInput{
				ProjectID:          testProjectID,
				AgentID:            fixture.AgentID,
				ModelCallContextID: parentClaim.Context.ID,
				RuntimeLockID:      oldWork.RuntimeLock.ID,
				RecoveryKind:       executionstore.ModelCallRecoveryCompact,
				ErrorKind:          "context_window",
				ErrorCode:          "context_window",
				ErrorMessage:       "The model context exceeds the configured input budget.",
				ErrorDetails:       json.RawMessage(`{"test":"stale_checkpoint"}`),
			},
			SourceEventSequenceEnd: oldWork.Model.OpeningEventSequence,
		},
	)
	if err != nil {
		t.Fatalf("record compaction-triggering failure: %v", err)
	}
	oldClaim := handoff.CompactionCall
	recoveryAt := reapRuntimeLockForReplacementTest(t, ctx, fixture, oldWork, oldClaim.Context.ID)
	replacementWork, replacementClaim :=
		claimReplacementModelCallContext(t, ctx, fixture, oldClaim.Context.ID, recoveryAt)
	return fixture, oldWork, oldClaim, replacementWork, replacementClaim
}

func reapRuntimeLockForReplacementTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	work executionstore.ClaimedAgentWork,
	modelCallContextID ID,
) time.Time {
	t.Helper()
	expireAgentRuntimeLockForTest(t, ctx, fixture.Store, work.RuntimeLock.ID)
	reaped, err := fixture.Store.Execution().ReapExpiredAgentRuntimeLocks(ctx, 100)
	if err != nil || reaped != 1 {
		t.Fatalf("reap stale-worker runtime: reaped=%d err=%v", reaped, err)
	}
	modelContext, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		modelCallContextID,
	)
	if err != nil || !found || modelContext.RetryAt == nil {
		t.Fatalf("load stale-worker retry schedule: context=%+v found=%v err=%v", modelContext, found, err)
	}
	return modelContext.RetryAt.Add(time.Millisecond)
}

func claimReplacementModelCallContext(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	predecessorContextID ID,
	now time.Time,
) (executionstore.ClaimedAgentWork, executionstore.ModelCallClaim) {
	t.Helper()
	work, found, err := fixture.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
	if err != nil || !found || work.Kind != executionstore.AgentWorkModel ||
		work.Model.ModelCallContextID != predecessorContextID {
		t.Fatalf("claim replacement model work = %+v found=%v err=%v", work, found, err)
	}
	claim, err := fixture.Store.Execution().ClaimNextModelCallContext(ctx, executionstore.ClaimNextModelCallContextInput{
		ProjectID:                     testProjectID,
		AgentID:                       fixture.AgentID,
		PredecessorModelCallContextID: predecessorContextID,
		RuntimeLockID:                 work.RuntimeLock.ID,
	})
	if err != nil {
		t.Fatalf("claim replacement model context: %v", err)
	}
	if !claim.Created || !claim.Claimed || claim.Context.AttemptNumber != 2 ||
		claim.Context.ID == predecessorContextID {
		t.Fatalf(
			"replacement model context = %+v, want a new claimed attempt 2 after %s",
			claim,
			predecessorContextID,
		)
	}
	return work, claim
}

func assertModelCallContextState(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	modelCallContextID ID,
	want executionstore.ModelCallState,
) {
	t.Helper()
	modelContext, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		modelCallContextID,
	)
	if err != nil || !found || modelContext.State != want {
		t.Fatalf(
			"model context %s = %+v found=%v err=%v, want state %s",
			modelCallContextID,
			modelContext,
			found,
			err,
			want,
		)
	}
}

func assertReplacementModelCallContextUnchanged(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	claim executionstore.ModelCallClaim,
) {
	t.Helper()
	contextRecord, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		claim.Context.ID,
	)
	if err != nil || !found || contextRecord.State != executionstore.ModelCallContextStarted ||
		contextRecord.CompletedAt != nil {
		t.Fatalf(
			"replacement context = %+v found=%v err=%v, want unchanged started context",
			contextRecord,
			found,
			err,
		)
	}
}

func claimExhaustedCompactionModelContext(
	t *testing.T,
	ctx context.Context,
	fixtureName string,
) (processDaemonFixture, executionstore.ClaimedAgentWork, executionstore.ModelCallClaim, executionstore.ModelCallClaim) {
	t.Helper()
	fixture := newProcessDaemonFixture(t, ctx, fixtureName)
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	); err != nil {
		t.Fatalf("release compaction fixture runtime lock: %v", err)
	}
	input, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        fixture.AgentID,
		Actor:          mustOmnaraActorParams(t, fixture.UserID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"exercise compaction retry limit"}]`),
		IdempotencyKey: fixtureName,
	})
	if err != nil {
		t.Fatalf("create compaction content input: %v", err)
	}
	work, found, err := fixture.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
	if err != nil || !found || work.Kind != executionstore.AgentWorkModel ||
		!claimedOpeningInputIDsEqual(work, input.ID) {
		t.Fatalf("compaction fixture work = %+v found=%v err=%v", work, found, err)
	}
	parentClaim := claimTestNormalModelCallForWork(t, ctx, fixture, work, time.Now().UTC())
	handoff, err := fixture.Store.Execution().RecordModelCallFailureAndClaimCompaction(
		ctx,
		executionstore.RecordModelCallFailureAndClaimCompactionInput{
			ParentContextID: parentClaim.Context.ID,
			Failure: executionstore.RecordRecoverableModelCallFailureInput{
				ProjectID:          testProjectID,
				AgentID:            fixture.AgentID,
				ModelCallContextID: parentClaim.Context.ID,
				RuntimeLockID:      work.RuntimeLock.ID,
				RecoveryKind:       executionstore.ModelCallRecoveryCompact,
				ErrorKind:          "context_window",
				ErrorCode:          "context_window",
				ErrorMessage:       "The model context exceeds the configured input budget.",
				ErrorDetails:       json.RawMessage(`{"code":"context_window"}`),
			},
			SourceEventSequenceEnd: work.Model.OpeningEventSequence,
		},
	)
	if err != nil {
		t.Fatalf("record compaction-triggering failure: %v", err)
	}
	compactionClaim := handoff.CompactionCall
	for attempt := 1; attempt <= executionstore.MaxModelCallRetriesPerOperation; attempt++ {
		if compactionClaim.Context.AttemptNumber != attempt {
			t.Fatalf("compaction attempt = %d, want %d", compactionClaim.Context.AttemptNumber, attempt)
		}
		if _, err := fixture.Store.Execution().RecordRetryableModelCallFailure(
			ctx,
			executionstore.RecordRecoverableModelCallFailureInput{
				ProjectID:          testProjectID,
				AgentID:            fixture.AgentID,
				ModelCallContextID: compactionClaim.Context.ID,
				RuntimeLockID:      work.RuntimeLock.ID,
				ErrorKind:          "transient",
				ErrorCode:          "provider_unavailable",
				ErrorMessage:       "The model provider was temporarily unavailable.",
				RetryDelay:         0,
			},
		); err != nil {
			t.Fatalf("record compaction retry failure %d: %v", attempt, err)
		}
		if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
			ctx,
			testProjectID,
			fixture.AgentID,
			work.RuntimeLock.ID,
		); err != nil {
			t.Fatalf("release compaction runtime after failure %d: %v", attempt, err)
		}
		work, found, err = fixture.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
		if err != nil || !found || work.Kind != executionstore.AgentWorkModel ||
			work.Model.ModelCallContextID != compactionClaim.Context.ID {
			t.Fatalf("compaction retry work %d = %+v found=%v err=%v", attempt+1, work, found, err)
		}
		predecessorID := compactionClaim.Context.ID
		compactionClaim, err = fixture.Store.Execution().ClaimNextModelCallContext(
			ctx,
			executionstore.ClaimNextModelCallContextInput{
				ProjectID:                     testProjectID,
				AgentID:                       fixture.AgentID,
				PredecessorModelCallContextID: predecessorID,
				RuntimeLockID:                 work.RuntimeLock.ID,
			},
		)
		if err != nil || !compactionClaim.Created || !compactionClaim.Claimed ||
			compactionClaim.Context.ID == predecessorID ||
			compactionClaim.Context.AttemptNumber != attempt+1 {
			t.Fatalf("compaction retry context %d = %+v err=%v", attempt+1, compactionClaim, err)
		}
	}
	return fixture, work, parentClaim, compactionClaim
}

func assertExhaustedCompactionContextTerminalized(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	parentClaim, compactionClaim executionstore.ModelCallClaim,
	wantErrorCode string,
) {
	t.Helper()
	terminalContext, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		compactionClaim.Context.ID,
	)
	if err != nil || !found ||
		terminalContext.AttemptNumber != executionstore.MaxModelCallRetriesPerOperation+1 ||
		terminalContext.State != executionstore.ModelCallContextFailed ||
		terminalContext.RecoveryKind != "" ||
		terminalContext.ErrorKind != "runtime" ||
		terminalContext.ErrorCode != wantErrorCode {
		t.Fatalf("exhausted compaction context = %+v found=%v err=%v", terminalContext, found, err)
	}
	parentContext, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		parentClaim.Context.ID,
	)
	if err != nil || !found || parentContext.State != executionstore.ModelCallContextFailed ||
		parentContext.RecoveryKind != executionstore.ModelCallRecoveryCompact {
		t.Fatalf("compaction parent context = %+v found=%v err=%v", parentContext, found, err)
	}
	output, found, err := fixture.Store.Execution().GetModelOutputForContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		compactionClaim.Context.ID,
	)
	if err != nil || !found || output.StopReason != "error" ||
		output.ModelCallContextID != compactionClaim.Context.ID {
		t.Fatalf("terminal compaction output = %+v found=%v err=%v", output, found, err)
	}
	assertModelCallContextRetryHistory(
		t,
		ctx,
		fixture,
		compactionClaim.Context,
		executionstore.MaxModelCallRetriesPerOperation+1,
	)
}

func claimExhaustedNormalModelContext(
	t *testing.T,
	ctx context.Context,
	fixtureName string,
) (processDaemonFixture, executionstore.ClaimedAgentWork, executionstore.ModelCallClaim) {
	t.Helper()
	fixture := newProcessDaemonFixture(t, ctx, fixtureName)
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	); err != nil {
		t.Fatalf("release fixture runtime lock: %v", err)
	}
	input, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        fixture.AgentID,
		Actor:          mustOmnaraActorParams(t, fixture.UserID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"exercise model call retry limit"}]`),
		IdempotencyKey: fixtureName,
	})
	if err != nil {
		t.Fatalf("create content input: %v", err)
	}
	work, found, err := fixture.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
	if err != nil || !found || work.Kind != executionstore.AgentWorkModel ||
		!claimedOpeningInputIDsEqual(work, input.ID) {
		t.Fatalf("initial work = %+v found=%v err=%v", work, found, err)
	}
	modelClaim := claimTestNormalModelCallForWork(t, ctx, fixture, work, time.Now().UTC())
	for attempt := 1; attempt <= executionstore.MaxModelCallRetriesPerOperation; attempt++ {
		if modelClaim.Context.AttemptNumber != attempt {
			t.Fatalf("model call attempt = %d, want %d", modelClaim.Context.AttemptNumber, attempt)
		}
		if _, err := fixture.Store.Execution().RecordRetryableModelCallFailure(
			ctx,
			executionstore.RecordRecoverableModelCallFailureInput{
				ProjectID:          testProjectID,
				AgentID:            fixture.AgentID,
				ModelCallContextID: modelClaim.Context.ID,
				RuntimeLockID:      work.RuntimeLock.ID,
				ErrorKind:          "transient",
				ErrorCode:          "provider_unavailable",
				ErrorMessage:       "The model provider was temporarily unavailable.",
				RetryDelay:         0,
			},
		); err != nil {
			t.Fatalf("record model retry failure %d: %v", attempt, err)
		}
		if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
			ctx,
			testProjectID,
			fixture.AgentID,
			work.RuntimeLock.ID,
		); err != nil {
			t.Fatalf("release model runtime after failure %d: %v", attempt, err)
		}
		work, found, err = fixture.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
		if err != nil || !found || work.Kind != executionstore.AgentWorkModel ||
			work.Model.ModelCallContextID != modelClaim.Context.ID {
			t.Fatalf("model retry work %d = %+v found=%v err=%v", attempt+1, work, found, err)
		}
		predecessorID := modelClaim.Context.ID
		modelClaim, err = fixture.Store.Execution().ClaimNextModelCallContext(
			ctx,
			executionstore.ClaimNextModelCallContextInput{
				ProjectID:                     testProjectID,
				AgentID:                       fixture.AgentID,
				PredecessorModelCallContextID: predecessorID,
				RuntimeLockID:                 work.RuntimeLock.ID,
			},
		)
		if err != nil || !modelClaim.Created || !modelClaim.Claimed ||
			modelClaim.Context.ID == predecessorID ||
			modelClaim.Context.AttemptNumber != attempt+1 {
			t.Fatalf("model retry context %d = %+v err=%v", attempt+1, modelClaim, err)
		}
	}
	return fixture, work, modelClaim
}

func assertExhaustedNormalModelContextTerminalized(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	modelClaim executionstore.ModelCallClaim,
	wantErrorCode string,
) {
	t.Helper()
	contextRecord, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		modelClaim.Context.ID,
	)
	if err != nil || !found {
		t.Fatalf("load exhausted context: found=%v err=%v", found, err)
	}
	if contextRecord.AttemptNumber != executionstore.MaxModelCallRetriesPerOperation+1 ||
		contextRecord.State != executionstore.ModelCallContextFailed ||
		contextRecord.RecoveryKind != "" ||
		contextRecord.ErrorKind != "runtime" ||
		contextRecord.ErrorCode != wantErrorCode {
		t.Fatalf("exhausted context = %+v", contextRecord)
	}
	output, found, err := fixture.Store.Execution().GetModelOutputForContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		modelClaim.Context.ID,
	)
	if err != nil || !found {
		t.Fatalf("load exhausted output: found=%v err=%v", found, err)
	}
	if output.StopReason != "error" ||
		output.ModelCallContextID != modelClaim.Context.ID {
		t.Fatalf("exhausted output = %+v", output)
	}
	assertModelCallContextRetryHistory(
		t,
		ctx,
		fixture,
		modelClaim.Context,
		executionstore.MaxModelCallRetriesPerOperation+1,
	)
	if wakeups := countAgentWakeups(t, ctx, fixture.Store, fixture.AgentID); wakeups != 0 {
		t.Fatalf("exhausted context wakeups = %d, want 0", wakeups)
	}
}

func injectModelReadyFrontier(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	kind, key string,
) ID {
	t.Helper()
	now := time.Now().UTC()
	if kind == "steering" {
		input, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
			ProjectID:      testProjectID,
			AgentID:        fixture.AgentID,
			Actor:          mustOmnaraActorParams(t, fixture.UserID),
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"use the new direction"}]`),
			DeliveryMode:   executionstore.DeliveryModeSteering,
			IdempotencyKey: key + "-steering",
		})
		if err != nil {
			t.Fatalf("create steering frontier: %v", err)
		}
		return input.ID
	}
	config := mustCreateAgentConfigFromYAML(t, ctx, fixture.Store, key+"-config", `
instruction: Follow the newly configured direction.
model:
  provider_config: openai-prod
  name: test
`, now)
	if _, err := fixture.Store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: changeInputFromRecord(config),
		AgentID:                fixture.AgentID,
		ActorType:              identitystore.PrincipalTypeSystem,
		Reason:                 "test_model_ready_boundary",
		IdempotencyKey:         key + "-config-change",
	}); err != nil {
		t.Fatalf("create config frontier: %v", err)
	}
	return NilID
}

func assertModelReadyBoundaryPreempted(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	parentContextID, compactionContextID, terminalContextID, steeringInputID ID,
) {
	t.Helper()
	terminalContext, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		terminalContextID,
	)
	if err != nil || !found || terminalContext.State != executionstore.ModelCallContextFailed ||
		terminalContext.RecoveryKind != executionstore.ModelCallRecoveryRetry {
		t.Fatalf(
			"preempted retry context = %+v found=%v err=%v",
			terminalContext,
			found,
			err,
		)
	}
	parent, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		parentContextID,
	)
	if err != nil || !found || parent.State != executionstore.ModelCallContextFailed {
		t.Fatalf("preempted parent context = %+v found=%v err=%v", parent, found, err)
	}
	if compactionContextID != NilID {
		compactionContext, found, err := fixture.Store.Execution().GetModelCallContext(
			ctx,
			testProjectID,
			fixture.AgentID,
			compactionContextID,
		)
		if err != nil || !found || compactionContext.State != executionstore.ModelCallContextFailed {
			t.Fatalf("preempted compaction context = %+v found=%v err=%v", compactionContext, found, err)
		}
	}
	for _, contextID := range []ID{parentContextID, compactionContextID} {
		if contextID == NilID {
			continue
		}
		if _, found, err := fixture.Store.Execution().GetModelOutputForContext(
			ctx,
			testProjectID,
			fixture.AgentID,
			contextID,
		); err != nil || found {
			t.Fatalf("preempted context %s output found=%v err=%v, want none", contextID, found, err)
		}
		var liveContexts int
		if err := fixture.Store.pool.QueryRow(ctx, `
SELECT count(*)
FROM model_call_contexts
WHERE project_id = $1 AND agent_id = $2 AND id = $3
  AND state = 'started'`, testProjectID, fixture.AgentID, contextID).Scan(&liveContexts); err != nil {
			t.Fatalf("count live preempted contexts: %v", err)
		}
		if liveContexts != 0 {
			t.Fatalf("preempted context %s is still live", contextID)
		}
	}
	if steeringInputID != NilID {
		var state string
		var admitted bool
		if err := fixture.Store.pool.QueryRow(ctx, `
SELECT state, admitted_event_id IS NOT NULL
FROM agent_inputs
WHERE project_id = $1 AND agent_id = $2 AND id = $3`,
			testProjectID,
			fixture.AgentID,
			steeringInputID,
		).Scan(&state, &admitted); err != nil {
			t.Fatalf("load admitted steering frontier: %v", err)
		}
		if state != "resolved" || !admitted {
			t.Fatalf("steering frontier state/admitted = %q/%v, want resolved/true", state, admitted)
		}
	}
	var readyAt time.Time
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT wake.ready_at
FROM agent_wakeups wake
JOIN agents agent ON agent.id = wake.agent_id
WHERE agent.project_id = $1 AND wake.agent_id = $2`,
		testProjectID,
		fixture.AgentID,
	).Scan(&readyAt); err != nil {
		t.Fatalf("load model-ready boundary wakeup: %v", err)
	}
	if readyAt.IsZero() {
		t.Fatal("model-ready boundary wakeup has a zero ready time")
	}
}

func claimedOpeningInputIDsEqual(claim executionstore.ClaimedAgentWork, inputIDs ...ID) bool {
	if len(claim.Model.InputIDs) != len(inputIDs) {
		return false
	}
	for index := range inputIDs {
		if claim.Model.InputIDs[index] != inputIDs[index] {
			return false
		}
	}
	return true
}

func assertTerminalFailureSettledBeforeModelReadyFrontier(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	parentContextID, compactionContextID, terminalContextID, steeringInputID ID,
) {
	t.Helper()
	terminalContext, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		terminalContextID,
	)
	if err != nil || !found || terminalContext.State != executionstore.ModelCallContextFailed ||
		terminalContext.RecoveryKind != "" {
		t.Fatalf("settled terminal context = %+v found=%v err=%v", terminalContext, found, err)
	}
	parent, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		parentContextID,
	)
	if err != nil || !found {
		t.Fatalf("load settled parent context = %+v found=%v err=%v", parent, found, err)
	}
	if compactionContextID == NilID {
		if parent.ID != terminalContext.ID || parent.State != terminalContext.State {
			t.Fatalf("settled normal context = %+v, want terminal context %+v", parent, terminalContext)
		}
	} else if parent.State != executionstore.ModelCallContextFailed ||
		parent.RecoveryKind != executionstore.ModelCallRecoveryCompact {
		t.Fatalf("settled compaction parent = %+v, want failed compact recovery", parent)
	}
	output, found, err := fixture.Store.Execution().GetModelOutputForContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		terminalContextID,
	)
	if err != nil || !found || output.StopReason != "error" ||
		output.ModelCallContextID != terminalContextID {
		t.Fatalf("settled terminal output = %+v found=%v err=%v", output, found, err)
	}
	if compactionContextID != NilID {
		compactionContext, found, err := fixture.Store.Execution().GetModelCallContext(
			ctx,
			testProjectID,
			fixture.AgentID,
			compactionContextID,
		)
		if err != nil || !found || compactionContext.ID != terminalContext.ID ||
			compactionContext.State != terminalContext.State {
			t.Fatalf("settled compaction context = %+v found=%v err=%v", compactionContext, found, err)
		}
	}
	if steeringInputID == NilID {
		return
	}
	var state string
	var admitted bool
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT state, admitted_event_id IS NOT NULL
FROM agent_inputs
WHERE project_id = $1 AND agent_id = $2 AND id = $3`,
		testProjectID,
		fixture.AgentID,
		steeringInputID,
	).Scan(&state, &admitted); err != nil {
		t.Fatalf("load steering waiting after terminal output: %v", err)
	}
	if state != "received" || admitted {
		t.Fatalf(
			"steering after terminal output state=%q admitted=%v, want received for the next model-ready claim",
			state,
			admitted,
		)
	}
}

func claimTestNormalModelCallForWork(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	claim executionstore.ClaimedAgentWork,
	now time.Time,
) executionstore.ModelCallClaim {
	t.Helper()
	agent, err := fixture.Store.Execution().GetAgentInProject(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("load agent for model context: %v", err)
	}
	var openingEventSequence int64
	if err := fixture.Store.pool.QueryRow(ctx, `SELECT max(event.sequence) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.agent_input_id = ANY($3::uuid[])`, testProjectID, fixture.AgentID, claim.Model.InputIDs).
		Scan(&openingEventSequence); err != nil {
		t.Fatalf("load opening event sequence: %v", err)
	}
	modelClaim, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		RuntimeLockID:      claim.RuntimeLock.ID,
		OpeningInputIDs:    claim.Model.InputIDs,
		AgentConfigID:      agent.CurrentConfigID,
		InputEventSequence: openingEventSequence,
	})
	if err != nil {
		t.Fatalf("claim model context: %v", err)
	}
	return modelClaim
}

func countAgentWakeups(t *testing.T, ctx context.Context, store *Store, agentID ID) int {
	t.Helper()
	var wakeups int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM agent_wakeups wake JOIN agents agent ON agent.id = wake.agent_id WHERE agent.project_id = $1 AND wake.agent_id = $2`, testProjectID, agentID).
		Scan(&wakeups); err != nil {
		t.Fatalf("count agent wakeups: %v", err)
	}
	return wakeups
}

func requireAgentWakeupCoverage(
	t *testing.T,
	ctx context.Context,
	store *Store,
	projectID, agentID ID,
) {
	t.Helper()
	var runnable, hasWakeup, hasRuntimeLock bool
	if err := store.pool.QueryRow(ctx, `
SELECT agent_next_wakeup_ready_at($1, $2) IS NOT NULL,
       EXISTS (SELECT 1 FROM agent_wakeups WHERE agent_id = $2),
       EXISTS (SELECT 1 FROM agent_runtime_locks WHERE agent_id = $2)
`, projectID, agentID).Scan(&runnable, &hasWakeup, &hasRuntimeLock); err != nil {
		t.Fatalf("load agent wakeup coverage: %v", err)
	}
	if runnable && !hasWakeup && !hasRuntimeLock {
		t.Fatalf("agent %s has runnable work without a wakeup or runtime lock", agentID)
	}
}

func assertModelCallContextRetryHistory(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	reference executionstore.ModelCallContextRecord,
	want int,
) {
	t.Helper()
	var count, distinctAttemptNumbers, minAttemptNumber, maxAttemptNumber int
	var err error
	if reference.OperationKind == executionstore.ModelCallOperationNormal {
		err = fixture.Store.pool.QueryRow(ctx, `
SELECT count(*), count(DISTINCT attempt_number), min(attempt_number), max(attempt_number)
FROM model_call_contexts
WHERE project_id = $1
  AND agent_id = $2
  AND operation_kind = 'normal'
  AND input_event_sequence = $3
  AND agent_config_id = $4`,
			testProjectID,
			fixture.AgentID,
			reference.InputEventSequence,
			reference.AgentConfigID,
		).Scan(&count, &distinctAttemptNumbers, &minAttemptNumber, &maxAttemptNumber)
	} else {
		err = fixture.Store.pool.QueryRow(ctx, `
SELECT count(*), count(DISTINCT attempt_number), min(attempt_number), max(attempt_number)
FROM model_call_contexts
WHERE project_id = $1
  AND agent_id = $2
  AND operation_kind = 'compaction'
  AND input_event_sequence = $3
  AND agent_config_id = $4
	AND source_event_sequence_end = $5`,
			testProjectID,
			fixture.AgentID,
			reference.InputEventSequence,
			reference.AgentConfigID,
			reference.SourceEventSequenceEnd,
		).Scan(&count, &distinctAttemptNumbers, &minAttemptNumber, &maxAttemptNumber)
	}
	if err != nil {
		t.Fatalf("load model call context retry history: %v", err)
	}
	if count != want || distinctAttemptNumbers != want ||
		minAttemptNumber != 1 || maxAttemptNumber != want {
		t.Fatalf(
			"model call context retry history count/distinct/min/max = %d/%d/%d/%d, want %d/%d/1/%d",
			count,
			distinctAttemptNumbers,
			minAttemptNumber,
			maxAttemptNumber,
			want,
			want,
			want,
		)
	}
}

func retryBackoffWithQueuedInput(
	t *testing.T,
	ctx context.Context,
	name string,
) (processDaemonFixture, executionstore.AgentInputRecord, time.Time) {
	t.Helper()
	fixture := newProcessDaemonFixture(t, ctx, name)
	openingInput, _, _, err := fixture.Store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      testProjectID,
			AgentID:        fixture.AgentID,
			Actor:          mustOmnaraActorParams(t, fixture.UserID),
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"start backed-off turn"}]`),
			DeliveryMode:   executionstore.DeliveryModeSteering,
			IdempotencyKey: name + "-opening",
		},
	)
	if err != nil {
		t.Fatalf("create retry opening input: %v", err)
	}
	admitted, found := admitNextAgentInputAndOpenTurnForTest(
		t,
		ctx,
		fixture.Store,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	)
	if !found || len(admitted.Events) != 1 {
		t.Fatalf("admit retry opening input found=%v turn=%+v", found, admitted)
	}
	agent, err := fixture.Store.Execution().GetAgentInProject(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("load retry agent: %v", err)
	}
	claim, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		RuntimeLockID:      fixture.Lock.ID,
		OpeningInputIDs:    []ID{openingInput.ID},
		AgentConfigID:      agent.CurrentConfigID,
		InputEventSequence: admitted.Events[0].Sequence,
	})
	if err != nil {
		t.Fatalf("claim retry model call: %v", err)
	}
	failed, err := fixture.Store.Execution().RecordRetryableModelCallFailure(
		ctx,
		executionstore.RecordRecoverableModelCallFailureInput{
			ProjectID:          testProjectID,
			AgentID:            fixture.AgentID,
			ModelCallContextID: claim.Context.ID,
			RuntimeLockID:      fixture.Lock.ID,
			ErrorKind:          "transient",
			ErrorCode:          "overloaded",
			ErrorMessage:       "provider overloaded",
			RetryDelay:         time.Hour,
		},
	)
	if err != nil {
		t.Fatalf("record retryable failure: %v", err)
	}
	if failed.RetryAt == nil {
		t.Fatal("retryable failure has no durable retry time")
	}
	retryAt := *failed.RetryAt
	queuedInput, _, _, err := fixture.Store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      testProjectID,
			AgentID:        fixture.AgentID,
			Actor:          mustOmnaraActorParams(t, fixture.UserID),
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"queued behind retry"}]`),
			DeliveryMode:   executionstore.DeliveryModeQueued,
			IdempotencyKey: name + "-queued",
		},
	)
	if err != nil {
		t.Fatalf("create queued input during retry backoff: %v", err)
	}
	if readyAt := agentWakeupReadyAt(t, ctx, fixture.Store, fixture.AgentID); !readyAt.Equal(retryAt) {
		t.Fatalf("initial retry wakeup = %s, want %s", readyAt, retryAt)
	}
	return fixture, queuedInput, retryAt
}

func agentWakeupReadyAt(t *testing.T, ctx context.Context, store *Store, agentID ID) time.Time {
	t.Helper()
	var readyAt time.Time
	if err := store.pool.QueryRow(ctx, `
SELECT wake.ready_at
FROM agent_wakeups wake
JOIN agents agent ON agent.id = wake.agent_id
WHERE agent.project_id = $1 AND wake.agent_id = $2
`, testProjectID, agentID).Scan(&readyAt); err != nil {
		t.Fatalf("load agent wakeup: %v", err)
	}
	return readyAt
}

func databaseStatementTime(t *testing.T, ctx context.Context, store *Store) time.Time {
	t.Helper()
	var now time.Time
	if err := store.pool.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&now); err != nil {
		t.Fatalf("load database statement time: %v", err)
	}
	return now
}

func requestAgentRuntimeCancelForTest(
	ctx context.Context,
	store *Store,
	projectID, agentID ID,
	now time.Time,
) (executionstore.AgentRuntimeLockRecord, error) {
	row, err := dbsqlc.New(store.pool).
		RequestAgentRuntimeCancel(ctx, dbsqlc.RequestAgentRuntimeCancelParams{ProjectID: projectID, AgentID: agentID})
	if errors.Is(err, pgx.ErrNoRows) {
		return executionstore.AgentRuntimeLockRecord{}, nil
	}
	if err != nil {
		return executionstore.AgentRuntimeLockRecord{}, err
	}
	return executionstore.IntegrationAgentRuntimeLockRecordFromCancelSQLC(row), nil
}
