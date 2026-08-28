//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

const backlogRankStride int64 = 1024

func TestPromoteQueuedInputToSteeringIsIdempotentAfterPromotionAndAdmission(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "idempotent-backlog-promotion")
	createQueued := func(label string) executionstore.AgentInputRecord {
		input, _, _, err := fixture.Store.Execution().CreateAgentContentInput(
			ctx,
			executionstore.CreateAgentContentInputInput{
				ProjectID:      testProjectID,
				AgentID:        fixture.AgentID,
				Actor:          mustOmnaraActorParams(t, fixture.UserID),
				ContentBlocks:  json.RawMessage(`[{"type":"text","text":"` + label + `"}]`),
				IdempotencyKey: "idem-idempotent-promotion-" + label,
			},
		)
		if err != nil {
			t.Fatalf("create %s input: %v", label, err)
		}
		return input
	}
	promote := func(inputID ID) error {
		return fixture.Store.Execution().PromoteQueuedInputToSteering(
			ctx,
			executionstore.PromoteQueuedInputToSteeringInput{
				ProjectID: testProjectID,
				AgentID:   fixture.AgentID,
				InputID:   inputID,
			},
		)
	}

	admittedFirst := createQueued("admitted-first")
	admitted, found := admitNextAgentInputAndOpenTurnForTest(
		t,
		ctx,
		fixture.Store,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	)
	if !found || len(admitted.Inputs) != 1 || admitted.Inputs[0].ID != admittedFirst.ID {
		t.Fatalf("admitted inputs = %+v, want %s", admitted.Inputs, admittedFirst.ID)
	}
	if err := promote(admittedFirst.ID); err != nil {
		t.Fatalf("promote after admission: %v", err)
	}
	if err := promote(admittedFirst.ID); err != nil {
		t.Fatalf("repeat promotion after admission: %v", err)
	}

	promotedFirst := createQueued("promoted-first")
	if err := promote(promotedFirst.ID); err != nil {
		t.Fatalf("promote queued input: %v", err)
	}
	if err := promote(promotedFirst.ID); err != nil {
		t.Fatalf("repeat promotion while steering: %v", err)
	}

	canceled := createQueued("canceled")
	if err := fixture.Store.Execution().CancelQueuedBacklogInput(
		ctx,
		executionstore.CancelQueuedBacklogInputInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			InputID:   canceled.ID,
		},
	); err != nil {
		t.Fatalf("cancel queued input: %v", err)
	}
	if err := promote(canceled.ID); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("promote canceled input error = %v, want state transition conflict", err)
	}
}

func TestQueuedBacklogMutationsRemainAvailableAfterCancel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 18, 17, 0, 0, 0, time.UTC)
	var agentID ID
	user := mustCreateProjectOperatorUser(t, ctx, store, "backlog-mutation@example.com", "Backlog Mutation")

	agentID = mustCreateAgent(t, ctx, store, now)
	queuedInput, _, _, err := store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      testProjectID,
			AgentID:        agentID,
			Actor:          mustOmnaraActorParams(t, user.ID),
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"queued"}]`),
			IdempotencyKey: "idem-stopped-backlog-queued",
		},
	)
	if err != nil {
		t.Fatalf("create queued input: %v", err)
	}
	steeringInput, _, _, err := store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      testProjectID,
			AgentID:        agentID,
			Actor:          mustOmnaraActorParams(t, user.ID),
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"steering"}]`),
			DeliveryMode:   executionstore.DeliveryModeSteering,
			IdempotencyKey: "idem-stopped-backlog-steering",
		},
	)
	if err != nil {
		t.Fatalf("create steering input: %v", err)
	}
	_, err = pool.Exec(ctx, `UPDATE agent_inputs SET metadata = '{"mutated":true}'::jsonb WHERE id = $1`, queuedInput.ID)
	assertPgErrorMessage(t, err, "25006", "agent_input intent and identity are immutable")
	_, err = pool.Exec(ctx, `DELETE FROM agent_inputs WHERE id = $1`, queuedInput.ID)
	assertPgErrorMessage(t, err, "25006", "agent_inputs are immutable")
	_, err = pool.Exec(ctx, `
UPDATE agent_inputs
SET state = 'canceled',
    canceled_at = $2,
    delivery_mode = 'steering',
    input_rank = input_rank + 1
WHERE id = $1
`, queuedInput.ID, now.Add(time.Second))
	assertPgErrorMessage(
		t,
		err,
		"25006",
		"agent_input delivery may change only while content remains received",
	)
	beforeCancelEvents, err := store.Execution().ListAgentEventsForRead(ctx, testProjectID, agentID, 0, 100)
	if err != nil {
		t.Fatalf("list events before no-op cancel: %v", err)
	}
	cancelResult, err := store.Execution().CancelAgent(
		ctx,
		executionstore.CancelAgentInput{
			ProjectID: testProjectID,
			AgentID:   agentID,
			Actor:     mustOmnaraActorParams(t, user.ID),
		},
	)
	if err != nil {
		t.Fatalf("cancel agent: %v", err)
	}
	if cancelResult.Event.ID != NilID || cancelResult.RuntimeCancelRequested || cancelResult.Affected {
		t.Fatalf(
			"idle cancel should be a no-op event=%+v runtime_cancel_requested=%v affected=%v",
			cancelResult.Event,
			cancelResult.RuntimeCancelRequested,
			cancelResult.Affected,
		)
	}
	afterCancelEvents, err := store.Execution().ListAgentEventsForRead(ctx, testProjectID, agentID, 0, 100)
	if err != nil {
		t.Fatalf("list events after no-op cancel: %v", err)
	}
	if len(afterCancelEvents) != len(beforeCancelEvents) {
		t.Fatalf("idle cancel appended events: before=%d after=%d", len(beforeCancelEvents), len(afterCancelEvents))
	}
	var queuedState, steeringState string
	if err := pool.QueryRow(ctx, `SELECT state FROM agent_inputs WHERE id = $1`, queuedInput.ID).
		Scan(&queuedState); err != nil {
		t.Fatalf("load queued input state after no-op cancel: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT state FROM agent_inputs WHERE id = $1`, steeringInput.ID).
		Scan(&steeringState); err != nil {
		t.Fatalf("load steering input state after no-op cancel: %v", err)
	}
	if queuedState != "received" || steeringState != "received" {
		t.Fatalf("idle cancel changed input states: queued=%s steering=%s", queuedState, steeringState)
	}

	if err := store.Execution().MoveQueuedBacklogInput(
		ctx,
		executionstore.MoveQueuedBacklogInputInput{
			ProjectID: testProjectID,
			AgentID:   agentID,
			InputID:   queuedInput.ID,
			Position:  executionstore.MoveQueuedBacklogInputToFront,
		},
	); err != nil {
		t.Fatalf("move queued input after cancel: %v", err)
	}
	if err := store.Execution().PromoteQueuedInputToSteering(
		ctx,
		executionstore.PromoteQueuedInputToSteeringInput{
			ProjectID: testProjectID,
			AgentID:   agentID,
			InputID:   queuedInput.ID,
		},
	); err != nil {
		t.Fatalf("promote queued input after cancel: %v", err)
	}
	if err := store.Execution().DemoteSteeringInputToQueued(
		ctx,
		executionstore.DemoteSteeringInputToQueuedInput{
			ProjectID: testProjectID,
			AgentID:   agentID,
			InputID:   steeringInput.ID,
		},
	); err != nil {
		t.Fatalf("demote steering input after cancel: %v", err)
	}
}

func TestCancelQueuedBacklogInputReconcilesWakeupAndResetsEmptyQueueRank(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateProjectOperatorUser(t, ctx, store, "backlog-cancel@example.com", "Backlog Cancel")
	agentID := mustCreateAgent(t, ctx, store, time.Date(2026, 5, 18, 17, 0, 0, 0, time.UTC))
	createQueued := func(label string) executionstore.AgentInputRecord {
		input, _, _, err := store.Execution().CreateAgentContentInput(
			ctx,
			executionstore.CreateAgentContentInputInput{
				ProjectID:      testProjectID,
				AgentID:        agentID,
				Actor:          mustOmnaraActorParams(t, user.ID),
				ContentBlocks:  json.RawMessage(`[{"type":"text","text":"` + label + `"}]`),
				IdempotencyKey: "idem-backlog-cancel-" + label,
			},
		)
		if err != nil {
			t.Fatalf("create %s input: %v", label, err)
		}
		return input
	}
	first := createQueued("first")
	second := createQueued("second")
	if first.InputRank != backlogRankStride || second.InputRank != 2*backlogRankStride {
		t.Fatalf(
			"initial ranks = (%d, %d), want (%d, %d)",
			first.InputRank,
			second.InputRank,
			backlogRankStride,
			2*backlogRankStride,
		)
	}
	if err := store.Execution().CancelQueuedBacklogInput(ctx, executionstore.CancelQueuedBacklogInputInput{
		ProjectID: testProjectID,
		AgentID:   agentID,
		InputID:   first.ID,
	}); err != nil {
		t.Fatalf("cancel first input: %v", err)
	}
	if count := countAgentWakeups(t, ctx, store, agentID); count != 1 {
		t.Fatalf("wakeup count with one queued input = %d, want 1", count)
	}
	if err := store.Execution().CancelQueuedBacklogInput(ctx, executionstore.CancelQueuedBacklogInputInput{
		ProjectID: testProjectID,
		AgentID:   agentID,
		InputID:   second.ID,
	}); err != nil {
		t.Fatalf("cancel second input: %v", err)
	}
	if count := countAgentWakeups(t, ctx, store, agentID); count != 0 {
		t.Fatalf("wakeup count with empty queue = %d, want 0", count)
	}

	third := createQueued("third")
	if third.InputRank != backlogRankStride {
		t.Fatalf("rank after queue emptied = %d, want %d", third.InputRank, backlogRankStride)
	}
}

func TestListQueuedBacklogInputsPaginatesSteeringBeforeQueueOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 18, 17, 0, 0, 0, time.UTC)
	user := mustCreateProjectOperatorUser(t, ctx, store, "backlog-pagination@example.com", "Backlog Pagination")
	agentID := mustCreateAgent(t, ctx, store, now)
	createInput := func(label string, mode executionstore.AgentInputDeliveryMode) executionstore.AgentInputRecord {
		input, _, _, err := store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
			ProjectID:      testProjectID,
			AgentID:        agentID,
			Actor:          mustOmnaraActorParams(t, user.ID),
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"` + label + `"}]`),
			DeliveryMode:   mode,
			IdempotencyKey: "idem-backlog-page-" + label,
		})
		if err != nil {
			t.Fatalf("create %s input: %v", label, err)
		}
		return input
	}

	first := createInput("first", executionstore.DeliveryModeQueued)
	second := createInput("second", executionstore.DeliveryModeQueued)
	third := createInput("third", executionstore.DeliveryModeQueued)
	move := func(inputID ID, position executionstore.MoveQueuedBacklogInputPosition, anchorID ID) {
		t.Helper()
		if err := store.Execution().MoveQueuedBacklogInput(ctx, executionstore.MoveQueuedBacklogInputInput{
			ProjectID:     testProjectID,
			AgentID:       agentID,
			InputID:       inputID,
			Position:      position,
			AnchorInputID: anchorID,
		}); err != nil {
			t.Fatalf("move input %s %s: %v", inputID, position, err)
		}
	}
	assertOrder := func(want ...ID) {
		t.Helper()
		got, err := store.Execution().ListQueuedBacklogInputs(
			ctx,
			executionstore.ListQueuedBacklogInputsInput{
				ProjectID: testProjectID,
				AgentID:   agentID,
				Limit:     len(want),
			},
		)
		if err != nil {
			t.Fatalf("list reordered backlog: %v", err)
		}
		if len(got.Inputs) != len(want) {
			t.Fatalf("reordered backlog length = %d, want %d", len(got.Inputs), len(want))
		}
		for i, input := range got.Inputs {
			if input.ID != want[i] {
				t.Fatalf("reordered backlog position %d = %s, want %s", i, input.ID, want[i])
			}
		}
	}
	move(third.ID, executionstore.MoveQueuedBacklogInputBefore, second.ID)
	if firstRank := loadAgentInputRank(t, ctx, pool, first.ID); firstRank != backlogRankStride {
		t.Fatalf("unmoved first input rank = %d, want %d", firstRank, backlogRankStride)
	}
	wantMidpoint := 3 * backlogRankStride / 2
	if thirdRank := loadAgentInputRank(t, ctx, pool, third.ID); thirdRank != wantMidpoint {
		t.Fatalf("moved third input rank = %d, want midpoint %d", thirdRank, wantMidpoint)
	}
	if secondRank := loadAgentInputRank(t, ctx, pool, second.ID); secondRank != 2*backlogRankStride {
		t.Fatalf("unmoved second input rank = %d, want %d", secondRank, 2*backlogRankStride)
	}

	firstSteering := createInput("first-steering", executionstore.DeliveryModeSteering)
	secondSteering := createInput("second-steering", executionstore.DeliveryModeSteering)
	page1, err := store.Execution().ListQueuedBacklogInputs(
		ctx,
		executionstore.ListQueuedBacklogInputsInput{ProjectID: testProjectID, AgentID: agentID, Limit: 3},
	)
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if !page1.HasMore {
		t.Fatal("first page HasMore = false, want true")
	}
	if len(page1.Inputs) != 3 {
		t.Fatalf("first page length = %d, want 3", len(page1.Inputs))
	}
	if page1.Inputs[0].ID != firstSteering.ID ||
		page1.Inputs[1].ID != secondSteering.ID ||
		page1.Inputs[2].ID != first.ID {
		t.Fatalf("first page ids = %v, want steering inputs followed by %s", page1.Inputs, first.ID)
	}

	last := page1.Inputs[len(page1.Inputs)-1]
	page2, err := store.Execution().ListQueuedBacklogInputs(ctx, executionstore.ListQueuedBacklogInputsInput{
		ProjectID: testProjectID,
		AgentID:   agentID,
		Limit:     2,
		After: executionstore.AgentInputQueueCursor{
			Set:          true,
			DeliveryMode: last.DeliveryMode,
			InputRank:    last.InputRank,
			QueuedAt:     last.QueuedAt,
			ID:           last.ID,
		},
	})
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if page2.HasMore {
		t.Fatal("second page HasMore = true, want false")
	}
	if len(page2.Inputs) != 2 {
		t.Fatalf("second page length = %d, want 2", len(page2.Inputs))
	}
	if page2.Inputs[0].ID != third.ID || page2.Inputs[1].ID != second.ID {
		t.Fatalf("second page ids = %v, want [%s %s]", page2.Inputs, third.ID, second.ID)
	}

	move(second.ID, executionstore.MoveQueuedBacklogInputToFront, NilID)
	assertOrder(firstSteering.ID, secondSteering.ID, second.ID, first.ID, third.ID)
	move(second.ID, executionstore.MoveQueuedBacklogInputToBack, NilID)
	assertOrder(firstSteering.ID, secondSteering.ID, first.ID, third.ID, second.ID)
	move(first.ID, executionstore.MoveQueuedBacklogInputAfter, second.ID)
	assertOrder(firstSteering.ID, secondSteering.ID, third.ID, second.ID, first.ID)
}

func TestMoveQueuedBacklogInputRebalancesExhaustedRankGap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateProjectOperatorUser(t, ctx, store, "backlog-rebalance@example.com", "Backlog Rebalance")
	agentID := mustCreateAgent(t, ctx, store, time.Date(2026, 5, 18, 17, 0, 0, 0, time.UTC))
	createQueued := func(label string) executionstore.AgentInputRecord {
		input, _, _, err := store.Execution().CreateAgentContentInput(
			ctx,
			executionstore.CreateAgentContentInputInput{
				ProjectID:      testProjectID,
				AgentID:        agentID,
				Actor:          mustOmnaraActorParams(t, user.ID),
				ContentBlocks:  json.RawMessage(`[{"type":"text","text":"` + label + `"}]`),
				IdempotencyKey: "idem-backlog-rebalance-" + label,
			},
		)
		if err != nil {
			t.Fatalf("create %s input: %v", label, err)
		}
		return input
	}
	first := createQueued("first")
	second := createQueued("second")
	third := createQueued("third")
	if _, err := pool.Exec(ctx, `
UPDATE agent_inputs
SET input_rank = CASE id WHEN $1 THEN 1 WHEN $2 THEN 2 ELSE input_rank END
WHERE project_id = $3
  AND agent_id = $4
  AND id IN ($1, $2)
`, first.ID, second.ID, testProjectID, agentID); err != nil {
		t.Fatalf("exhaust rank gap: %v", err)
	}
	if err := store.Execution().MoveQueuedBacklogInput(ctx, executionstore.MoveQueuedBacklogInputInput{
		ProjectID:     testProjectID,
		AgentID:       agentID,
		InputID:       third.ID,
		Position:      executionstore.MoveQueuedBacklogInputBefore,
		AnchorInputID: testID("missing-backlog-anchor"),
	}); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("move before missing anchor error = %v, want state transition conflict", err)
	}
	if firstRank, secondRank := loadAgentInputRank(t, ctx, pool, first.ID),
		loadAgentInputRank(t, ctx, pool, second.ID); firstRank != 1 || secondRank != 2 {
		t.Fatalf("invalid move changed exhausted ranks to (%d, %d)", firstRank, secondRank)
	}

	if err := store.Execution().MoveQueuedBacklogInput(ctx, executionstore.MoveQueuedBacklogInputInput{
		ProjectID:     testProjectID,
		AgentID:       agentID,
		InputID:       third.ID,
		Position:      executionstore.MoveQueuedBacklogInputBefore,
		AnchorInputID: second.ID,
	}); err != nil {
		t.Fatalf("move input through exhausted rank gap: %v", err)
	}
	if firstRank := loadAgentInputRank(t, ctx, pool, first.ID); firstRank != backlogRankStride {
		t.Fatalf("rebalanced first input rank = %d, want %d", firstRank, backlogRankStride)
	}
	wantMidpoint := 3 * backlogRankStride / 2
	if thirdRank := loadAgentInputRank(t, ctx, pool, third.ID); thirdRank != wantMidpoint {
		t.Fatalf("moved third input rank = %d, want midpoint %d", thirdRank, wantMidpoint)
	}
	if secondRank := loadAgentInputRank(t, ctx, pool, second.ID); secondRank != 2*backlogRankStride {
		t.Fatalf("rebalanced second input rank = %d, want %d", secondRank, 2*backlogRankStride)
	}
}

func TestConcurrentQueuedBacklogMovesSerializePerAgent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateProjectOperatorUser(
		t,
		ctx,
		store,
		"backlog-concurrent-moves@example.com",
		"Backlog Concurrent Moves",
	)
	agentID := mustCreateAgent(t, ctx, store, time.Date(2026, 5, 18, 17, 0, 0, 0, time.UTC))
	createQueued := func(label string) executionstore.AgentInputRecord {
		input, _, _, err := store.Execution().CreateAgentContentInput(
			ctx,
			executionstore.CreateAgentContentInputInput{
				ProjectID:      testProjectID,
				AgentID:        agentID,
				Actor:          mustOmnaraActorParams(t, user.ID),
				ContentBlocks:  json.RawMessage(`[{"type":"text","text":"` + label + `"}]`),
				IdempotencyKey: "idem-backlog-concurrent-" + label,
			},
		)
		if err != nil {
			t.Fatalf("create %s input: %v", label, err)
		}
		return input
	}

	first := createQueued("first")
	second := createQueued("second")
	third := createQueued("third")
	fourth := createQueued("fourth")
	if _, err := pool.Exec(ctx, `
UPDATE agent_inputs
SET input_rank = CASE id WHEN $1 THEN 1 WHEN $2 THEN 2 ELSE input_rank END
WHERE project_id = $3
  AND agent_id = $4
  AND id IN ($1, $2)
`, first.ID, second.ID, testProjectID, agentID); err != nil {
		t.Fatalf("exhaust rank gap: %v", err)
	}

	blockingTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin blocking transaction: %v", err)
	}
	defer func() { _ = blockingTx.Rollback(ctx) }()
	if _, err := store.q.WithTx(blockingTx).LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: agentID},
	); err != nil {
		t.Fatalf("lock agent before concurrent moves: %v", err)
	}

	moveResults := make(chan error, 2)
	moveBeforeSecond := func(inputID ID) {
		moveResults <- store.Execution().MoveQueuedBacklogInput(
			context.Background(),
			executionstore.MoveQueuedBacklogInputInput{
				ProjectID:     testProjectID,
				AgentID:       agentID,
				InputID:       inputID,
				Position:      executionstore.MoveQueuedBacklogInputBefore,
				AnchorInputID: second.ID,
			},
		)
	}
	go moveBeforeSecond(third.ID)
	go moveBeforeSecond(fourth.ID)
	integrationdb.WaitForLockWaiters(t, ctx, pool, "FROM agents", 2)

	if err := blockingTx.Commit(ctx); err != nil {
		t.Fatalf("release agent lock: %v", err)
	}
	for range 2 {
		select {
		case err := <-moveResults:
			if err != nil {
				t.Fatalf("concurrent backlog move: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for concurrent backlog moves")
		}
	}

	backlog, err := store.Execution().ListQueuedBacklogInputs(
		ctx,
		executionstore.ListQueuedBacklogInputsInput{
			ProjectID: testProjectID,
			AgentID:   agentID,
			Limit:     4,
		},
	)
	if err != nil {
		t.Fatalf("list backlog after concurrent moves: %v", err)
	}
	if len(backlog.Inputs) != 4 {
		t.Fatalf("backlog length = %d, want 4", len(backlog.Inputs))
	}
	if backlog.Inputs[0].ID != first.ID || backlog.Inputs[3].ID != second.ID {
		t.Fatalf("concurrent move order = %v, want first input first and second input last", backlog.Inputs)
	}
	middleIsSerialOrder :=
		(backlog.Inputs[1].ID == third.ID && backlog.Inputs[2].ID == fourth.ID) ||
			(backlog.Inputs[1].ID == fourth.ID && backlog.Inputs[2].ID == third.ID)
	if !middleIsSerialOrder {
		t.Fatalf("concurrent move order = %v, want one of the two valid serial orders", backlog.Inputs)
	}
	for i := 1; i < len(backlog.Inputs); i++ {
		if backlog.Inputs[i-1].InputRank >= backlog.Inputs[i].InputRank {
			t.Fatalf("backlog ranks are not strictly increasing: %v", backlog.Inputs)
		}
	}
}

func loadAgentInputRank(t *testing.T, ctx context.Context, pool *pgxpool.Pool, inputID ID) int64 {
	t.Helper()
	var rank int64
	if err := pool.QueryRow(ctx, `SELECT input_rank FROM agent_inputs WHERE id = $1`, inputID).Scan(&rank); err != nil {
		t.Fatalf("load agent input rank: %v", err)
	}
	return rank
}

func TestCreateAgentContentInputOrdersQueueTimeAfterAgentLock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 7, 28, 13, 0, 0, 0, time.UTC)
	user := mustCreateProjectOperatorUser(
		t,
		ctx,
		store,
		"backlog-lock-order@example.com",
		"Backlog Lock Order")

	agentID := mustCreateAgent(t, ctx, store, now)
	actor := mustOmnaraActorParams(t, user.ID)

	waitingTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin waiting input transaction: %v", err)
	}
	defer func() { _ = waitingTx.Rollback(ctx) }()
	waitingAgent, err := executionstore.IntegrationLoadAgentTx(ctx, waitingTx, agentID)
	if err != nil {
		t.Fatalf("load waiting input agent: %v", err)
	}

	blockingTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin blocking input transaction: %v", err)
	}
	defer func() { _ = blockingTx.Rollback(ctx) }()
	blockingQueries := store.q.WithTx(blockingTx)
	if _, err := blockingQueries.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: agentID},
	); err != nil {
		t.Fatalf("lock agent for first input: %v", err)
	}
	var blockingPID int32
	if err := blockingTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockingPID); err != nil {
		t.Fatalf("get blocking input backend: %v", err)
	}

	type createResult struct {
		input executionstore.AgentInputRecord
		err   error
	}
	secondContent := json.RawMessage(`[{"type":"text","text":"second"}]`)
	secondBlocks, err := executionstore.IntegrationParseAgentInputContentBlocks(secondContent)
	if err != nil {
		t.Fatalf("parse second input content: %v", err)
	}
	waitingResult := make(chan createResult, 1)
	go func() {
		result, err := executionstore.IntegrationCreateAgentContentInputTx(
			context.Background(),
			notifications.NewTxNotifications(),
			waitingTx,
			store.q.WithTx(waitingTx),
			waitingAgent,
			executionstore.CreateAgentContentInputInput{
				ProjectID:      testProjectID,
				AgentID:        agentID,
				Actor:          actor,
				ContentBlocks:  secondContent,
				IdempotencyKey: "backlog-lock-order-second",
			},
			secondBlocks,
		)
		if err == nil {
			err = waitingTx.Commit(context.Background())
		}
		waitingResult <- createResult{input: result.AgentInput, err: err}
	}()
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, pool, "FROM agents", blockingPID)

	firstContent := json.RawMessage(`[{"type":"text","text":"first"}]`)
	firstBlocks, err := executionstore.IntegrationParseAgentInputContentBlocks(firstContent)
	if err != nil {
		t.Fatalf("parse first input content: %v", err)
	}
	first, err := executionstore.IntegrationCreateAgentContentInputTx(
		ctx,
		notifications.NewTxNotifications(),
		blockingTx,
		blockingQueries,
		waitingAgent,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      testProjectID,
			AgentID:        agentID,
			Actor:          actor,
			ContentBlocks:  firstContent,
			IdempotencyKey: "backlog-lock-order-first",
		},
		firstBlocks,
	)
	if err != nil {
		t.Fatalf("create first input: %v", err)
	}
	var releaseFloor time.Time
	if err := blockingTx.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&releaseFloor); err != nil {
		t.Fatalf("read input lock release floor: %v", err)
	}
	if err := blockingTx.Commit(ctx); err != nil {
		t.Fatalf("commit first input: %v", err)
	}

	second := <-waitingResult
	if second.err != nil {
		t.Fatalf("create second input after lock wait: %v", second.err)
	}
	if first.AgentInput.InputRank != 1024 || second.input.InputRank != 2048 {
		t.Fatalf(
			"input ranks = (%d, %d), want (1024, 2048)",
			first.AgentInput.InputRank,
			second.input.InputRank,
		)
	}
	if second.input.QueuedAt.Before(first.AgentInput.QueuedAt) ||
		second.input.QueuedAt.Before(releaseFloor) {
		t.Fatalf(
			"queue times = first %s second %s release %s",
			first.AgentInput.QueuedAt,
			second.input.QueuedAt,
			releaseFloor,
		)
	}
}
