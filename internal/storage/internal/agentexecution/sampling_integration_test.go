//go:build integration

package agentexecution_test

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func sampleHistory(t *testing.T, f executionFixture, turns int) uuid.UUID {
	t.Helper()
	agent := f.agent(t)
	for i := range turns {
		sequence := int64(2*i + 1)
		turn, _ := f.opening(t, agent, sequence, uuid.Nil, "content")
		model := f.modelContext(t, agent, sequence, "normal", nil, 1)
		f.output(t, agent, turn, model, sequence+1, "end_turn", "")
	}
	return agent
}

func TestExecutionHeadSampleBoundedHistory(t *testing.T) {
	f := releasedExecutionFixture(t)
	short := sampleHistory(t, f, 1)
	long := sampleHistory(t, f, 500)
	require.NoError(t, applyExecutionCutover(t.Context(), f.pool))
	_, err := f.pool.Exec(t.Context(), "ANALYZE")
	require.NoError(t, err)
	var counts, buffers []int
	for _, agent := range []uuid.UUID{short, long} {
		tx, err := f.pool.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		require.NoError(t, err)
		defer func() { _ = tx.Rollback(t.Context()) }()
		counted := &integrationdb.CountingTx{Tx: tx}
		differences, err := agentexecution.CompareExecutionHead(t.Context(), counted, agentexecution.AgentRoute{
			AgentID: agent, ProjectID: f.project, RootAgentID: agent})
		require.NoError(t, err)
		require.Empty(t, differences)
		total := 0
		for _, statement := range counted.Trace {
			var plan []byte
			require.NoError(t, tx.QueryRow(t.Context(), "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+statement.Query.SQL,
				statement.Query.Arguments...).Scan(&plan))
			total += integrationdb.PlanBuffers(t, plan)
			t.Logf("agent=%s query=%s buffers=%d plan=%s", agent, strings.Split(statement.Query.SQL, "\n")[0],
				integrationdb.PlanBuffers(t, plan), plan)
		}
		require.NoError(t, tx.Rollback(t.Context()))
		counts = append(counts, counted.Statements)
		buffers = append(buffers, total)
	}
	t.Logf("turns=1 statements=%d buffers=%d; turns=500 statements=%d buffers=%d",
		counts[0], buffers[0], counts[1], buffers[1])
	require.LessOrEqual(t, counts[1], counts[0]+1)
	require.LessOrEqual(t, buffers[1], buffers[0]+40)
	require.LessOrEqual(t, buffers[1], 250)
}

func TestExecutionHeadSamplerDriftAndNoLocks(t *testing.T) {
	f := releasedExecutionFixture(t)
	clean := sampleHistory(t, f, 3)
	corrupt := sampleHistory(t, f, 2)
	require.NoError(t, applyExecutionCutover(t.Context(), f.pool))
	_, err := f.pool.Exec(t.Context(), `UPDATE agent_execution_state
SET stop_sequence=stop_sequence+1,normal_context_id=NULL WHERE agent_id=$1`, corrupt)
	require.NoError(t, err)
	locked, err := f.pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = locked.Rollback(t.Context()) }()
	_, err = locked.Exec(t.Context(),
		"SELECT a.id FROM agents a JOIN agent_execution_state h ON h.agent_id=a.id FOR UPDATE OF a,h")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	report, err := executionstore.New(f.pool, executionstore.Config{}).SampleExecutionHeads(ctx, uuid.Nil, 10)
	require.NoError(t, err)
	require.Len(t, report.Samples, 2)
	for _, sample := range report.Samples {
		require.NoError(t, sample.Err)
		if sample.AgentID == clean {
			require.Empty(t, sample.Differences)
			continue
		}
		require.Equal(t, corrupt, sample.AgentID)
		var fields []string
		for _, diff := range sample.Differences {
			fields = append(fields, diff.Field)
		}
		require.ElementsMatch(t, []string{"stop_sequence", "normal_context_id"}, fields)
	}
}

func TestExecutionHeadSamplerTimeoutIsolation(t *testing.T) {
	f := releasedExecutionFixture(t)
	slow := sampleHistory(t, f, 1)
	clean := sampleHistory(t, f, 1)
	require.NoError(t, applyExecutionCutover(t.Context(), f.pool))
	rows, err := f.pool.Query(t.Context(), `SELECT column_name FROM information_schema.columns
WHERE table_name='model_call_contexts' AND table_schema='public' ORDER BY ordinal_position`)
	require.NoError(t, err)
	columns, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	for i, column := range columns {
		columns[i] = pgx.Identifier{column}.Sanitize()
		if column == "state" {
			columns[i] = "sample_context_state(agent_id,state) AS state"
		}
	}
	_, err = f.pool.Exec(t.Context(), fmt.Sprintf(`
CREATE FUNCTION sample_context_state(agent uuid,state text) RETURNS text LANGUAGE plpgsql AS $f$
 BEGIN
 IF current_setting('transaction_read_only')<>'on' OR current_setting('transaction_isolation')<>'repeatable read' THEN
 RAISE EXCEPTION 'not a read-only snapshot';
 END IF;
 IF current_setting('transaction_timeout')<>'5s' THEN RAISE EXCEPTION 'unbounded snapshot'; END IF;
 IF agent='%s'::uuid THEN PERFORM pg_sleep(3); END IF;
 RETURN state;
 END $f$;
 ALTER TABLE model_call_contexts RENAME TO sample_contexts;
 CREATE VIEW model_call_contexts AS SELECT %s FROM sample_contexts`, slow, strings.Join(columns, ",")))
	require.NoError(t, err)
	report, err := executionstore.New(f.pool, executionstore.Config{}).SampleExecutionHeads(t.Context(), uuid.Nil, 10)
	require.NoError(t, err)
	require.Len(t, report.Samples, 2)
	for _, sample := range report.Samples {
		if sample.AgentID == slow {
			require.ErrorContains(t, sample.Err, "57014")
			continue
		}
		require.Equal(t, clean, sample.AgentID)
		require.NoError(t, sample.Err)
		require.Empty(t, sample.Differences)
	}
}

func TestExecutionHeadSamplingIndexPlan(t *testing.T) {
	f := migratedExecutionFixture(t)
	_, err := f.pool.Exec(t.Context(), `INSERT INTO agents(
id,root_agent_id,org_id,project_id,current_config_id,state,created_at,updated_at,archived_at)
 SELECT id,id,$1,$2,$3,CASE WHEN n%2=0 THEN 'active' ELSE 'archived' END,now(),now(),
 CASE WHEN n%2=0 THEN NULL ELSE now() END
 FROM (SELECT n,uuidv7() AS id FROM generate_series(1,10000) n) seeds`, f.org, f.project, f.config)
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(), `INSERT INTO agent_execution_state(agent_id,incomplete_tools)
SELECT id,false FROM agents; ANALYZE agents; ANALYZE agent_execution_state`)
	require.NoError(t, err)
	query := integrationdb.GeneratedQuery(t, "internal/executiondb/sampling.sql.go", "ExecutionSampleCandidates")
	tx, err := f.pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(t.Context()) }()
	_, err = tx.Exec(t.Context(), "SET LOCAL plan_cache_mode=force_generic_plan")
	require.NoError(t, err)
	_, err = tx.Exec(t.Context(), "PREPARE sample_plan(uuid,int) AS "+query)
	require.NoError(t, err)
	var plan []byte
	require.NoError(t, tx.QueryRow(t.Context(), `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)
EXECUTE sample_plan('00000000-0000-0000-0000-000000000000',10)`).Scan(&plan))
	t.Logf("selection plan=%s", plan)
	indexed := false
	integrationdb.ReadPlan(t, plan).Walk(func(node integrationdb.PlanNode) {
		require.NotEqual(t, "Sort", node.Type)
		require.NotEqual(t, "Seq Scan", node.Type)
		if node.Relation == "agents" {
			require.Equal(t, "agents_pkey", node.Index)
			indexed = true
		}
	})
	require.True(t, indexed)
	require.LessOrEqual(t, integrationdb.PlanBuffers(t, plan), 70)
}

func TestExecutionHeadSampleBoundedTurnOutputs(t *testing.T) {
	f := releasedExecutionFixture(t)
	agents := make([]uuid.UUID, 2)
	for i, outputs := range []int{1, 100} {
		agent := f.agent(t)
		agents[i] = agent
		turn, _ := f.opening(t, agent, 1, uuid.Nil, "content")
		for j := range outputs {
			watermark := int64(2*j + 1)
			model := f.modelContext(t, agent, watermark, "normal", nil, 1)
			_, call := f.output(t, agent, turn, model, watermark+1, "tool_use", "awaiting_authorization")
			f.complete(t, agent, turn, call, watermark+2)
		}
		model := f.modelContext(t, agent, int64(2*outputs+1), "normal", nil, 1)
		f.output(t, agent, turn, model, int64(2*outputs+2), "end_turn", "")
	}
	require.NoError(t, applyExecutionCutover(t.Context(), f.pool))
	for _, agent := range agents {
		tx, err := f.pool.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		require.NoError(t, err)
		counted := &integrationdb.CountingTx{Tx: tx}
		differences, err := agentexecution.CompareExecutionHead(t.Context(), counted, agentexecution.AgentRoute{
			AgentID: agent, ProjectID: f.project, RootAgentID: agent})
		require.NoError(t, err)
		require.Empty(t, differences)
		require.LessOrEqual(t, counted.Statements, 12)
		t.Logf("agent=%s statements=%d", agent, counted.Statements)
		require.NoError(t, tx.Rollback(t.Context()))
	}
}

func TestExecutionHeadSampleIneligibleIsDistinctFromMissingFacts(t *testing.T) {
	f := migratedExecutionFixture(t)
	agent := f.create(t, uuid.New(), uuid.Nil)
	_, err := f.pool.Exec(t.Context(), "UPDATE agents SET state='archived',archived_at=now() WHERE id=$1", agent)
	require.NoError(t, err)
	tx, err := f.pool.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(t.Context()) }()
	_, err = agentexecution.CompareExecutionHead(t.Context(), tx, agentexecution.AgentRoute{
		ProjectID: f.project, AgentID: agent, RootAgentID: agent})
	require.ErrorIs(t, err, agentexecution.ErrSampleIneligible)
	_, err = agentexecution.CompareExecutionHead(t.Context(), tx, agentexecution.AgentRoute{
		ProjectID: f.project, AgentID: uuid.New()})
	require.ErrorIs(t, err, pgx.ErrNoRows)
	require.NotErrorIs(t, err, agentexecution.ErrSampleIneligible)
}

func TestExecutionHeadSamplerMissingHead(t *testing.T) {
	f := migratedExecutionFixture(t)
	agent := f.create(t, uuid.New(), uuid.Nil)
	_, err := f.pool.Exec(t.Context(), "DELETE FROM agent_execution_state WHERE agent_id=$1", agent)
	require.NoError(t, err)
	report, err := executionstore.New(f.pool, executionstore.Config{}).SampleExecutionHeads(t.Context(), uuid.Nil, 10)
	require.NoError(t, err)
	require.Len(t, report.Samples, 1)
	require.NoError(t, report.Samples[0].Err)
	require.Equal(t, []agentexecution.HeadDifference{{Field: "head_missing", Stored: true, Rebuilt: false}},
		report.Samples[0].Differences)
}

func TestExecutionHeadSamplerMissingReadiness(t *testing.T) {
	for _, work := range []string{"input", "tool"} {
		t.Run(work, func(t *testing.T) {
			var f executionFixture
			var agent uuid.UUID
			if work == "input" {
				f = migratedExecutionFixture(t)
				agent = f.create(t, uuid.New(), uuid.Nil)
				unit, handle := f.handle(t, agent)
				receiveCommand(t, handle, "queued", "pending")
				require.NoError(t, unit.Commit(t.Context(), "receive"))
			} else {
				f = releasedExecutionFixture(t)
				agent = f.agent(t)
				turn, _ := f.opening(t, agent, 1, uuid.Nil, "content")
				model := f.modelContext(t, agent, 1, "normal", nil, 1)
				f.output(t, agent, turn, model, 2, "tool_use", "awaiting_authorization")
				require.NoError(t, applyExecutionCutover(t.Context(), f.pool))
			}
			store := executionstore.New(f.pool, executionstore.Config{})
			report, err := store.SampleExecutionHeads(t.Context(), uuid.Nil, 10)
			require.NoError(t, err)
			require.Len(t, report.Samples, 1)
			require.NoError(t, report.Samples[0].Err)
			require.Empty(t, report.Samples[0].Differences)
			tag, err := f.pool.Exec(t.Context(),
				"UPDATE agent_execution_state SET logical_ready_at=NULL WHERE agent_id=$1 AND logical_ready_at IS NOT NULL", agent)
			require.NoError(t, err)
			require.EqualValues(t, 1, tag.RowsAffected())
			report, err = store.SampleExecutionHeads(t.Context(), uuid.Nil, 10)
			require.NoError(t, err)
			require.Len(t, report.Samples, 1)
			require.NoError(t, report.Samples[0].Err)
			require.Len(t, report.Samples[0].Differences, 1)
			difference := report.Samples[0].Differences[0]
			require.Equal(t, "logical_ready_at", difference.Field)
			require.Nil(t, difference.Stored)
			require.NotNil(t, difference.Rebuilt)
		})
	}
}

func TestExecutionHeadSamplesRotateActiveAgents(t *testing.T) {
	for _, active := range []int{0, 3, 23} {
		t.Run(fmt.Sprint(active), func(t *testing.T) {
			f := migratedExecutionFixture(t)
			ids := make([]uuid.UUID, max(1, active)*100)
			for i := range ids {
				ids[i] = uuid.New()
				if i%2 == 0 {
					var err error
					ids[i], err = uuid.NewV7()
					require.NoError(t, err)
				}
			}
			slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
			_, err := f.pool.Exec(t.Context(), `INSERT INTO agents(
id,root_agent_id,org_id,project_id,current_config_id,created_at,updated_at,state,archived_at)
SELECT id,id,$1,$2,$3,now(),now(),CASE WHEN n%100=0 AND $5>0 THEN 'active' ELSE 'archived' END,
CASE WHEN n%100=0 AND $5>0 THEN NULL ELSE now() END
FROM unnest($4::uuid[]) WITH ORDINALITY AS seeds(id,n)`, f.org, f.project, f.config, ids, active)
			require.NoError(t, err)
			_, err = f.pool.Exec(t.Context(), `INSERT INTO agent_execution_state(agent_id,incomplete_tools)
SELECT id,false FROM agents WHERE state='active'`)
			require.NoError(t, err)
			var expected []uuid.UUID
			for i := range active {
				expected = append(expected, ids[(i+1)*100-1])
			}
			store := executionstore.New(f.pool, executionstore.Config{})
			var cursor uuid.UUID
			offset := 0
			for range 4 {
				report, err := store.SampleExecutionHeads(t.Context(), cursor, 10)
				require.NoError(t, err)
				require.Len(t, report.Samples, min(10, active))
				for _, sample := range report.Samples {
					require.NoError(t, sample.Err)
					require.Empty(t, sample.Differences)
					require.Equal(t, expected[offset%active], sample.AgentID)
					offset++
				}
				if active > 0 {
					require.Equal(t, report.Samples[len(report.Samples)-1].AgentID, report.Cursor)
				} else {
					require.Equal(t, cursor, report.Cursor)
				}
				cursor = report.Cursor
			}
		})
	}
}
