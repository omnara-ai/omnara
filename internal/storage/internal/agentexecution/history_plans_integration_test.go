//go:build integration

package agentexecution_test

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestStepReadsStayBoundedWithHistory(t *testing.T) {
	f, _ := stepFixture(t)
	u, _, _, prepared := claimPreparedStep(t, f)
	require.NoError(t, u.Commit(t.Context(), "history seed"))
	tx, err := f.pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(t.Context()) }()
	_, err = tx.Exec(t.Context(), `SET LOCAL session_replication_role=replica`)
	require.NoError(t, err)
	_, err = tx.Exec(t.Context(), `INSERT INTO agents(id,root_agent_id,org_id,project_id,state,
		current_config_id,created_at,updated_at)
 SELECT id,id,$1,$2,'active',$3,statement_timestamp(),
  statement_timestamp() FROM (SELECT gen_random_uuid() id FROM generate_series(1,500)) ids`, f.org, f.project, f.config)
	require.NoError(t, err)
	target := uuid.New()
	_, err = tx.Exec(t.Context(), `INSERT INTO model_call_contexts(id,org_id,project_id,agent_id,
		operation_kind,attempt_number,
 agent_config_id,configured_model_revision_id,input_event_sequence,source_event_sequence_end,
  runtime_lock_id,state,recovery_kind,error_kind,created_at,completed_at,turn_id,opening_input_ids,
  opening_event_sequence)
 SELECT CASE WHEN n=4 THEN $2 ELSE gen_random_uuid() END,c.org_id,c.project_id,c.agent_id,'compaction',n,
  c.agent_config_id,c.configured_model_revision_id,
 c.input_event_sequence,c.input_event_sequence,c.runtime_lock_id,'failed','reduce_compaction_source',
  'context_length',statement_timestamp(),statement_timestamp(),c.turn_id,c.opening_input_ids,c.opening_event_sequence
 FROM model_call_contexts c CROSS JOIN generate_series(1,4) n WHERE c.id=$1`, prepared.Context.ID, target)
	require.NoError(t, err)
	trace := &integrationdb.CountingTx{Tx: tx}
	q := executiondb.New()
	rows := q.LoadExecutionFacts(t.Context(), trace,
		[]executiondb.LoadExecutionFactsParams{{ProjectID: f.project, AgentID: prepared.Context.AgentID,
			TurnID: &prepared.Context.TurnID, MaxContextInputSequence: prepared.Context.InputEventSequence}})
	rows.QueryRow(func(_ int, row executiondb.LoadExecutionFactsRow, err error) {
		require.NoError(t, err)
		require.Equal(t, prepared.Context.Opening.EventSequence, row.FirstOpeningSequence)
	})
	require.NoError(t, rows.Close())
	attempt, err := q.ReadExecutionAttempt(t.Context(), trace,
		executiondb.ReadExecutionAttemptParams{AgentID: prepared.Context.AgentID, ID: target})
	require.NoError(t, err)
	require.EqualValues(t, 3, attempt.SourceExcerptReductionCount)
	external, err := dbsqlc.New(trace).GetModelCallContext(t.Context(),
		dbsqlc.GetModelCallContextParams{ProjectID: f.project, AgentID: prepared.Context.AgentID, ID: target})
	require.NoError(t, err)
	require.EqualValues(t, 3, external.SourceExcerptReductionCount)
	queries := trace.Trace
	require.Len(t, queries, 3)
	baseline := map[string]int{}
	for _, history := range []int{0, 5000} {
		if history > 0 {
			_, err = tx.Exec(t.Context(), `WITH ids AS MATERIALIZED (
 SELECT gen_random_uuid() AS id,gen_random_uuid() AS event_id,n FROM generate_series(1,$3::int) n
 ), inputs AS (
 INSERT INTO agent_inputs(id,project_id,agent_id,state,input_rank,input_kind,delivery_mode,queued_at,
  admitted_event_id,admitted_at,resolved_at)
 SELECT id,$1,$2,'resolved',n,'content','queued',statement_timestamp(),event_id,statement_timestamp(),
  statement_timestamp() FROM ids RETURNING id
 ) INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,agent_input_id,is_opening_event,created_at)
 SELECT event_id,$2,gen_random_uuid(),100+n,'agent_input',id,true,statement_timestamp() FROM ids`,
				f.project, prepared.Context.AgentID, history)
			require.NoError(t, err)
			_, err = tx.Exec(t.Context(), `INSERT INTO model_call_contexts(id,org_id,project_id,agent_id,
				operation_kind,attempt_number,
 agent_config_id,configured_model_revision_id,input_event_sequence,source_event_sequence_end,
  runtime_lock_id,state,recovery_kind,error_kind,created_at,completed_at,turn_id,opening_input_ids,
  opening_event_sequence)
 SELECT gen_random_uuid(),c.org_id,c.project_id,c.agent_id,'compaction',1,c.agent_config_id,
  c.configured_model_revision_id,
 100+n,100+n,c.runtime_lock_id,'failed','reduce_compaction_source','context_length',statement_timestamp(),
  statement_timestamp(),c.turn_id,c.opening_input_ids,c.opening_event_sequence
 FROM model_call_contexts c CROSS JOIN generate_series(1,$2::int) n WHERE c.id=$1`, prepared.Context.ID, history)
			require.NoError(t, err)
		}
		_, err = tx.Exec(t.Context(),
			`ANALYZE agents; ANALYZE agent_events;
 ANALYZE agent_inputs; ANALYZE model_call_contexts`)
		require.NoError(t, err)
		for _, mode := range []string{"force_custom_plan", "force_generic_plan", "auto"} {
			_, err = tx.Exec(t.Context(), "SET LOCAL plan_cache_mode="+mode)
			require.NoError(t, err)
			for i, query := range queries {
				name := fmt.Sprintf("history_%d_%d_%s", history, i, mode)
				_, err = tx.Exec(t.Context(), "PREPARE "+name+" AS "+query.Query.SQL)
				require.NoError(t, err)
				args := make([]string, len(query.Query.Arguments))
				for j, arg := range query.Query.Arguments {
					switch v := arg.(type) {
					case uuid.UUID:
						args[j] = "'" + v.String() + "'"
					case *uuid.UUID:
						if v == nil {
							args[j] = "NULL"
						} else {
							args[j] = "'" + v.String() + "'"
						}
					case int64:
						args[j] = strconv.FormatInt(v, 10)
					default:
						t.Fatalf("unexpected query argument %T", arg)
					}
				}
				statement := "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) EXECUTE " + name + "(" + strings.Join(args, ",") + ")"
				var raw []byte
				for range 7 {
					require.NoError(t, tx.QueryRow(t.Context(), statement).Scan(&raw))
				}
				var plan []struct {
					Plan struct {
						Hit  int `json:"Shared Hit Blocks"`
						Read int `json:"Shared Read Blocks"`
					}
				}
				require.NoError(t, json.Unmarshal(raw, &plan))
				buffers := plan[0].Plan.Hit + plan[0].Plan.Read
				key := fmt.Sprintf("%d/%s", i, mode)
				t.Logf("history=%d query=%s buffers=%d", history, key, buffers)
				require.LessOrEqual(t, buffers, 100, string(raw))
				if history == 0 {
					baseline[key] = buffers
				} else {
					require.LessOrEqual(t, buffers, baseline[key]+20, string(raw))
				}
			}
		}
	}
}
