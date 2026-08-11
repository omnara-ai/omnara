//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestMaintenanceRecoveryCrossesWakeupAndRuntimeLockBatchBoundaries(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"maintenance-cursor@example.com",
		"Maintenance Cursor",
	)
	configID := mustCreateAgentConfig(t, ctx, store, testProjectID, "maintenance-cursor", now)

	const agentCount = 501
	for index := 0; index < agentCount; index++ {
		agent, err := store.Execution().CreateAgentFixture(ctx, executionstore.AgentFixtureInput{
			ProjectID:       testProjectID,
			CurrentConfigID: configID,
		})
		if err != nil {
			t.Fatalf("create maintenance cursor agent %d: %v", index, err)
		}
		if _, _, _, err := store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
			ProjectID:      testProjectID,
			AgentID:        agent.ID,
			Actor:          mustOmnaraActorParams(t, user.ID),
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"maintenance cursor work"}]`),
			IdempotencyKey: fmt.Sprintf("maintenance-cursor-input-%03d", index),
		}); err != nil {
			t.Fatalf("create maintenance cursor input %d: %v", index, err)
		}
	}
	if tag, err := pool.Exec(ctx, `DELETE FROM agent_wakeups wake USING agents agent WHERE agent.id = wake.agent_id AND agent.project_id = $1`, testProjectID); err != nil {
		t.Fatalf("delete initial maintenance cursor wakeups: %v", err)
	} else if tag.RowsAffected() != agentCount {
		t.Fatalf("deleted initial wakeups = %d, want %d", tag.RowsAffected(), agentCount)
	}

	staleAt := now.Add(-2 * time.Hour)
	if tag, err := pool.Exec(ctx, `
INSERT INTO agent_runtime_locks(
  agent_id, worker_process_id,
  started_at, renewed_at, lease_expires_at
)
SELECT id,
       $3,
       $2::timestamptz - interval '2 minutes',
       $2::timestamptz - interval '1 minute',
       $2::timestamptz
FROM agents
WHERE project_id = $1
`, testProjectID, staleAt, testWorkerProcessID); err != nil {
		t.Fatalf("seed stale maintenance cursor locks: %v", err)
	} else if tag.RowsAffected() != agentCount {
		t.Fatalf("seeded stale locks = %d, want %d", tag.RowsAffected(), agentCount)
	}

	firstReaped, err := store.Execution().ReapExpiredAgentRuntimeLocks(ctx, 500)
	if err != nil {
		t.Fatalf("reap first expired-lock batch across maintenance cursor: %v", err)
	}
	secondReaped, err := store.Execution().ReapExpiredAgentRuntimeLocks(ctx, 500)
	if err != nil {
		t.Fatalf("reap second expired-lock batch across maintenance cursor: %v", err)
	}
	reaped := firstReaped + secondReaped
	if reaped != agentCount {
		t.Fatalf("reaped stale locks = %d, want %d", reaped, agentCount)
	}
	assertMaintenanceCursorCounts(t, ctx, pool, agentCount, 0, "runtime_lock_reap")

	if tag, err := pool.Exec(ctx, `DELETE FROM agent_wakeups wake USING agents agent WHERE agent.id = wake.agent_id AND agent.project_id = $1`, testProjectID); err != nil {
		t.Fatalf("delete reaper wakeups before rebuild: %v", err)
	} else if tag.RowsAffected() != agentCount {
		t.Fatalf("deleted reaper wakeups = %d, want %d", tag.RowsAffected(), agentCount)
	}
	rebuilt, err := store.Execution().RebuildMissingAgentWakeupsForAllProjects(ctx)
	if err != nil {
		t.Fatalf("rebuild wakeups across maintenance cursor: %v", err)
	}
	if rebuilt != agentCount {
		t.Fatalf("rebuilt wakeups = %d, want %d", rebuilt, agentCount)
	}
	assertMaintenanceCursorCounts(t, ctx, pool, agentCount, 0, "maintenance_rebuild")
}

func assertMaintenanceCursorCounts(
	t *testing.T,
	ctx context.Context,
	pool interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	wantWakeups, wantLocks int,
	wakeupReason string,
) {
	t.Helper()
	var wakeups, locks int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE wake.metadata->>'reason' = $2),
       (SELECT count(*) FROM agent_runtime_locks runtime_lock
        JOIN agents lock_agent ON lock_agent.id = runtime_lock.agent_id
        WHERE lock_agent.project_id = $1)
FROM agent_wakeups wake
JOIN agents agent ON agent.id = wake.agent_id
WHERE agent.project_id = $1
`, testProjectID, wakeupReason).Scan(&wakeups, &locks); err != nil {
		t.Fatalf("count maintenance cursor state: %v", err)
	}
	if wakeups != wantWakeups || locks != wantLocks {
		t.Fatalf(
			"maintenance cursor wakeups/locks = %d/%d, want %d/%d",
			wakeups,
			locks,
			wantWakeups,
			wantLocks,
		)
	}
}

func TestClaimNextAgentWorkRecoversUnstartedTurnAfterRuntimeRelease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "claim_recovers_unstarted_turn")
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
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"recover me"}]`),
		IdempotencyKey: "claim-recovers-unstarted-turn",
	})
	if err != nil {
		t.Fatalf("create content input: %v", err)
	}
	firstClaim, found, err := fixture.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
	if err != nil {
		t.Fatalf("claim initial input work: %v", err)
	}
	if !found || firstClaim.Kind != executionstore.AgentWorkModel || firstClaim.Model.TurnID == NilID ||
		!claimedOpeningInputIDsEqual(firstClaim, input.ID) {
		t.Fatalf("initial claim = %+v found=%v, want executable turn for input %s", firstClaim, found, input.ID)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		firstClaim.RuntimeLock.ID,
	); err != nil {
		t.Fatalf("release runtime before model context: %v", err)
	}
	if err := fixture.Store.Execution().DeleteAgentWakeup(ctx, testProjectID, fixture.AgentID); err != nil {
		t.Fatalf("delete wakeup before rebuild: %v", err)
	}
	rebuilt, err := fixture.Store.Execution().RebuildMissingAgentWakeups(ctx, testProjectID)
	if err != nil {
		t.Fatalf("rebuild unstarted turn wakeup: %v", err)
	}
	if rebuilt != 1 {
		t.Fatalf("rebuild restored %d wakeups for unstarted turn, want 1", rebuilt)
	}
	if wakeups := countAgentWakeups(t, ctx, fixture.Store, fixture.AgentID); wakeups != 1 {
		t.Fatalf("rebuilt unstarted-turn wakeups = %d, want 1", wakeups)
	}
	recoveredClaim, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		testClaimNextAgentWorkInput(),
	)
	if err != nil {
		t.Fatalf("claim recovered unstarted turn: %v", err)
	}
	if !found || recoveredClaim.Kind != executionstore.AgentWorkModel {
		t.Fatalf("recovered claim = %+v found=%v, want executable", recoveredClaim, found)
	}
	if recoveredClaim.Model.TurnID != firstClaim.Model.TurnID || !claimedOpeningInputIDsEqual(recoveredClaim, input.ID) {
		t.Fatalf("recovered claim = %+v, want same turn/input", recoveredClaim)
	}
}

func TestStartedModelContextOnLiveLeaseIsNotSchedulerResumable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "claim_keeps_active_context_claimable")
	now := fixture.Now.Add(time.Minute)
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
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"active context"}]`),
		IdempotencyKey: "claim-keeps-active-context-claimable",
	})
	if err != nil {
		t.Fatalf("create content input: %v", err)
	}
	claim, found, err := fixture.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
	if err != nil {
		t.Fatalf("claim input work: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkModel || !claimedOpeningInputIDsEqual(claim, input.ID) {
		t.Fatalf("claim = %+v found=%v, want executable input %s", claim, found, input.ID)
	}
	claimTestNormalModelCallForWork(t, ctx, fixture, claim, now.Add(2*time.Second))
	if seed, found, err := fixture.Store.Execution().NextAgentModelWork(
		ctx,
		testProjectID,
		fixture.AgentID,
	); err != nil || found {
		t.Fatalf(
			"live-lease started context model work = %+v found=%v error=%v, want none",
			seed,
			found,
			err,
		)
	}
}

func TestClaimNextAgentWorkRecoversFailedAbandonedContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "claim_recovers_failed_abandoned_context")
	fixture.Store = newIntegrationStore(
		fixture.Store.pool,
		WithModelCallRetryBackoff(executionstore.ModelCallRetryBackoff),
	)
	now := fixture.Now.Add(time.Minute)
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
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"retry abandoned"}]`),
		IdempotencyKey: "claim-recovers-failed-abandoned-context",
	})
	if err != nil {
		t.Fatalf("create content input: %v", err)
	}
	claim, found, err := fixture.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
	if err != nil {
		t.Fatalf("claim input work: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkModel || !claimedOpeningInputIDsEqual(claim, input.ID) {
		t.Fatalf("claim = %+v found=%v, want executable input %s", claim, found, input.ID)
	}
	modelClaim := claimTestNormalModelCallForWork(t, ctx, fixture, claim, now.Add(2*time.Second))
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		claim.RuntimeLock.ID,
	); err != nil {
		t.Fatalf("release runtime before provider response: %v", err)
	}
	contextRecord, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		modelClaim.Context.ID,
	)
	if err != nil || !found {
		t.Fatalf("load interrupted context: found=%v err=%v", found, err)
	}
	var errorDetails map[string]any
	if err := json.Unmarshal(contextRecord.ErrorDetails, &errorDetails); err != nil {
		t.Fatalf("decode interrupted context details: %v", err)
	}
	if contextRecord.State != executionstore.ModelCallContextFailed ||
		contextRecord.RecoveryKind != executionstore.ModelCallRecoveryRetry ||
		contextRecord.ErrorKind != "runtime" ||
		contextRecord.ErrorCode != "runtime_released_before_model_result_acceptance" ||
		contextRecord.RetryAt == nil || contextRecord.CompletedAt == nil ||
		!contextRecord.RetryAt.After(*contextRecord.CompletedAt) ||
		errorDetails["outcome_ambiguous"] != true {
		t.Fatalf(
			"interrupted context = %+v, want ambiguous failed/retry runtime interruption with delayed retry",
			contextRecord,
		)
	}
	wantRetryAt := contextRecord.CompletedAt.Add(executionstore.ModelCallRetryBackoff(
		contextRecord.AttemptNumber,
		contextRecord.ID.String(),
	))
	if !contextRecord.RetryAt.Equal(wantRetryAt) {
		t.Fatalf("interrupted retry_at = %s, want exact durable backoff %s", contextRecord.RetryAt, wantRetryAt)
	}
	var wakeupAt time.Time
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT wake.ready_at
FROM agent_wakeups wake
JOIN agents agent ON agent.id = wake.agent_id
WHERE agent.project_id = $1 AND wake.agent_id = $2
`, testProjectID, fixture.AgentID).Scan(&wakeupAt); err != nil {
		t.Fatalf("load interrupted-context wakeup: %v", err)
	}
	if !wakeupAt.Equal(*contextRecord.RetryAt) {
		t.Fatalf("interrupted wakeup = %s, want retry_at %s", wakeupAt, contextRecord.RetryAt)
	}
	if err := fixture.Store.Execution().DeleteAgentWakeup(ctx, testProjectID, fixture.AgentID); err != nil {
		t.Fatalf("delete wakeup before rebuild: %v", err)
	}
	rebuilt, err := fixture.Store.Execution().RebuildMissingAgentWakeups(ctx, testProjectID)
	if err != nil {
		t.Fatalf("rebuild failed abandoned context wakeup: %v", err)
	}
	if rebuilt != 1 {
		t.Fatalf("rebuild restored %d wakeups for failed abandoned context, want 1", rebuilt)
	}
	if turn, err := fixture.Store.q.CurrentContinuableAgentTurn(
		ctx,
		dbsqlc.CurrentContinuableAgentTurnParams{ProjectID: testProjectID, AgentID: fixture.AgentID},
	); err != nil {
		t.Fatalf("load current continuable turn for recoverable failed context: %v", err)
	} else if turn.ID != claim.Model.TurnID {
		t.Fatalf("recoverable failed context current turn = %+v, want turn %s", turn, claim.Model.TurnID)
	}
	if wakeups := countAgentWakeups(t, ctx, fixture.Store, fixture.AgentID); wakeups != 1 {
		t.Fatalf("rebuilt recoverable-context wakeups = %d, want 1", wakeups)
	}
	if _, err := fixture.Store.pool.Exec(
		ctx,
		`ALTER TABLE model_call_contexts DISABLE TRIGGER model_call_contexts_transition_guard`,
	); err != nil {
		t.Fatalf("disable model context transition guard: %v", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE model_call_contexts
SET retry_at = statement_timestamp() - interval '1 millisecond'
WHERE project_id = $1 AND agent_id = $2 AND id = $3`,
		testProjectID,
		fixture.AgentID,
		modelClaim.Context.ID,
	); err != nil {
		t.Fatalf("mature failed-context retry deadline: %v", err)
	}
	if _, err := fixture.Store.pool.Exec(
		ctx,
		`ALTER TABLE model_call_contexts ENABLE TRIGGER model_call_contexts_transition_guard`,
	); err != nil {
		t.Fatalf("enable model context transition guard: %v", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE agent_wakeups wake
SET ready_at = statement_timestamp() - interval '1 millisecond'
FROM agents agent
WHERE agent.id = wake.agent_id AND agent.project_id = $1 AND wake.agent_id = $2`,
		testProjectID,
		fixture.AgentID,
	); err != nil {
		t.Fatalf("mature failed-context retry wakeup: %v", err)
	}
	recovered, found, err := fixture.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
	if err != nil {
		t.Fatalf("claim failed abandoned context recovery: %v", err)
	}
	if !found || recovered.Kind != executionstore.AgentWorkModel || recovered.Model.TurnID != claim.Model.TurnID ||
		!claimedOpeningInputIDsEqual(recovered, input.ID) {
		t.Fatalf("failed abandoned context recovery claim = %+v found=%v, want same executable turn/input", recovered, found)
	}
	retryClaim, err := fixture.Store.Execution().ClaimNextModelCallContext(ctx, executionstore.ClaimNextModelCallContextInput{
		ProjectID:                     testProjectID,
		AgentID:                       fixture.AgentID,
		PredecessorModelCallContextID: modelClaim.Context.ID,
		RuntimeLockID:                 recovered.RuntimeLock.ID,
	})
	if err != nil {
		t.Fatalf("claim second durable model context: %v", err)
	}
	if !retryClaim.Created || !retryClaim.Claimed ||
		retryClaim.Context.AttemptNumber != 2 ||
		retryClaim.Context.ID == modelClaim.Context.ID {
		t.Fatalf("retry claim = %+v, want a new context at attempt 2", retryClaim)
	}
	cancelResult, err := fixture.Store.Execution().CancelAgent(
		ctx,
		executionstore.CancelAgentInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			Actor:     mustOmnaraActorParams(t, fixture.UserID),
		},
	)
	if err != nil {
		t.Fatalf("cancel failed abandoned context: %v", err)
	}
	if cancelResult.Event.ID == NilID || !cancelResult.Affected {
		t.Fatalf(
			"cancel failed abandoned context = event %+v affected %v, want affected event",
			cancelResult.Event,
			cancelResult.Affected,
		)
	}
}

func TestClaimNextAgentWorkRecoversAmbiguousAbandonedContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "claim_recovers_ambiguous_abandoned_context")
	now := fixture.Now.Add(time.Minute)
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
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"retry ambiguous send"}]`),
		IdempotencyKey: "claim-recovers-ambiguous-abandoned-context",
	})
	if err != nil {
		t.Fatalf("create content input: %v", err)
	}
	claim, found, err := fixture.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
	if err != nil {
		t.Fatalf("claim input work: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkModel || !claimedOpeningInputIDsEqual(claim, input.ID) {
		t.Fatalf("claim = %+v found=%v, want executable input %s", claim, found, input.ID)
	}
	modelClaim := claimTestNormalModelCallForWork(t, ctx, fixture, claim, now.Add(2*time.Second))
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		claim.RuntimeLock.ID,
	); err != nil {
		t.Fatalf("release runtime with unresolved model call: %v", err)
	}
	contextRecord, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		modelClaim.Context.ID,
	)
	if err != nil || !found {
		t.Fatalf("load interrupted context: found=%v err=%v", found, err)
	}
	if contextRecord.State != executionstore.ModelCallContextFailed ||
		contextRecord.RecoveryKind != executionstore.ModelCallRecoveryRetry ||
		contextRecord.ErrorKind != "runtime" ||
		contextRecord.ErrorCode != "runtime_released_before_model_result_acceptance" ||
		contextRecord.RetryAt == nil {
		t.Fatalf(
			"interrupted context = %+v, want failed/retry with an immediate retry time",
			contextRecord,
		)
	}
	var errorDetails map[string]any
	if err := json.Unmarshal(contextRecord.ErrorDetails, &errorDetails); err != nil {
		t.Fatalf("decode interrupted context details: %v", err)
	}
	if ambiguous, ok := errorDetails["outcome_ambiguous"].(bool); !ok || !ambiguous {
		t.Fatalf("interrupted context details = %s, want outcome_ambiguous=true", contextRecord.ErrorDetails)
	}
	recovered, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		testClaimNextAgentWorkInput(),
	)
	if err != nil {
		t.Fatalf("claim ambiguous abandoned context recovery: %v", err)
	}
	if !found || recovered.Kind != executionstore.AgentWorkModel || recovered.Model.TurnID != claim.Model.TurnID ||
		!claimedOpeningInputIDsEqual(recovered, input.ID) {
		t.Fatalf("ambiguous recovery claim = %+v found=%v, want same executable turn/input", recovered, found)
	}
	retryClaim, err := fixture.Store.Execution().ClaimNextModelCallContext(ctx, executionstore.ClaimNextModelCallContextInput{
		ProjectID:                     testProjectID,
		AgentID:                       fixture.AgentID,
		PredecessorModelCallContextID: modelClaim.Context.ID,
		RuntimeLockID:                 recovered.RuntimeLock.ID,
	})
	if err != nil {
		t.Fatalf("claim retry after ambiguous interruption: %v", err)
	}
	if !retryClaim.Created || !retryClaim.Claimed ||
		retryClaim.Context.AttemptNumber != 2 ||
		retryClaim.Context.ID == modelClaim.Context.ID {
		t.Fatalf("retry claim = %+v, want a new context at attempt 2", retryClaim)
	}
}

func TestReleaseExhaustedModelContextIsTerminal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, work, modelClaim := claimExhaustedNormalModelContext(
		t,
		ctx,
		"release_exhausted_model_context",
	)

	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		work.RuntimeLock.ID,
	); err != nil {
		t.Fatalf("release exhausted runtime: %v", err)
	}
	assertExhaustedNormalModelContextTerminalized(
		t,
		ctx,
		fixture,
		modelClaim,
		"runtime_released_before_model_result_acceptance",
	)
}

func TestReapExhaustedModelContextIsTerminal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, work, modelClaim := claimExhaustedNormalModelContext(
		t,
		ctx,
		"reap_exhausted_model_context",
	)
	expireAgentRuntimeLockForTest(t, ctx, fixture.Store, work.RuntimeLock.ID)
	reaped, err := fixture.Store.Execution().ReapExpiredAgentRuntimeLocks(ctx, 100)
	if err != nil || reaped != 1 {
		t.Fatalf("reap exhausted runtime: reaped=%d err=%v", reaped, err)
	}
	assertExhaustedNormalModelContextTerminalized(
		t,
		ctx,
		fixture,
		modelClaim,
		"runtime_lease_expired_before_model_result_acceptance",
	)
}

func TestReapExhaustedCompactionContextIsTerminal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, work, parentClaim, compactionClaim := claimExhaustedCompactionModelContext(
		t,
		ctx,
		"reap_exhausted_compaction_context",
	)
	expireAgentRuntimeLockForTest(t, ctx, fixture.Store, work.RuntimeLock.ID)
	reaped, err := fixture.Store.Execution().ReapExpiredAgentRuntimeLocks(ctx, 100)
	if err != nil || reaped != 1 {
		t.Fatalf("reap exhausted compaction runtime: reaped=%d err=%v", reaped, err)
	}
	assertExhaustedCompactionContextTerminalized(
		t,
		ctx,
		fixture,
		parentClaim,
		compactionClaim,
		"runtime_lease_expired_before_model_result_acceptance",
	)
}

func TestReapedModelCallWorkerCannotPublishAfterReplacementClaim(t *testing.T) {
	t.Parallel()
	t.Run("model output", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		fixture, oldWork, oldClaim, replacementClaim :=
			claimReplacementAfterReapedNormalModelCall(t, ctx, "stale_worker_model_output")
		_, err := fixture.Store.Execution().RecordModelOutputAndCompleteContext(
			ctx,
			executionstore.RecordModelOutputAndCompleteContextInput{
				ProjectID:          testProjectID,
				AgentID:            fixture.AgentID,
				RuntimeLockID:      oldWork.RuntimeLock.ID,
				ModelCallContextID: oldClaim.Context.ID,
				ProviderResponse: modelenvelope.ResponseEnvelope{
					RequestedProviderModelSlug: modelProviderSlugForContext(
						t, ctx, fixture.Store, testProjectID, fixture.AgentID, oldClaim.Context.ID,
					),
					APIFormat:  modelprotocol.APIFormatOpenAIResponses,
					APIVariant: modelprotocol.APIVariantDefault,
					Normalized: modelenvelope.ResponseNormalized{
						ID:         "resp_stale_worker",
						Content:    []modelenvelope.ResponsePart{{Type: "text", Text: "late output"}},
						StopReason: modelenvelope.StopReasonEndTurn,
					},
				},
			},
		)
		if !errors.Is(err, storeerr.ErrRuntimeLockInactive) {
			t.Fatalf("stale worker publication error = %v, want %v", err, storeerr.ErrRuntimeLockInactive)
		}
		if _, found, err := fixture.Store.Execution().GetModelOutputForContext(
			ctx,
			testProjectID,
			fixture.AgentID,
			oldClaim.Context.ID,
		); err != nil || found {
			t.Fatalf("stale worker output found=%v err=%v, want none", found, err)
		}
		assertModelCallContextState(t, ctx, fixture, oldClaim.Context.ID, executionstore.ModelCallContextFailed)
		assertReplacementModelCallContextUnchanged(t, ctx, fixture, replacementClaim)
	})

	t.Run("context checkpoint", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		fixture, oldWork, oldClaim, replacementWork, replacementClaim :=
			claimReplacementAfterReapedCompaction(t, ctx, "stale_worker_checkpoint")
		_, err := fixture.Store.Execution().PublishContextCheckpoint(ctx, executionstore.PublishContextCheckpointInput{
			ProjectID:          testProjectID,
			AgentID:            fixture.AgentID,
			RuntimeLockID:      oldWork.RuntimeLock.ID,
			ModelCallContextID: oldClaim.Context.ID,
			Summary:            "late checkpoint",
			APIFormat:          modelprotocol.APIFormatOpenAIResponses,
			APIVariant:         modelprotocol.APIVariantDefault,
			ProviderResponseID: "resp_stale_checkpoint",
		})
		if !errors.Is(err, storeerr.ErrRuntimeLockInactive) {
			t.Fatalf("stale checkpoint publication error = %v, want %v", err, storeerr.ErrRuntimeLockInactive)
		}
		if _, found, err := fixture.Store.Execution().GetContextCheckpointByProducerContext(
			ctx,
			testProjectID,
			fixture.AgentID,
			oldClaim.Context.ID,
		); err != nil || found {
			t.Fatalf("stale worker checkpoint found=%v err=%v, want none", found, err)
		}
		assertModelCallContextState(t, ctx, fixture, oldClaim.Context.ID, executionstore.ModelCallContextFailed)
		assertReplacementModelCallContextUnchanged(t, ctx, fixture, replacementClaim)
		checkpoint, err := fixture.Store.Execution().PublishContextCheckpoint(ctx, executionstore.PublishContextCheckpointInput{
			ProjectID:          testProjectID,
			AgentID:            fixture.AgentID,
			RuntimeLockID:      replacementWork.RuntimeLock.ID,
			ModelCallContextID: replacementClaim.Context.ID,
			Summary:            "replacement checkpoint",
			APIFormat:          modelprotocol.APIFormatOpenAIResponses,
			APIVariant:         modelprotocol.APIVariantDefault,
			ProviderResponseID: "resp_replacement_checkpoint",
		})
		if err != nil {
			t.Fatalf("replacement checkpoint publication: %v", err)
		}
		if checkpoint.ProducerModelCallContextID != replacementClaim.Context.ID {
			t.Fatalf(
				"replacement checkpoint producer = context %s, want %s",
				checkpoint.ProducerModelCallContextID,
				replacementClaim.Context.ID,
			)
		}
		assertModelCallContextState(t, ctx, fixture, oldClaim.Context.ID, executionstore.ModelCallContextFailed)
		assertModelCallContextState(t, ctx, fixture, replacementClaim.Context.ID, executionstore.ModelCallContextSucceeded)
		contextRecord, found, err := fixture.Store.Execution().GetModelCallContext(
			ctx,
			testProjectID,
			fixture.AgentID,
			replacementClaim.Context.ID,
		)
		if err != nil || !found {
			t.Fatalf("load replacement checkpoint context: found=%v err=%v", found, err)
		}
		if contextRecord.State != executionstore.ModelCallContextSucceeded {
			t.Fatalf(
				"replacement checkpoint context state = %q, want %q",
				contextRecord.State,
				executionstore.ModelCallContextSucceeded,
			)
		}
	})
}

func TestCompleteRuntimeToolCallRejectsCanceledRuntime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "runtime_tool_canceled_runtime")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"runtime_tool_canceled_runtime",
		"run_command",
	)
	now := fixture.Now.Add(time.Minute)
	if _, err := startAsyncToolCallForTest(
		ctx,
		fixture.Store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    toolCallID,
			RuntimeLockID: fixture.Lock.ID,
		},
	); err != nil {
		t.Fatalf("start runtime tool call: %v", err)
	}
	if _, err := requestAgentRuntimeCancelForTest(
		ctx,
		fixture.Store,
		testProjectID,
		fixture.AgentID,
		now.Add(time.Second),
	); err != nil {
		t.Fatalf("cancel runtime: %v", err)
	}
	contentParts, err := executionstore.ToolResultContentParts(json.RawMessage(`{"ok":false,"error":"runtime canceled"}`))
	if err != nil {
		t.Fatalf("build runtime failure content parts: %v", err)
	}
	if _, err := fixture.Store.Execution().CompleteRuntimeToolCall(ctx, executionstore.CompleteRuntimeToolCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ID:                 toolCallID,
		RuntimeLockID:      fixture.Lock.ID,
		Outcome:            executionstore.ToolResultOutcomeFailed,
		ResultContentParts: contentParts,
	}); !errors.Is(err, storeerr.ErrRuntimeLockInactive) {
		t.Fatalf("complete runtime tool call after cancel error = %v, want runtime lock inactive", err)
	}
	var wakeups int
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT count(*)
FROM agent_wakeups wake
JOIN agents agent ON agent.id = wake.agent_id
WHERE agent.project_id = $1
  AND wake.agent_id = $2
  AND wake.metadata->>'tool_call_id' = $3
`, testProjectID, fixture.AgentID, toolCallID.String()).Scan(&wakeups); err != nil {
		t.Fatalf("count runtime completion wakeups: %v", err)
	}
	if wakeups != 0 {
		t.Fatalf("runtime tool completion wakeups = %d, want 0", wakeups)
	}
}

func TestRuntimeOwnedWriteRechecksLeaseAfterAgentLockWait(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "runtime_write_lock_wait")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"runtime_write_lock_wait",
		"web_search",
	)

	lockTx, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin agent lock holder: %v", err)
	}
	defer func() { _ = lockTx.Rollback(ctx) }()
	if _, err := lockTx.Exec(
		ctx,
		`SELECT id FROM agents WHERE project_id = $1 AND id = $2 FOR UPDATE`,
		testProjectID,
		fixture.AgentID,
	); err != nil {
		t.Fatalf("lock agent row: %v", err)
	}

	applicationName := "runtime-owned-write-lock-wait"
	writerConfig := fixture.Store.pool.Config()
	writerConfig.MaxConns = 1
	writerConfig.ConnConfig.RuntimeParams["application_name"] = applicationName
	writerPool, err := pgxpool.NewWithConfig(ctx, writerConfig)
	if err != nil {
		t.Fatalf("open writer pool: %v", err)
	}
	t.Cleanup(writerPool.Close)
	writerStore := newIntegrationStore(writerPool)

	started := make(chan error, 1)
	go func() {
		_, startErr := startAsyncToolCallForTest(
			context.Background(),
			writerStore,
			executionstore.ExecuteToolCallInput{
				ProjectID:     testProjectID,
				AgentID:       fixture.AgentID,
				ToolCallID:    toolCallID,
				RuntimeLockID: fixture.Lock.ID,
			},
		)
		started <- startErr
	}()
	integrationdb.WaitForApplicationLockWaiter(t, ctx, fixture.Store.pool, applicationName)

	commandTag, err := lockTx.Exec(
		ctx,
		`UPDATE agent_runtime_locks
SET renewed_at = started_at,
    lease_expires_at = statement_timestamp() - interval '1 microsecond'
WHERE id = $1
  AND started_at < statement_timestamp() - interval '1 microsecond'`,
		fixture.Lock.ID,
	)
	if err != nil {
		t.Fatalf("expire runtime while owned write waits: %v", err)
	}
	if commandTag.RowsAffected() != 1 {
		t.Fatalf("expired runtime rows = %d, want 1", commandTag.RowsAffected())
	}
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatalf("release agent row: %v", err)
	}

	if err := <-started; !errors.Is(err, storeerr.ErrRuntimeLockInactive) {
		t.Fatalf("runtime-owned write after lock wait error = %v, want inactive runtime lock", err)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("load tool call after rejected start: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateReady {
		t.Fatalf("tool call state after rejected start = %q, want ready", toolCall.State)
	}
}

func TestExpiredRuntimeFailsOwnedToolAndFencesLateCompletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "expired_runtime_tool")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "expired_runtime_tool", "web_search")
	if _, err := startAsyncToolCallForTest(
		ctx,
		fixture.Store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ToolCallID:    toolCallID,
			RuntimeLockID: fixture.Lock.ID,
		},
	); err != nil {
		t.Fatalf("start runtime tool call: %v", err)
	}
	expireAgentRuntimeLockForTest(t, ctx, fixture.Store, fixture.Lock.ID)
	reaped, err := fixture.Store.Execution().ReapExpiredAgentRuntimeLocks(ctx, 100)
	if err != nil {
		t.Fatalf("reap expired runtime tool owner: %v", err)
	}
	if reaped != 1 {
		t.Fatalf("reaped runtime locks = %d, want 1", reaped)
	}
	record, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("load reaped runtime tool call: %v", err)
	}
	if record.State != "completed" ||
		!strings.Contains(string(record.ResultContentParts), "external outcome is unknown") {
		t.Fatalf("reaped runtime tool call = %+v", record)
	}
	lateParts, err := executionstore.ToolResultContentParts(json.RawMessage(`{"ok":true}`))
	if err != nil {
		t.Fatalf("build late runtime tool result: %v", err)
	}
	if _, err := fixture.Store.Execution().CompleteRuntimeToolCall(ctx, executionstore.CompleteRuntimeToolCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ID:                 toolCallID,
		RuntimeLockID:      fixture.Lock.ID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		ResultContentParts: lateParts,
	}); !errors.Is(err, storeerr.ErrRuntimeLockInactive) {
		t.Fatalf("late runtime tool completion error = %v, want runtime lock inactive", err)
	}
	replacement, err := fixture.Store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		testID("expired_runtime_tool_replacement"),
		time.Minute,
	)
	if err != nil {
		t.Fatalf("acquire replacement runtime: %v", err)
	}
	if replacement.ID == fixture.Lock.ID {
		t.Fatalf("replacement reused expired runtime id %s", replacement.ID)
	}
}

func TestExpiredRuntimePreservesDurablyWaitingQuestionInteraction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "expired_runtime_question")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"expired_runtime_question",
		"ask_question",
	)
	interaction := createQuestionInteractionForTest(
		t,
		ctx,
		fixture,
		toolCallID,
	)
	expireAgentRuntimeLockForTest(t, ctx, fixture.Store, fixture.Lock.ID)
	if reaped, err := fixture.Store.Execution().ReapExpiredAgentRuntimeLocks(ctx, 100); err != nil {
		t.Fatalf("reap expired question runtime: %v", err)
	} else if reaped != 1 {
		t.Fatalf("reaped runtime locks = %d, want 1", reaped)
	}
	waitingTool, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("load reaped question tool call: %v", err)
	}
	if waitingTool.State != executionstore.ToolCallStateWaiting ||
		waitingTool.Outcome != "" ||
		waitingTool.RuntimeLockID != NilID ||
		waitingTool.CompletedAt != nil {
		t.Fatalf("reaped question tool call = %+v, want unowned waiting work", waitingTool)
	}
	storedInteraction, found, err := fixture.Store.Execution().GetAgentInteraction(
		ctx,
		testProjectID,
		fixture.AgentID,
		interaction.ID,
	)
	if err != nil {
		t.Fatalf("load reaped question interaction: %v", err)
	}
	if !found || storedInteraction.State != executionstore.AgentInteractionStateOpen || !storedInteraction.ResolvedAt.IsZero() {
		t.Fatalf(
			"reaped question interaction = %+v found=%v, want unresolved open interaction",
			storedInteraction,
			found,
		)
	}
}
