//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestQueuedInputWaitsForAmbiguousCompactionRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, compaction := sentCompactionFrontierFixture(t, ctx, "queued_waits_compaction")
	queued := createFrontierRaceInput(
		t,
		ctx,
		fixture,
		executionstore.DeliveryModeQueued,
		"queued-waits-compaction",
		fixture.Now.Add(20*time.Second),
	)

	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	); err != nil {
		t.Fatalf("release ambiguous compaction runtime: %v", err)
	}
	modelContext := mustLoadModelCallContextForFrontierRace(t, ctx, fixture, compaction.Context.ID)
	if modelContext.State != executionstore.ModelCallContextFailed ||
		modelContext.RecoveryKind != executionstore.ModelCallRecoveryRetry ||
		modelContext.RetryAt == nil {
		t.Fatalf("released compaction context = %+v, want failed retry", modelContext)
	}
	assertAmbiguousRuntimeRecoveryForFrontierRace(t, modelContext)

	work, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		testClaimNextAgentWorkInput(),
	)
	if err != nil {
		t.Fatalf("claim ambiguous compaction retry: %v", err)
	}
	if !found || work.Kind != executionstore.AgentWorkModel || work.Model.Kind != executionstore.ModelWorkResume ||
		work.Model.ModelCallContextID != compaction.Context.ID {
		t.Fatalf(
			"claimed work = %+v found=%v, want compaction context %s before queued input",
			work,
			found,
			compaction.Context.ID,
		)
	}
	assertAgentInputWaitingForFrontierRace(t, ctx, fixture, queued.ID)
}

func TestSteeringStartsNewFrontierAfterAmbiguousNormalSendWithCapacity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, _, normal := newStartedNormalModelCallTestFixture(
		t,
		ctx,
		"steering_supersedes_ambiguous_normal",
	)
	steering := createFrontierRaceInput(
		t,
		ctx,
		fixture,
		executionstore.DeliveryModeSteering,
		"steering-supersedes-ambiguous-normal",
		fixture.Now.Add(20*time.Second),
	)

	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	); err != nil {
		t.Fatalf("release ambiguous normal runtime: %v", err)
	}
	modelContext := mustLoadModelCallContextForFrontierRace(t, ctx, fixture, normal.Context.ID)
	if modelContext.State != executionstore.ModelCallContextFailed ||
		modelContext.RecoveryKind != executionstore.ModelCallRecoveryRetry ||
		modelContext.RetryAt == nil {
		t.Fatalf("released normal context = %+v, want failed retry with capacity", modelContext)
	}
	assertAmbiguousRuntimeRecoveryForFrontierRace(t, modelContext)

	work, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		testClaimNextAgentWorkInput(),
	)
	if err != nil {
		t.Fatalf("claim steering after ambiguous normal send: %v", err)
	}
	if !found || work.Kind != executionstore.AgentWorkModel || work.Model.Kind != executionstore.ModelWorkStart ||
		work.Model.AdmittedInputTurn.Turn.ID == NilID ||
		len(work.Model.InputIDs) == 0 ||
		work.Model.InputIDs[len(work.Model.InputIDs)-1] != steering.ID {
		t.Fatalf("steering work = %+v found=%v, want fresh admitted turn", work, found)
	}
	fresh := claimTestNormalModelCallForWork(t, ctx, fixture, work, time.Now().UTC())
	assertFrontierRaceContextState(t, ctx, fixture, normal.Context.ID, executionstore.ModelCallContextFailed)
	if fresh.Context.ID == normal.Context.ID || fresh.Context.AttemptNumber != 1 {
		t.Fatalf("fresh steering claim = %+v, want a new context at attempt 1", fresh)
	}
	assertModelCallContextRetryHistory(t, ctx, fixture, normal.Context, 1)
}

func TestSteeringStartsNewFrontierAfterAmbiguousCompactionSendWithCapacity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, compaction := sentCompactionFrontierFixture(t, ctx, "steering_ambiguous_compaction")
	parent, found, err := fixture.Store.Execution().GetNormalModelCallContextForFrontier(
		ctx,
		testProjectID,
		fixture.AgentID,
		compaction.Context.InputEventSequence,
	)
	if err != nil || !found {
		t.Fatalf("load compaction parent: found=%v err=%v", found, err)
	}
	steering := createFrontierRaceInput(
		t,
		ctx,
		fixture,
		executionstore.DeliveryModeSteering,
		"steering-supersedes-ambiguous-compaction",
		fixture.Now.Add(20*time.Second),
	)

	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	); err != nil {
		t.Fatalf("release ambiguous compaction runtime: %v", err)
	}
	modelContext := mustLoadModelCallContextForFrontierRace(t, ctx, fixture, compaction.Context.ID)
	if modelContext.State != executionstore.ModelCallContextFailed ||
		modelContext.RecoveryKind != executionstore.ModelCallRecoveryRetry ||
		modelContext.RetryAt == nil {
		t.Fatalf("released compaction context = %+v, want failed retry with capacity", modelContext)
	}
	assertAmbiguousRuntimeRecoveryForFrontierRace(t, modelContext)

	work, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		testClaimNextAgentWorkInput(),
	)
	if err != nil {
		t.Fatalf("claim steering after ambiguous compaction send: %v", err)
	}
	if !found || work.Kind != executionstore.AgentWorkModel || work.Model.Kind != executionstore.ModelWorkStart ||
		work.Model.AdmittedInputTurn.Turn.ID == NilID ||
		len(work.Model.InputIDs) == 0 ||
		work.Model.InputIDs[len(work.Model.InputIDs)-1] != steering.ID {
		t.Fatalf("steering work = %+v found=%v, want fresh admitted turn", work, found)
	}
	fresh := claimTestNormalModelCallForWork(t, ctx, fixture, work, time.Now().UTC())
	assertFrontierRaceContextState(t, ctx, fixture, compaction.Context.ID, executionstore.ModelCallContextFailed)
	assertFrontierRaceContextState(
		t,
		ctx,
		fixture,
		parent.ID,
		executionstore.ModelCallContextFailed,
	)
	if fresh.Context.AttemptNumber != 1 {
		t.Fatalf("fresh post-compaction steering attempt = %d, want 1", fresh.Context.AttemptNumber)
	}
	assertModelCallContextRetryHistory(t, ctx, fixture, compaction.Context, 1)
}

func TestCompletedCompactionPublicationWinsBeforeWaitingSteering(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, compaction := sentCompactionFrontierFixture(t, ctx, "completed_compaction_wins")
	parent, found, err := fixture.Store.Execution().GetNormalModelCallContextForFrontier(
		ctx,
		testProjectID,
		fixture.AgentID,
		compaction.Context.InputEventSequence,
	)
	if err != nil || !found {
		t.Fatalf("load compaction parent: found=%v err=%v", found, err)
	}
	steering := createFrontierRaceInput(
		t,
		ctx,
		fixture,
		executionstore.DeliveryModeSteering,
		"steering-after-completed-compaction",
		fixture.Now.Add(20*time.Second),
	)

	checkpoint, err := publishCheckpointForRangeTest(
		t,
		ctx,
		fixture,
		compaction,
		"accepted summary before steering",
		fixture.Now.Add(21*time.Second),
	)
	if err != nil {
		t.Fatalf("publish complete compaction with waiting steering: %v", err)
	}
	if checkpoint.ID == NilID || checkpoint.CheckpointEventID == NilID {
		t.Fatalf("published checkpoint = %+v, want durable checkpoint and event", checkpoint)
	}
	assertFrontierRaceContextState(t, ctx, fixture, compaction.Context.ID, executionstore.ModelCallContextSucceeded)
	assertFrontierRaceContextState(
		t,
		ctx,
		fixture,
		parent.ID,
		executionstore.ModelCallContextFailed,
	)
	assertAgentInputWaitingForFrontierRace(t, ctx, fixture, steering.ID)

	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	); err != nil {
		t.Fatalf("release runtime after checkpoint publication: %v", err)
	}
	work, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		testClaimNextAgentWorkInput(),
	)
	if err != nil {
		t.Fatalf("claim steering after accepted checkpoint: %v", err)
	}
	if !found || work.Kind != executionstore.AgentWorkModel || work.Model.Kind != executionstore.ModelWorkStart ||
		work.Model.AdmittedInputTurn.Turn.ID == NilID ||
		len(work.Model.InputIDs) == 0 ||
		work.Model.InputIDs[len(work.Model.InputIDs)-1] != steering.ID {
		t.Fatalf("post-checkpoint steering work = %+v found=%v", work, found)
	}
}

func sentCompactionFrontierFixture(
	t *testing.T,
	ctx context.Context,
	name string,
) (processDaemonFixture, executionstore.ModelCallClaim) {
	t.Helper()
	fixture, admitted, _ := newMultiInputContinuationSeedFixture(t, ctx, name)
	frontier := admitted.Events[len(admitted.Events)-1].Sequence
	claim := claimSentCompactionForRangeTest(
		t,
		ctx,
		fixture,
		1,
		frontier,
		frontier,
		fixture.Now.Add(10*time.Second),
	)
	return fixture, claim
}

func createFrontierRaceInput(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	deliveryMode executionstore.AgentInputDeliveryMode,
	idempotencyKey string,
	now time.Time,
) executionstore.AgentInputRecord {
	t.Helper()
	input, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        fixture.AgentID,
		Actor:          mustOmnaraActorParams(t, fixture.UserID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"new frontier input"}]`),
		DeliveryMode:   deliveryMode,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		t.Fatalf("create %s input: %v", deliveryMode, err)
	}
	return input
}

func mustLoadModelCallContextForFrontierRace(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	modelCallContextID ID,
) executionstore.ModelCallContextRecord {
	t.Helper()
	modelContext, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		modelCallContextID,
	)
	if err != nil || !found {
		t.Fatalf("load model call context %s: found=%v err=%v", modelCallContextID, found, err)
	}
	return modelContext
}

func assertAgentInputWaitingForFrontierRace(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	inputID ID,
) {
	t.Helper()
	var waiting int
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT count(*)::int
FROM agent_inputs input
WHERE input.project_id = $1
  AND input.agent_id = $2
  AND input.id = $3
  AND input.state = 'received'
  AND input.admitted_event_id IS NULL
`, testProjectID, fixture.AgentID, inputID).Scan(&waiting); err != nil {
		t.Fatalf("read waiting input %s: %v", inputID, err)
	}
	if waiting != 1 {
		t.Fatalf("waiting input %s rows = %d, want 1", inputID, waiting)
	}
}

func assertFrontierRaceContextState(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	contextID ID,
	want executionstore.ModelCallState,
) {
	t.Helper()
	contextRecord, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		contextID,
	)
	if err != nil || !found || contextRecord.State != want {
		t.Fatalf(
			"model call context %s = %+v found=%v err=%v, want state %s",
			contextID,
			contextRecord,
			found,
			err,
			want,
		)
	}
}

func assertAmbiguousRuntimeRecoveryForFrontierRace(
	t *testing.T,
	contextRecord executionstore.ModelCallContextRecord,
) {
	t.Helper()
	var details map[string]any
	if err := json.Unmarshal(contextRecord.ErrorDetails, &details); err != nil {
		t.Fatalf("decode runtime recovery details: %v", err)
	}
	if ambiguous, ok := details["outcome_ambiguous"].(bool); !ok || !ambiguous {
		t.Fatalf("runtime recovery details = %s, want outcome_ambiguous=true", contextRecord.ErrorDetails)
	}
}
