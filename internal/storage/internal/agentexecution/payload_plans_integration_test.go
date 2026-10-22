//go:build integration

package agentexecution_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/stretchr/testify/require"
)

func TestExecutionPayloadInsertsUseGenericPlans(t *testing.T) {
	f, a, _ := commandFixture(t)
	u, h := f.handle(t, a)
	for range 20 {
		receiveCommand(t, h, "steering", "payload")
		_, err := h.AdmitInputs(t.Context())
		require.NoError(t, err)
	}
	for _, name := range []string{"InsertExecutionContent", "AppendExecutionEvent"} {
		var generic, custom int64
		require.NoError(
			t,
			u.DB().
				QueryRow(t.Context(),
					`SELECT generic_plans,custom_plans FROM pg_prepared_statements WHERE statement LIKE $1`,
					"-- name: "+name+" :%").
				Scan(&generic, &custom),
		)
		require.GreaterOrEqual(t, generic, int64(15), name)
		require.LessOrEqual(t, custom, int64(5), name)
	}
}

func TestExecutionPayloadBatchOrdinalsAndTimestamps(t *testing.T) {
	f, a, lease := commandFixture(t)
	u, h := f.handle(t, a)
	received, err := h.ReceiveContent(t.Context(), agentexecution.ReceiveContentInput{
		DeliveryMode: "queued", Content: []agentexecution.Content{
			{Ordinal: 9, Kind: "text", Text: "first"}, {Ordinal: 7, Kind: "text", Text: "second"},
		},
	})
	require.NoError(t, err)
	require.NoError(t, u.Commit(t.Context(), "receive batch"))
	u, h = f.handle(t, a)
	_, err = h.AdmitInputs(t.Context())
	require.NoError(t, err)
	prepared := prepareCommand(t, h, lease)
	require.NoError(t, u.Commit(t.Context(), "prepare"))
	var ordinals []int32
	var same, later bool
	require.NoError(t, f.pool.QueryRow(t.Context(), `
SELECT array_agg(b.ordinal ORDER BY b.ordinal),bool_and(b.created_at=i.queued_at),bool_and(e.created_at>i.queued_at)
 FROM agent_inputs i JOIN content_blocks b ON b.owner_agent_input_id=i.id AND b.agent_id=i.agent_id
 JOIN agent_events e ON e.agent_input_id=i.id AND e.agent_id=i.agent_id WHERE i.id=$1`,
		received.ID).Scan(&ordinals, &same, &later))
	require.Equal(t, []int32{0, 1}, ordinals)
	require.True(t, same)
	require.True(t, later)
	response := commandResponse(modelenvelope.StopReasonEndTurn)
	response.Normalized.Content = []modelenvelope.ResponsePart{
		{Type: "text", Text: "first"}, {Type: "text"},
		{Type: "reasoning", Text: "third"}, {Type: "text", Text: "fourth"},
	}
	input := agentexecution.PrepareOutput(agentexecution.AcceptOutputInput{
		ContextID: prepared.Context.ID, RuntimeLockID: lease, Response: response,
	})
	u, h = f.handle(t, a)
	trace, err := agentexecution.TraceExecutionCommand(u, func() error {
		_, err := h.AcceptOutput(t.Context(), input)
		return err
	})
	require.NoError(t, err)
	inserts := 0
	for _, statement := range trace {
		if strings.Contains(statement.Query.SQL, "name: InsertExecutionContent") ||
			strings.Contains(statement.Query.SQL, "name: QueueExecutionContent") {
			inserts++
		}
	}
	require.Equal(t, 1, inserts)
	require.NoError(t, u.Commit(t.Context(), "accept batch"))
	var output uuid.UUID
	require.NoError(t, f.pool.QueryRow(t.Context(), `
SELECT o.id,array_agg(b.ordinal ORDER BY b.ordinal),bool_and(b.created_at=o.created_at AND e.created_at=o.created_at)
 FROM model_outputs o JOIN content_blocks b ON b.owner_model_output_id=o.id AND b.agent_id=o.agent_id
 JOIN agent_events e ON e.model_output_id=o.id AND e.agent_id=o.agent_id
 WHERE o.model_call_context_id=$1 GROUP BY o.id`,
		prepared.Context.ID).Scan(&output, &ordinals, &same))
	require.Equal(t, []int32{0, 2, 3}, ordinals)
	require.True(t, same)
	_, err = f.pool.Exec(t.Context(), `
INSERT INTO content_blocks(agent_id,owner_kind,owner_model_output_id,ordinal,block_kind,text_content,created_at)
 VALUES($1,'model_output',$2,4,'text','late',statement_timestamp())`, a, output)
	require.ErrorContains(t, err, "writable model output")
}

func TestExecutionPayloadScopeAndOwnership(t *testing.T) {
	f, a, _ := commandFixture(t)
	other := f.create(t, uuid.New(), uuid.Nil)
	u, h := f.handle(t, a)
	received, err := h.ReceiveContent(t.Context(), agentexecution.ReceiveContentInput{DeliveryMode: "queued"})
	require.NoError(t, err)
	require.NoError(t, u.Commit(t.Context(), "receive owner"))
	for _, tc := range []struct {
		name           string
		project, agent uuid.UUID
		rows           int64
		fails          bool
	}{
		{"scope", uuid.New(), a, 0, true}, {"owner", f.project, other, 0, true}, {"valid", f.project, a, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := f.pool.Begin(t.Context())
			require.NoError(t, err)
			defer func() { _ = tx.Rollback(t.Context()) }()
			rows, err := executiondb.New().InsertExecutionContent(t.Context(), tx, executiondb.InsertExecutionContentParams{
				ProjectID: tc.project, AgentID: tc.agent, OwnerKind: "agent_input", InputID: &received.ID,
				Parts: json.RawMessage(`[{"ordinal":0,"kind":"text","text":"one","exclude":false,"metadata":{}},{"ordinal":1,"kind":"text","text":"two","exclude":false,"metadata":{}}]`),
			})
			if tc.fails {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.rows, rows)
				var same bool
				require.NoError(t, tx.QueryRow(t.Context(), `
SELECT bool_and(b.created_at=i.queued_at) FROM content_blocks b
 JOIN agent_inputs i ON i.agent_id=b.agent_id AND i.id=b.owner_agent_input_id WHERE i.id=$1`,
					received.ID).Scan(&same))
				require.True(t, same)
			}
		})
	}
}

func TestToolContentBatchPreservesJSONNullAndTimestamps(t *testing.T) {
	f, a, lease := commandFixture(t)
	u, h := f.handle(t, a)
	ref := toolCommand(t, h, lease, "built_in")
	completed, err := h.CompleteTool(t.Context(), agentexecution.ToolCompletion{
		ToolRef: ref, Authorize: true, Outcome: "succeeded", Content: []agentexecution.Content{
			{Kind: "structured_data", Data: json.RawMessage(`null`)},
			{Kind: "structured_data", Data: json.RawMessage(`{"result":true}`)},
			{Kind: "text", Text: "done"},
		},
	})
	require.NoError(t, err)
	require.NoError(t, u.Commit(t.Context(), "tool content batch"))
	var ordinals []int32
	var same, rawNull bool
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT array_agg(b.ordinal ORDER BY b.ordinal),
 bool_and(b.created_at=r.completed_at AND e.created_at=r.completed_at),
 bool_or(b.structured_data='null'::jsonb)
 FROM tool_call_results r JOIN content_blocks b ON b.agent_id=r.agent_id AND b.owner_tool_call_result_id=r.id
 JOIN agent_events e ON e.agent_id=r.agent_id AND e.tool_call_result_id=r.id WHERE r.id=$1`,
		completed.ResultID).Scan(&ordinals, &same, &rawNull))
	require.Equal(t, []int32{0, 1, 2}, ordinals)
	require.True(t, same)
	require.True(t, rawNull)
}
