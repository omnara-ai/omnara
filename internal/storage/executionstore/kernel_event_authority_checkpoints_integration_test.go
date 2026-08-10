//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestContextCheckpointEventDatabaseGuardRejectsWrongTurn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, admitted, _ := newMultiInputContinuationSeedFixture(t, ctx, "checkpoint_event_wrong_turn_guard")
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
	checkpoint, err := publishCheckpointForRangeTest(
		t,
		ctx,
		fixture,
		claim,
		"checkpoint turn guard",
		fixture.Now.Add(5*time.Second),
	)
	if err != nil {
		t.Fatalf("publish checkpoint fixture: %v", err)
	}
	wrongTurnID := appendSyntheticLatestContentTurnForFrontierTest(
		t,
		ctx,
		fixture,
		"checkpoint_event_wrong_turn_guard_later_turn",
		fixture.Now.Add(6*time.Second),
	)
	sequence, err := fixture.Store.Execution().MaxEventSequence(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("load max event sequence: %v", err)
	}
	_, err = fixture.Store.pool.Exec(ctx, `
INSERT INTO agent_events(
  agent_id, turn_id, sequence, event_kind,
  idempotency_key, context_checkpoint_id, created_at
)
VALUES ($1, $2, $3, 'context_checkpoint', $4, $5, $6)
`,
		fixture.AgentID,
		wrongTurnID,
		sequence+1,
		"wrong-turn-checkpoint-event:"+checkpoint.ID.String(),
		checkpoint.ID,
		fixture.Now.Add(7*time.Second),
	)
	if err == nil || !strings.Contains(err.Error(), "must use turn") ||
		!strings.Contains(err.Error(), "derived from producer context") {
		t.Fatalf("wrong-turn checkpoint event error = %v, want producer-context turn check", err)
	}
}

func TestContextCheckpointControlEventPreservesLatestSemanticEvent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, admitted, _ := newMultiInputContinuationSeedFixture(t, ctx, "checkpoint_control_projection")
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
	checkpoint, err := publishCheckpointForRangeTest(
		t,
		ctx,
		fixture,
		claim,
		"checkpoint control projection",
		fixture.Now.Add(5*time.Second),
	)
	if err != nil {
		t.Fatalf("publish checkpoint: %v", err)
	}
	var producerTurnID, semanticEventID ID
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT event.turn_id, event.id
FROM agent_events event
JOIN agents agent ON agent.id = event.agent_id
WHERE agent.project_id = $1
  AND event.agent_id = $2
  AND event.sequence = $3
`, testProjectID, fixture.AgentID, claim.Context.InputEventSequence).Scan(
		&producerTurnID,
		&semanticEventID,
	); err != nil {
		t.Fatalf("load checkpoint producer turn: %v", err)
	}

	var latestEventID, latestSemanticEventID ID
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT turn.latest_event_id,
	   turn.latest_semantic_event_id
FROM agent_turns turn
JOIN agents agent ON agent.id = turn.agent_id
WHERE agent.project_id = $1
  AND turn.agent_id = $2
  AND turn.id = $3
`, testProjectID, fixture.AgentID, producerTurnID).Scan(
		&latestEventID,
		&latestSemanticEventID,
	); err != nil {
		t.Fatalf("load checkpoint turn projection: %v", err)
	}
	if latestEventID != checkpoint.CheckpointEventID || latestSemanticEventID != semanticEventID {
		t.Fatalf(
			"turn event pointers = latest %s semantic %s, want checkpoint %s semantic %s",
			latestEventID,
			latestSemanticEventID,
			checkpoint.CheckpointEventID,
			semanticEventID,
		)
	}
	eventsForRead, err := fixture.Store.Execution().ListTurnEventsForRead(
		ctx,
		testProjectID,
		fixture.AgentID,
		producerTurnID,
		0,
		100,
	)
	if err != nil {
		t.Fatalf("list checkpoint turn events: %v", err)
	}
	var checkpointEvent executionstore.AgentEventReadRecord
	for _, event := range eventsForRead {
		if event.ID == checkpoint.CheckpointEventID {
			checkpointEvent = event
			break
		}
	}
	if checkpointEvent.ID == NilID || checkpointEvent.ActorID != NilID {
		t.Fatalf("checkpoint event = %+v, want a system event without an actor", checkpointEvent)
	}
}

func TestContextCheckpointPublicationRecordsProviderCost(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, admitted, _ := newMultiInputContinuationSeedFixture(t, ctx, "checkpoint_provider_cost")
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
	input := executionstore.PublishContextCheckpointInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID,
		RuntimeLockID: fixture.Lock.ID, ModelCallContextID: claim.Context.ID,
		Summary:                 "checkpoint with provider cost",
		APIFormat:               modelprotocol.APIFormatOpenAIResponses,
		APIVariant:              modelprotocol.APIVariantDefault,
		ProviderRequestID:       "req_checkpoint_cost",
		ProviderResponseID:      "resp_checkpoint_cost",
		ProviderReportedCostUSD: "0.0000025",
		Usage: modelenvelope.Usage{
			InputTokens:         11,
			UncachedInputTokens: 11,
			OutputTokens:        7,
		},
	}
	_, err := fixture.Store.Execution().PublishContextCheckpoint(ctx, input)
	if err != nil {
		t.Fatalf("publish checkpoint: %v", err)
	}
	contextRecord, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		claim.Context.ID,
	)
	if err != nil || !found {
		t.Fatalf("load compaction context: found=%v err=%v", found, err)
	}
	if contextRecord.ProviderReportedCostUSD != input.ProviderReportedCostUSD {
		t.Fatalf(
			"provider-reported cost = %q, want %q",
			contextRecord.ProviderReportedCostUSD,
			input.ProviderReportedCostUSD,
		)
	}
}

func TestContextCheckpointPublicationRejectsRepeatedTransition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, admitted, _ := newMultiInputContinuationSeedFixture(
		t,
		ctx,
		"checkpoint_repeated_publication",
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
	input := executionstore.PublishContextCheckpointInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		RuntimeLockID:      fixture.Lock.ID,
		ModelCallContextID: claim.Context.ID,
		Summary:            "single checkpoint publication",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		APIVariant:         modelprotocol.APIVariantDefault,
		ProviderResponseID: "resp_single_checkpoint_publication",
	}
	if _, err := fixture.Store.Execution().PublishContextCheckpoint(ctx, input); err != nil {
		t.Fatalf("publish checkpoint: %v", err)
	}
	if _, err := fixture.Store.Execution().PublishContextCheckpoint(
		ctx,
		input,
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("repeat checkpoint publication error = %v, want %v", err, storeerr.ErrStateTransitionConflict)
	}
}

func TestContextCheckpointLineageDerivesPriorCheckpointFromEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, admitted, _ := newMultiInputContinuationSeedFixture(t, ctx, "checkpoint_event_derived_lineage")
	firstFrontier := admitted.Events[len(admitted.Events)-1].Sequence
	firstClaim := claimSentCompactionForRangeTest(
		t,
		ctx,
		fixture,
		1,
		firstFrontier,
		firstFrontier,
		fixture.Now.Add(4*time.Second),
	)
	firstCheckpoint, err := publishCheckpointForRangeTest(
		t,
		ctx,
		fixture,
		firstClaim,
		"first cumulative checkpoint",
		fixture.Now.Add(5*time.Second),
	)
	if err != nil {
		t.Fatalf("publish first checkpoint: %v", err)
	}
	appendSyntheticLatestContentTurnForFrontierTest(
		t,
		ctx,
		fixture,
		"checkpoint_event_derived_lineage_later_turn",
		fixture.Now.Add(6*time.Second),
	)

	secondFrontier, err := fixture.Store.Execution().MaxEventSequence(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("load second compaction frontier: %v", err)
	}
	secondClaim := claimSentCompactionForRangeTest(
		t,
		ctx,
		fixture,
		firstCheckpoint.SummarizedThroughEventSequence+1,
		secondFrontier,
		secondFrontier,
		fixture.Now.Add(7*time.Second),
	)
	secondCheckpoint, err := publishCheckpointForRangeTest(
		t,
		ctx,
		fixture,
		secondClaim,
		"second cumulative checkpoint",
		fixture.Now.Add(8*time.Second),
	)
	if err != nil {
		t.Fatalf("publish second checkpoint: %v", err)
	}
	secondSourceStart, err := executionstore.IntegrationCompactionSourceStartTx(ctx, fixture.Store.q, secondClaim.Context)
	if err != nil {
		t.Fatalf("derive second compaction source start: %v", err)
	}
	if want := firstCheckpoint.SummarizedThroughEventSequence + 1; secondSourceStart != want {
		t.Fatalf("second compaction source start = %d, want %d", secondSourceStart, want)
	}
	if secondCheckpoint.SummarizedThroughEventSequence != secondFrontier ||
		secondCheckpoint.SummarizedThroughEventSequence <= firstCheckpoint.SummarizedThroughEventSequence {
		t.Fatalf("checkpoint lineage did not advance: first=%+v second=%+v", firstCheckpoint, secondCheckpoint)
	}
}

func TestContextCheckpointRejectsInactiveRuntimeBeforeReaping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		invalidate func(*testing.T, context.Context, processDaemonFixture, time.Time)
	}{
		{
			name: "expired",
			invalidate: func(t *testing.T, ctx context.Context, fixture processDaemonFixture, _ time.Time) {
				expireAgentRuntimeLockForTest(t, ctx, fixture.Store, fixture.Lock.ID)
			},
		},
		{
			name: "canceled",
			invalidate: func(t *testing.T, ctx context.Context, fixture processDaemonFixture, now time.Time) {
				if _, err := requestAgentRuntimeCancelForTest(
					ctx,
					fixture.Store,
					testProjectID,
					fixture.AgentID,
					now,
				); err != nil {
					t.Fatalf("request runtime cancel: %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture, admitted, _ := newMultiInputContinuationSeedFixture(
				t,
				ctx,
				"checkpoint_inactive_"+test.name,
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
			test.invalidate(t, ctx, fixture, fixture.Now.Add(5*time.Second))
			if _, err := publishCheckpointForRangeTest(
				t,
				ctx,
				fixture,
				claim,
				"late "+test.name+" summary",
				fixture.Now.Add(6*time.Second),
			); !errors.Is(err, storeerr.ErrRuntimeLockInactive) {
				t.Fatalf("%s checkpoint error = %v, want runtime lock inactive", test.name, err)
			}
			if _, found, err := fixture.Store.Execution().GetContextCheckpointByProducerContext(
				ctx,
				testProjectID,
				fixture.AgentID,
				claim.Context.ID,
			); err != nil || found {
				t.Fatalf("checkpoint after rejected %s runtime write found=%v err=%v", test.name, found, err)
			}
		})
	}
}

func TestContextCheckpointAndCancellationSerializeWithOneWinner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("cancellation wins", func(t *testing.T) {
		t.Parallel()
		fixture, admitted, _ := newMultiInputContinuationSeedFixture(t, ctx, "checkpoint_cancel_wins")
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
		cancelResult, err := fixture.Store.Execution().CancelAgent(
			ctx,
			executionstore.CancelAgentInput{
				ProjectID: testProjectID,
				AgentID:   fixture.AgentID,
				Actor:     mustOmnaraActorParams(t, fixture.UserID),
			},
		)
		if err != nil {
			t.Fatalf("cancel active compaction: %v", err)
		}
		if !cancelResult.Affected || !cancelResult.RuntimeCancelRequested {
			t.Fatalf("cancel active compaction = %+v, want affected runtime", cancelResult)
		}
		contextRecord, found, err := fixture.Store.Execution().GetModelCallContext(
			ctx,
			testProjectID,
			fixture.AgentID,
			claim.Context.ID,
		)
		if err != nil || !found {
			t.Fatalf("load canceled compaction context: found=%v err=%v", found, err)
		}
		if contextRecord.State != executionstore.ModelCallContextCanceled ||
			contextRecord.RecoveryKind != "" || contextRecord.ErrorKind != "canceled" {
			t.Fatalf("canceled compaction context = %+v", contextRecord)
		}
		if _, err := publishCheckpointForRangeTest(
			t,
			ctx,
			fixture,
			claim,
			"late checkpoint",
			fixture.Now.Add(6*time.Second),
		); err == nil {
			t.Fatal("canceled compaction accepted a late checkpoint")
		}
		if _, found, err := fixture.Store.Execution().GetContextCheckpointByProducerContext(
			ctx,
			testProjectID,
			fixture.AgentID,
			claim.Context.ID,
		); err != nil || found {
			t.Fatalf("late checkpoint found=%v err=%v, want none", found, err)
		}
	})

	t.Run("checkpoint wins", func(t *testing.T) {
		t.Parallel()
		fixture, admitted, _ := newMultiInputContinuationSeedFixture(t, ctx, "checkpoint_publish_wins")
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
		checkpoint, err := publishCheckpointForRangeTest(
			t,
			ctx,
			fixture,
			claim,
			"published before cancel",
			fixture.Now.Add(5*time.Second),
		)
		if err != nil {
			t.Fatalf("publish checkpoint before cancel: %v", err)
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
			t.Fatalf("cancel after checkpoint publication: %v", err)
		}
		if !cancelResult.Affected || !cancelResult.RuntimeCancelRequested {
			t.Fatalf("cancel after checkpoint = %+v, want continuation cancellation", cancelResult)
		}
		contextRecord, found, err := fixture.Store.Execution().GetModelCallContext(
			ctx,
			testProjectID,
			fixture.AgentID,
			claim.Context.ID,
		)
		if err != nil || !found {
			t.Fatalf("load published compaction context: found=%v err=%v", found, err)
		}
		stored, found, err := fixture.Store.Execution().GetContextCheckpoint(
			ctx,
			testProjectID,
			fixture.AgentID,
			checkpoint.ID,
		)
		if err != nil || !found {
			t.Fatalf("load immutable published checkpoint: found=%v err=%v", found, err)
		}
		if contextRecord.State != executionstore.ModelCallContextSucceeded ||
			stored.ProducerModelCallContextID != contextRecord.ID ||
			stored.Summary != "published before cancel" {
			t.Fatalf("published checkpoint lineage changed after cancel: %+v / %+v", contextRecord, stored)
		}
	})
}

func TestKernelContextCheckpointRejectsOpenToolCallAuthority(t *testing.T) {
	ctx := context.Background()

	for _, state := range []string{"open", "running"} {
		t.Run(state, func(t *testing.T) {
			fixture := newProcessDaemonFixture(t, ctx, "kernel_checkpoint_"+state+"_tool_call")
			now := fixture.Now.Add(time.Minute)
			toolCallID := createToolCallForProcessTest(t, ctx, fixture, "kernel_checkpoint_"+state+"_tool_call", "read_process")
			if state == "running" {
				claimToolCallForTest(
					t,
					ctx,
					fixture.Store,
					fixture.AgentID,
					toolCallID,
					fixture.Lock.ID,
					true,
				)
			}
			sourceSequence := toolCallSourceSequenceForCheckpointTest(t, ctx, fixture, toolCallID)
			claim := claimSentCompactionForRangeTest(t, ctx, fixture, 1, sourceSequence, sourceSequence, now.Add(time.Second))
			_, err := publishCheckpointForRangeTest(t, ctx, fixture, claim, "unsafe-"+state, now.Add(2*time.Second))
			if err == nil || !strings.Contains(err.Error(), "cuts an active tool call") {
				t.Fatalf("checkpoint %s tool authority error = %v", state, err)
			}
		})
	}

	t.Run("completed", func(t *testing.T) {
		fixture := newProcessDaemonFixture(t, ctx, "kernel_checkpoint_completed_tool_call")
		now := fixture.Now.Add(time.Minute)
		toolCallID := createToolCallForProcessTest(t, ctx, fixture, "kernel_checkpoint_completed_tool_call", "read_process")
		completed, err := fixture.Store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
			ProjectID: testProjectID, AgentID: fixture.AgentID, ID: toolCallID,
			Outcome: executionstore.ToolResultOutcomeSucceeded, RuntimeLockID: fixture.Lock.ID,
			ResultContentParts: json.RawMessage(`[{"type":"structured_data","value":{"ok":true}}]`),
		})
		if err != nil {
			t.Fatalf("complete tool call: %v", err)
		}
		appended := listTypedToolResultEventsForToolCall(t, ctx, fixture.Store, fixture.AgentID, completed.ID)
		if len(appended) != 1 {
			t.Fatalf("tool result events = %+v, want one", appended)
		}
		claim := claimSentCompactionForRangeTest(
			t, ctx, fixture, 1, appended[0].Sequence, appended[0].Sequence, now.Add(time.Second),
		)
		checkpoint, err := publishCheckpointForRangeTest(t, ctx, fixture, claim, "safe", now.Add(2*time.Second))
		if err != nil {
			t.Fatalf("checkpoint closed tool authority: %v", err)
		}
		if checkpoint.SummarizedThroughEventSequence != appended[0].Sequence {
			t.Fatalf("checkpoint end = %d, want %d", checkpoint.SummarizedThroughEventSequence, appended[0].Sequence)
		}
	})
}
