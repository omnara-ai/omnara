//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestReplaceCompactionSourceRejectsLargerRange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "compaction_source_monotone_shrink")
	inputs := make([]executionstore.AgentInputRecord, 0, 2)
	for index, text := range []string{"first opening input", "second opening input"} {
		input, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
			ProjectID:      testProjectID,
			AgentID:        fixture.AgentID,
			Actor:          mustOmnaraActorParams(t, fixture.UserID),
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"` + text + `"}]`),
			DeliveryMode:   executionstore.DeliveryModeSteering,
			IdempotencyKey: fmt.Sprintf("compaction-source-monotone-%d", index),
		})
		if err != nil {
			t.Fatalf("create steering input %d: %v", index, err)
		}
		inputs = append(inputs, input)
	}
	admitted, found := admitNextAgentInputAndOpenTurnForTest(
		t,
		ctx,
		fixture.Store,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	)
	if !found || len(admitted.Events) != 2 {
		t.Fatalf("admitted opening events = %+v found=%v, want two", admitted.Events, found)
	}
	agent, err := fixture.Store.Execution().GetAgentInProject(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("load agent: %v", err)
	}
	parent, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		RuntimeLockID:      fixture.Lock.ID,
		OpeningInputIDs:    []ID{inputs[0].ID, inputs[1].ID},
		AgentConfigID:      agent.CurrentConfigID,
		InputEventSequence: admitted.Events[1].Sequence,
	})
	if err != nil {
		t.Fatalf("claim blocked normal context: %v", err)
	}
	handoff, err := fixture.Store.Execution().RecordModelCallFailureAndClaimCompaction(
		ctx,
		executionstore.RecordModelCallFailureAndClaimCompactionInput{
			ParentContextID: parent.Context.ID,
			Failure: executionstore.RecordRecoverableModelCallFailureInput{
				ProjectID:          testProjectID,
				AgentID:            fixture.AgentID,
				ModelCallContextID: parent.Context.ID,
				RuntimeLockID:      fixture.Lock.ID,
				RecoveryKind:       executionstore.ModelCallRecoveryCompact,
				ErrorKind:          "context_window",
				ErrorCode:          "context_window",
				ErrorMessage:       "model context exceeded",
			},
			SourceEventSequenceEnd: admitted.Events[0].Sequence,
		},
	)
	if err != nil {
		t.Fatalf("record compaction recovery: %v", err)
	}
	compactionClaim := handoff.CompactionCall
	_, err = fixture.Store.Execution().ReplaceCompactionSource(ctx, executionstore.ReplaceCompactionSourceInput{
		ProjectID:                  testProjectID,
		AgentID:                    fixture.AgentID,
		RuntimeLockID:              fixture.Lock.ID,
		ModelCallContextID:         compactionClaim.Context.ID,
		ErrorKind:                  "context_window",
		ErrorCode:                  "candidate_projection_over_budget",
		ErrorMessage:               "candidate checkpoint did not fit",
		NextSourceEventSequenceEnd: admitted.Events[1].Sequence,
	})
	if !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("larger source replacement err = %v, want %v", err, storeerr.ErrStateTransitionConflict)
	}
	contextRecord, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		compactionClaim.Context.ID,
	)
	if err != nil || !found {
		t.Fatalf("load compaction context after rejected expansion: found=%v err=%v", found, err)
	}
	if contextRecord.State != executionstore.ModelCallContextStarted {
		t.Fatalf("rejected expansion mutated context: %+v", contextRecord)
	}
}

func TestSucceededCompactionCannotStartAnotherBranch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, admitted, _ := newMultiInputContinuationSeedFixture(
		t,
		ctx,
		"succeeded_compaction_single_branch",
	)
	frontier := admitted.Events[len(admitted.Events)-1].Sequence
	claim := claimSentCompactionForRangeTest(
		t,
		ctx,
		fixture,
		1,
		frontier,
		frontier,
		fixture.Now.Add(4*time.Second),
	)
	if _, err := publishCheckpointForRangeTest(
		t,
		ctx,
		fixture,
		claim,
		"completed compaction",
		fixture.Now.Add(5*time.Second),
	); err != nil {
		t.Fatalf("publish successful checkpoint: %v", err)
	}
	parent, found, err := fixture.Store.Execution().GetNormalModelCallContextForFrontier(
		ctx,
		testProjectID,
		fixture.AgentID,
		claim.Context.InputEventSequence,
	)
	if err != nil || !found {
		t.Fatalf("load compaction parent: found=%v err=%v", found, err)
	}

	_, err = fixture.Store.Execution().ClaimCompactionModelCall(ctx, executionstore.ClaimCompactionModelCallInput{
		ProjectID:              testProjectID,
		AgentID:                fixture.AgentID,
		RuntimeLockID:          fixture.Lock.ID,
		InputEventSequence:     claim.Context.InputEventSequence,
		SourceEventSequenceEnd: *claim.Context.SourceEventSequenceEnd - 1,
		ParentContextID:        parent.ID,
	})
	if !errors.Is(err, storeerr.ErrAgentNotAdvanceable) {
		t.Fatalf("second compaction branch error = %v, want %v", err, storeerr.ErrAgentNotAdvanceable)
	}
}

func TestCompactionSourceAdjustmentFollowsRetryHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, admitted, _ := newMultiInputContinuationSeedFixture(
		t,
		ctx,
		"compaction_adjustment_after_retry",
	)
	frontier := admitted.Events[len(admitted.Events)-1].Sequence
	first := claimSentCompactionForRangeTest(
		t,
		ctx,
		fixture,
		1,
		frontier,
		frontier,
		fixture.Now.Add(4*time.Second),
	)
	if _, err := fixture.Store.Execution().RecordRetryableModelCallFailure(
		ctx,
		executionstore.RecordRecoverableModelCallFailureInput{
			ProjectID:          testProjectID,
			AgentID:            fixture.AgentID,
			ModelCallContextID: first.Context.ID,
			RuntimeLockID:      fixture.Lock.ID,
			ErrorKind:          "transient",
			ErrorCode:          "provider_unavailable",
			ErrorMessage:       "provider is temporarily unavailable",
			RetryDelay:         0,
		},
	); err != nil {
		t.Fatalf("record compaction retry: %v", err)
	}
	retry, err := fixture.Store.Execution().ClaimNextModelCallContext(ctx, executionstore.ClaimNextModelCallContextInput{
		ProjectID:                     testProjectID,
		AgentID:                       fixture.AgentID,
		PredecessorModelCallContextID: first.Context.ID,
		RuntimeLockID:                 fixture.Lock.ID,
	})
	if err != nil {
		t.Fatalf("claim compaction retry: %v", err)
	}
	if !retry.Created || !retry.Claimed || retry.Context.AttemptNumber != 2 {
		t.Fatalf("compaction retry = %+v, want claimed attempt 2", retry)
	}

	replacementEnd := *retry.Context.SourceEventSequenceEnd - 1
	replacement, err := fixture.Store.Execution().ReplaceCompactionSource(ctx, executionstore.ReplaceCompactionSourceInput{
		ProjectID:                  testProjectID,
		AgentID:                    fixture.AgentID,
		RuntimeLockID:              fixture.Lock.ID,
		ModelCallContextID:         retry.Context.ID,
		ErrorKind:                  "context_window",
		ErrorCode:                  "candidate_projection_over_budget",
		ErrorMessage:               "candidate checkpoint did not fit",
		NextSourceEventSequenceEnd: replacementEnd,
	})
	if err != nil || replacement.BoundaryPreempted {
		t.Fatalf("replace compaction source after retry: result=%+v err=%v", replacement, err)
	}

	var replacementCount int
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT count(*)::integer
FROM model_call_contexts
WHERE project_id = $1
  AND agent_id = $2
  AND operation_kind = 'compaction'
  AND input_event_sequence = $3
  AND source_event_sequence_end = $4
  AND attempt_number = 1
  AND state = 'started'
`, testProjectID, fixture.AgentID, retry.Context.InputEventSequence, replacementEnd).Scan(
		&replacementCount,
	); err != nil {
		t.Fatalf("count replacement compaction context: %v", err)
	}
	if replacementCount != 1 {
		t.Fatalf("replacement compaction contexts = %d, want 1", replacementCount)
	}
}
