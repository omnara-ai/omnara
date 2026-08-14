//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestToolCallLifecyclePublishesCommittedUpdatesOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, _, claim := newStartedNormalModelCallTestFixture(t, ctx, "tool_call_updates")
	publisher := &recordingPostCommitPublisher{}
	fixture.Store = newIntegrationStore(
		fixture.Store.pool,
		WithPostCommitPublisher(publisher),
	)

	_, toolCalls := recordToolCallBatchForContextTest(
		t,
		ctx,
		fixture,
		claim.Context.ID,
		"tool_call_updates",
		[]toolCallForContextTest{{
			ProviderCallID: "call_tool_call_updates",
			Name:           "read_file",
			Input:          json.RawMessage(`{"path":"README.md"}`),
			Type:           toolcatalog.ToolTypeBuiltIn,
		}},
		fixture.Now,
	)
	if len(toolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(toolCalls))
	}
	toolCall := toolCalls[0]

	readyInput := executionstore.MarkToolCallReadyInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ID:            toolCall.ID,
		RuntimeLockID: fixture.Lock.ID,
	}
	if _, err := fixture.Store.Execution().MarkToolCallReady(ctx, readyInput); err != nil {
		t.Fatalf("mark tool call ready: %v", err)
	}
	if _, err := fixture.Store.Execution().MarkToolCallReady(ctx, readyInput); err != nil {
		t.Fatalf("replay mark tool call ready: %v", err)
	}

	completion := executionstore.CompleteToolCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ID:                 toolCall.ID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		RuntimeLockID:      fixture.Lock.ID,
		ResultContentParts: json.RawMessage(`[{"type":"text","text":"done"}]`),
	}
	if _, err := fixture.Store.Execution().CompleteToolCall(ctx, completion); err != nil {
		t.Fatalf("complete tool call: %v", err)
	}
	if _, err := fixture.Store.Execution().CompleteToolCall(ctx, completion); err != nil {
		t.Fatalf("replay complete tool call: %v", err)
	}

	want := []string{
		string(executionstore.ToolCallStateAwaitingAuthorization),
		string(executionstore.ToolCallStateReady),
		string(executionstore.ToolCallStateCompleted),
	}
	if got := publisher.toolCallStates(toolCall.ID); !slices.Equal(got, want) {
		t.Fatalf("tool call update states = %v, want %v", got, want)
	}
}

func TestToolCallBindingBatchRejectsEnvelopeMismatchWithoutPartialState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, _, claim := newStartedNormalModelCallTestFixture(
		t,
		ctx,
		"tool_proposal_envelope_mismatch",
	)
	providerModelSlug := modelProviderSlugForContext(
		t,
		ctx,
		fixture.Store,
		testProjectID,
		fixture.AgentID,
		claim.Context.ID,
	)
	_, _, err := fixture.Store.Execution().RecordToolCallSourceAndCompleteContext(
		ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID:          testProjectID,
			AgentID:            fixture.AgentID,
			RuntimeLockID:      fixture.Lock.ID,
			ModelCallContextID: claim.Context.ID,
			ProviderResponse: modelenvelope.ResponseEnvelope{
				RequestedProviderModelSlug: providerModelSlug,
				ServedProviderModelSlug:    providerModelSlug,
				APIFormat:                  modelprotocol.APIFormatOpenAIResponses,
				APIVariant:                 modelprotocol.APIVariantDefault,
				Normalized: modelenvelope.ResponseNormalized{
					ID: "resp_tool_proposal_envelope_mismatch",
					Content: []modelenvelope.ResponsePart{{
						Type:           modelenvelope.ResponsePartTypeToolCall,
						ProviderCallID: "call_from_envelope",
						ToolName:       "read_file",
						ToolInput:      json.RawMessage(`{"path":"README.md"}`),
					}},
					StopReason: modelenvelope.StopReasonToolUse,
				},
			},
			ToolCallBindings: []executionstore.ToolCallBindingInput{{
				ProviderCallID: "call_from_binding",
				Type:           toolcatalog.ToolTypeBuiltIn,
			}},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "has no tool call binding") {
		t.Fatalf("record mismatched tool binding error = %v", err)
	}

	var modelOutputs, contentBlocks, toolCalls int
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM model_outputs WHERE model_call_context_id = $1),
  (SELECT count(*)
     FROM content_blocks block
     JOIN model_outputs output
       ON output.agent_id = block.agent_id
      AND output.id = block.owner_model_output_id
    WHERE output.model_call_context_id = $1),
  (SELECT count(*) FROM tool_call_read_projection WHERE model_call_context_id = $1)`,
		claim.Context.ID,
	).Scan(&modelOutputs, &contentBlocks, &toolCalls); err != nil {
		t.Fatalf("count partially published tool proposal state: %v", err)
	}
	if modelOutputs != 0 || contentBlocks != 0 || toolCalls != 0 {
		t.Fatalf(
			"partial tool proposal state outputs=%d blocks=%d calls=%d, want all zero",
			modelOutputs,
			contentBlocks,
			toolCalls,
		)
	}
	contextRecord, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		claim.Context.ID,
	)
	if err != nil || !found {
		t.Fatalf("load model context after rejected proposal found=%v err=%v", found, err)
	}
	if contextRecord.State != executionstore.ModelCallContextStarted {
		t.Fatalf("rejected proposal context state = %s, want started", contextRecord.State)
	}
}

func TestKernelTypedFrontierToolResultAuthority(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "kernel_tool_result_authority")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "kernel_tool_result_authority", "read_process")
	domainContentParts := json.RawMessage(
		`[{"type":"text","text":"ok"},{"type":"structured_data","value":{"ok":true}}]`,
	)

	completion := executionstore.CompleteToolCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ID:                 toolCallID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		RuntimeLockID:      fixture.Lock.ID,
		ResultContentParts: domainContentParts,
	}
	toolCall, err := fixture.Store.Execution().CompleteToolCall(ctx, completion)
	if err != nil {
		t.Fatalf("complete tool call with result authority: %v", err)
	}
	result, found, err := fixture.Store.Execution().GetToolCallResultAuthorityByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	)
	if err != nil || !found {
		t.Fatalf("load tool result authority found=%v err=%v", found, err)
	}
	resultEvents := listTypedToolResultEventsForToolCall(
		t,
		ctx,
		fixture.Store,
		fixture.AgentID,
		toolCallID,
	)
	if len(resultEvents) != 1 || resultEvents[0].Kind != events.KindToolResult {
		t.Fatalf("typed tool result events = %+v, want one", resultEvents)
	}
	if toolCall.State != executionstore.ToolCallStateCompleted || toolCall.CompletedAt == nil {
		t.Fatalf("tool call terminal state = %s completed_at=%v", toolCall.State, toolCall.CompletedAt)
	}
	storedToolCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("load completed tool call: %v", err)
	}
	if storedToolCall.Outcome != executionstore.ToolResultOutcomeSucceeded {
		t.Fatalf(
			"tool result outcome = %s, want %s",
			storedToolCall.Outcome,
			executionstore.ToolResultOutcomeSucceeded,
		)
	}
	if !sameJSON(storedToolCall.ResultContentParts, domainContentParts) {
		t.Fatalf(
			"tool result domain content = %s, want %s",
			storedToolCall.ResultContentParts,
			domainContentParts,
		)
	}

	replayed, err := fixture.Store.Execution().CompleteToolCall(ctx, completion)
	if err != nil {
		t.Fatalf("replay atomic tool completion: %v", err)
	}
	if replayed.ID != toolCall.ID {
		t.Fatalf("replayed tool call id = %s, want %s", replayed.ID, toolCall.ID)
	}
	replayedResult, found, err := fixture.Store.Execution().GetToolCallResultAuthorityByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	)
	if err != nil || !found {
		t.Fatalf("load replayed tool result authority found=%v err=%v", found, err)
	}
	replayedEvents := listTypedToolResultEventsForToolCall(
		t,
		ctx,
		fixture.Store,
		fixture.AgentID,
		toolCallID,
	)
	if replayedResult.ID != result.ID || len(replayedEvents) != 1 ||
		replayedEvents[0].ID != resultEvents[0].ID {
		t.Fatalf(
			"replayed result/event result=%s/%s events=%+v/%+v",
			replayedResult.ID,
			result.ID,
			replayedEvents,
			resultEvents,
		)
	}
	var blockCount int
	if err := fixture.Store.pool.QueryRow(ctx, `SELECT count(*) FROM content_blocks block JOIN agents agent ON agent.id = block.agent_id WHERE agent.project_id = $1 AND block.agent_id = $2 AND block.owner_tool_call_result_id = $3`, testProjectID, fixture.AgentID, result.ID).Scan(&blockCount); err != nil {
		t.Fatalf("count tool result content blocks: %v", err)
	}
	if blockCount != 2 {
		t.Fatalf("tool result domain content block count = %d, want 2", blockCount)
	}

	conflict := completion
	conflict.ResultContentParts = json.RawMessage(
		`[{"type":"structured_data","value":{"ok":false}}]`,
	)
	if _, err := fixture.Store.Execution().CompleteToolCall(ctx, conflict); !errors.Is(
		err,
		storeerr.ErrIdempotencyConflict,
	) {
		t.Fatalf("conflicting tool result error = %v, want %v", err, storeerr.ErrIdempotencyConflict)
	}

	pendingToolCallID := createToolCallForProcessTestWithPermission(
		t,
		ctx,
		fixture,
		"kernel_tool_result_authority_pending",
		"read_process",
		false,
	)
	_, err = fixture.Store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ID:                 pendingToolCallID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		RuntimeLockID:      fixture.Lock.ID,
		ResultContentParts: json.RawMessage(`[{"type":"text","text":"ok"}]`),
	})
	if !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("pending tool result admission error = %v, want %v", err, storeerr.ErrStateTransitionConflict)
	}
	interaction := createPermissionInteractionForTest(
		t,
		ctx,
		fixture,
		pendingToolCallID,
		permissionRequestForStorageTest(t, "read_process"),
	)
	_, err = fixture.Store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ID:                 pendingToolCallID,
		Outcome:            executionstore.ToolResultOutcomeDenied,
		RuntimeLockID:      fixture.Lock.ID,
		ResultContentParts: json.RawMessage(`[{"type":"text","text":"permission denied"}]`),
	})
	if err != nil {
		t.Fatalf("complete denied tool call: %v", err)
	}
	closed, found, err := fixture.Store.Execution().GetAgentInteraction(
		ctx,
		testProjectID,
		fixture.AgentID,
		interaction.ID,
	)
	if err != nil || !found {
		t.Fatalf("load closed interaction found=%v err=%v", found, err)
	}
	if closed.State != executionstore.AgentInteractionStateCanceled || closed.ResolvedAt.IsZero() || !sameJSON(
		closed.Resolution,
		json.RawMessage(`{"reason":"tool_call_completed"}`),
	) {
		t.Fatalf(
			"closed interaction state=%s resolved_at=%v resolution=%s",
			closed.State,
			closed.ResolvedAt,
			closed.Resolution,
		)
	}
}

func TestKernelToolResultAuthorityReplayWithMixedContentAndOutput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "kernel_tool_result_authority_mixed_replay")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"kernel_tool_result_authority_mixed_replay",
		"read_process",
	)

	input := executionstore.CompleteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ID:            toolCallID,
		RuntimeLockID: fixture.Lock.ID,
		Outcome:       executionstore.ToolResultOutcomeSucceeded,
		ResultContentParts: json.RawMessage(
			`[{"type":"text","text":"done"},{"type":"structured_data","value":{"ok":true,"value":42}}]`,
		),
	}
	completed, err := fixture.Store.Execution().CompleteToolCall(ctx, input)
	if err != nil {
		t.Fatalf("complete mixed tool result authority: %v", err)
	}
	replayed, err := fixture.Store.Execution().CompleteToolCall(ctx, input)
	if err != nil {
		t.Fatalf("replay mixed tool result authority: %v", err)
	}
	if replayed.ID != completed.ID {
		t.Fatalf("mixed replay execution id = %s, want %s", replayed.ID, completed.ID)
	}
	resultEvents := listTypedToolResultEventsForToolCall(
		t,
		ctx,
		fixture.Store,
		fixture.AgentID,
		toolCallID,
	)
	if len(resultEvents) != 1 {
		t.Fatalf("mixed replay tool result events = %+v, want one", resultEvents)
	}
}

func TestKernelToolCallBlockLinkageConstraints(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "kernel_tool_call_block_linkage")
	now := fixture.Now.Add(time.Minute)
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "kernel_tool_call_block_linkage", "read_process")
	modelOutputID := modelOutputIDForToolCall(t, ctx, fixture.Store, fixture.AgentID, toolCallID)
	if _, err := fixture.Store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ID:                 toolCallID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		RuntimeLockID:      fixture.Lock.ID,
		ResultContentParts: json.RawMessage(`[{"type":"text","text":"done"}]`),
	}); err != nil {
		t.Fatalf("complete first tool call: %v", err)
	}
	openingInputID, resultWatermark := openingInputAndWatermarkForProcessToolCallTest(
		t,
		ctx,
		fixture,
		toolCallID,
	)
	agent, err := fixture.Store.Execution().GetAgentInProject(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("load agent: %v", err)
	}
	otherContext := claimNormalContextAtFrontierTest(
		t,
		ctx,
		fixture,
		[]ID{openingInputID},
		agent.CurrentConfigID,
		resultWatermark,
		now.Add(time.Second),
	)
	_, otherToolCalls := recordToolCallBatchForContextTest(
		t,
		ctx,
		fixture,
		otherContext.ID,
		"kernel_tool_call_block_linkage_other",
		[]toolCallForContextTest{{
			ProviderCallID: "call_kernel_tool_call_block_linkage_other",
			Name:           "read_process",
			Input:          json.RawMessage(`{}`),
		}},
		now.Add(2*time.Second),
	)
	if len(otherToolCalls) != 1 {
		t.Fatalf("other tool batch = %+v, want one call", otherToolCalls)
	}
	otherToolCallID := otherToolCalls[0].ID
	otherModelOutputID := modelOutputIDForToolCall(t, ctx, fixture.Store, fixture.AgentID, otherToolCallID)
	if _, err := executionstore.IntegrationCreateContentBlockTx(ctx, fixture.Store.pool, executionstore.CreateContentBlockInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		OwnerKind:          executionstore.ContentBlockOwnerModelOutput,
		OwnerModelOutputID: otherModelOutputID,
		Ordinal:            1,
		BlockKind:          executionstore.ContentBlockKindToolCall,
		ToolCallID:         toolCallID,
	}); !isPgConstraintViolation(err) {
		t.Fatalf("mismatched tool call block model output error = %v, want constraint violation", err)
	}
	var blockID, linkedToolCallID ID
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT block.id, block.tool_call_id
FROM content_blocks AS block
JOIN tool_calls AS call
  ON call.agent_id = block.agent_id
 AND call.id = block.tool_call_id
JOIN agents agent ON agent.id = block.agent_id
WHERE agent.project_id = $1
  AND block.agent_id = $2
  AND block.owner_model_output_id = $3
  AND block.ordinal = 0
  AND block.block_kind = 'tool_call'
  AND call.id = $4
`, testProjectID, fixture.AgentID, modelOutputID, toolCallID).Scan(&blockID, &linkedToolCallID); err != nil {
		t.Fatalf("load matching tool call block linkage: %v", err)
	}
	if linkedToolCallID != toolCallID {
		t.Fatalf("content block tool call link = %s, want %s", linkedToolCallID, toolCallID)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE tool_calls
SET name = 'mutated_tool'
WHERE agent_id = $1 AND id = $2
`, fixture.AgentID, toolCallID); !isPgReadOnlySQLTransaction(err) {
		t.Fatalf("mutate tool call proposal error = %v, want SQLSTATE 25006", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE content_blocks
SET tool_call_id = NULL
WHERE agent_id = $1 AND id = $2
`, fixture.AgentID, blockID); !isPgReadOnlySQLTransaction(err) {
		t.Fatalf("unlink tool call content block error = %v, want SQLSTATE 25006", err)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
DELETE FROM tool_calls
WHERE agent_id = $1 AND id = $2
`, fixture.AgentID, toolCallID); !isPgReadOnlySQLTransaction(err) {
		t.Fatalf("delete tool call error = %v, want SQLSTATE 25006", err)
	}
}

func TestToolCallInputRequiresJSONObject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "tool_call_input_requires_object")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"tool_call_input_requires_object",
		"read_process",
	)
	if _, err := fixture.Store.pool.Exec(ctx, `
INSERT INTO tool_calls (
    id,
    agent_id,
    model_output_id,
    provider_call_id,
    name,
    input,
    type,
    state,
    runtime_lock_id,
    created_at
)
SELECT
    uuidv7(),
    agent_id,
    model_output_id,
    provider_call_id || '_invalid_input',
    name,
    '[]'::jsonb,
    type,
    state,
    runtime_lock_id,
    created_at
FROM tool_calls
WHERE agent_id = $1
  AND id = $2
`, fixture.AgentID, toolCallID); !isPgCheckViolation(err) {
		t.Fatalf("insert tool call with array input error = %v, want check violation", err)
	}
}

func TestKernelToolCallProviderIdentityBound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "kernel_tool_call_provider_identity_bound")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"kernel_tool_call_provider_identity_bound",
		"read_process",
	)
	_, err := fixture.Store.pool.Exec(ctx, `
INSERT INTO tool_calls(
  agent_id, model_output_id,
  provider_call_id, name, input, type, state, created_at
)
SELECT agent_id, model_output_id,
	       $3, name, input, type, state, created_at
FROM tool_calls
WHERE agent_id = $1 AND id = $2
`, fixture.AgentID, toolCallID, strings.Repeat("x", 2_001))
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("oversized provider call id error = %v", err)
	}
}

func TestKernelAppendCompletedToolResultWritesTypedAuthority(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "kernel_append_completed_tool_result_authority")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"kernel_append_completed_tool_result_authority",
		"read_process",
	)

	completed, err := fixture.Store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ID:                 toolCallID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		RuntimeLockID:      fixture.Lock.ID,
		ResultContentParts: json.RawMessage(`[{"type":"structured_data","value":{"ok":true}}]`),
	})
	if err != nil {
		t.Fatalf("complete tool call: %v", err)
	}
	appended := listTypedToolResultEventsForToolCall(t, ctx, fixture.Store, fixture.AgentID, toolCallID)
	if len(appended) != 1 || appended[0].Kind != events.KindToolResult {
		t.Fatalf("events = %+v, want one tool_result from completion transaction", appended)
	}
	var resultID ID
	if err := fixture.Store.pool.QueryRow(ctx, `SELECT event.tool_call_result_id FROM agent_events event JOIN agents agent ON agent.id = event.agent_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.id = $3`, testProjectID, fixture.AgentID, appended[0].ID).Scan(&resultID); err != nil {
		t.Fatalf("load typed event result pointer: %v", err)
	}
	if isNilID(resultID) {
		t.Fatalf("tool_result event has nil tool_call_result_id")
	}
	result, found, err := fixture.Store.Execution().GetToolCallResultAuthorityByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("get tool call result authority: %v", err)
	}
	if !found || result.ID != resultID {
		t.Fatalf("tool call result = %+v found=%v pointer=%s", result, found, resultID)
	}
	seed, found, err := fixture.Store.Execution().NextAgentModelWork(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("next continuation seed after typed tool result: %v", err)
	}
	if !found || seed.TurnID != completed.TurnID || len(seed.InputIDs) != 1 {
		t.Fatalf(
			"continuation seed after typed tool result found=%v seed=%+v want turn %s",
			found,
			seed,
			completed.TurnID,
		)
	}
}

func TestKernelCompletedToolResultReadUsesTypedAuthority(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "kernel_tool_result_content_authority")
	textToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"kernel_tool_result_content_authority_text",
		"read_process",
	)

	textCompleted, err := fixture.Store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ID:                 textToolCallID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		RuntimeLockID:      fixture.Lock.ID,
		ResultContentParts: json.RawMessage(`[{"type":"text","text":"from content block authority"}]`),
	})
	if err != nil {
		t.Fatalf("complete text tool call: %v", err)
	}
	textRows, err := fixture.Store.Execution().ListCompletedToolCallsForTurn(
		ctx,
		testProjectID,
		fixture.AgentID,
		textCompleted.TurnID,
	)
	if err != nil {
		t.Fatalf("list completed text tool calls: %v", err)
	}
	if len(textRows) != 1 {
		t.Fatalf("text tool calls = %d, want 1", len(textRows))
	}
	if !strings.Contains(string(textRows[0].ResultContentParts), "from content block authority") {
		t.Fatalf("text result content parts = %s, want content_blocks authority only", textRows[0].ResultContentParts)
	}

	structuredToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"kernel_tool_result_content_authority_structured",
		"read_process",
	)
	structuredCompleted, err := fixture.Store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ID:                 structuredToolCallID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		RuntimeLockID:      fixture.Lock.ID,
		ResultContentParts: json.RawMessage(`[{"type":"structured_data","value":{"ok":true,"source":"typed-result"}}]`),
	})
	if err != nil {
		t.Fatalf("complete structured tool call: %v", err)
	}
	structuredRows, err := fixture.Store.Execution().ListCompletedToolCallsForTurn(
		ctx,
		testProjectID,
		fixture.AgentID,
		structuredCompleted.TurnID,
	)
	if err != nil {
		t.Fatalf("list completed structured tool calls: %v", err)
	}
	if len(structuredRows) != 1 {
		t.Fatalf("structured tool calls = %d, want 1", len(structuredRows))
	}
	if !strings.Contains(string(structuredRows[0].ResultContentParts), "typed-result") {
		t.Fatalf(
			"structured result content parts = %s, want tool_call_results authority",
			structuredRows[0].ResultContentParts,
		)
	}
}

func TestKernelZeroVisibleOutputToolResultIsTerminalAuthority(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "kernel_zero_visible_tool_result")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "kernel_zero_visible_tool_result", "read_process")

	completed, err := fixture.Store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ID:                 toolCallID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		RuntimeLockID:      fixture.Lock.ID,
		ResultContentParts: json.RawMessage(`[]`),
	})
	if err != nil {
		t.Fatalf("complete zero-output tool call: %v", err)
	}
	result, found, err := fixture.Store.Execution().GetToolCallResultAuthorityByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("get zero-output tool result authority: %v", err)
	}
	if !found {
		t.Fatalf("zero-output result authority found=%v result=%+v", found, result)
	}
	rows, err := fixture.Store.Execution().ListCompletedToolCallsForTurn(ctx, testProjectID, fixture.AgentID, completed.TurnID)
	if err != nil {
		t.Fatalf("list zero-output completed tool calls: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != toolCallID || string(rows[0].ResultContentParts) != "[]" {
		t.Fatalf("zero-output completed rows = %+v", rows)
	}
}

func TestKernelCompletedToolResultRejectsUntypedMarker(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "kernel_append_completed_tool_result_untyped")
	now := fixture.Now.Add(time.Minute)
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"kernel_append_completed_tool_result_untyped",
		"read_process",
	)

	completed, err := fixture.Store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ID:                 toolCallID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		RuntimeLockID:      fixture.Lock.ID,
		ResultContentParts: json.RawMessage(`[{"type":"structured_data","value":{"ok":true}}]`),
	})
	if err != nil {
		t.Fatalf("complete tool call: %v", err)
	}
	turnID := turnIDForProcessToolCallTest(t, ctx, fixture, toolCallID)
	_, err = fixture.Store.pool.Exec(ctx, `
		INSERT INTO agent_events(agent_id, turn_id, sequence, event_kind, idempotency_key, created_at)
		VALUES ($1, $2, 999, 'tool_result', $3, $4)
	`, fixture.AgentID, turnID, "untyped-tool-call-completed:"+completed.ID.String(), now.Add(time.Second))
	if err == nil {
		t.Fatalf("untyped tool result marker unexpectedly appended")
	}
	appended := listTypedToolResultEventsForToolCall(t, ctx, fixture.Store, fixture.AgentID, toolCallID)
	if len(appended) != 1 {
		t.Fatalf("typed events = %+v, want one authority event", appended)
	}
}

func TestKernelInvalidToolResultRollsBackCompletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "kernel_invalid_tool_result_rollback")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"kernel_invalid_tool_result_rollback",
		"read_process",
	)

	_, err := fixture.Store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ID:            toolCallID,
		Outcome:       executionstore.ToolResultOutcomeSucceeded,
		RuntimeLockID: fixture.Lock.ID,
		ResultContentParts: json.RawMessage(
			`[{"type":"structured_data","value":{"ok":true},"transport_metadata":"discarded"}]`,
		),
	})
	if !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("complete tool call error = %v, want invalid request", err)
	}

	toolCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("get tool call after rejected completion: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateReady ||
		toolCall.Outcome != "" ||
		toolCall.CompletedAt != nil {
		t.Fatalf("tool call changed after rejected completion: %+v", toolCall)
	}
	if _, found, err := fixture.Store.Execution().GetToolCallResultAuthorityByToolCall(
		ctx,
		testProjectID,
		fixture.AgentID,
		toolCallID,
	); err != nil {
		t.Fatalf("get result after rejected completion: %v", err)
	} else if found {
		t.Fatal("rejected completion persisted a tool result")
	}
	if events := listTypedToolResultEventsForToolCall(
		t,
		ctx,
		fixture.Store,
		fixture.AgentID,
		toolCallID,
	); len(events) != 0 {
		t.Fatalf("rejected completion persisted result events: %+v", events)
	}
}
