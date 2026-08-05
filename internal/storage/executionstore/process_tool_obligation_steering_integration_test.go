//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestRunnableToolWinsOverBlockedSibling(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "runnable_tool_with_blocked_sibling")
	blockedSibling := builtInProcessToolCallBatchItem("blocked_sibling", "run_command")
	blockedSibling.Allowed = false
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"runnable_tool_with_blocked_sibling",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("runnable_sibling", "read_process"),
			blockedSibling,
		},
	)
	createPermissionInteractionForTest(
		t,
		ctx,
		fixture,
		toolCallIDs[1],
		permissionRequestForStorageTest(t, "run_command"),
	)
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	); err != nil {
		t.Fatalf("release tool-producing runtime: %v", err)
	}

	work, found, err := fixture.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
	if err != nil {
		t.Fatalf("claim runnable sibling: %v", err)
	}
	if !found || work.Kind != executionstore.AgentWorkTool {
		t.Fatalf("claimed work = %+v found=%v, want executable tool batch", work, found)
	}
	blocked, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallIDs[1])
	if err != nil {
		t.Fatalf("load blocked sibling: %v", err)
	}
	if blocked.State != executionstore.ToolCallStateAwaitingPermission {
		t.Fatalf("blocked sibling state = %q, want awaiting_permission", blocked.State)
	}
}

func TestSteeringWaitsForPendingToolObligation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "steering_waits_for_pending_tool")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"steering_waits_for_pending_tool",
		"read_process",
	)
	modelContextID := modelContextIDForProcessToolCallTest(t, ctx, fixture, toolCallID)
	turnID := turnIDForProcessToolCallTest(t, ctx, fixture, toolCallID)
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	); err != nil {
		t.Fatalf("release tool-producing runtime: %v", err)
	}

	steering, _, _, err := fixture.Store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      testProjectID,
			AgentID:        fixture.AgentID,
			Actor:          mustOmnaraActorParams(t, fixture.UserID),
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"use the new destination next"}]`),
			DeliveryMode:   executionstore.DeliveryModeSteering,
			IdempotencyKey: "steering-waits-for-pending-tool",
		},
	)
	if err != nil {
		t.Fatalf("create steering input: %v", err)
	}

	work, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		testClaimNextAgentWorkInput(),
	)
	if err != nil {
		t.Fatalf("claim pending tool obligation: %v", err)
	}
	if !found || work.Kind != executionstore.AgentWorkTool || work.Tool.TurnID != turnID ||
		work.Tool.ModelCallContextID != modelContextID ||
		work.Model.AdmittedInputTurn.Turn.ID != NilID {
		t.Fatalf(
			"claimed work = %+v found=%v, want existing turn %s context %s without steering admission",
			work,
			found,
			turnID,
			modelContextID,
		)
	}

	var pendingTool, waitingSteering int
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT
  count(*) FILTER (
    WHERE tool_call.id = $3
      AND tool_call.state = 'ready'
  )::int,
  (SELECT count(*)::int
   FROM agent_inputs input
   WHERE input.project_id = $1
     AND input.agent_id = $2
     AND input.id = $4
     AND input.state = 'received'
     AND input.admitted_event_id IS NULL)
FROM tool_call_read_projection tool_call
WHERE tool_call.project_id = $1
  AND tool_call.agent_id = $2
`, testProjectID, fixture.AgentID, toolCallID, steering.ID).Scan(
		&pendingTool,
		&waitingSteering,
	); err != nil {
		t.Fatalf("read pending obligation and waiting steering: %v", err)
	}
	if pendingTool != 1 || waitingSteering != 1 {
		t.Fatalf(
			"pending tool=%d waiting steering=%d, want both preserved for ordered execution",
			pendingTool,
			waitingSteering,
		)
	}
}

func TestIdempotentSteeringReplayDoesNotCancelOpenInteraction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "idempotent_steering_preserves_interaction")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"idempotent_steering_preserves_interaction",
		"ask_question",
	)
	interaction := createQuestionInteractionForTest(t, ctx, fixture, toolCallID)
	create := executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        fixture.AgentID,
		Actor:          mustOmnaraActorParams(t, fixture.UserID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"wait for the answer"}]`),
		DeliveryMode:   executionstore.DeliveryModeSteering,
		IdempotencyKey: "idempotent-steering-preserves-interaction",
	}
	first, _, created, err := fixture.Store.Execution().CreateAgentContentInput(ctx, create)
	if err != nil || !created {
		t.Fatalf("create steering input: created=%v err=%v", created, err)
	}
	create.CancelOpenInteractions = true
	replayed, _, created, err := fixture.Store.Execution().CreateAgentContentInput(ctx, create)
	if err != nil || created || replayed.ID != first.ID {
		t.Fatalf("replay steering input = %+v created=%v err=%v", replayed, created, err)
	}
	preserved, found, err := fixture.Store.Execution().GetAgentInteraction(
		ctx,
		testProjectID,
		fixture.AgentID,
		interaction.ID,
	)
	if err != nil {
		t.Fatalf("load interaction after replay: %v", err)
	}
	if !found || preserved.State != executionstore.AgentInteractionStateOpen || preserved.ResolvedByInputID != NilID {
		t.Fatalf("interaction after replay = %+v found=%v, want open", preserved, found)
	}
}

func TestPromoteQueuedInputPreservesOpenInteractionsByDefault(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "promotion_preserves_interaction")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"promotion_preserves_interaction",
		"ask_question",
	)
	interaction := createQuestionInteractionForTest(t, ctx, fixture, toolCallID)
	queued, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        fixture.AgentID,
		Actor:          mustOmnaraActorParams(t, fixture.UserID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"steer after the answer"}]`),
		IdempotencyKey: "promotion-preserves-interaction",
	})
	if err != nil {
		t.Fatalf("create queued input: %v", err)
	}
	if err := fixture.Store.Execution().PromoteQueuedInputToSteering(
		ctx,
		executionstore.PromoteQueuedInputToSteeringInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			InputID:   queued.ID,
		},
	); err != nil {
		t.Fatalf("promote queued input: %v", err)
	}
	preserved, found, err := fixture.Store.Execution().GetAgentInteraction(
		ctx,
		testProjectID,
		fixture.AgentID,
		interaction.ID,
	)
	if err != nil {
		t.Fatalf("load interaction after promotion: %v", err)
	}
	if !found || preserved.State != executionstore.AgentInteractionStateOpen || preserved.ResolvedByInputID != NilID {
		t.Fatalf("interaction after promotion = %+v found=%v, want open", preserved, found)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("load question tool call after promotion: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateWaiting {
		t.Fatalf("question tool call after promotion = %q, want waiting", toolCall.State)
	}
}

func TestPromoteQueuedInputCanCancelCurrentTurnInteractions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "promotion_cancels_interactions")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"promotion_cancels_interactions",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("promotion_question_one", "ask_question"),
			builtInProcessToolCallBatchItem("promotion_question_two", "ask_question"),
			builtInProcessToolCallBatchItem("promotion_unrelated_tool", "read_process"),
		},
	)
	interactions := []executionstore.AgentInteractionRecord{
		createQuestionInteractionForTest(t, ctx, fixture, toolCallIDs[0]),
		createQuestionInteractionForTest(t, ctx, fixture, toolCallIDs[1]),
	}
	queued, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        fixture.AgentID,
		Actor:          mustOmnaraActorParams(t, fixture.UserID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"continue without the questions"}]`),
		IdempotencyKey: "promotion-cancels-interactions",
	})
	if err != nil {
		t.Fatalf("create queued input: %v", err)
	}
	if err := fixture.Store.Execution().PromoteQueuedInputToSteering(
		ctx,
		executionstore.PromoteQueuedInputToSteeringInput{
			ProjectID:              testProjectID,
			AgentID:                fixture.AgentID,
			InputID:                queued.ID,
			CancelOpenInteractions: true,
		},
	); err != nil {
		t.Fatalf("promote queued input with interaction cancellation: %v", err)
	}
	for _, interaction := range interactions {
		canceled, found, err := fixture.Store.Execution().GetAgentInteraction(
			ctx,
			testProjectID,
			fixture.AgentID,
			interaction.ID,
		)
		if err != nil {
			t.Fatalf("load canceled interaction %s: %v", interaction.ID, err)
		}
		if !found || canceled.State != executionstore.AgentInteractionStateCanceled || canceled.ResolvedByInputID != queued.ID {
			t.Fatalf(
				"interaction %s after promotion = %+v found=%v, want canceled by %s",
				interaction.ID,
				canceled,
				found,
				queued.ID,
			)
		}
		toolCall, err := fixture.Store.Execution().GetToolCall(
			ctx,
			testProjectID,
			fixture.AgentID,
			interaction.ToolCallID,
		)
		if err != nil {
			t.Fatalf("load canceled interaction tool call %s: %v", interaction.ToolCallID, err)
		}
		if toolCall.State != executionstore.ToolCallStateCompleted || toolCall.Outcome != executionstore.ToolResultOutcomeCanceled {
			t.Fatalf(
				"tool call %s after promotion = state %q outcome %q, want completed/canceled",
				toolCall.ID,
				toolCall.State,
				toolCall.Outcome,
			)
		}
	}
	unrelated, err := fixture.Store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallIDs[2],
	)
	if err != nil {
		t.Fatalf("load unrelated sibling tool call: %v", err)
	}
	if unrelated.State != executionstore.ToolCallStateReady || unrelated.Outcome != "" {
		t.Fatalf(
			"unrelated sibling after promotion = state %q outcome %q, want ready without outcome",
			unrelated.State,
			unrelated.Outcome,
		)
	}
}
