//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
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
	if seed, found, err := fixture.Store.Execution().NextAgentModelWork(ctx, testProjectID, fixture.AgentID); err != nil {
		t.Fatalf("load continuation seed for config-only turn: %v", err)
	} else if found {
		t.Fatalf("config-only continuation seed = %+v, want none", seed)
	}
	if turn, err := fixture.Store.q.CurrentContinuableAgentTurn(
		ctx,
		dbsqlc.CurrentContinuableAgentTurnParams{ProjectID: testProjectID, AgentID: fixture.AgentID},
	); err == nil {
		t.Fatalf("config-only current continuable turn = %+v, want none", turn)
	} else if !errors.Is(
		err,
		pgx.ErrNoRows,
	) {
		t.Fatalf("load current continuable turn for config-only turn: %v", err)
	}
	if err := fixture.Store.Execution().DeleteAgentWakeup(ctx, testProjectID, fixture.AgentID); err != nil {
		t.Fatalf("delete wakeup before config-only rebuild: %v", err)
	}
	rebuilt, err := fixture.Store.Execution().RebuildMissingAgentWakeups(ctx, testProjectID)
	if err != nil {
		t.Fatalf("rebuild config-only wakeup: %v", err)
	}
	if rebuilt != 0 {
		t.Fatalf("rebuild restored %d wakeups for config-only turn, want 0", rebuilt)
	}
	if err := fixture.Store.Execution().MarkAgentWakeup(
		ctx,
		testProjectID,
		fixture.AgentID,
		json.RawMessage(`{"reason":"test"}`),
	); err != nil {
		t.Fatalf("mark config-only wakeup: %v", err)
	}
	claim, handled, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		testClaimNextAgentWorkInput(),
	)
	if err != nil {
		t.Fatalf("reconcile stale config-only wakeup: %v", err)
	}
	if !handled || claim.Kind != executionstore.AgentWorkNone {
		t.Fatalf("stale config-only wakeup claim = %+v handled=%v, want non-executable cleanup", claim, handled)
	}
	if wakeups := countAgentWakeups(t, ctx, fixture.Store, fixture.AgentID); wakeups != 0 {
		t.Fatalf("config-only wakeups after claim reconciliation = %d, want 0", wakeups)
	}
}

func TestClaimNormalModelCallRejectsInputOutsideTurnOpening(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "model_context_rejects_non_opening_input")
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
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"valid turn input"}]`),
		IdempotencyKey: "model-context-valid-turn-input",
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
	var configInputID ID
	var configEventSequence int64
	if err := fixture.Store.pool.QueryRow(ctx, `
		SELECT input.id, event.sequence
		FROM agent_inputs input
		JOIN agent_events event ON event.agent_id = input.agent_id
		  AND event.id = input.admitted_event_id
		WHERE input.project_id = $1
		  AND input.agent_id = $2
		  AND input.input_kind = 'config_change'
		ORDER BY event.sequence DESC
		LIMIT 1`, testProjectID, fixture.AgentID).Scan(&configInputID, &configEventSequence); err != nil {
		t.Fatalf("load config-change input: %v", err)
	}
	agent, err := fixture.Store.Execution().GetAgentInProject(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("load agent for model context: %v", err)
	}
	_, err = fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		RuntimeLockID:      claim.RuntimeLock.ID,
		OpeningInputIDs:    []ID{configInputID},
		AgentConfigID:      agent.CurrentConfigID,
		InputEventSequence: configEventSequence,
	})
	if !errors.Is(err, storeerr.ErrAgentNotAdvanceable) {
		t.Fatalf("claim model context with non-opening source error = %v, want ErrAgentNotAdvanceable", err)
	}
}

func TestClaimNormalModelCallRequiresExactOpeningInputSet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "model_context_exact_opening_inputs")
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	); err != nil {
		t.Fatalf("release fixture runtime lock: %v", err)
	}
	first, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        fixture.AgentID,
		Actor:          mustOmnaraActorParams(t, fixture.UserID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"first steering"}]`),
		DeliveryMode:   "steering",
		IdempotencyKey: "exact-opening-input-first",
	})
	if err != nil {
		t.Fatalf("create first steering input: %v", err)
	}
	second, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        fixture.AgentID,
		Actor:          mustOmnaraActorParams(t, fixture.UserID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"second steering"}]`),
		DeliveryMode:   "steering",
		IdempotencyKey: "exact-opening-input-second",
	})
	if err != nil {
		t.Fatalf("create second steering input: %v", err)
	}
	claim, found, err := fixture.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
	if err != nil {
		t.Fatalf("claim steering work: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkModel || len(claim.Model.InputIDs) != 2 {
		t.Fatalf("claim = %+v found=%v, want two opening inputs", claim, found)
	}
	inputIDs := []ID{first.ID, second.ID}
	if claim.Model.InputIDs[0] != inputIDs[0] || claim.Model.InputIDs[1] != inputIDs[1] {
		t.Fatalf("claim input ids = %v, want %v", claim.Model.InputIDs, inputIDs)
	}
	agent, err := fixture.Store.Execution().GetAgentInProject(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("load agent for model context: %v", err)
	}
	var watermark int64
	if err := fixture.Store.pool.QueryRow(ctx, `SELECT max(event.sequence) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.agent_input_id = ANY($3::uuid[])`, testProjectID, fixture.AgentID, claim.Model.InputIDs).
		Scan(&watermark); err != nil {
		t.Fatalf("load opening watermark: %v", err)
	}
	_, err = fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		RuntimeLockID:      claim.RuntimeLock.ID,
		OpeningInputIDs:    []ID{first.ID},
		AgentConfigID:      agent.CurrentConfigID,
		InputEventSequence: watermark,
	})
	if !errors.Is(err, storeerr.ErrAgentNotAdvanceable) {
		t.Fatalf("missing opening input error = %v, want ErrAgentNotAdvanceable", err)
	}
	_, err = fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		RuntimeLockID:      claim.RuntimeLock.ID,
		OpeningInputIDs:    []ID{first.ID, first.ID},
		AgentConfigID:      agent.CurrentConfigID,
		InputEventSequence: watermark,
	})
	if !errors.Is(err, storeerr.ErrAgentNotAdvanceable) {
		t.Fatalf("duplicate opening input error = %v, want ErrAgentNotAdvanceable", err)
	}
	if _, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		RuntimeLockID:      claim.RuntimeLock.ID,
		OpeningInputIDs:    inputIDs,
		AgentConfigID:      agent.CurrentConfigID,
		InputEventSequence: watermark,
	}); err != nil {
		t.Fatalf("claim model context with exact opening sources: %v", err)
	}
}

func TestRebuildMissingWakeupForCompletedToolResultUntilLaterModelOutput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "rebuild_completed_tool_result_frontier")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "rebuild_completed_tool_result_frontier", "read_process")
	turnID := turnIDForProcessToolCallTest(t, ctx, fixture, toolCallID)
	agent, err := fixture.Store.Execution().GetAgentInProject(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("load agent for continuation context: %v", err)
	}
	var openingInputID ID
	if err := fixture.Store.pool.QueryRow(ctx, `SELECT event.agent_input_id FROM agent_events event JOIN agents agent ON agent.id = event.agent_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.turn_id = $3 AND event.is_opening_event ORDER BY event.sequence LIMIT 1`, testProjectID, fixture.AgentID, turnID).
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
	if err := fixture.Store.Execution().DeleteAgentWakeup(ctx, testProjectID, fixture.AgentID); err != nil {
		t.Fatalf("delete wakeup before rebuild: %v", err)
	}
	rebuilt, err := fixture.Store.Execution().RebuildMissingAgentWakeups(ctx, testProjectID)
	if err != nil {
		t.Fatalf("rebuild wakeups after completed tool result: %v", err)
	}
	if rebuilt != 1 {
		t.Fatalf("rebuild restored %d wakeups after completed tool result, want 1", rebuilt)
	}
	if err := fixture.Store.Execution().DeleteAgentWakeup(ctx, testProjectID, fixture.AgentID); err != nil {
		t.Fatalf("delete rebuilt wakeup: %v", err)
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
		[]ID{openingInputID},
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
		"rebuild_completed_tool_result_continuation",
		"end_turn",
		"resp_rebuild_completed_tool_result_continuation",
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
	if turn, err := fixture.Store.q.CurrentContinuableAgentTurn(
		ctx,
		dbsqlc.CurrentContinuableAgentTurnParams{ProjectID: testProjectID, AgentID: fixture.AgentID},
	); err == nil {
		t.Fatalf("current continuable turn after continuation output = %+v, want none", turn)
	} else if !errors.Is(
		err,
		pgx.ErrNoRows,
	) {
		t.Fatalf("load current continuable turn after continuation output: %v", err)
	}
	rebuilt, err = fixture.Store.Execution().RebuildMissingAgentWakeups(ctx, testProjectID)
	if err != nil {
		t.Fatalf("rebuild wakeups after continuation output: %v", err)
	}
	if rebuilt != 0 {
		t.Fatalf("rebuild restored %d wakeups after continuation output, want 0", rebuilt)
	}
}

func TestRebuildMissingWakeupForPendingToolPermission(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "rebuild_pending_tool_permission")
	toolCallID := createToolCallForProcessTestWithPermission(
		t,
		ctx,
		fixture,
		"rebuild_pending_tool_permission",
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
	if err := fixture.Store.Execution().DeleteAgentWakeup(ctx, testProjectID, fixture.AgentID); err != nil {
		t.Fatalf("delete wakeup before rebuild: %v", err)
	}
	rebuilt, err := fixture.Store.Execution().RebuildMissingAgentWakeups(ctx, testProjectID)
	if err != nil {
		t.Fatalf("rebuild pending permission wakeup: %v", err)
	}
	if rebuilt != 1 {
		t.Fatalf("rebuild restored %d wakeups for pending permission, want 1", rebuilt)
	}

	claim, found, err := fixture.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
	if err != nil {
		t.Fatalf("claim pending permission work: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkTool || claim.Tool.TurnID != turnID {
		t.Fatalf("claim = %+v found=%v, want tool authorization work in turn %s", claim, found, turnID)
	}
}

func TestRebuildMissingWakeupForResolvedInteraction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "rebuild_resolved_interaction")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "rebuild_resolved_interaction", "ask_question")
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
	if err := fixture.Store.Execution().DeleteAgentWakeup(ctx, testProjectID, fixture.AgentID); err != nil {
		t.Fatalf("delete wakeup before rebuild: %v", err)
	}
	rebuilt, err := fixture.Store.Execution().RebuildMissingAgentWakeups(ctx, testProjectID)
	if err != nil {
		t.Fatalf("rebuild wakeup after resolved interaction: %v", err)
	}
	if rebuilt != 1 {
		t.Fatalf("rebuild restored %d wakeups after resolved interaction, want 1", rebuilt)
	}
}

func TestClaimNextAgentWorkStartsNewTurnAfterCanceledInteraction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "claim_ignores_stopped_open_interaction")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "claim_ignores_stopped_open_interaction", "ask_question")
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
	if cancelResult.Event.ID == NilID || !cancelResult.Affected {
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
	if turn, err := fixture.Store.q.CurrentContinuableAgentTurn(
		ctx,
		dbsqlc.CurrentContinuableAgentTurnParams{ProjectID: testProjectID, AgentID: fixture.AgentID},
	); err == nil {
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
	if !found || claim.Kind != executionstore.AgentWorkModel || !claimedOpeningInputIDsEqual(claim, input.ID) || claim.Model.TurnID == interaction.TurnID {
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
	publisher := &recordingPostCommitPublisher{}
	fixture.Store = newIntegrationStore(
		fixture.Store.pool,
		WithPostCommitPublisher(publisher),
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
	if wakeups := countAgentWakeups(t, ctx, fixture.Store, fixture.AgentID); wakeups != 1 {
		t.Fatalf("permission approval wakeups = %d, want 1", wakeups)
	}
	states := publisher.toolCallStates(toolCallID)
	if len(states) != 3 ||
		states[0] != string(executionstore.ToolCallStateAwaitingAuthorization) ||
		states[1] != string(executionstore.ToolCallStateAwaitingPermission) ||
		states[2] != string(executionstore.ToolCallStateReady) {
		t.Fatalf("permission tool call update states = %v, want authorization, permission, then ready", states)
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
	if readyAt := agentWakeupReadyAt(t, ctx, fixture.Store, fixture.AgentID); readyAt.Before(promotionStartedAt) || readyAt.After(promotionFinishedAt) || !readyAt.Before(retryAt) {
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
	if readyAt := agentWakeupReadyAt(t, ctx, fixture.Store, fixture.AgentID); readyAt.Before(result.Event.At) || readyAt.After(cancelFinishedAt) {
		t.Fatalf(
			"queued backlog wakeup after cancel = %s, want database time in [%s, %s]",
			readyAt,
			result.Event.At,
			cancelFinishedAt,
		)
	}
}

func TestClaimNormalModelCallRejectsOpeningInputsPastWatermark(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "model_context_opening_watermark")
	now := fixture.Now
	firstInput, _, _, err := fixture.Store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      testProjectID,
			AgentID:        fixture.AgentID,
			Actor:          mustOmnaraActorParams(t, fixture.UserID),
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"first"}]`),
			DeliveryMode:   executionstore.DeliveryModeSteering,
			IdempotencyKey: "idem-opening-watermark-first",
		},
	)
	if err != nil {
		t.Fatalf("create first steering input: %v", err)
	}
	secondInput, _, _, err := fixture.Store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      testProjectID,
			AgentID:        fixture.AgentID,
			Actor:          mustOmnaraActorParams(t, fixture.UserID),
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"second"}]`),
			DeliveryMode:   executionstore.DeliveryModeSteering,
			IdempotencyKey: "idem-opening-watermark-second",
		},
	)
	if err != nil {
		t.Fatalf("create second steering input: %v", err)
	}
	admitted, found := admitNextAgentInputAndOpenTurnForTest(
		t,
		ctx,
		fixture.Store,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	)
	if !found || len(admitted.Inputs) != 2 || admitted.Inputs[0].ID != firstInput.ID ||
		admitted.Inputs[1].ID != secondInput.ID ||
		len(admitted.Events) != 2 {
		t.Fatalf("expected two opening steering inputs, found=%v admitted=%+v", found, admitted)
	}
	turns, err := fixture.Store.Execution().ListAgentTurnsForRead(
		ctx,
		testProjectID,
		fixture.AgentID,
		0,
		1,
	)
	if err != nil {
		t.Fatalf("list admitted turn: %v", err)
	}
	if len(turns) != 1 || turns[0].ID != admitted.Turn.ID ||
		!turns[0].StartedAt.Equal(admitted.Events[0].At) ||
		!turns[0].UpdatedAt.Equal(admitted.Events[1].At) {
		t.Fatalf(
			"derived turn timestamps = %+v, want started=%s updated=%s",
			turns,
			admitted.Events[0].At,
			admitted.Events[1].At,
		)
	}
	agent, err := fixture.Store.Execution().GetAgentInProject(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("load agent: %v", err)
	}
	inputIDs := []ID{firstInput.ID, secondInput.ID}
	claimContext := func(openingInputIDs []ID, frontier int64, at time.Time) (executionstore.ModelCallClaim, error) {
		return fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
			ProjectID:          testProjectID,
			AgentID:            fixture.AgentID,
			RuntimeLockID:      fixture.Lock.ID,
			OpeningInputIDs:    openingInputIDs,
			AgentConfigID:      agent.CurrentConfigID,
			InputEventSequence: frontier,
		})
	}
	if _, err := claimContext(
		[]ID{firstInput.ID, firstInput.ID},
		admitted.Events[1].Sequence,
		now.Add(3500*time.Millisecond),
	); !errors.Is(err, storeerr.ErrAgentNotAdvanceable) {
		t.Fatalf("claim model context with duplicate opening input err=%v, want %v", err, storeerr.ErrAgentNotAdvanceable)
	}
	if _, err := claimContext(
		[]ID{firstInput.ID},
		admitted.Events[1].Sequence,
		now.Add(3600*time.Millisecond),
	); !errors.Is(err, storeerr.ErrAgentNotAdvanceable) {
		t.Fatalf("claim model context with subset opening input err=%v, want %v", err, storeerr.ErrAgentNotAdvanceable)
	}
	if _, err := claimContext(
		inputIDs,
		admitted.Events[0].Sequence,
		now.Add(4*time.Second),
	); !errors.Is(err, storeerr.ErrAgentNotAdvanceable) {
		t.Fatalf("claim model context before second opening event err=%v, want %v", err, storeerr.ErrAgentNotAdvanceable)
	}
	firstClaim, err := claimContext(inputIDs, admitted.Events[1].Sequence, now.Add(5*time.Second))
	if err != nil {
		t.Fatalf("claim model context after full opening set: %v", err)
	}
	repeatedClaim, err := claimContext(inputIDs, admitted.Events[1].Sequence, now.Add(6*time.Second))
	if err != nil {
		t.Fatalf("repeat semantic model context claim: %v", err)
	}
	if repeatedClaim.Context.ID != firstClaim.Context.ID ||
		repeatedClaim.Created {
		t.Fatalf("repeated semantic claim = %+v, want existing context", repeatedClaim)
	}
}
