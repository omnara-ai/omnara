//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func listTypedToolResultEventsForToolCall(
	t *testing.T,
	ctx context.Context,
	store *Store,
	agentID, toolCallID ID,
) []events.Event {
	t.Helper()
	rows, err := store.pool.Query(ctx, `
SELECT event.id, event.agent_id, event.sequence, event.event_kind, event.created_at, coalesce(event.idempotency_key, '')
FROM agent_events event
JOIN tool_call_results result ON result.agent_id = event.agent_id
  AND result.id = event.tool_call_result_id
JOIN tool_call_read_projection tool_call
  ON tool_call.agent_id = result.agent_id
 AND tool_call.id = result.tool_call_id
WHERE tool_call.project_id = $1
  AND event.agent_id = $2
  AND result.tool_call_id = $3
  AND event.event_kind = 'tool_result'
ORDER BY event.sequence`, testProjectID, agentID, toolCallID)
	if err != nil {
		t.Fatalf("list typed tool result events: %v", err)
	}
	defer rows.Close()
	out := []events.Event{}
	for rows.Next() {
		var id, rowAgentID ID
		var sequence int64
		var kind, key string
		var at time.Time
		if err := rows.Scan(&id, &rowAgentID, &sequence, &kind, &at, &key); err != nil {
			t.Fatalf("scan typed tool result event: %v", err)
		}
		event, err := events.New(
			events.NewInput{
				ID:             id,
				AgentID:        rowAgentID,
				Sequence:       sequence,
				Kind:           events.Kind(kind),
				At:             at,
				IdempotencyKey: key,
			},
		)
		if err != nil {
			t.Fatalf("construct typed tool result event: %v", err)
		}
		out = append(out, event)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate typed tool result events: %v", err)
	}
	return out
}

func modelOutputIDForToolCall(t *testing.T, ctx context.Context, store *Store, agentID, toolCallID ID) ID {
	t.Helper()
	var modelOutputID ID
	if err := store.pool.QueryRow(ctx, `
SELECT model_output_id
FROM tool_call_read_projection
WHERE project_id = $1 AND agent_id = $2 AND id = $3
`, testProjectID, agentID, toolCallID).Scan(&modelOutputID); err != nil {
		t.Fatalf("load tool call model output: %v", err)
	}
	return modelOutputID
}

func modelOutputContextForTurnTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	turnID ID,
	testName string,
	now time.Time,
) ID {
	t.Helper()
	var openingInputID ID
	var inputSequence int64
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT event.agent_input_id, event.sequence
FROM agent_events event
JOIN agents agent ON agent.id = event.agent_id
WHERE agent.project_id = $1
  AND event.agent_id = $2
  AND event.turn_id = $3
  AND event.is_opening_event
ORDER BY event.sequence
LIMIT 1
`, testProjectID, fixture.AgentID, turnID).Scan(&openingInputID, &inputSequence); err != nil {
		t.Fatalf("load opening event for model output: %v", err)
	}
	agent, err := fixture.Store.Execution().GetAgentInProject(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("load agent for model output: %v", err)
	}
	var toolCallID ID
	if err := fixture.Store.pool.QueryRow(ctx, `
		SELECT tool_call.id
		FROM tool_call_read_projection tool_call
		WHERE tool_call.project_id = $1
		  AND tool_call.agent_id = $2
		  AND tool_call.turn_id = $3
		  AND tool_call.state = 'ready'
		ORDER BY tool_call.created_at, tool_call.id
		LIMIT 1`, testProjectID, fixture.AgentID, turnID).Scan(&toolCallID); err != nil {
		t.Fatalf("load pending tool call for model continuation: %v", err)
	}
	if _, err := fixture.Store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ID:                 toolCallID,
		RuntimeLockID:      fixture.Lock.ID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		ResultContentParts: json.RawMessage(`[]`),
	}); err != nil {
		t.Fatalf("complete tool call for model continuation: %v", err)
	}
	if err := fixture.Store.pool.QueryRow(ctx, `
		SELECT max(event.sequence)
		FROM agent_events event
		JOIN agents agent ON agent.id = event.agent_id
		WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.turn_id = $3`,
		testProjectID, fixture.AgentID, turnID).Scan(&inputSequence); err != nil {
		t.Fatalf("load continuation event sequence for model output: %v", err)
	}
	work, found, err := fixture.Store.Execution().NextAgentModelWork(
		ctx,
		testProjectID,
		fixture.AgentID,
	)
	if err != nil {
		t.Fatalf("select model work for bare model output: %v", err)
	}
	if !found {
		t.Fatal("select model work for bare model output: no work found")
	}
	claim, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID:                testProjectID,
		AgentID:                  fixture.AgentID,
		RuntimeLockID:            fixture.Lock.ID,
		OpeningInputIDs:          []ID{openingInputID},
		AgentConfigID:            agent.CurrentConfigID,
		InputEventSequence:       inputSequence,
		SourceModelCallContextID: work.ModelCallContextID,
		SourceModelOutputID:      work.SourceModelOutputID,
	})
	if err != nil {
		t.Fatalf("claim bare model output context: %v", err)
	}
	return claim.Context.ID
}

func createModelOutputEventForTurnTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	turnID, contextID ID,
	testName, stopReason, providerResponseID string,
	now time.Time,
) (executionstore.ModelOutputAuthorityRecord, executionstore.TypedAgentEventRecord) {
	t.Helper()
	contextRecord, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		contextID,
	)
	if err != nil || !found {
		t.Fatalf("load model context for output found=%v err=%v", found, err)
	}
	providerModelSlug := modelProviderSlugForContext(
		t,
		ctx,
		fixture.Store,
		testProjectID,
		fixture.AgentID,
		contextID,
	)
	if contextRecord.State != executionstore.ModelCallContextStarted {
		t.Fatalf("model context state = %s, want started", contextRecord.State)
	}
	responseID := providerResponseID
	if responseID == "" {
		responseID = "resp_" + testName
	}
	envelope := modelenvelope.ResponseEnvelope{
		RequestedProviderModelSlug: providerModelSlug,
		ServedProviderModelSlug:    providerModelSlug,
		APIFormat:                  modelprotocol.APIFormatOpenAIResponses,
		APIVariant:                 "default",
		Normalized: modelenvelope.ResponseNormalized{
			ID:         responseID,
			StopReason: modelenvelope.StopReason(stopReason),
		},
	}
	event, err := fixture.Store.Execution().RecordModelOutputAndCompleteContext(
		ctx,
		executionstore.RecordModelOutputAndCompleteContextInput{
			ProjectID:          testProjectID,
			AgentID:            fixture.AgentID,
			RuntimeLockID:      fixture.Lock.ID,
			ModelCallContextID: contextID,
			ProviderResponse:   envelope,
		},
	)
	if err != nil {
		t.Fatalf("record model output and complete context: %v", err)
	}
	modelOutput, found, err := fixture.Store.Execution().GetModelOutputForContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		contextID,
	)
	if err != nil || !found {
		t.Fatalf("load recorded model output: found=%v err=%v", found, err)
	}
	return modelOutput, executionstore.TypedAgentEventRecord{
		Event:         event,
		TurnID:        turnID,
		ModelOutputID: modelOutput.ID,
	}
}

func recordToolCallBatchForContextTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	contextID ID,
	testName string,
	calls []toolCallForContextTest,
	now time.Time,
) (events.Event, []executionstore.ToolCallRecord) {
	t.Helper()
	contextRecord, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		contextID,
	)
	if err != nil || !found {
		t.Fatalf("load model context for tool batch: found=%v err=%v", found, err)
	}
	providerModelSlug := modelProviderSlugForContext(
		t,
		ctx,
		fixture.Store,
		testProjectID,
		fixture.AgentID,
		contextID,
	)
	if contextRecord.State != executionstore.ModelCallContextStarted {
		t.Fatalf("model context state = %s, want started", contextRecord.State)
	}
	parts := make([]modelenvelope.ResponsePart, 0, len(calls))
	bindings := make([]executionstore.ToolCallBindingInput, 0, len(calls))
	for index := range calls {
		if calls[index].Type == "" {
			calls[index].Type = toolcatalog.ToolTypeBuiltIn
		}
		parts = append(parts, modelenvelope.ResponsePart{
			Type:           "tool_call",
			ProviderCallID: calls[index].ProviderCallID,
			ToolName:       calls[index].Name,
			ToolInput:      calls[index].Input,
		})
		bindings = append(bindings, executionstore.ToolCallBindingInput{
			ID:             calls[index].ID,
			ProviderCallID: calls[index].ProviderCallID,
			Type:           calls[index].Type,
		})
	}
	event, toolCalls, err := fixture.Store.Execution().RecordToolCallSourceAndCompleteContext(
		ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID:          testProjectID,
			AgentID:            fixture.AgentID,
			RuntimeLockID:      fixture.Lock.ID,
			ModelCallContextID: contextID,
			ProviderResponse: modelenvelope.ResponseEnvelope{
				RequestedProviderModelSlug: providerModelSlug,
				ServedProviderModelSlug:    providerModelSlug,
				APIFormat:                  modelprotocol.APIFormatOpenAIResponses,
				APIVariant:                 modelprotocol.APIVariantDefault,
				Normalized: modelenvelope.ResponseNormalized{
					ID:         "resp_" + testName,
					Content:    parts,
					StopReason: modelenvelope.StopReasonToolUse,
				},
			},
			ToolCallBindings: bindings,
		},
	)
	if err != nil {
		t.Fatalf("record tool call batch: %v", err)
	}
	return event, toolCalls
}

type toolCallForContextTest struct {
	ID             ID
	ProviderCallID string
	Name           string
	Input          json.RawMessage
	Type           string
}

func markToolCallReadyForTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	toolCallID ID,
	now time.Time,
) {
	t.Helper()
	if _, err := fixture.Store.Execution().MarkToolCallReady(ctx, executionstore.MarkToolCallReadyInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ID:            toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}); err != nil {
		t.Fatalf("mark tool call ready: %v", err)
	}
}

func createContextForAdmittedTurnTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	admitted executionstore.AdmittedAgentInputTurn,
	agent executionstore.AgentRecord,
	testName string,
	now time.Time,
) executionstore.ModelCallContextRecord {
	t.Helper()
	inputIDs := make([]ID, 0, len(admitted.Inputs))
	for _, input := range admitted.Inputs {
		inputIDs = append(inputIDs, input.ID)
	}
	return claimNormalContextAtFrontierTest(
		t,
		ctx,
		fixture,
		inputIDs,
		agent.CurrentConfigID,
		admitted.Events[len(admitted.Events)-1].Sequence,
		now,
	)
}

func claimNormalContextAtFrontierTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	openingInputIDs []ID,
	agentConfigID ID,
	inputEventSequence int64,
	now time.Time,
) executionstore.ModelCallContextRecord {
	t.Helper()
	work, found, err := fixture.Store.Execution().NextAgentModelWork(
		ctx,
		testProjectID,
		fixture.AgentID,
	)
	if err != nil {
		t.Fatalf("select model work for context claim: %v", err)
	}
	if !found {
		t.Fatal("select model work for context claim: no work found")
	}
	claim, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID:                testProjectID,
		AgentID:                  fixture.AgentID,
		RuntimeLockID:            fixture.Lock.ID,
		OpeningInputIDs:          openingInputIDs,
		AgentConfigID:            agentConfigID,
		InputEventSequence:       inputEventSequence,
		SourceModelCallContextID: work.ModelCallContextID,
		SourceModelOutputID:      work.SourceModelOutputID,
	})
	if err != nil {
		t.Fatalf("claim model context: %v", err)
	}
	return claim.Context
}

func appendSyntheticLatestContentTurnForFrontierTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	testName string,
	now time.Time,
) ID {
	t.Helper()
	inputID := testID("input_" + testName)
	turnID := testID("turn_" + testName)
	actorID := fixture.omnaraActorID(t, ctx)
	if _, err := fixture.Store.pool.Exec(ctx, `
		INSERT INTO agent_inputs(id, project_id, agent_id, state, delivery_mode, actor_id, input_kind, queued_at, metadata)
		VALUES ($1, $2, $3, 'received', 'queued', $4, 'content', $5, '{}'::jsonb)
	`, inputID, testProjectID, fixture.AgentID, actorID, now); err != nil {
		t.Fatalf("insert synthetic latest input: %v", err)
	}
	tx, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin synthetic latest turn: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	event, err := executionstore.IntegrationAppendTypedAgentEventTx(ctx, notifications.NewTxNotifications(), tx, executionstore.AppendTypedAgentEventInput{
		ProjectID:      testProjectID,
		AgentID:        fixture.AgentID,
		TurnID:         turnID,
		IsOpeningEvent: true,
		Kind:           events.KindAgentInput,
		IdempotencyKey: "agent_input:" + inputID.String(),
		AgentInputID:   inputID,
	})
	if err != nil {
		t.Fatalf("append synthetic latest input event: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE agent_inputs
		SET state = 'resolved',
		    admitted_event_id = $4,
		    admitted_at = $5,
		    resolved_at = $5
		WHERE id = $1 AND project_id = $2 AND agent_id = $3
	`, inputID, testProjectID, fixture.AgentID, event.Event.ID, now); err != nil {
		t.Fatalf("resolve synthetic latest input: %v", err)
	}
	var turnSequence int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(turn.turn_sequence), 0) + 1 FROM agent_turns turn JOIN agents agent ON agent.id = turn.agent_id WHERE agent.project_id = $1 AND turn.agent_id = $2`, testProjectID, fixture.AgentID).Scan(&turnSequence); err != nil {
		t.Fatalf("load next turn sequence: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO agent_turns(id, agent_id, turn_sequence, latest_event_id, latest_semantic_event_id)
		VALUES ($1, $2, $4, $3, $3)
	`, turnID, fixture.AgentID, event.Event.ID, turnSequence); err != nil {
		t.Fatalf("insert synthetic latest turn: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit synthetic latest turn: %v", err)
	}
	return turnID
}

func assertFrontierCountTest(t *testing.T, ctx context.Context, store *Store, label, query string, agentID, id ID, want int) {
	t.Helper()
	var got int
	if err := store.pool.QueryRow(ctx, query, testProjectID, agentID, id).Scan(&got); err != nil {
		t.Fatalf("%s: count frontier rows: %v", label, err)
	}
	if got != want {
		t.Fatalf("%s: frontier rows = %d, want %d", label, got, want)
	}
}

func newStartedNormalModelCallTestFixture(
	t *testing.T,
	ctx context.Context,
	testName string,
) (processDaemonFixture, executionstore.AgentRecord, executionstore.ModelCallClaim) {
	t.Helper()
	fixture, admitted, agent := newMultiInputContinuationSeedFixture(t, ctx, testName)
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
		t.Fatalf("claim started normal model call: %v", err)
	}
	return fixture, agent, claim
}

func newMultiInputContinuationSeedFixture(
	t *testing.T,
	ctx context.Context,
	testName string,
) (processDaemonFixture, executionstore.AdmittedAgentInputTurn, executionstore.AgentRecord) {
	t.Helper()
	fixture := newProcessDaemonFixture(t, ctx, testName)
	firstInput, _, _, err := fixture.Store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      testProjectID,
			AgentID:        fixture.AgentID,
			Actor:          mustOmnaraActorParams(t, fixture.UserID),
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"first"}]`),
			DeliveryMode:   executionstore.DeliveryModeSteering,
			IdempotencyKey: "idem-" + testName + "-first",
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
			IdempotencyKey: "idem-" + testName + "-second",
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
	agent, err := fixture.Store.Execution().GetAgentInProject(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("load agent: %v", err)
	}
	return fixture, admitted, agent
}

func completeToolCallForContinuationSeedTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	turnID, contextID ID,
	testName string,
	now time.Time,
) executionstore.ToolCallRecord {
	t.Helper()
	_, toolCalls := recordToolCallBatchForContextTest(
		t,
		ctx,
		fixture,
		contextID,
		testName,
		[]toolCallForContextTest{{
			ProviderCallID: "call_" + testName,
			Name:           "read_process",
			Input:          json.RawMessage(`{}`),
			Type:           toolcatalog.ToolTypeBuiltIn,
		}},
		now,
	)
	if len(toolCalls) != 1 || toolCalls[0].TurnID != turnID {
		t.Fatalf("recorded tool calls = %+v, want one in turn %s", toolCalls, turnID)
	}
	toolCall := toolCalls[0]
	if _, err := fixture.Store.Execution().MarkToolCallReady(ctx, executionstore.MarkToolCallReadyInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ID:            toolCall.ID,
		RuntimeLockID: fixture.Lock.ID,
	}); err != nil {
		t.Fatalf("mark tool call ready: %v", err)
	}
	completed, err := fixture.Store.Execution().CompleteToolCall(
		ctx,
		executionstore.CompleteToolCallInput{
			ProjectID:          testProjectID,
			AgentID:            fixture.AgentID,
			ID:                 toolCall.ID,
			Outcome:            executionstore.ToolResultOutcomeSucceeded,
			RuntimeLockID:      fixture.Lock.ID,
			ResultContentParts: json.RawMessage(`[{"type":"text","text":"done"}]`),
		},
	)
	if err != nil {
		t.Fatalf("complete tool call: %v", err)
	}
	return completed
}

func completeModelContextForOutputTest(t *testing.T, ctx context.Context, fixture processDaemonFixture, contextID, modelOutputID ID, providerResponseID string, now time.Time) {
	t.Helper()
	_ = providerResponseID
	_ = now
	contextRecord, found, err := fixture.Store.Execution().GetModelCallContext(ctx, testProjectID, fixture.AgentID, contextID)
	if err != nil || !found {
		t.Fatalf("load completed model context found=%v err=%v", found, err)
	}
	if contextRecord.State != executionstore.ModelCallContextSucceeded {
		t.Fatalf("model context state = %s, want succeeded", contextRecord.State)
	}
	output, found, err := fixture.Store.Execution().GetModelOutputForContext(ctx, testProjectID, fixture.AgentID, contextID)
	if err != nil || !found || output.ID != modelOutputID {
		t.Fatalf("model output for context found=%v output=%+v err=%v", found, output, err)
	}
}

func toolCallSourceSequenceForCheckpointTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	toolCallID ID,
) int64 {
	t.Helper()
	var sequence int64
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT source_event_sequence
FROM tool_call_read_projection tool_call
WHERE tool_call.project_id = $1 AND tool_call.agent_id = $2 AND tool_call.id = $3
`, testProjectID, fixture.AgentID, toolCallID).Scan(&sequence); err != nil {
		t.Fatalf("load tool call source sequence: %v", err)
	}
	return sequence
}

func claimSentCompactionForRangeTest(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	start, end, frontier int64,
	now time.Time,
) executionstore.ModelCallClaim {
	t.Helper()
	latestSequence, err := fixture.Store.Execution().MaxEventSequence(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("load compaction source frontier: %v", err)
	}
	if latestSequence != frontier {
		t.Fatalf("compaction source frontier = %d, want %d", latestSequence, frontier)
	}
	overflowInput, _, _, err := fixture.Store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      testProjectID,
			AgentID:        fixture.AgentID,
			Actor:          mustOmnaraActorParams(t, fixture.UserID),
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"trigger checkpoint overflow"}]`),
			DeliveryMode:   executionstore.DeliveryModeSteering,
			IdempotencyKey: fmt.Sprintf("checkpoint-overflow-%d-%d-%d", start, end, now.UnixNano()),
		},
	)
	if err != nil {
		t.Fatalf("create checkpoint overflow input: %v", err)
	}
	tx, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin checkpoint overflow admission: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if _, err := qtx.LockAgentInProject(ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: testProjectID,
		ID:        fixture.AgentID,
	}); err != nil {
		t.Fatalf("lock checkpoint overflow agent: %v", err)
	}
	admitted, err := executionstore.IntegrationAdmitLockedAgentInputsAndOpenTurnTx(
		ctx,
		notifications.NewTxNotifications(),
		tx,
		qtx,
		executionstore.IntegrationAdmitAgentInputAndOpenTurnInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
		},
		[]executionstore.AgentInputRecord{overflowInput},
	)
	if err != nil {
		t.Fatalf("admit checkpoint overflow input: %v", err)
	}
	overflowFrontier := admitted.Events[len(admitted.Events)-1].Sequence
	openingInputIDs, _, err := executionstore.IntegrationModelCallOpeningInputSet(
		ctx,
		qtx,
		testProjectID,
		fixture.AgentID,
		admitted.Turn.ID,
		overflowFrontier,
	)
	if err != nil {
		t.Fatalf("load checkpoint overflow opening inputs: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit checkpoint overflow admission: %v", err)
	}
	if len(admitted.Inputs) != 1 || admitted.Inputs[0].ID != overflowInput.ID {
		t.Fatalf("admitted checkpoint overflow input = %+v", admitted)
	}
	snapshot, err := fixture.Store.Execution().CaptureAgentConfigForEventWatermark(
		ctx,
		testProjectID,
		fixture.AgentID,
		overflowFrontier,
	)
	if err != nil {
		t.Fatalf("capture checkpoint overflow config: %v", err)
	}
	parent, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		RuntimeLockID:      fixture.Lock.ID,
		OpeningInputIDs:    openingInputIDs,
		AgentConfigID:      snapshot.AgentConfig.ID,
		InputEventSequence: overflowFrontier,
	})
	if err != nil {
		t.Fatalf("claim checkpoint overflow parent: %v", err)
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
				ErrorCode:          "test_checkpoint_overflow",
				ErrorMessage:       "model context exceeded",
			},
			SourceEventSequenceEnd: end,
		},
	)
	if err != nil {
		t.Fatalf("record checkpoint overflow parent failure: %v", err)
	}
	return handoff.CompactionCall
}

func publishCheckpointForRangeTest(
	_ *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
	claim executionstore.ModelCallClaim,
	summary string,
	now time.Time,
) (executionstore.ContextCheckpointRecord, error) {
	return fixture.Store.Execution().PublishContextCheckpoint(ctx, executionstore.PublishContextCheckpointInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID,
		RuntimeLockID: fixture.Lock.ID, ModelCallContextID: claim.Context.ID,
		Summary:            summary,
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		APIVariant:         modelprotocol.APIVariantDefault,
		ProviderResponseID: "resp_" + summary,
	})
}

func toolCallLineageForContinuationSeedTest(t *testing.T, ctx context.Context, fixture processDaemonFixture, toolCallID ID) (ID, ID, ID) {
	t.Helper()
	var sourceEventID, modelContextID, modelOutputID ID
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT source_event_id, model_call_context_id, model_output_id
FROM tool_call_read_projection
WHERE project_id = $1
  AND agent_id = $2
  AND id = $3
`, testProjectID, fixture.AgentID, toolCallID).Scan(&sourceEventID, &modelContextID, &modelOutputID); err != nil {
		t.Fatalf("load tool call lineage: %v", err)
	}
	return sourceEventID, modelContextID, modelOutputID
}

func openingInputForContinuationSeedTest(t *testing.T, ctx context.Context, fixture processDaemonFixture, toolCallID ID) ID {
	t.Helper()
	var inputID ID
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT opening_event.agent_input_id
FROM tool_call_read_projection tool_call
JOIN agent_events opening_event ON opening_event.agent_id = tool_call.agent_id
  AND opening_event.turn_id = tool_call.turn_id
  AND opening_event.is_opening_event
  AND opening_event.event_kind = 'agent_input'
  AND opening_event.agent_input_id IS NOT NULL
WHERE tool_call.project_id = $1
  AND tool_call.agent_id = $2
  AND tool_call.id = $3
ORDER BY opening_event.sequence
LIMIT 1
`, testProjectID, fixture.AgentID, toolCallID).Scan(&inputID); err != nil {
		t.Fatalf("load opening input for tool call: %v", err)
	}
	return inputID
}

func appendCancelStopEventForContinuationSeedTest(t *testing.T, ctx context.Context, fixture processDaemonFixture, turnID ID, now time.Time) events.Event {
	t.Helper()
	actorID := fixture.omnaraActorID(t, ctx)
	tx, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin cancel stop event fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	controlType := "cancel_current"
	controlInput, err := qtx.InsertControlAgentInput(ctx, dbsqlc.InsertControlAgentInputParams{
		ProjectID:           testProjectID,
		AgentID:             fixture.AgentID,
		ActorID:             sqlcIDFromNil(actorID),
		ControlType:         &controlType,
		IdempotencyScope:    sqlcTextFromEmpty("agent_control"),
		InputIdempotencyKey: sqlcTextFromEmpty("test-cancel-stop:" + turnID.String()),
		Metadata:            json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("insert cancel control input: %v", err)
	}
	eventRecord, err := executionstore.IntegrationAppendTypedAgentEventTx(ctx, notifications.NewTxNotifications(), tx, executionstore.AppendTypedAgentEventInput{
		ProjectID:      testProjectID,
		AgentID:        fixture.AgentID,
		TurnID:         turnID,
		Kind:           events.KindAgentInput,
		IdempotencyKey: "agent_input:" + controlInput.ID.String(),
		AgentInputID:   controlInput.ID,
	})
	if err != nil {
		t.Fatalf("append cancel stop event: %v", err)
	}
	if err := executionstore.IntegrationUpdateAgentTurnLatestEventQuery(ctx, qtx, testProjectID, fixture.AgentID, turnID, eventRecord.Event.ID, NilID); err != nil {
		t.Fatalf("update turn latest cancel event: %v", err)
	}
	if err := qtx.ResolveControlAgentInput(ctx, dbsqlc.ResolveControlAgentInputParams{ProjectID: testProjectID, AgentID: fixture.AgentID, ID: controlInput.ID, ControlType: &controlType, EventID: &eventRecord.Event.ID}); err != nil {
		t.Fatalf("resolve cancel control input: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit cancel stop event fixture: %v", err)
	}
	return eventRecord.Event
}

func isPgConstraintViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "23505" || pgErr.Code == "23514" || pgErr.Code == "23503")
}

func assertPgErrorMessage(t *testing.T, err error, code, message string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code || pgErr.Message != message {
		t.Fatalf("database error = %v, want %s %q", err, code, message)
	}
}

func isPgCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

func isPgForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

func isPgReadOnlySQLTransaction(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "25006"
}
