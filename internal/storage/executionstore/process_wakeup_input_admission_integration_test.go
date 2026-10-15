//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func TestConfigOnlyTurnDoesNotBecomeContinuationSeed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "config_only_not_seed")
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	); err != nil {
		t.Fatalf("release fixture runtime lock: %v", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx,
		`INSERT INTO agent_wakeups(agent_id,ready_at,updated_at) VALUES($1,statement_timestamp(),statement_timestamp())`,
		fixture.AgentID); err != nil {
		t.Fatal(err)
	}
	claim, handled, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		testClaimNextAgentWorkInput(),
	)
	if err != nil {
		t.Fatalf("reconcile stale config-only wakeup: %v", err)
	}
	if !handled || claim.Kind != executionstore.AgentWorkNone {
		t.Fatalf(
			"stale config-only wakeup claim = %+v handled=%v, want non-executable cleanup",
			claim,
			handled,
		)
	}
	if wakeups := countAgentWakeups(t, ctx, fixture.Store, fixture.AgentID); wakeups != 0 {
		t.Fatalf("config-only wakeups after claim reconciliation = %d, want 0", wakeups)
	}
}

func TestCompletedToolResultWakeupClearsAfterLaterModelOutput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "completed_tool_result_frontier")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"completed_tool_result_frontier",
		"read_process",
	)
	turnID := turnIDForProcessToolCallTest(t, ctx, fixture, toolCallID)
	agent, err := fixture.Store.Execution().GetAgentInProject(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("load agent for continuation context: %v", err)
	}
	var openingInputID uuid.UUID
	if err := fixture.Store.pool.QueryRow(
		ctx,
		`SELECT event.agent_input_id FROM agent_events event JOIN agents agent ON agent.id = event.agent_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.turn_id = $3 AND event.is_opening_event ORDER BY event.sequence LIMIT 1`,
		testProjectID, fixture.AgentID, turnID,
	).
		Scan(&openingInputID); err != nil {
		t.Fatalf("load opening input for continuation context: %v", err)
	}
	if _, err := fixture.Store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ID:                 toolCallID,
		RuntimeLockID:      fixture.Lock.ID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		ResultContentParts: json.RawMessage(`[{"type":"text","text":"done"}]`),
	}); err != nil {
		t.Fatalf("complete tool call: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	); err != nil {
		t.Fatalf("release runtime lock: %v", err)
	}
	requireAgentWakeupCoverage(t, ctx, fixture.Store, testProjectID, fixture.AgentID)
	if wakeups := countAgentWakeups(t, ctx, fixture.Store, fixture.AgentID); wakeups != 1 {
		t.Fatalf("completed-tool-result wakeups = %d, want 1", wakeups)
	}
	if err := fixture.Store.Execution().DeleteAgentWakeup(ctx, testProjectID, fixture.AgentID); err != nil {
		t.Fatalf("consume completed-tool-result wakeup: %v", err)
	}
	recoveryNow := time.Now().UTC().Add(time.Second)
	continuationLock, err := fixture.Store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire runtime for continuation context: %v", err)
	}
	continuationFixture := fixture
	continuationFixture.Lock = continuationLock
	continuationWatermark, err := fixture.Store.Execution().MaxEventSequence(
		ctx,
		testProjectID,
		fixture.AgentID,
	)
	if err != nil {
		t.Fatalf("load continuation watermark: %v", err)
	}
	continuationContext := claimNormalContextAtFrontierTest(
		t,
		ctx,
		continuationFixture,
		[]uuid.UUID{openingInputID},
		agent.CurrentConfigID,
		continuationWatermark,
		recoveryNow.Add(250*time.Millisecond),
	)
	createModelOutputEventForTurnTest(
		t,
		ctx,
		continuationFixture,
		turnID,
		continuationContext.ID,
		"completed_tool_result_continuation",
		"end_turn",
		"resp_completed_tool_result_continuation",
		recoveryNow.Add(500*time.Millisecond),
	)
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		continuationLock.ID,
	); err != nil {
		t.Fatalf("release runtime for continuation context: %v", err)
	}
	if turn,
		err := currentContinuableTurnForTest(ctx,
		fixture.Store,
		testProjectID,
		fixture.AgentID); err == nil {
		t.Fatalf("current continuable turn after continuation output = %+v, want none", turn)
	} else if !errors.Is(
		err,
		pgx.ErrNoRows,
	) {
		t.Fatalf("load current continuable turn after continuation output: %v", err)
	}
	requireAgentWakeupCoverage(t, ctx, fixture.Store, testProjectID, fixture.AgentID)
	if wakeups := countAgentWakeups(t, ctx, fixture.Store, fixture.AgentID); wakeups != 0 {
		t.Fatalf("wakeups after continuation output = %d, want 0", wakeups)
	}
}

func TestRuntimeReleaseCreatesWakeupForPendingToolPermission(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "release_pending_tool_permission")
	toolCallID := createToolCallForProcessTestWithPermission(
		t,
		ctx,
		fixture,
		"release_pending_tool_permission",
		"read_process",
		false,
	)
	turnID := turnIDForProcessToolCallTest(t, ctx, fixture, toolCallID)

	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	); err != nil {
		t.Fatalf("release runtime lock: %v", err)
	}
	requireAgentWakeupCoverage(t, ctx, fixture.Store, testProjectID, fixture.AgentID)

	claim, found, err := fixture.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
	if err != nil {
		t.Fatalf("claim pending permission work: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkTool || claim.Tool.TurnID != turnID {
		t.Fatalf("claim = %+v found=%v, want tool authorization work in turn %s", claim, found, turnID)
	}
}

func TestResolvedInteractionCreatesWakeup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "resolved_interaction_wakeup")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "resolved_interaction_wakeup", "ask_question")
	interaction := createQuestionInteractionForTest(t, ctx, fixture, toolCallID)
	questionToolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get question tool call: %v", err)
	}
	if questionToolCall.State != executionstore.ToolCallStateWaiting {
		t.Fatalf(
			"ask_question tool call state after question interaction = %q, want waiting",
			questionToolCall.State,
		)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	); err != nil {
		t.Fatalf("release runtime lock: %v", err)
	}
	if _, err := fixture.Store.Execution().ResolveAgentInteraction(
		ctx,
		executionstore.ResolveAgentInteractionInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			ID:        interaction.ID,
			Resolution: interactionform.Resolution{Answers: []interactionform.Answer{{
				OptionIndices: []int{0},
			}}},
			Actor: mustOmnaraActorParams(t, fixture.UserID),
		},
	); err != nil {
		t.Fatalf("resolve question interaction: %v", err)
	}
	resolvedToolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get resolved question tool call: %v", err)
	}
	if resolvedToolCall.State != "completed" || resolvedToolCall.CompletedAt == nil {
		t.Fatalf(
			"ask_question tool call after resolving question = state %q completed_at %v, want completed",
			resolvedToolCall.State,
			resolvedToolCall.CompletedAt,
		)
	}
	requireAgentWakeupCoverage(t, ctx, fixture.Store, testProjectID, fixture.AgentID)
	if wakeups := countAgentWakeups(t, ctx, fixture.Store, fixture.AgentID); wakeups != 1 {
		t.Fatalf("resolved-interaction wakeups = %d, want 1", wakeups)
	}
}

func TestClaimNextAgentWorkStartsNewTurnAfterCanceledInteraction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "claim_ignores_stopped_open_interaction")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"claim_ignores_stopped_open_interaction",
		"ask_question",
	)
	interaction := createQuestionInteractionForTest(t, ctx, fixture, toolCallID)
	cancelResult, err := fixture.Store.Execution().CancelAgent(
		ctx,
		executionstore.CancelAgentInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			Actor:     mustOmnaraActorParams(t, fixture.UserID),
		},
	)
	if err != nil {
		t.Fatalf("cancel agent: %v", err)
	}
	if cancelResult.Event.ID == uuid.Nil || !cancelResult.Affected {
		t.Fatalf("cancel event = %+v affected=%v, want stop event", cancelResult.Event, cancelResult.Affected)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	); err != nil {
		t.Fatalf("release canceled runtime lock: %v", err)
	}
	closedInteraction, found, err := fixture.Store.Execution().GetAgentInteraction(
		ctx,
		testProjectID,
		fixture.AgentID,
		interaction.ID,
	)
	if err != nil || !found || closedInteraction.State != executionstore.AgentInteractionStateCanceled {
		t.Fatalf("canceled interaction = %+v found=%v err=%v", closedInteraction, found, err)
	}
	if turn,
		err := currentContinuableTurnForTest(ctx,
		fixture.Store,
		testProjectID,
		fixture.AgentID); err == nil {
		t.Fatalf("canceled interaction turn remained continuable: %+v", turn)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("load current continuable turn after cancellation: %v", err)
	}
	if _, err := fixture.Store.Execution().ResolveAgentInteraction(
		ctx,
		executionstore.ResolveAgentInteractionInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			ID:        interaction.ID,
			Resolution: interactionform.Resolution{Answers: []interactionform.Answer{{
				OptionIndices: []int{0},
			}}},
			Actor: mustOmnaraActorParams(t, fixture.UserID),
		},
	); !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("resolve canceled interaction error = %v, want ErrIdempotencyConflict", err)
	}
	var responseInputs int
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT count(*)::int
FROM agent_inputs
WHERE project_id = $1
  AND agent_id = $2
  AND input_kind = 'interaction_response'
  AND target_interaction_id = $3
`, testProjectID, fixture.AgentID, interaction.ID).Scan(&responseInputs); err != nil {
		t.Fatalf("count stopped interaction response inputs: %v", err)
	}
	if responseInputs != 0 {
		t.Fatalf("stopped interaction response inputs = %d, want 0", responseInputs)
	}
	var responseEvents int
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT count(*)::int
FROM agent_events event
JOIN agent_inputs input ON input.agent_id = event.agent_id
  AND input.id = event.agent_input_id
WHERE input.project_id = $1
  AND event.agent_id = $2
  AND event.turn_id = $3
  AND input.input_kind = 'interaction_response'
  AND input.target_interaction_id = $4
`, testProjectID, fixture.AgentID, interaction.TurnID, interaction.ID).Scan(&responseEvents); err != nil {
		t.Fatalf("count stopped interaction response events: %v", err)
	}
	if responseEvents != 0 {
		t.Fatalf("stopped interaction response events = %d, want 0", responseEvents)
	}
	input, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        fixture.AgentID,
		Actor:          mustOmnaraActorParams(t, fixture.UserID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"next queued input"}]`),
		DeliveryMode:   "queued",
		IdempotencyKey: "claim-ignores-stopped-open-interaction-input",
	})
	if err != nil {
		t.Fatalf("create next queued input: %v", err)
	}
	claim, found, err := fixture.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
	if err != nil {
		t.Fatalf("claim next work: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkModel || !claimedOpeningInputIDsEqual(claim, input.ID) ||
		claim.Model.TurnID == interaction.TurnID {
		t.Fatalf(
			"claim found=%v claim=%+v, want new executable turn for input %s after stopped interaction turn %s",
			found,
			claim,
			input.ID,
			interaction.TurnID,
		)
	}
}

func TestPermissionApprovalReturnsToolCallToPending(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "permission_approval_pending")
	enableEventWebhook(t, fixture, nil)
	publisher := &recordingPostCommitPublisher{}
	fixture.Store = newIntegrationStore(
		fixture.Store.pool,
		storage.WithPostCommitPublisher(publisher),
	)
	toolCallID := createToolCallForProcessTestWithPermission(
		t,
		ctx,
		fixture,
		"permission_approval_pending",
		"run_command",
		false,
	)
	interaction := createPermissionInteractionForTest(
		t,
		ctx,
		fixture,
		toolCallID,
		permissionRequestForStorageTest(t, "run_command"),
	)
	waiting, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get waiting tool call: %v", err)
	}
	if waiting.State != executionstore.ToolCallStateAwaitingPermission {
		t.Fatalf("tool execution after permission prompt state=%q, want awaiting_permission", waiting.State)
	}
	if _, err := fixture.Store.Execution().ResolveAgentInteraction(ctx, executionstore.ResolveAgentInteractionInput{
		ProjectID: testProjectID,
		AgentID:   fixture.AgentID,
		ID:        interaction.ID,
		Resolution: interactionform.Resolution{Answers: []interactionform.Answer{{
			OptionIndices: []int{toolpermission.AllowOptionIndex},
		}}},
		Actor: mustOmnaraActorParams(t, fixture.UserID),
	}); err != nil {
		t.Fatalf("resolve permission interaction: %v", err)
	}
	allowed, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get allowed tool call: %v", err)
	}
	if allowed.State != executionstore.ToolCallStateReady {
		t.Fatalf("tool execution after permission allow state=%q, want ready", allowed.State)
	}
	if wakeups := countAgentWakeups(t, ctx, fixture.Store, fixture.AgentID); wakeups != 0 {
		t.Fatalf("owned agent has %d wakeups", wakeups)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(ctx,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID); err != nil {
		t.Fatal(err)
	}
	if wakeups := countAgentWakeups(t, ctx, fixture.Store, fixture.AgentID); wakeups != 1 {
		t.Fatalf("permission approval wakeups = %d, want 1", wakeups)
	}
	states := publisher.toolCallStates(toolCallID)
	assertWebhookToolStates(t, fixture, toolCallID, states)
	if len(states) != 3 ||
		states[0] != string(executionstore.ToolCallStateAwaitingAuthorization) ||
		states[1] != string(executionstore.ToolCallStateAwaitingPermission) ||
		states[2] != string(executionstore.ToolCallStateReady) {
		t.Fatalf(
			"permission tool call update states = %v, want authorization, permission, then ready",
			states,
		)
	}
}

func TestPromotingQueuedInputDuringRetryBackoffAdvancesWakeup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, queuedInput, retryAt := retryBackoffWithQueuedInput(
		t,
		ctx,
		"promote_queued_during_retry",
	)
	promotionStartedAt := databaseStatementTime(t, ctx, fixture.Store)
	if err := fixture.Store.Execution().PromoteQueuedInputToSteering(
		ctx,
		executionstore.PromoteQueuedInputToSteeringInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			InputID:   queuedInput.ID,
		},
	); err != nil {
		t.Fatalf("promote queued input during retry backoff: %v", err)
	}
	promotionFinishedAt := databaseStatementTime(t, ctx, fixture.Store)
	if readyAt := agentWakeupReadyAt(
		t, ctx, fixture.Store, fixture.AgentID,
	); readyAt.Before(promotionStartedAt) || readyAt.After(promotionFinishedAt) || !readyAt.Before(retryAt) {
		t.Fatalf(
			"promoted steering wakeup = %s, want database time in [%s, %s] before retry %s",
			readyAt,
			promotionStartedAt,
			promotionFinishedAt,
			retryAt,
		)
	}
}

func TestCancelQueuedInputDuringRetryBackoffPreservesWakeup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, queuedInput, retryAt := retryBackoffWithQueuedInput(
		t,
		ctx,
		"cancel_queued_during_retry",
	)
	if err := fixture.Store.Execution().CancelQueuedBacklogInput(
		ctx,
		executionstore.CancelQueuedBacklogInputInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			InputID:   queuedInput.ID,
		},
	); err != nil {
		t.Fatalf("cancel queued input during retry backoff: %v", err)
	}
	if readyAt := agentWakeupReadyAt(t, ctx, fixture.Store, fixture.AgentID); !readyAt.Equal(retryAt) {
		t.Fatalf("retry wakeup after queued input cancel = %s, want %s", readyAt, retryAt)
	}
}

func TestCancelDuringRetryBackoffAdvancesQueuedBacklogWakeup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, _, _ := retryBackoffWithQueuedInput(
		t,
		ctx,
		"cancel_retry_with_backlog",
	)
	result, err := fixture.Store.Execution().CancelAgent(
		ctx,
		executionstore.CancelAgentInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			Actor:     mustOmnaraActorParams(t, fixture.UserID),
		},
	)
	if err != nil {
		t.Fatalf("cancel backed-off turn with queued backlog: %v", err)
	}
	if !result.Affected {
		t.Fatalf("cancel result = %+v, want affected turn", result)
	}
	cancelFinishedAt := databaseStatementTime(t, ctx, fixture.Store)
	if readyAt := agentWakeupReadyAt(
		t, ctx, fixture.Store, fixture.AgentID,
	); readyAt.Before(result.Event.At) || readyAt.After(cancelFinishedAt) {
		t.Fatalf(
			"queued backlog wakeup after cancel = %s, want database time in [%s, %s]",
			readyAt,
			result.Event.At,
			cancelFinishedAt,
		)
	}
}
