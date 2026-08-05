//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestConcurrentRetryClaimsReuseOneDurableContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, admitted, agent := newMultiInputContinuationSeedFixture(t, ctx, "concurrent_retry_claim")
	openingInputIDs := make([]ID, 0, len(admitted.Inputs))
	for _, input := range admitted.Inputs {
		openingInputIDs = append(openingInputIDs, input.ID)
	}
	claim, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		RuntimeLockID:      fixture.Lock.ID,
		OpeningInputIDs:    openingInputIDs,
		AgentConfigID:      agent.CurrentConfigID,
		InputEventSequence: admitted.Events[len(admitted.Events)-1].Sequence,
	})
	if err != nil {
		t.Fatalf("claim initial model call: %v", err)
	}
	if _, err := fixture.Store.Execution().RecordRetryableModelCallFailure(ctx, executionstore.RecordRecoverableModelCallFailureInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ModelCallContextID: claim.Context.ID,
		RuntimeLockID:      fixture.Lock.ID,
		ErrorKind:          "transient",
		ErrorCode:          "provider_unavailable",
		ErrorMessage:       "provider is temporarily unavailable",
		RetryDelay:         0,
	}); err != nil {
		t.Fatalf("record retryable failure: %v", err)
	}

	blocker, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin retry-claim blocker: %v", err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if _, err := dbsqlc.New(blocker).LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: fixture.AgentID},
	); err != nil {
		t.Fatalf("lock agent before concurrent retry claims: %v", err)
	}
	var blockerPID int
	if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatalf("load retry-claim blocker backend: %v", err)
	}

	type claimResult struct {
		claim executionstore.ModelCallClaim
		err   error
	}
	start := make(chan struct{})
	results := make(chan claimResult, 2)
	for range 2 {
		go func() {
			<-start
			result, err := fixture.Store.Execution().ClaimNextModelCallContext(ctx, executionstore.ClaimNextModelCallContextInput{
				ProjectID:                     testProjectID,
				AgentID:                       fixture.AgentID,
				PredecessorModelCallContextID: claim.Context.ID,
				RuntimeLockID:                 fixture.Lock.ID,
			})
			results <- claimResult{claim: result, err: err}
		}()
	}
	close(start)

	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiters int
		if err := fixture.Store.pool.QueryRow(ctx, `
WITH RECURSIVE blocking_chain(waiter_pid, blocker_pid) AS (
  SELECT activity.pid, unnest(pg_blocking_pids(activity.pid))
  FROM pg_stat_activity activity
  WHERE activity.datname = current_database()
  UNION
  SELECT chain.waiter_pid, unnest(pg_blocking_pids(blocker.pid))
  FROM blocking_chain chain
  JOIN pg_stat_activity blocker ON blocker.pid = chain.blocker_pid
)
SELECT count(DISTINCT activity.pid)::integer
FROM pg_stat_activity activity
JOIN blocking_chain chain ON chain.waiter_pid = activity.pid
WHERE activity.datname = current_database()
  AND activity.wait_event_type = 'Lock'
  AND chain.blocker_pid = $1
  AND activity.query LIKE '%-- name: LockAgentInProject :one%'`, blockerPID).Scan(&waiters); err != nil {
			t.Fatalf("count blocked retry claims: %v", err)
		}
		if waiters >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("concurrent retry claims did not both block on the agent lock; waiters=%d", waiters)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatalf("release retry-claim blocker: %v", err)
	}

	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent retry claims errors = %v / %v", first.err, second.err)
	}
	if first.claim.Context.ID != second.claim.Context.ID ||
		first.claim.Context.ID == claim.Context.ID ||
		first.claim.Context.AttemptNumber != 2 || second.claim.Context.AttemptNumber != 2 {
		t.Fatalf("concurrent retry claims = %+v / %+v, want one shared retry context 2", first.claim, second.claim)
	}
	if first.claim.Created == second.claim.Created || first.claim.Claimed == second.claim.Claimed ||
		first.claim.Created != first.claim.Claimed || second.claim.Created != second.claim.Claimed {
		t.Fatalf("concurrent retry claims = %+v / %+v, want exactly one creator and sender", first.claim, second.claim)
	}
	var contextRows, liveRows int
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT count(*)::integer,
       count(*) FILTER (WHERE state = 'started')::integer
FROM model_call_contexts
WHERE project_id = $1
  AND agent_id = $2
  AND operation_kind = 'normal'
  AND input_event_sequence = $3
  AND agent_config_id = $4
  AND attempt_number = 2`,
		testProjectID,
		fixture.AgentID,
		claim.Context.InputEventSequence,
		claim.Context.AgentConfigID,
	).Scan(&contextRows, &liveRows); err != nil {
		t.Fatalf("count concurrent retry contexts: %v", err)
	}
	if contextRows != 1 || liveRows != 1 {
		t.Fatalf("concurrent retry context rows=%d live=%d, want one", contextRows, liveRows)
	}
}

func TestRetryClaimRejectedAfterStopEvent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, _, claim := newStartedNormalModelCallTestFixture(
		t,
		ctx,
		"retry_claim_after_stop",
	)
	retryAt := fixture.Now.Add(2 * time.Second)
	if _, err := fixture.Store.Execution().RecordRetryableModelCallFailure(
		ctx,
		executionstore.RecordRecoverableModelCallFailureInput{
			ProjectID:          testProjectID,
			AgentID:            fixture.AgentID,
			ModelCallContextID: claim.Context.ID,
			RuntimeLockID:      fixture.Lock.ID,
			ErrorKind:          "transient",
			ErrorCode:          "provider_unavailable",
			ErrorMessage:       "provider is temporarily unavailable",
			RetryDelay:         0,
		},
	); err != nil {
		t.Fatalf("record retryable failure: %v", err)
	}
	var turnID ID
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT turn_id
FROM model_call_context_turns
WHERE project_id = $1
  AND agent_id = $2
  AND model_call_context_id = $3
`, testProjectID, fixture.AgentID, claim.Context.ID).Scan(&turnID); err != nil {
		t.Fatalf("load failed context turn: %v", err)
	}
	appendCancelStopEventForContinuationSeedTest(
		t,
		ctx,
		fixture,
		turnID,
		retryAt.Add(time.Millisecond),
	)

	next, err := fixture.Store.Execution().ClaimNextModelCallContext(ctx, executionstore.ClaimNextModelCallContextInput{
		ProjectID:                     testProjectID,
		AgentID:                       fixture.AgentID,
		PredecessorModelCallContextID: claim.Context.ID,
		RuntimeLockID:                 fixture.Lock.ID,
	})
	if err != nil {
		t.Fatalf("claim retry after stop: %v", err)
	}
	if next.Created || next.Claimed || next.Context.ID != claim.Context.ID {
		t.Fatalf("retry after stop = %+v, want existing unclaimed context", next)
	}
}

func TestExhaustedNormalContextCanHandOffAndTerminalizeCompaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, work, claim := claimExhaustedNormalModelContext(
		t,
		ctx,
		"exhausted_normal_context_compaction_handoff",
	)
	if claim.Context.AttemptNumber != executionstore.MaxModelCallRetriesPerOperation+1 ||
		claim.Context.State != executionstore.ModelCallContextStarted {
		t.Fatalf("exhausted normal context = %+v", claim.Context)
	}

	handoff, err := fixture.Store.Execution().RecordModelCallFailureAndClaimCompaction(
		ctx,
		executionstore.RecordModelCallFailureAndClaimCompactionInput{
			ParentContextID: claim.Context.ID,
			Failure: executionstore.RecordRecoverableModelCallFailureInput{
				ProjectID:          testProjectID,
				AgentID:            fixture.AgentID,
				ModelCallContextID: claim.Context.ID,
				RuntimeLockID:      work.RuntimeLock.ID,
				RecoveryKind:       executionstore.ModelCallRecoveryCompact,
				ErrorKind:          "context_window",
				ErrorCode:          "context_window",
				ErrorMessage:       "The model context exceeds the configured input budget.",
				RetryDelay:         0,
			},
			SourceEventSequenceEnd: claim.Context.InputEventSequence,
		},
	)
	if err != nil {
		t.Fatalf("handoff exhausted normal context to compaction: %v", err)
	}
	if handoff.BoundaryPreempted ||
		handoff.ParentContext.AttemptNumber != executionstore.MaxModelCallRetriesPerOperation+1 ||
		handoff.ParentContext.RecoveryKind != executionstore.ModelCallRecoveryCompact {
		t.Fatalf("parent handoff = %+v", handoff)
	}
	if !handoff.CompactionCall.Created || !handoff.CompactionCall.Claimed ||
		handoff.CompactionCall.Context.OperationKind != executionstore.ModelCallOperationCompaction ||
		handoff.CompactionCall.Context.AttemptNumber != 1 {
		t.Fatalf("fresh compaction budget = %+v", handoff.CompactionCall)
	}

	if err := fixture.Store.Execution().RecordTerminalCompactionFailure(
		ctx,
		executionstore.RecordTerminalCompactionFailureInput{
			ProjectID:          testProjectID,
			AgentID:            fixture.AgentID,
			RuntimeLockID:      work.RuntimeLock.ID,
			ModelCallContextID: handoff.CompactionCall.Context.ID,
			ErrorKind:          "auth",
			ErrorCode:          "invalid_api_key",
			ErrorMessage:       "The model provider rejected its credentials.",
			ErrorDetails:       json.RawMessage(`{"test":"exhausted_parent"}`),
		},
	); err != nil {
		t.Fatalf("terminalize failed compaction with exhausted parent: %v", err)
	}

	parentContext, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		claim.Context.ID,
	)
	if err != nil || !found || parentContext.State != executionstore.ModelCallContextFailed ||
		parentContext.RecoveryKind != executionstore.ModelCallRecoveryCompact {
		t.Fatalf("terminal parent context = %+v found=%v err=%v", parentContext, found, err)
	}
	output, found, err := fixture.Store.Execution().GetModelOutputForContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		handoff.CompactionCall.Context.ID,
	)
	if err != nil || !found || output.StopReason != "error" ||
		output.ModelCallContextID != handoff.CompactionCall.Context.ID {
		t.Fatalf("terminal compaction output = %+v found=%v err=%v", output, found, err)
	}
	terminalContext, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		output.ModelCallContextID,
	)
	if err != nil || !found || terminalContext.AttemptNumber != 1 ||
		terminalContext.State != executionstore.ModelCallContextFailed ||
		terminalContext.RecoveryKind != "" {
		t.Fatalf("terminal compaction context = %+v found=%v err=%v", terminalContext, found, err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		work.RuntimeLock.ID,
	); err != nil {
		t.Fatalf("release runtime after terminal compaction failure: %v", err)
	}
}

func TestParentRetryWaitsWhileCompactionContextIsLive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "one_live_model_context_per_agent")
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	); err != nil {
		t.Fatalf("release one-live fixture runtime: %v", err)
	}
	now := time.Now().UTC()
	input, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        fixture.AgentID,
		Actor:          mustOmnaraActorParams(t, fixture.UserID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"exercise runtime context uniqueness"}]`),
		IdempotencyKey: "one-live-model-context-per-runtime",
	})
	if err != nil {
		t.Fatalf("create one-live input: %v", err)
	}
	work, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		testClaimNextAgentWorkInput(),
	)
	if err != nil || !found || work.Kind != executionstore.AgentWorkModel || !claimedOpeningInputIDsEqual(work, input.ID) {
		t.Fatalf("claim one-live work = %+v found=%v err=%v", work, found, err)
	}
	parent := claimTestNormalModelCallForWork(t, ctx, fixture, work, now.Add(2*time.Second))
	handoff, err := fixture.Store.Execution().RecordModelCallFailureAndClaimCompaction(
		ctx,
		executionstore.RecordModelCallFailureAndClaimCompactionInput{
			ParentContextID: parent.Context.ID,
			Failure: executionstore.RecordRecoverableModelCallFailureInput{
				ProjectID:          testProjectID,
				AgentID:            fixture.AgentID,
				ModelCallContextID: parent.Context.ID,
				RuntimeLockID:      work.RuntimeLock.ID,
				RecoveryKind:       executionstore.ModelCallRecoveryCompact,
				ErrorKind:          "context_window",
				ErrorCode:          "context_window",
				ErrorMessage:       "The model context exceeds the configured input budget.",
			},
			SourceEventSequenceEnd: work.Model.OpeningEventSequence,
		},
	)
	if err != nil {
		t.Fatalf("record one-live compaction handoff: %v", err)
	}
	if !handoff.CompactionCall.Created || !handoff.CompactionCall.Claimed {
		t.Fatalf("one-live compaction handoff = %+v", handoff)
	}

	retry, err := fixture.Store.Execution().ClaimNextModelCallContext(ctx, executionstore.ClaimNextModelCallContextInput{
		ProjectID:                     testProjectID,
		AgentID:                       fixture.AgentID,
		PredecessorModelCallContextID: parent.Context.ID,
		RuntimeLockID:                 work.RuntimeLock.ID,
	})
	if err != nil {
		t.Fatalf("claim parent retry during live compaction: %v", err)
	}
	if retry.Created || retry.Claimed || retry.Context.ID != parent.Context.ID {
		t.Fatalf("parent retry during live compaction = %+v, want existing unclaimed parent", retry)
	}
}

func TestTerminalModelCallFailureSettlesBeforeNewModelReadyFrontier(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{string(executionstore.ModelCallOperationNormal), string(executionstore.ModelCallOperationCompaction)} {
		for _, frontier := range []string{"steering", "config"} {
			operation, frontier := operation, frontier
			t.Run(operation+"_"+frontier, func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				name := "direct_terminal_" + operation + "_" + frontier
				if operation == string(executionstore.ModelCallOperationNormal) {
					fixture, work, claim := claimExhaustedNormalModelContext(t, ctx, name)
					steeringID := injectModelReadyFrontier(t, ctx, fixture, frontier, name)
					result, err := fixture.Store.Execution().RecordModelCallErrorAndCompleteContext(
						ctx,
						executionstore.RecordModelCallErrorAndCompleteContextInput{
							ProjectID:          testProjectID,
							AgentID:            fixture.AgentID,
							RuntimeLockID:      work.RuntimeLock.ID,
							ModelCallContextID: claim.Context.ID,
							ErrorKind:          "auth",
							ErrorCode:          "invalid_api_key",
							ErrorMessage:       "The model provider rejected its credentials.",
							ErrorDetails:       json.RawMessage(`{"test":"boundary"}`),
						},
					)
					if err != nil {
						t.Fatalf("record normal terminal failure before frontier: %v", err)
					}
					if result.ID == NilID {
						t.Fatalf("terminal result = %+v, want a durable error output", result)
					}
					assertTerminalFailureSettledBeforeModelReadyFrontier(
						t,
						ctx,
						fixture,
						claim.Context.ID,
						NilID,
						claim.Context.ID,
						steeringID,
					)
					return
				}

				fixture, work, parent, claim := claimExhaustedCompactionModelContext(t, ctx, name)
				steeringID := injectModelReadyFrontier(t, ctx, fixture, frontier, name)
				err := fixture.Store.Execution().RecordTerminalCompactionFailure(
					ctx,
					executionstore.RecordTerminalCompactionFailureInput{
						ProjectID:          testProjectID,
						AgentID:            fixture.AgentID,
						RuntimeLockID:      work.RuntimeLock.ID,
						ModelCallContextID: claim.Context.ID,
						ErrorKind:          "auth",
						ErrorCode:          "invalid_api_key",
						ErrorMessage:       "The model provider rejected its credentials.",
						ErrorDetails:       json.RawMessage(`{"test":"boundary"}`),
					},
				)
				if err != nil {
					t.Fatalf("record compaction terminal failure before frontier: %v", err)
				}
				assertTerminalFailureSettledBeforeModelReadyFrontier(
					t, ctx, fixture, parent.Context.ID, claim.Context.ID, claim.Context.ID, steeringID,
				)
			})
		}
	}
}

func TestExhaustedRuntimeRecoverySettlesBeforeNewModelReadyFrontier(t *testing.T) {
	t.Parallel()
	for _, recovery := range []string{"release", "reap"} {
		for _, operation := range []string{string(executionstore.ModelCallOperationNormal), string(executionstore.ModelCallOperationCompaction)} {
			for _, frontier := range []string{"steering", "config"} {
				recovery, operation, frontier := recovery, operation, frontier
				t.Run(recovery+"_"+operation+"_"+frontier, func(t *testing.T) {
					t.Parallel()
					ctx := context.Background()
					name := "recovery_" + recovery + "_" + operation + "_" + frontier
					var fixture processDaemonFixture
					var work executionstore.ClaimedAgentWork
					var parentContextID, compactionContextID, terminalContextID ID
					if operation == string(executionstore.ModelCallOperationNormal) {
						var claim executionstore.ModelCallClaim
						fixture, work, claim = claimExhaustedNormalModelContext(t, ctx, name)
						parentContextID = claim.Context.ID
						terminalContextID = claim.Context.ID
					} else {
						var parent, claim executionstore.ModelCallClaim
						fixture, work, parent, claim = claimExhaustedCompactionModelContext(t, ctx, name)
						parentContextID = parent.Context.ID
						compactionContextID = claim.Context.ID
						terminalContextID = claim.Context.ID
					}
					steeringID := injectModelReadyFrontier(t, ctx, fixture, frontier, name)
					if recovery == "release" {
						if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
							ctx,
							testProjectID,
							fixture.AgentID,
							work.RuntimeLock.ID,
						); err != nil {
							t.Fatalf("release exhausted runtime before frontier: %v", err)
						}
					} else {
						expireAgentRuntimeLockForTest(t, ctx, fixture.Store, work.RuntimeLock.ID)
						reaped, err := fixture.Store.Execution().ReapExpiredAgentRuntimeLocks(ctx, 100)
						if err != nil || reaped != 1 {
							t.Fatalf("reap exhausted runtime before frontier: reaped=%d err=%v", reaped, err)
						}
					}
					assertTerminalFailureSettledBeforeModelReadyFrontier(
						t,
						ctx,
						fixture,
						parentContextID,
						compactionContextID,
						terminalContextID,
						steeringID,
					)
				})
			}
		}
	}
}

func TestTerminalModelCallErrorStopsUntilNewInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "terminal_model_error_then_new_input")
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
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"try the provider"}]`),
		IdempotencyKey: "terminal-model-error",
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
	terminalErrorInput := executionstore.RecordModelCallErrorAndCompleteContextInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		RuntimeLockID:      claim.RuntimeLock.ID,
		ModelCallContextID: modelClaim.Context.ID,
		ErrorKind:          "auth",
		ErrorCode:          "invalid_api_key",
		ErrorMessage:       "The model provider rejected its credentials.",
		ErrorDetails:       json.RawMessage(`{"retryable":false}`),
	}
	errorEvent, err := fixture.Store.Execution().RecordModelCallErrorAndCompleteContext(ctx, terminalErrorInput)
	if err != nil {
		t.Fatalf("record terminal model error: %v", err)
	}
	if errorEvent.Kind != "model_output" {
		t.Fatalf("terminal error event = %+v, want model_output", errorEvent)
	}
	replayedErrorEvent, err := fixture.Store.Execution().RecordModelCallErrorAndCompleteContext(ctx, terminalErrorInput)
	if err != nil {
		t.Fatalf("replay terminal model error after ambiguous commit: %v", err)
	}
	if replayedErrorEvent.ID != errorEvent.ID {
		t.Fatalf("replayed terminal error = %+v, want existing event %s", replayedErrorEvent, errorEvent.ID)
	}
	conflictingTerminalErrorInput := terminalErrorInput
	conflictingTerminalErrorInput.ErrorMessage = "different terminal error"
	if _, err := fixture.Store.Execution().RecordModelCallErrorAndCompleteContext(
		ctx,
		conflictingTerminalErrorInput,
	); !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("conflicting terminal error replay err = %v, want ErrIdempotencyConflict", err)
	}
	output, found, err := fixture.Store.Execution().GetModelOutputForContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		modelClaim.Context.ID,
	)
	if err != nil || !found {
		t.Fatalf("load terminal error output: found=%v err=%v", found, err)
	}
	if output.StopReason != "error" || output.ModelCallContextID != modelClaim.Context.ID {
		t.Fatalf("terminal error output = %+v, want error from the failed context", output)
	}
	var blockKind, blockText string
	if err := fixture.Store.pool.QueryRow(ctx, `
		SELECT block_kind, text_content
		FROM content_blocks block
		JOIN agents agent ON agent.id = block.agent_id
		WHERE agent.project_id = $1 AND block.agent_id = $2 AND block.owner_model_output_id = $3`,
		testProjectID, fixture.AgentID, output.ID,
	).Scan(&blockKind, &blockText); err != nil {
		t.Fatalf("load terminal error content block: %v", err)
	}
	if blockKind != "error" || blockText != "The model provider rejected its credentials." {
		t.Fatalf("terminal error block = %q/%q", blockKind, blockText)
	}
	forwardEvents, err := fixture.Store.Execution().ListAgentEventsForRead(
		ctx,
		testProjectID,
		fixture.AgentID,
		errorEvent.Sequence-1,
		1,
	)
	if err != nil || len(forwardEvents) != 1 {
		t.Fatalf("read terminal error forward: events=%+v err=%v", forwardEvents, err)
	}
	backwardEvents, err := fixture.Store.Execution().ListAgentEventsBeforeForRead(
		ctx,
		testProjectID,
		fixture.AgentID,
		errorEvent.Sequence+1,
		1,
	)
	if err != nil || len(backwardEvents) != 1 {
		t.Fatalf("read terminal error backward: events=%+v err=%v", backwardEvents, err)
	}
	const publicErrorBlock = `[{"text":"The model provider rejected its credentials.","type":"error"}]`
	assertJSONRawEqual(t, forwardEvents[0].ContentBlocks, publicErrorBlock)
	assertJSONRawEqual(t, backwardEvents[0].ContentBlocks, publicErrorBlock)
	var contextErrorDetails json.RawMessage
	if err := fixture.Store.pool.QueryRow(ctx, `
		SELECT error_details
		FROM model_call_contexts
		WHERE project_id = $1 AND agent_id = $2 AND id = $3`,
		testProjectID, fixture.AgentID, modelClaim.Context.ID,
	).Scan(&contextErrorDetails); err != nil {
		t.Fatalf("load terminal context error details: %v", err)
	}
	assertJSONRawEqual(t, contextErrorDetails, `{"retryable":false}`)
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		claim.RuntimeLock.ID,
	); err != nil {
		t.Fatalf("release runtime after terminal model error: %v", err)
	}
	if err := fixture.Store.Execution().DeleteAgentWakeup(ctx, testProjectID, fixture.AgentID); err != nil {
		t.Fatalf("delete wakeup before rebuild: %v", err)
	}
	rebuilt, err := fixture.Store.Execution().RebuildMissingAgentWakeups(ctx, testProjectID)
	if err != nil {
		t.Fatalf("rebuild terminal model error wakeup: %v", err)
	}
	if rebuilt != 0 {
		t.Fatalf("rebuild restored %d wakeups for terminal model error, want 0", rebuilt)
	}
	if seed, found, err := fixture.Store.Execution().NextAgentModelWork(ctx, testProjectID, fixture.AgentID); err != nil {
		t.Fatalf("load seed after terminal model error: %v", err)
	} else if found {
		t.Fatalf("terminal model error continuation seed = %+v, want none", seed)
	}
	newInput, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        fixture.AgentID,
		Actor:          mustOmnaraActorParams(t, fixture.UserID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"try again now"}]`),
		IdempotencyKey: "after-terminal-model-error",
	})
	if err != nil {
		t.Fatalf("create input after terminal model error: %v", err)
	}
	newWork, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		testClaimNextAgentWorkInput(),
	)
	if err != nil {
		t.Fatalf("claim input after terminal model error: %v", err)
	}
	if !found || newWork.Kind != executionstore.AgentWorkModel || !claimedOpeningInputIDsEqual(newWork, newInput.ID) ||
		newWork.Model.TurnID == claim.Model.TurnID {
		t.Fatalf("new work after terminal model error = %+v found=%v, want a fresh turn", newWork, found)
	}
	newModelClaim := claimTestNormalModelCallForWork(
		t,
		ctx,
		fixture,
		newWork,
		now.Add(7*time.Second),
	)
	if newModelClaim.Context.ID == modelClaim.Context.ID ||
		newModelClaim.Context.AttemptNumber != 1 {
		t.Fatalf("new input model claim = %+v, want a fresh context at attempt 1", newModelClaim)
	}
}
