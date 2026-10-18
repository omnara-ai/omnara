//go:build integration

package executionstore_test

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestPreparedModelCallTranscriptIgnoresLaterEvents(t *testing.T) {
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, "prepared_watermark")
	ids := createToolCallBatchForProcessTest(t, ctx, f, "prepared_watermark", []processToolCallBatchItem{
		builtInProcessToolCallBatchItem("first", "read_process"),
		builtInProcessToolCallBatchItem("second", "read_process"),
	})
	for _, id := range ids {
		ownedCompleteTool(t, ctx, f, id)
	}
	work := reclaimAgentWork(t, ctx, &f)
	require.Equal(t, executionstore.ModelWorkContinue, work.Model.Kind)
	prepared, err := f.Store.Execution().PrepareNormalModelCall(ctx, executionstore.PrepareNormalModelCallInput{
		ProjectID: testProjectID, AgentID: f.AgentID, RuntimeLockID: f.Lock.ID,
		OpeningInputIDs:          work.Model.InputIDs,
		SourceModelCallContextID: work.Model.SourceModelCallContextID,
		SourceModelOutputID:      work.Model.SourceModelOutputID,
	})
	require.NoError(t, err)
	require.True(t, prepared.Claim.Claimed)
	watermark := prepared.Claim.Context.InputEventSequence
	read := func() ([]executionstore.ContextEventRecord, []executionstore.ToolCallRecord) {
		events, err := f.Store.Execution().ListContextEvents(ctx, testProjectID, f.AgentID, 0, watermark, 500)
		require.NoError(t, err)
		calls, err := f.Store.Execution().ListCompletedToolCallsAtWatermark(ctx, testProjectID, f.AgentID, 0, watermark)
		require.NoError(t, err)
		return events, calls
	}
	events, calls := read()
	require.NotEmpty(t, events)
	require.Len(t, calls, 2)
	_, err = f.Store.Execution().RecordModelOutputAndCompleteContext(ctx,
		executionstore.RecordModelOutputAndCompleteContextInput{
			ProjectID: testProjectID, AgentID: f.AgentID, RuntimeLockID: f.Lock.ID,
			ModelCallContextID: prepared.Claim.Context.ID,
			ProviderResponse: modelenvelope.ResponseEnvelope{
				RequestedProviderModelSlug: modelProviderSlugForContext(
					t, ctx, f.Store, testProjectID, f.AgentID, prepared.Claim.Context.ID,
				),
				APIFormat:  modelprotocol.APIFormatOpenAIResponses,
				APIVariant: modelprotocol.APIVariantDefault,
				Normalized: modelenvelope.ResponseNormalized{
					ID:         "resp_after_watermark",
					Content:    []modelenvelope.ResponsePart{{Type: "text", Text: "later output"}},
					StopReason: modelenvelope.StopReasonEndTurn,
				},
			},
		})
	require.NoError(t, err)
	latest, err := f.Store.Execution().MaxEventSequence(ctx, testProjectID, f.AgentID)
	require.NoError(t, err)
	require.Greater(t, latest, watermark)
	laterEvents, laterCalls := read()
	require.Equal(t, events, laterEvents)
	require.Equal(t, calls, laterCalls)
}

const longTranscriptTurns = 350

const longTranscriptFixtureSQL = `
INSERT INTO agent_inputs(id,project_id,agent_id,state,input_kind,delivery_mode,control_type,agent_config_id,queued_at)
SELECT input_id, project_id, agent_id, 'received', 'content', 'queued', NULL, NULL, now() FROM transcript_turns
UNION ALL
SELECT extra_id, project_id, agent_id, 'received',
  CASE extra_kind WHEN 'empty' THEN 'content' WHEN 'control' THEN 'control' ELSE extra_kind END,
  CASE WHEN extra_kind IN ('content', 'empty') THEN 'steering' ELSE 'immediate' END,
  CASE extra_kind WHEN 'control' THEN 'cancel_current' END,
  CASE extra_kind WHEN 'config_change' THEN config_id END, now()
FROM transcript_turns;
INSERT INTO artifacts(id,agent_id,content_type,created_at)
SELECT artifact_id, agent_id, 'image/png', now() FROM transcript_turns WHERE k % 3 = 0;
INSERT INTO content_blocks(agent_id,owner_kind,owner_agent_input_id,ordinal,block_kind,text_content,artifact_id,
  created_at)
SELECT agent_id, 'agent_input', input_id, 0, 'text', 'input ' || k, NULL, now() FROM transcript_turns
UNION ALL
SELECT agent_id, 'agent_input', input_id, 1, 'artifact', NULL, artifact_id, now() FROM transcript_turns WHERE k % 3 = 0
UNION ALL
SELECT agent_id, 'agent_input', extra_id, 0, 'text', 'steer ' || k, NULL, now() FROM transcript_turns
WHERE extra_kind = 'content';
INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,agent_input_id,is_opening_event,idempotency_key,
  created_at)
SELECT input_event_id, agent_id, turn_id, b, 'agent_input', input_id, true, 'agent_input:' || input_id, now()
FROM transcript_turns
UNION ALL
SELECT extra_event_id, agent_id, turn_id, b + 5, 'agent_input', extra_id, false, 'agent_input:' || extra_id, now()
FROM transcript_turns;
UPDATE agent_inputs input SET state = 'resolved', admitted_event_id = admitted.event_id, admitted_at = now(),
  resolved_at = now()
FROM (
  SELECT agent_id, input_id AS id, input_event_id AS event_id FROM transcript_turns
  UNION ALL
  SELECT agent_id, extra_id, extra_event_id FROM transcript_turns WHERE extra_kind <> 'config_change'
) admitted
WHERE input.agent_id = admitted.agent_id AND input.id = admitted.id;
UPDATE agent_inputs input SET state = 'resolved', admitted_event_id = turn.extra_event_id, admitted_at = now(),
  resolved_at = now(), opening_input_ids = ARRAY[turn.input_id], opening_event_sequence = turn.b
FROM transcript_turns turn
WHERE input.agent_id = turn.agent_id AND input.id = turn.extra_id AND input.input_kind = 'config_change';
INSERT INTO agent_turns(id,agent_id,turn_sequence,latest_event_id,latest_semantic_event_id)
SELECT turn_id, agent_id, b, extra_event_id, extra_event_id FROM transcript_turns;
DO $fixture$
DECLARE
  turn record;
BEGIN
  FOR turn IN SELECT * FROM transcript_turns ORDER BY k LOOP
    INSERT INTO model_call_contexts(id,org_id,project_id,agent_id,turn_id,operation_kind,attempt_number,agent_config_id,
      configured_model_revision_id,input_event_sequence,runtime_lock_id,created_at,opening_input_ids,
      opening_event_sequence)
    VALUES (turn.context1_id, turn.org_id, turn.project_id, turn.agent_id, turn.turn_id, 'normal', 1, turn.config_id,
      turn.revision_id, turn.b, turn.context1_id, now(), ARRAY[turn.input_id], turn.b);
    INSERT INTO model_outputs(id,agent_id,model_call_context_id,stop_reason,provider_replay,created_at)
    VALUES (turn.output1_id, turn.agent_id, turn.context1_id, 'tool_use',
      CASE WHEN turn.k % 2 = 0 THEN jsonb_build_object('turn', turn.k) END, now());
    INSERT INTO tool_calls(id,agent_id,model_output_id,provider_call_id,name,input,type,state,created_at)
    VALUES (turn.call_a_id, turn.agent_id, turn.output1_id, 'call_a_' || turn.k, 'read_file',
        jsonb_build_object('k', turn.k), 'built_in', 'awaiting_authorization', now()),
      (turn.call_b_id, turn.agent_id, turn.output1_id, 'call_b_' || turn.k, 'list_files', '{}', 'built_in',
        'awaiting_authorization', now());
    INSERT INTO content_blocks(agent_id,owner_kind,owner_model_output_id,ordinal,block_kind,text_content,tool_call_id,
      created_at)
    VALUES (turn.agent_id, 'model_output', turn.output1_id, 0, 'reasoning', 'thinking ' || turn.k, NULL, now()),
      (turn.agent_id, 'model_output', turn.output1_id, 1, 'text', 'calling ' || turn.k, NULL, now()),
      (turn.agent_id, 'model_output', turn.output1_id, 2, 'tool_call', NULL, turn.call_a_id, now()),
      (turn.agent_id, 'model_output', turn.output1_id, 3, 'tool_call', NULL, turn.call_b_id, now());
    INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,model_output_id,created_at)
    VALUES (turn.output1_event_id, turn.agent_id, turn.turn_id, turn.b + 1, 'model_output', turn.output1_id, now());
    UPDATE model_call_contexts SET state = 'succeeded', api_format = 'openai-responses', api_variant = 'default',
      input_tokens_total = 100 + turn.k, uncached_input_tokens = 100 + turn.k, output_tokens_total = 10,
      reasoning_output_tokens = 5, input_fingerprint = repeat('a', 64), completed_at = now()
    WHERE agent_id = turn.agent_id AND id = turn.context1_id;
    INSERT INTO model_call_contexts(id,org_id,project_id,agent_id,turn_id,operation_kind,attempt_number,agent_config_id,
      configured_model_revision_id,input_event_sequence,runtime_lock_id,created_at,opening_input_ids,
      opening_event_sequence)
    VALUES (turn.context2_id, turn.org_id, turn.project_id, turn.agent_id, turn.turn_id, 'normal', 1, turn.config_id,
      turn.revision_id, turn.b + 3, turn.context2_id, now(), ARRAY[turn.input_id], turn.b);
    IF turn.output2_failed THEN
      UPDATE model_call_contexts SET state = 'failed', error_kind = 'provider', completed_at = now()
      WHERE agent_id = turn.agent_id AND id = turn.context2_id;
      INSERT INTO model_outputs(id,agent_id,model_call_context_id,stop_reason,created_at)
      VALUES (turn.output2_id, turn.agent_id, turn.context2_id, 'error', now());
      INSERT INTO content_blocks(agent_id,owner_kind,owner_model_output_id,ordinal,block_kind,text_content,created_at)
      VALUES (turn.agent_id, 'model_output', turn.output2_id, 0, 'error', 'failed ' || turn.k, now());
    ELSE
      INSERT INTO model_outputs(id,agent_id,model_call_context_id,stop_reason,created_at)
      VALUES (turn.output2_id, turn.agent_id, turn.context2_id, 'end_turn', now());
      IF turn.k % 5 <> 4 THEN
        INSERT INTO content_blocks(agent_id,owner_kind,owner_model_output_id,ordinal,block_kind,text_content,created_at)
        VALUES (turn.agent_id, 'model_output', turn.output2_id, 0, 'text', 'done ' || turn.k, now());
      END IF;
      UPDATE model_call_contexts SET state = 'succeeded', api_format = 'openai-responses', api_variant = 'default',
        input_tokens_total = 200 + turn.k, uncached_input_tokens = 200 + turn.k, output_tokens_total = 10,
        input_fingerprint = repeat('b', 64), completed_at = now()
      WHERE agent_id = turn.agent_id AND id = turn.context2_id;
    END IF;
    INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,model_output_id,created_at)
    VALUES (turn.output2_event_id, turn.agent_id, turn.turn_id, turn.b + 4, 'model_output', turn.output2_id, now());
  END LOOP;
END
$fixture$;
UPDATE tool_calls call SET state = 'completed' FROM transcript_turns turn
WHERE call.agent_id = turn.agent_id AND call.id IN (turn.call_a_id, turn.call_b_id);
INSERT INTO tool_call_results(id,agent_id,tool_call_id,outcome,completed_at)
SELECT result_a_id, agent_id, call_a_id, 'succeeded', now() FROM transcript_turns
UNION ALL
SELECT result_b_id, agent_id, call_b_id, 'failed', now() FROM transcript_turns;
INSERT INTO content_blocks(agent_id,owner_kind,owner_tool_call_result_id,ordinal,block_kind,text_content,
  structured_data,exclude_from_model_context,created_at)
SELECT agent_id, 'tool_call_result', result_a_id, 0, 'text', 'result ' || k, NULL, false, now() FROM transcript_turns
UNION ALL
SELECT agent_id, 'tool_call_result', result_a_id, 1, 'structured_data', NULL, jsonb_build_object('k', k), false, now()
FROM transcript_turns
UNION ALL
SELECT agent_id, 'tool_call_result', result_b_id, 0, 'text', 'hidden ' || k, NULL, true, now() FROM transcript_turns;
INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,tool_call_result_id,created_at)
SELECT result_a_event_id, agent_id, turn_id, b + 2, 'tool_result', result_a_id, now() FROM transcript_turns
UNION ALL
SELECT result_b_event_id, agent_id, turn_id, b + 3, 'tool_result', result_b_id, now() FROM transcript_turns`

const multiTenantTranscriptStatistics = `
ALTER TABLE agent_events ALTER COLUMN agent_id SET (n_distinct = -0.02);
ALTER TABLE agent_inputs ALTER COLUMN agent_id SET (n_distinct = -0.02);
ALTER TABLE model_outputs ALTER COLUMN agent_id SET (n_distinct = -0.02);
ALTER TABLE model_call_contexts ALTER COLUMN agent_id SET (n_distinct = -0.02),
  ALTER COLUMN project_id SET (n_distinct = -0.1);
ALTER TABLE tool_calls ALTER COLUMN agent_id SET (n_distinct = -0.02);
ALTER TABLE tool_call_results ALTER COLUMN agent_id SET (n_distinct = -0.02);
ALTER TABLE content_blocks ALTER COLUMN agent_id SET (n_distinct = -0.02);
ANALYZE agents, agent_events, agent_inputs, model_outputs, model_call_contexts, tool_calls, tool_call_results,
  content_blocks`

func TestContextEventsPageLongTranscript(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store, agentID, watermark := newLongTranscriptAgent(t, ctx)
	var events []executionstore.ContextEventRecord
	pages := 0
	for after := int64(0); after < watermark; after = events[len(events)-1].Sequence {
		page, err := store.Execution().ListContextEvents(ctx, testProjectID, agentID, after, watermark, 500)
		require.NoError(t, err)
		events = append(events, page...)
		pages++
		if len(page) < 500 {
			break
		}
	}
	require.Equal(t, 3, pages)
	for i := range events {
		events[i].CreatedAt = time.Time{}
		events[i].ContentParts = canonicalJSON(t, events[i].ContentParts)
		events[i].ProviderReplay = canonicalJSON(t, events[i].ProviderReplay)
	}
	require.Equal(t, expectedLongTranscriptEvents(t, ctx, store, agentID), events)
	query := integrationdb.GeneratedQuery(t, "../internal/dbsqlc/agent_turns.sql.go", "ListContextEvents")
	requireOutputBoundedPlan(t, ctx, store.pool, query, agentID, int64(0), watermark, testProjectID, 50)
}

func TestTranscriptReadsStayAgentBoundUnderGenericPlans(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store, agentID, watermark := newLongTranscriptAgent(t, ctx)
	callID := longTranscriptID(agentID, "call_a", longTranscriptTurns-1)
	tx := integrationdb.BeginTx(t, ctx, store.pool)
	_, err := tx.Exec(ctx, multiTenantTranscriptStatistics+"; SET LOCAL plan_cache_mode = force_generic_plan")
	require.NoError(t, err)
	for _, read := range []struct{ file, name, args string }{
		{"agent_turns", "ListContextEvents", fmt.Sprintf("'%s', 0, %d, '%s', 50", agentID, watermark, testProjectID)},
		{"tool_calls", "ListCompletedToolCallsAtWatermark",
			fmt.Sprintf("%d, %d, '%s', '%s'", watermark-12, watermark, testProjectID, agentID)},
		{"tool_calls", "GetToolCall", fmt.Sprintf("'%s', '%s', '%s'", testProjectID, agentID, callID)},
		{"agent_machine_bindings", "GetToolCallAgentConfigID",
			fmt.Sprintf("'%s', '%s', '%s'", testProjectID, agentID, callID)},
	} {
		query := integrationdb.GeneratedQuery(t, "../internal/dbsqlc/"+read.file+".sql.go", read.name)
		_, err := tx.Exec(ctx, "PREPARE transcript_read AS "+query)
		require.NoError(t, err, read.name)
		requireOutputBoundedPlan(t, ctx, tx, "EXECUTE transcript_read("+read.args+")")
		_, err = tx.Exec(ctx, "DEALLOCATE transcript_read")
		require.NoError(t, err)
	}
}

func requireOutputBoundedPlan(
	t *testing.T,
	ctx context.Context,
	db interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	statement string,
	args ...any,
) {
	t.Helper()
	var raw []byte
	require.NoError(t, db.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+statement, args...).Scan(&raw))
	rows := int(integrationdb.ReadPlan(t, raw).Rows)
	require.Positive(t, rows, "%s", raw)
	require.LessOrEqual(t, integrationdb.PlanBuffers(t, raw), 40*rows+40, "%s", raw)
}

func newLongTranscriptAgent(t *testing.T, ctx context.Context) (*Store, uuid.UUID, int64) {
	t.Helper()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	agentID := mustCreateAgent(t, ctx, store)
	return store, agentID, createLongTranscriptForTest(t, ctx, store, agentID)
}

func longTranscriptID(agentID uuid.UUID, name string, turn int) uuid.UUID {
	return uuid.UUID(md5.Sum(fmt.Appendf(nil, "%s:%s:%d", agentID, name, turn)))
}

func canonicalJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, ok := value.(json.RawMessage)
	if !ok {
		var err error
		raw, err = json.Marshal(value)
		require.NoError(t, err)
	}
	if raw == nil {
		return nil
	}
	var decoded any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	canonical, err := json.Marshal(decoded)
	require.NoError(t, err)
	return canonical
}

func expectedLongTranscriptEvents(
	t *testing.T,
	ctx context.Context,
	store *Store,
	agentID uuid.UUID,
) []executionstore.ContextEventRecord {
	t.Helper()
	model := executionstore.ContextEventRecord{ProjectID: testProjectID, AgentID: agentID,
		Role: modelprotocol.RoleAssistant}
	require.NoError(t, store.pool.QueryRow(ctx, `
SELECT agent.current_config_id, model.current_revision_id, revision.model_provider_config_id,
  revision.provider_model_slug
FROM agents agent
JOIN agent_configs config ON config.project_id = agent.project_id AND config.id = agent.current_config_id
JOIN configured_models model ON model.org_id = config.org_id AND model.id = config.configured_model_id
JOIN configured_model_revisions revision ON revision.org_id = model.org_id
  AND revision.id = model.current_revision_id
WHERE agent.id = $1`, agentID).Scan(&model.AgentConfigID, &model.ConfiguredModelRevisionID,
		&model.ModelProviderConfigID, &model.RequestedModelSlug))
	part := func(kind, field string, value any) map[string]any { return map[string]any{"type": kind, field: value} }
	var events []executionstore.ContextEventRecord
	for turn := range longTranscriptTurns {
		id := func(name string) uuid.UUID { return longTranscriptID(agentID, name, turn) }
		event := func(name string, sequence int64, parts ...any) executionstore.ContextEventRecord {
			return executionstore.ContextEventRecord{ID: id(name), SourceEventID: id(name), ProjectID: testProjectID,
				AgentID: agentID, Role: modelprotocol.RoleUser, Sequence: sequence,
				ContentParts: canonicalJSON(t, append([]any{}, parts...))}
		}
		output := func(name string, sequence int64, contextName string, inputSequence int64,
			stopReason modelenvelope.StopReason, parts ...any,
		) executionstore.ContextEventRecord {
			record := model
			record.ID, record.SourceEventID, record.Sequence = id(name+"_event"), id(name+"_event"), sequence
			record.ModelOutputID, record.ModelCallContextID = id(name), id(contextName)
			record.InputEventSequence, record.StopReason = inputSequence, stopReason
			record.ContentParts = canonicalJSON(t, append([]any{}, parts...))
			if stopReason != modelenvelope.StopReasonError {
				record.APIFormat, record.APIVariant = modelprotocol.APIFormatOpenAIResponses, modelprotocol.APIVariantDefault
			}
			return record
		}
		base := int64(2 + 6*turn)
		parts := []any{part("text", "text", fmt.Sprintf("input %d", turn))}
		if turn%3 == 0 {
			parts = append(parts, part("media_ref", "artifact_id", id("artifact")))
		}
		opening := event("input_event", base, parts...)
		opening.AgentInputID = id("input")
		toolUse := output("output1", base+1, "context1", base, modelenvelope.StopReasonToolUse,
			part("reasoning", "text", fmt.Sprintf("thinking %d", turn)), part("text", "text", fmt.Sprintf("calling %d", turn)),
			part("tool_call", "tool_call_id", id("call_a")), part("tool_call", "tool_call_id", id("call_b")))
		toolUse.InputFingerprint = strings.Repeat("a", 64)
		toolUse.Usage = modelenvelope.Usage{InputTokens: 100 + turn, UncachedInputTokens: 100 + turn,
			OutputTokens: 10, ReasoningTokens: 5}
		if turn%2 == 0 {
			toolUse.ProviderReplay = canonicalJSON(t, map[string]any{"turn": turn})
		}
		var answer executionstore.ContextEventRecord
		switch {
		case turn%7 == 3:
			answer = output("output2", base+4, "context2", base+3, modelenvelope.StopReasonError,
				part("error", "text", fmt.Sprintf("failed %d", turn)))
		case turn%5 == 4:
			answer = output("output2", base+4, "context2", base+3, modelenvelope.StopReasonEndTurn)
		default:
			answer = output("output2", base+4, "context2", base+3, modelenvelope.StopReasonEndTurn,
				part("text", "text", fmt.Sprintf("done %d", turn)))
		}
		if answer.StopReason != modelenvelope.StopReasonError {
			answer.InputFingerprint = strings.Repeat("b", 64)
			answer.Usage = modelenvelope.Usage{InputTokens: 200 + turn, UncachedInputTokens: 200 + turn, OutputTokens: 10}
		}
		events = append(events, opening, toolUse, answer)
		switch turn % 4 {
		case 0:
			steering := event("extra_event", base+5, part("text", "text", fmt.Sprintf("steer %d", turn)))
			steering.AgentInputID = id("extra")
			events = append(events, steering)
		case 1:
			configChange := event("extra_event", base+5, part("text", "text",
				"Agent configuration changed. The current system prompt, model, and tool policy are reflected in this model call."))
			configChange.AgentInputID = id("extra")
			events = append(events, configChange)
		}
	}
	return events
}

func createLongTranscriptForTest(t *testing.T, ctx context.Context, store *Store, agentID uuid.UUID) int64 {
	t.Helper()
	tx := integrationdb.BeginTx(t, ctx, store.pool)
	_, err := tx.Exec(ctx, "SET LOCAL plan_cache_mode = force_custom_plan")
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `
CREATE TEMP TABLE transcript_turns ON COMMIT DROP AS
SELECT k, agent.id AS agent_id, agent.org_id, agent.project_id, agent.current_config_id AS config_id,
  model.current_revision_id AS revision_id,
  coalesce((SELECT max(sequence) FROM agent_events WHERE agent_id = agent.id), 0) + 1 + 6 * k AS b,
  md5(agent.id || ':turn:' || k)::uuid AS turn_id, md5(agent.id || ':input:' || k)::uuid AS input_id,
  md5(agent.id || ':input_event:' || k)::uuid AS input_event_id,
  md5(agent.id || ':artifact:' || k)::uuid AS artifact_id,
  md5(agent.id || ':context1:' || k)::uuid AS context1_id, md5(agent.id || ':output1:' || k)::uuid AS output1_id,
  md5(agent.id || ':output1_event:' || k)::uuid AS output1_event_id,
  md5(agent.id || ':call_a:' || k)::uuid AS call_a_id, md5(agent.id || ':call_b:' || k)::uuid AS call_b_id,
  md5(agent.id || ':result_a:' || k)::uuid AS result_a_id, md5(agent.id || ':result_b:' || k)::uuid AS result_b_id,
  md5(agent.id || ':result_a_event:' || k)::uuid AS result_a_event_id,
  md5(agent.id || ':result_b_event:' || k)::uuid AS result_b_event_id,
  md5(agent.id || ':context2:' || k)::uuid AS context2_id, md5(agent.id || ':output2:' || k)::uuid AS output2_id,
  md5(agent.id || ':output2_event:' || k)::uuid AS output2_event_id,
  md5(agent.id || ':extra:' || k)::uuid AS extra_id, md5(agent.id || ':extra_event:' || k)::uuid AS extra_event_id,
  CASE k % 4 WHEN 0 THEN 'content' WHEN 1 THEN 'config_change' WHEN 2 THEN 'empty' ELSE 'control' END AS extra_kind,
  k % 7 = 3 AS output2_failed
FROM agents agent
JOIN agent_configs config ON config.project_id = agent.project_id AND config.id = agent.current_config_id
JOIN configured_models model ON model.org_id = config.org_id AND model.id = config.configured_model_id
CROSS JOIN generate_series(0, $2::int - 1) k
WHERE agent.id = $1`, agentID, longTranscriptTurns)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, longTranscriptFixtureSQL)
	require.NoError(t, err)
	var watermark int64
	require.NoError(t, tx.QueryRow(ctx, `SELECT max(sequence) FROM agent_events WHERE agent_id = $1`, agentID).
		Scan(&watermark))
	require.NoError(t, tx.Commit(ctx))
	return watermark
}
