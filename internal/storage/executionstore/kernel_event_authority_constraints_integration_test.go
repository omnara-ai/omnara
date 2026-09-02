//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestKernelEventLineageFieldsAreImmutable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "kernel_event_lineage_immutable")
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"kernel_event_lineage_immutable",
		[]processToolCallBatchItem{
			builtInProcessToolCallBatchItem("kernel_event_lineage_immutable", "read_process"),
			builtInProcessToolCallBatchItem("kernel_event_lineage_immutable_interaction", "ask_question"),
		},
	)
	toolCallID, interactionToolCallID := toolCallIDs[0], toolCallIDs[1]
	turnID := turnIDForProcessToolCallTest(t, ctx, fixture, toolCallID)
	modelContextID := modelContextIDForProcessToolCallTest(t, ctx, fixture, toolCallID)
	modelOutputID := modelOutputIDForToolCall(t, ctx, fixture.Store, fixture.AgentID, toolCallID)
	interaction := createQuestionInteractionForTest(
		t,
		ctx,
		fixture,
		interactionToolCallID,
	)
	if _, err := fixture.Store.pool.Exec(ctx, `
INSERT INTO agent_interactions(
  agent_id, tool_call_id, interaction_kind, state,
  request, resolution, created_at, resolved_at
)
VALUES ($1, $2, 'permission', 'resolved', '{}', '{}', statement_timestamp(), statement_timestamp())
`, fixture.AgentID, interactionToolCallID); !isPgCheckViolation(err) {
		t.Fatalf("insert terminal interaction error = %v, want check violation", err)
	}

	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE model_call_contexts
SET input_event_sequence = input_event_sequence + 1
WHERE project_id = $1 AND agent_id = $2 AND id = $3
`, testProjectID, fixture.AgentID, modelContextID); !isPgReadOnlySQLTransaction(err) {
		t.Fatalf("mutate model context lineage error = %v, want SQLSTATE 25006", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE agent_turns
SET turn_sequence = turn_sequence + 1000
WHERE agent_id = $1 AND id = $2
`, fixture.AgentID, turnID); !isPgReadOnlySQLTransaction(err) {
		t.Fatalf("mutate agent turn lineage error = %v, want SQLSTATE 25006", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
	UPDATE model_outputs
	SET stop_reason = 'refusal'
	WHERE agent_id = $1 AND id = $2
	`, fixture.AgentID, modelOutputID); !isPgReadOnlySQLTransaction(err) {
		t.Fatalf("mutate model output error = %v, want SQLSTATE 25006", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
	DELETE FROM model_outputs
	WHERE agent_id = $1 AND id = $2
	`, fixture.AgentID, modelOutputID); !isPgReadOnlySQLTransaction(err) {
		t.Fatalf("delete model output error = %v, want SQLSTATE 25006", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE tool_calls
SET model_output_id = gen_random_uuid()
WHERE agent_id = $1 AND id = $2
`, fixture.AgentID, toolCallID); !isPgReadOnlySQLTransaction(err) {
		t.Fatalf("mutate tool call lineage error = %v, want SQLSTATE 25006", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE tool_calls
SET state = 'awaiting_permission'
WHERE agent_id = $1 AND id = $2
`, fixture.AgentID, toolCallID); !isPgCheckViolation(err) {
		t.Fatalf("illegal tool call transition error = %v, want check violation", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE agent_interactions
SET tool_call_id = gen_random_uuid()
WHERE agent_id = $1 AND id = $2
`, fixture.AgentID, interaction.ID); !isPgReadOnlySQLTransaction(err) {
		t.Fatalf("mutate interaction lineage error = %v, want SQLSTATE 25006", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE agent_interactions
SET state = 'canceled', resolution = '{"reason":"test"}', resolved_at = statement_timestamp()
WHERE agent_id = $1 AND id = $2
`, fixture.AgentID, interaction.ID); err != nil {
		t.Fatalf("cancel interaction for terminal immutability test: %v", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE agent_interactions
SET state = 'open', resolution = '{}', resolved_at = NULL
WHERE agent_id = $1 AND id = $2
`, fixture.AgentID, interaction.ID); !isPgReadOnlySQLTransaction(err) {
		t.Fatalf("reopen terminal interaction error = %v, want SQLSTATE 25006", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
DELETE FROM agent_interactions
WHERE agent_id = $1 AND id = $2
`, fixture.AgentID, interaction.ID); !isPgReadOnlySQLTransaction(err) {
		t.Fatalf("delete interaction error = %v, want SQLSTATE 25006", err)
	}
}

func TestKernelToolCallTransitionGraph(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "kernel_tool_call_transition_graph")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"kernel_tool_call_transition_graph",
		"read_process",
	)
	modelOutputID := modelOutputIDForToolCall(t, ctx, fixture.Store, fixture.AgentID, toolCallID)

	paths := map[string][]string{
		"awaiting_authorization": nil,
		"awaiting_permission":    {"awaiting_permission"},
		"ready":                  {"ready"},
		"running":                {"ready", "running"},
		"waiting":                {"ready", "waiting"},
	}
	transitions := []struct {
		from string
		to   string
	}{
		{from: "awaiting_authorization", to: "awaiting_permission"},
		{from: "awaiting_authorization", to: "ready"},
		{from: "awaiting_authorization", to: "completed"},
		{from: "awaiting_permission", to: "ready"},
		{from: "awaiting_permission", to: "completed"},
		{from: "ready", to: "running"},
		{from: "ready", to: "waiting"},
		{from: "ready", to: "completed"},
		{from: "running", to: "ready"},
		{from: "running", to: "waiting"},
		{from: "running", to: "completed"},
		{from: "waiting", to: "completed"},
	}

	for _, transition := range transitions {
		t.Run(transition.from+"_to_"+transition.to, func(t *testing.T) {
			tx, err := fixture.Store.pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin transition transaction: %v", err)
			}
			defer func() { _ = tx.Rollback(ctx) }()

			var id ID
			if err := tx.QueryRow(ctx, `
INSERT INTO tool_calls(
  agent_id, model_output_id, provider_call_id,
  name, input, type, state, created_at
)
VALUES ($1, $2, $3, 'read_process', '{}', 'built_in', 'awaiting_authorization', statement_timestamp())
RETURNING id
`, fixture.AgentID, modelOutputID, "graph_"+transition.from+"_"+transition.to).Scan(&id); err != nil {
				t.Fatalf("insert transition tool call: %v", err)
			}

			advance := func(state string) {
				t.Helper()
				result, err := tx.Exec(ctx, `
UPDATE tool_calls
SET state = $3,
    runtime_lock_id = CASE WHEN $3 = 'running' THEN $4::uuid ELSE NULL::uuid END
WHERE agent_id = $1 AND id = $2
`, fixture.AgentID, id, state, fixture.Lock.ID)
				if err != nil {
					t.Fatalf("transition tool call to %s: %v", state, err)
				}
				if result.RowsAffected() != 1 {
					t.Fatalf("transition tool call to %s affected %d rows, want 1", state, result.RowsAffected())
				}
			}
			for _, state := range paths[transition.from] {
				advance(state)
			}
			advance(transition.to)

			var state string
			var hasRuntimeOwner bool
			if err := tx.QueryRow(ctx, `
SELECT state, runtime_lock_id IS NOT NULL
FROM tool_calls
WHERE agent_id = $1 AND id = $2
`, fixture.AgentID, id).Scan(&state, &hasRuntimeOwner); err != nil {
				t.Fatalf("load transitioned tool call: %v", err)
			}
			if state != transition.to || hasRuntimeOwner != (transition.to == "running") {
				t.Fatalf(
					"transitioned tool call state=%s has_runtime_owner=%v, want state=%s has_runtime_owner=%v",
					state,
					hasRuntimeOwner,
					transition.to,
					transition.to == "running",
				)
			}
		})
	}

	if _, err := fixture.Store.pool.Exec(ctx, `
INSERT INTO tool_calls(
  agent_id, model_output_id, provider_call_id,
  name, input, type, state, created_at
)
VALUES ($1, $2, 'graph_invalid_initial_state', 'read_process', '{}', 'built_in', 'ready', statement_timestamp())
`, fixture.AgentID, modelOutputID); !isPgCheckViolation(err) {
		t.Fatalf("invalid initial tool call state error = %v, want check violation", err)
	}

	claimToolCallForTest(
		t,
		ctx,
		fixture.Store,
		fixture.AgentID,
		toolCallID,
		fixture.Lock.ID,
		true,
	)
	var incomplete bool
	if err := fixture.Store.pool.QueryRow(
		ctx,
		`SELECT agent_has_incomplete_tool_batch($1, $2)`,
		testProjectID,
		fixture.AgentID,
	).Scan(&incomplete); err != nil || !incomplete {
		t.Fatalf("running tool batch incomplete = %v, error = %v, want true", incomplete, err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
DELETE FROM agent_runtime_locks
WHERE agent_id = $1 AND id = $2
`, fixture.AgentID, fixture.Lock.ID); !isPgForeignKeyViolation(err) {
		t.Fatalf("delete owned runtime lock error = %v, want foreign key violation", err)
	}
	otherFixture := newProcessDaemonFixture(t, ctx, "kernel_tool_call_transition_graph_other_agent")
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE tool_calls
SET runtime_lock_id = $3
WHERE agent_id = $1 AND id = $2
`, fixture.AgentID, toolCallID, otherFixture.Lock.ID); !isPgCheckViolation(err) {
		t.Fatalf("assign another agent's runtime lock error = %v, want check violation", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE tool_calls
SET runtime_lock_id = gen_random_uuid()
WHERE agent_id = $1 AND id = $2
`, fixture.AgentID, toolCallID); !isPgCheckViolation(err) {
		t.Fatalf("replace running tool call owner error = %v, want check violation", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	); err != nil {
		t.Fatalf("release runtime lock with running tool call: %v", err)
	}
	if err := fixture.Store.pool.QueryRow(
		ctx,
		`SELECT agent_has_incomplete_tool_batch($1, $2)`,
		testProjectID,
		fixture.AgentID,
	).Scan(&incomplete); err != nil || incomplete {
		t.Fatalf("completed tool batch incomplete = %v, error = %v, want false", incomplete, err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE tool_calls
SET state = 'ready'
WHERE agent_id = $1 AND id = $2
`, fixture.AgentID, toolCallID); !isPgReadOnlySQLTransaction(err) {
		t.Fatalf("transition completed tool call error = %v, want SQLSTATE 25006", err)
	}
}

func TestKernelAgentInputEventRequiresResolvedBacklink(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "kernel_agent_input_event_backlink")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"kernel_agent_input_event_backlink",
		"read_process",
	)
	turnID := turnIDForProcessToolCallTest(t, ctx, fixture, toolCallID)

	tx, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin orphan agent-input event tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var inputID ID
	if err := tx.QueryRow(ctx, `
INSERT INTO agent_inputs(
  project_id, agent_id, state, input_kind, delivery_mode,
  control_type, queued_at
)
VALUES ($1, $2, 'received', 'control', 'immediate', 'cancel_current', statement_timestamp())
RETURNING id
`, testProjectID, fixture.AgentID).Scan(&inputID); err != nil {
		t.Fatalf("insert unresolved agent input: %v", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO agent_events(
  agent_id, turn_id, sequence, event_kind,
  idempotency_key, agent_input_id, created_at
)
SELECT id, $2, next_event_sequence, 'agent_input', $3, $4, statement_timestamp()
FROM agents
WHERE project_id = $1 AND id = $5
`, testProjectID, turnID, "agent_input:"+inputID.String(), inputID, fixture.AgentID); err != nil {
		t.Fatalf("insert orphan agent-input event: %v", err)
	}
	if _, err := tx.Exec(ctx, `SET CONSTRAINTS agent_input_events_admission_valid IMMEDIATE`); !isPgCheckViolation(err) {
		t.Fatalf("orphan agent-input event error = %v, want check violation", err)
	}
}

func TestKernelModelOutputEventMembershipMustMatchContextTurn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "kernel_model_output_turn_boundary")
	firstToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"kernel_model_output_turn_boundary_first",
		"read_process",
	)
	firstTurnID := settleToolCallTurnForNextInputTest(
		t,
		ctx,
		fixture,
		firstToolCallID,
		"kernel_model_output_turn_boundary_first",
		fixture.Now.Add(11*time.Second),
	)
	secondFixture := fixture
	secondFixture.Now = fixture.Now.Add(2 * time.Second)
	secondToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		secondFixture,
		"kernel_model_output_turn_boundary_second",
		"read_process",
	)
	secondTurnID := turnIDForProcessToolCallTest(t, ctx, fixture, secondToolCallID)
	secondContextID := modelOutputContextForTurnTest(
		t,
		ctx,
		fixture,
		secondTurnID,
		"kernel_model_output_turn_boundary",
		fixture.Now.Add(2*time.Minute),
	)
	providerModelSlug := modelProviderSlugForContext(
		t, ctx, fixture.Store, testProjectID, fixture.AgentID, secondContextID,
	)
	tx, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin cross-turn model output tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	secondModelOutput, err := executionstore.IntegrationCreateModelOutputAuthorityTx(
		ctx,
		tx,
		executionstore.CreateModelOutputAuthorityInput{
			ProjectID:               testProjectID,
			AgentID:                 fixture.AgentID,
			ModelCallContextID:      secondContextID,
			ServedProviderModelSlug: providerModelSlug,
			StopReason:              "end_turn",
		},
	)
	if err != nil {
		t.Fatalf("create cross-turn model output: %v", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO agent_events(id, agent_id, turn_id, sequence, event_kind, model_output_id, created_at)
VALUES (uuidv7(), $1, $2, (SELECT next_event_sequence FROM agents WHERE id = $1), 'model_output', $3, now())
`, fixture.AgentID, firstTurnID, secondModelOutput.ID); !isPgCheckViolation(err) {
		t.Fatalf("cross-turn model output membership error = %v, want check violation", err)
	}
}

func TestKernelTurnOpeningEventMustBeAgentInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "kernel_turn_opening_event_kind")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "kernel_turn_opening_event_kind", "read_process")
	turnID := turnIDForProcessToolCallTest(t, ctx, fixture, toolCallID)
	contextID := modelOutputContextForTurnTest(
		t,
		ctx,
		fixture,
		turnID,
		"kernel_turn_opening_event_kind",
		fixture.Now.Add(2*time.Minute),
	)
	providerModelSlug := modelProviderSlugForContext(
		t, ctx, fixture.Store, testProjectID, fixture.AgentID, contextID,
	)
	tx, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin model output opening event tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	modelOutput, err := executionstore.IntegrationCreateModelOutputAuthorityTx(
		ctx,
		tx,
		executionstore.CreateModelOutputAuthorityInput{
			ProjectID:               testProjectID,
			AgentID:                 fixture.AgentID,
			ModelCallContextID:      contextID,
			ServedProviderModelSlug: providerModelSlug,
			StopReason:              "end_turn",
		},
	)
	if err != nil {
		t.Fatalf("create model output opening event fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO agent_events(id, agent_id, turn_id, sequence, event_kind, model_output_id, is_opening_event, created_at)
VALUES (uuidv7(), $1, $2, (SELECT next_event_sequence FROM agents WHERE id = $1), 'model_output', $3, true, now())
`, fixture.AgentID, turnID, modelOutput.ID); !isPgCheckViolation(err) {
		t.Fatalf("model output opening event error = %v, want check violation", err)
	}
}

func TestKernelToolResultEventMembershipMustMatchToolCallTurn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "kernel_tool_result_turn_boundary")
	bridgeToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"kernel_tool_result_turn_boundary_first",
		"read_process",
	)
	firstTurnID := settleToolCallTurnForNextInputTest(
		t,
		ctx,
		fixture,
		bridgeToolCallID,
		"kernel_tool_result_turn_boundary_first",
		fixture.Now.Add(11*time.Second),
	)
	secondFixture := fixture
	secondFixture.Now = fixture.Now.Add(2 * time.Second)
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		secondFixture,
		"kernel_tool_result_turn_boundary_second",
		"read_process",
	)
	now := fixture.Now.Add(time.Minute)
	tx, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tool result boundary tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
UPDATE tool_calls
SET state = 'completed',
    runtime_lock_id = NULL
WHERE agent_id = $1 AND id = $2
`, fixture.AgentID, toolCallID); err != nil {
		t.Fatalf("stage completed tool call: %v", err)
	}
	var resultID ID
	if err := tx.QueryRow(ctx, `
INSERT INTO tool_call_results(agent_id, tool_call_id, outcome, completed_at)
SELECT tool_call.agent_id, tool_call.id, 'succeeded', $3
FROM tool_calls tool_call
WHERE tool_call.agent_id = $1 AND tool_call.id = $2
RETURNING id
`, fixture.AgentID, toolCallID, now).Scan(&resultID); err != nil {
		t.Fatalf("insert tool result authority: %v", err)
	}

	if _, err := tx.Exec(ctx, `
INSERT INTO agent_events(id, agent_id, turn_id, sequence, event_kind, tool_call_result_id, created_at)
VALUES (uuidv7(), $1, $2, (SELECT next_event_sequence FROM agents WHERE id = $1), 'tool_result', $3, now())
`, fixture.AgentID, firstTurnID, resultID); !isPgCheckViolation(err) {
		t.Fatalf("cross-turn tool result membership error = %v, want check violation", err)
	}
}

func settleToolCallTurnForNextInputTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	toolCallID ID,
	testName string,
	now time.Time,
) ID {
	t.Helper()
	turnID := turnIDForProcessToolCallTest(t, ctx, fixture, toolCallID)
	contextID := modelOutputContextForTurnTest(t, ctx, fixture, turnID, testName, now)
	createModelOutputEventForTurnTest(
		t,
		ctx,
		fixture,
		turnID,
		contextID,
		testName+"-end-turn",
		string(modelenvelope.StopReasonEndTurn),
		"",
		now.Add(time.Millisecond),
	)
	return turnID
}

func TestKernelInteractionDerivesToolCallTurn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "kernel_interaction_turn_boundary")
	firstToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"kernel_interaction_turn_boundary_first",
		"ask_question",
	)
	firstTurnID := turnIDForProcessToolCallTest(t, ctx, fixture, firstToolCallID)
	interaction := createQuestionInteractionForTest(t, ctx, fixture, firstToolCallID)
	if interaction.TurnID != firstTurnID {
		t.Fatalf("interaction turn = %s, want %s", interaction.TurnID, firstTurnID)
	}
}

func TestKernelContentBlockOwnerAndOrdinalConstraints(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "kernel_content_block_owner_ordinal")
	now := fixture.Now.Add(time.Minute)
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "kernel_content_block_owner_ordinal", "read_process")
	modelOutputID := modelOutputIDForToolCall(t, ctx, fixture.Store, fixture.AgentID, toolCallID)
	for index, test := range []struct {
		name  string
		input executionstore.CreateContentBlockInput
	}{
		{
			name: "text",
			input: executionstore.CreateContentBlockInput{
				BlockKind:   executionstore.ContentBlockKindText,
				TextContent: "before\x00after",
			},
		},
		{
			name: "structured data",
			input: executionstore.CreateContentBlockInput{
				BlockKind:      executionstore.ContentBlockKindStructuredData,
				StructuredData: json.RawMessage(`{"value":"before\u0000after"}`),
			},
		},
		{
			name: "metadata",
			input: executionstore.CreateContentBlockInput{
				BlockKind:   executionstore.ContentBlockKindText,
				TextContent: "safe",
				Metadata:    map[string]string{"key": "before\x00after"},
			},
		},
	} {
		t.Run("database unsafe "+test.name, func(t *testing.T) {
			test.input.ProjectID = testProjectID
			test.input.AgentID = fixture.AgentID
			test.input.OwnerKind = executionstore.ContentBlockOwnerModelOutput
			test.input.OwnerModelOutputID = modelOutputID
			test.input.Ordinal = int32(index + 10)
			_, err := executionstore.IntegrationCreateContentBlockTx(ctx, fixture.Store.pool, test.input)
			if err == nil || !strings.Contains(err.Error(), "U+0000") {
				t.Fatalf("create database-unsafe content block error = %v, want U+0000", err)
			}
		})
	}

	var block executionstore.ContentBlockRecord
	var blockKind string
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT block.id, block.owner_model_output_id, block.ordinal, block.block_kind
FROM content_blocks block
JOIN agents agent ON agent.id = block.agent_id
WHERE agent.project_id = $1
  AND block.agent_id = $2
  AND block.owner_model_output_id = $3
  AND ordinal = 0
`, testProjectID, fixture.AgentID, modelOutputID).Scan(
		&block.ID,
		&block.OwnerModelOutputID,
		&block.Ordinal,
		&blockKind,
	); err != nil {
		t.Fatalf("load model output content block: %v", err)
	}
	block.BlockKind = executionstore.ContentBlockKind(blockKind)
	if block.OwnerModelOutputID != modelOutputID || block.Ordinal != 0 || block.BlockKind != executionstore.ContentBlockKindToolCall {
		t.Fatalf("content block = %+v", block)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE content_blocks
SET text_content = 'mutated'
WHERE agent_id = $1 AND id = $2
`, fixture.AgentID, block.ID); err == nil {
		t.Fatalf("mutating content block unexpectedly succeeded")
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
DELETE FROM content_blocks
WHERE agent_id = $1 AND id = $2
`, fixture.AgentID, block.ID); err == nil {
		t.Fatalf("deleting content block unexpectedly succeeded")
	}

	if _, err := executionstore.IntegrationCreateContentBlockTx(ctx, fixture.Store.pool, executionstore.CreateContentBlockInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		OwnerKind:          executionstore.ContentBlockOwnerModelOutput,
		OwnerModelOutputID: modelOutputID,
		Ordinal:            0,
		BlockKind:          executionstore.ContentBlockKindText,
		TextContent:        "duplicate",
	}); !isPgConstraintViolation(err) {
		t.Fatalf("duplicate content block ordinal error = %v, want constraint violation", err)
	}

	if _, err := executionstore.IntegrationCreateContentBlockTx(ctx, fixture.Store.pool, executionstore.CreateContentBlockInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		OwnerKind:          executionstore.ContentBlockOwnerModelOutput,
		OwnerModelOutputID: modelOutputID,
		Ordinal:            1,
		BlockKind:          executionstore.ContentBlockKindArtifact,
		TextContent:        "not an artifact",
	}); !isPgConstraintViolation(err) {
		t.Fatalf("invalid artifact block error = %v, want constraint violation", err)
	}
	if _, err := executionstore.IntegrationCreateContentBlockTx(ctx, fixture.Store.pool, executionstore.CreateContentBlockInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		OwnerKind:          executionstore.ContentBlockOwnerToolCallResult,
		OwnerModelOutputID: modelOutputID,
		Ordinal:            2,
		BlockKind:          executionstore.ContentBlockKindReasoning,
		TextContent:        "invalid owner/kind",
	}); !isPgConstraintViolation(err) {
		t.Fatalf("invalid owner/block kind error = %v, want constraint violation", err)
	}

	contentInputID := testID("content_block_agent_input_owner")
	actorID := fixture.omnaraActorID(t, ctx)
	if _, err := fixture.Store.pool.Exec(ctx, `
		INSERT INTO agent_inputs(id, project_id, agent_id, state, delivery_mode, actor_id, input_kind, queued_at, metadata)
		VALUES ($1, $2, $3, 'received', 'queued', $4, 'content', $5, '{}'::jsonb)
	`, contentInputID, testProjectID, fixture.AgentID, actorID, now); err != nil {
		t.Fatalf("insert content-bearing agent input: %v", err)
	}
	if _, err := executionstore.IntegrationCreateContentBlockTx(ctx, fixture.Store.pool, executionstore.CreateContentBlockInput{
		ProjectID:         testProjectID,
		AgentID:           fixture.AgentID,
		OwnerKind:         executionstore.ContentBlockOwnerAgentInput,
		OwnerAgentInputID: contentInputID,
		Ordinal:           0,
		BlockKind:         executionstore.ContentBlockKindText,
		TextContent:       "hello",
	}); err != nil {
		t.Fatalf("create agent input content block: %v", err)
	}
	configInputID := testID("content_block_config_input_owner")
	var currentConfigID ID
	if err := fixture.Store.pool.QueryRow(ctx, `
		SELECT current_config_id
		FROM agents
		WHERE project_id = $1 AND id = $2
	`, testProjectID, fixture.AgentID).Scan(&currentConfigID); err != nil {
		t.Fatalf("load current agent config: %v", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
		INSERT INTO agent_inputs(
			id, project_id, agent_id, state, delivery_mode,
			actor_id, input_kind, agent_config_id, queued_at, metadata
		)
		VALUES ($1, $2, $3, 'received', 'immediate', $4, 'config_change', $5, $6, '{}'::jsonb)
	`, configInputID, testProjectID, fixture.AgentID, actorID, currentConfigID, now); err != nil {
		t.Fatalf("insert config-change agent input: %v", err)
	}
	if _, err := executionstore.IntegrationCreateContentBlockTx(ctx, fixture.Store.pool, executionstore.CreateContentBlockInput{
		ProjectID:         testProjectID,
		AgentID:           fixture.AgentID,
		OwnerKind:         executionstore.ContentBlockOwnerAgentInput,
		OwnerAgentInputID: configInputID,
		Ordinal:           0,
		BlockKind:         executionstore.ContentBlockKindText,
		TextContent:       "must not be accepted",
	}); !isPgConstraintViolation(err) {
		t.Fatalf("config-change content block error = %v, want constraint violation", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
		UPDATE agent_inputs
		SET input_kind = 'control',
		    delivery_mode = 'immediate',
		    control_type = 'cancel_current'
		WHERE project_id = $1 AND agent_id = $2 AND id = $3
	`, testProjectID, fixture.AgentID, contentInputID); !isPgCode(err, "25006") {
		t.Fatalf("mutating content-bearing input subtype error = %v, want immutable-event SQLSTATE 25006", err)
	}
}

func TestAgentInputContentBlockInsertSerializesWithClosure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "agent_input_content_block_owner_lock")
	inputID := testID("agent-input-content-block-owner-lock")
	actorID := fixture.omnaraActorID(t, ctx)
	if _, err := fixture.Store.pool.Exec(ctx, `
INSERT INTO agent_inputs(
  id, project_id, agent_id, state, delivery_mode,
  actor_id, input_kind, queued_at, metadata
)
VALUES ($1, $2, $3, 'received', 'queued', $4, 'content', statement_timestamp(), '{}'::jsonb)
`, inputID, testProjectID, fixture.AgentID, actorID); err != nil {
		t.Fatalf("insert content input for owner lock: %v", err)
	}

	closureTx, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin agent input closure: %v", err)
	}
	defer func() { _ = closureTx.Rollback(ctx) }()
	var closurePID int32
	if err := closureTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&closurePID); err != nil {
		t.Fatalf("get agent input closure backend: %v", err)
	}
	if _, err := closureTx.Exec(ctx, `
UPDATE agent_inputs
SET state = 'canceled', canceled_at = statement_timestamp()
WHERE project_id = $1 AND agent_id = $2 AND id = $3
`, testProjectID, fixture.AgentID, inputID); err != nil {
		t.Fatalf("stage agent input closure: %v", err)
	}

	insertDone := make(chan error, 1)
	go func() {
		_, insertErr := fixture.Store.pool.Exec(context.Background(), `
-- agent_input_content_block_owner_lock
INSERT INTO content_blocks(
  agent_id, owner_kind, owner_agent_input_id,
  ordinal, block_kind, text_content, created_at
)
VALUES ($1, 'agent_input', $2, 0, 'text', 'concurrent block', statement_timestamp())
`, fixture.AgentID, inputID)
		insertDone <- insertErr
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		fixture.Store.pool,
		"agent_input_content_block_owner_lock",
		closurePID,
	)
	if err := closureTx.Commit(ctx); err != nil {
		t.Fatalf("commit agent input closure: %v", err)
	}
	if err := <-insertDone; !isPgCheckViolation(err) {
		t.Fatalf("content block after concurrent closure error = %v, want check violation", err)
	}
}

func TestModelOutputContentBlockInsertSerializesWithContextClosure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "model_output_content_block_owner_lock")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"model_output_content_block_owner_lock",
		"read_process",
	)
	turnID := turnIDForProcessToolCallTest(t, ctx, fixture, toolCallID)
	contextID := modelOutputContextForTurnTest(
		t,
		ctx,
		fixture,
		turnID,
		"model_output_content_block_owner_lock",
		fixture.Now.Add(2*time.Minute),
	)
	providerModelSlug := modelProviderSlugForContext(
		t,
		ctx,
		fixture.Store,
		testProjectID,
		fixture.AgentID,
		contextID,
	)

	blockTx, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin model output block transaction: %v", err)
	}
	defer func() { _ = blockTx.Rollback(ctx) }()
	var blockPID int32
	if err := blockTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockPID); err != nil {
		t.Fatalf("get model output block backend: %v", err)
	}
	output, err := executionstore.IntegrationCreateModelOutputAuthorityTx(ctx, blockTx, executionstore.CreateModelOutputAuthorityInput{
		ProjectID:               testProjectID,
		AgentID:                 fixture.AgentID,
		ModelCallContextID:      contextID,
		ServedProviderModelSlug: providerModelSlug,
		StopReason:              "end_turn",
	})
	if err != nil {
		t.Fatalf("create model output for owner lock: %v", err)
	}
	if _, err := executionstore.IntegrationCreateContentBlockTx(ctx, blockTx, executionstore.CreateContentBlockInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		OwnerKind:          executionstore.ContentBlockOwnerModelOutput,
		OwnerModelOutputID: output.ID,
		Ordinal:            0,
		BlockKind:          executionstore.ContentBlockKindText,
		TextContent:        "concurrent model output block",
	}); err != nil {
		t.Fatalf("create model output block for owner lock: %v", err)
	}

	closureDone := make(chan error, 1)
	go func() {
		_, closureErr := fixture.Store.pool.Exec(context.Background(), `
-- model_output_content_block_owner_lock
UPDATE model_call_contexts
SET state = 'canceled',
    error_kind = 'canceled',
    error_code = 'content_block_owner_lock',
    error_message = 'test cancellation',
    completed_at = statement_timestamp()
WHERE project_id = $1 AND agent_id = $2 AND id = $3 AND state = 'started'
`, testProjectID, fixture.AgentID, contextID)
		closureDone <- closureErr
	}()
	integrationdb.WaitForLockWaitBlockedBy(
		t,
		ctx,
		fixture.Store.pool,
		"model_output_content_block_owner_lock",
		blockPID,
	)
	if err := blockTx.Rollback(ctx); err != nil {
		t.Fatalf("release model output block transaction: %v", err)
	}
	if err := <-closureDone; err != nil {
		t.Fatalf("close model context after block rollback: %v", err)
	}
}

func TestKernelTypedFrontierAddAgentEventOnlyOnRealInsert(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	agentID := mustCreateAgent(t, ctx, store, now)
	user := mustCreateProjectOperatorUser(t, ctx, store, "wakeup-idem@example.com", "Wakeup Idempotency")
	input, _, _, err := store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        agentID,
		Actor:          mustOmnaraActorParams(t, user.ID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"wakeup test"}]`),
		IdempotencyKey: "wakeup-idem-input",
	})
	if err != nil {
		t.Fatalf("create input: %v", err)
	}

	var turnID ID
	if err := pool.QueryRow(ctx, `SELECT event.turn_id FROM agent_events event JOIN agents agent ON agent.id = event.agent_id WHERE agent.project_id = $1 AND event.agent_id = $2 ORDER BY event.sequence DESC LIMIT 1`, testProjectID, agentID).Scan(&turnID); err != nil {
		t.Fatalf("load existing turn id: %v", err)
	}

	countAgentEvents := func(txNotifications *notifications.TxNotifications) int {
		recorder := &recordingPostCommitPublisher{}
		txNotifications.Flush(ctx, recorder)
		count := 0
		for _, intent := range recorder.intents {
			event, ok := intent.(notifications.AgentEventCommitted)
			if !ok {
				continue
			}
			if event.AgentID == agentID {
				count++
			}
		}
		return count
	}

	txNotifications := notifications.NewTxNotifications()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := executionstore.IntegrationAppendTypedAgentEventTx(ctx, txNotifications, tx, executionstore.AppendTypedAgentEventInput{
		ProjectID:      testProjectID,
		AgentID:        agentID,
		TurnID:         turnID,
		Kind:           events.KindAgentInput,
		IdempotencyKey: "agent_input:" + input.ID.String(),
		AgentInputID:   input.ID,
	}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if got := countAgentEvents(txNotifications); got != 1 {
		t.Fatalf("AgentEventCommitted intents after real INSERT = %d, want 1", got)
	}

	txn2 := notifications.NewTxNotifications()
	if _, err := executionstore.IntegrationAppendTypedAgentEventTx(ctx, txn2, tx, executionstore.AppendTypedAgentEventInput{
		ProjectID:      testProjectID,
		AgentID:        agentID,
		TurnID:         turnID,
		Kind:           events.KindAgentInput,
		IdempotencyKey: "agent_input:" + input.ID.String(),
		AgentInputID:   input.ID,
	}); err != nil {
		t.Fatalf("second append (pointer re-find): %v", err)
	}
	if got := countAgentEvents(txn2); got != 0 {
		t.Fatalf("AgentEventCommitted intents on idempotent pointer re-find = %d, want 0", got)
	}
}

func TestKernelTypedFrontierRejectsMismatchedPointer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 24, 13, 0, 0, 0, time.UTC)
	agentID := mustCreateAgent(t, ctx, store, now)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin typed event validation tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = executionstore.IntegrationAppendTypedAgentEventTx(ctx, notifications.NewTxNotifications(), tx, executionstore.AppendTypedAgentEventInput{
		ProjectID:      testProjectID,
		AgentID:        agentID,
		TurnID:         testID("turn_bad_model_output_pointer"),
		Kind:           events.KindModelOutput,
		IdempotencyKey: "bad-model-output-pointer",
		AgentInputID:   testID("not_a_model_output"),
	})
	if err == nil || err.Error() != "model_output event requires model_output_id" {
		t.Fatalf("mismatched typed event error = %v, want model output validation error", err)
	}
}

func TestKernelTypedFrontierRequiresTurnMembership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 5, 24, 13, 30, 0, 0, time.UTC)
	agentID := mustCreateAgent(t, ctx, store, now)
	user := mustCreateProjectOperatorUser(t, ctx, store, "orphan-event@example.com", "Orphan Event")
	input, _, _, err := store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        agentID,
		Actor:          mustOmnaraActorParams(t, user.ID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"orphan"}]`),
		IdempotencyKey: "orphan-event-input",
	})
	if err != nil {
		t.Fatalf("create input for orphan event: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin orphan typed event tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = executionstore.IntegrationAppendTypedAgentEventTx(ctx, notifications.NewTxNotifications(), tx, executionstore.AppendTypedAgentEventInput{
		ProjectID:      testProjectID,
		AgentID:        agentID,
		TurnID:         testID("turn_orphan_event"),
		IsOpeningEvent: true,
		Kind:           events.KindAgentInput,
		IdempotencyKey: "agent_input:" + input.ID.String(),
		AgentInputID:   input.ID,
	})
	if err == nil {
		err = tx.Commit(ctx)
	}
	if !isPgConstraintViolation(err) {
		t.Fatalf("standalone typed event error = %v, want turn-membership constraint violation", err)
	}
}
