//go:build integration

package agentexecution_test

import (
	"context"
	"github.com/jackc/pgx/v5"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/stretchr/testify/require"
)

func TestExecutionSchemaContracts(t *testing.T) {
	f := releasedExecutionFixture(t)
	a, b := f.agent(t), f.agent(t)
	turn, _ := f.opening(t, a, 1, uuid.Nil, "content")
	c := f.modelContext(t, a, 1, "normal", nil, 1)
	output, _ := f.output(t, a, turn, c, 2, "max_tokens", "")
	otherTurn, _ := f.opening(t, b, 1, uuid.Nil, "content")
	otherContext := f.modelContext(t, b, 1, "normal", nil, 1)
	otherOutput, _ := f.output(t, b, otherTurn, otherContext, 2, "max_tokens", "")
	require.NoError(t, applyExecutionCutover(t.Context(), f.pool))
	var columns int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM information_schema.columns
 WHERE table_schema='public' AND table_name='agent_execution_state'
 AND (data_type IN ('json','jsonb') OR column_name = 'project_id')`).Scan(&columns))
	require.Zero(t, columns)
	var fks int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_constraint
 WHERE conrelid='agent_execution_state'::regclass AND contype='f'`).Scan(&fks))
	require.Equal(t, 8, fks)
	for _, index := range []string{"agents_root_idx",
		"tool_calls_output_idx",
		"tool_calls_output_incomplete_idx",
		"tool_calls_output_runnable_idx", "model_call_contexts_turn_kind_idx", "agent_events_output_boundary_idx"} {
		var valid bool
		require.NoError(
			t,
			f.pool.QueryRow(t.Context(), `
SELECT indisvalid AND indisready FROM pg_index WHERE indexrelid=$1::regclass
`, index).
				Scan(&valid),
		)
		require.True(t, valid, index)
	}
	for _, tc := range []struct {
		column string
		value  uuid.UUID
	}{
		{"current_turn_id",
			otherTurn},
		{"normal_context_id",
			otherContext},
		{"pending_output_limit_id",
			otherOutput},
	} {
		t.Run(tc.column, func(t *testing.T) {
			_, err := f.pool.Exec(
				t.Context(),
				`UPDATE agent_execution_state SET `+tc.column+`=$2 WHERE agent_id=$1`,
				a,
				tc.value,
			)
			var constraint *pgconn.PgError
			require.ErrorAs(t, err, &constraint)
			require.Equal(t, "23503", constraint.Code)
		})
	}
	_, err := f.pool.Exec(t.Context(), `UPDATE model_call_contexts SET opening_input_ids='{}' WHERE id=$1`, c)
	require.Error(t, err)
	_, err = f.pool.Exec(t.Context(), `UPDATE agents SET root_agent_id=$2 WHERE id=$1`, a, b)
	require.Error(t, err)
	_, err = f.pool.Exec(
		t.Context(),
		`
INSERT INTO agents(id,root_agent_id,parent_agent_id,subagent_key,org_id,project_id,state,current_config_id,
created_at,updated_at)
 VALUES($1,$2,$3,'child',$4,$5,'active',$6,$7,$7)`,
		uuid.New(),
		b,
		a,
		f.org,
		f.project,
		f.config,
		f.now,
	)
	require.ErrorContains(t, err, "root must match parent")
	child := uuid.New()
	_, err = f.pool.Exec(
		t.Context(),
		`
INSERT INTO agents(id,root_agent_id,parent_agent_id,subagent_key,org_id,project_id,state,current_config_id,
created_at,updated_at)
 VALUES($1,$2,$2,'child',$3,$4,'active',$5,$6,$6)`,
		child,
		a,
		f.org,
		f.project,
		f.config,
		f.now,
	)
	require.NoError(t, err)
	var childRoot uuid.UUID
	require.NoError(
		t,
		f.pool.QueryRow(t.Context(), `SELECT root_agent_id FROM agents WHERE id=$1`, child).Scan(&childRoot),
	)
	require.Equal(t, a, childRoot)
	require.NotEqual(t, output, otherOutput)
	for _, tc := range []struct {
		ids      []uuid.UUID
		sequence *int64
		valid    bool
	}{
		{[]uuid.UUID{}, nil, true}, {[]uuid.UUID{uuid.New()}, new(int64(1)), true},
		{nil, nil, false}, {[]uuid.UUID{}, new(int64(1)), false}, {[]uuid.UUID{uuid.New()}, nil, false},
		{[]uuid.UUID{a, a}, new(int64(1)), false}, {[]uuid.UUID{uuid.Nil}, new(int64(1)), false},
	} {
		var valid bool
		require.NoError(
			t,
			f.pool.QueryRow(t.Context(), `SELECT execution_opening_is_valid($1,$2)`, tc.ids, tc.sequence).
				Scan(&valid),
		)
		require.Equal(t, tc.valid, valid)
	}
	for _, array := range []string{
		"'[0:0]={11111111-1111-4111-8111-111111111111}'",
		"'{{11111111-1111-4111-8111-111111111111}}'", "'{NULL}'",
	} {
		var valid bool
		require.NoError(t, f.pool.QueryRow(t.Context(),
			"SELECT execution_opening_is_valid("+array+"::uuid[],1)").Scan(&valid))
		require.False(t, valid)
	}

}

func TestExecutionReconstructionIgnoresCorruptHead(t *testing.T) {
	f := releasedExecutionFixture(t)
	agent := f.agent(t)
	turn, input := f.opening(t, agent, 1, uuid.Nil, "content")
	c := f.modelContext(t, agent, 1, "normal", nil, 1)
	output, _ := f.output(t, agent, turn, c, 2, "max_tokens", "")
	require.NoError(t, applyExecutionCutover(t.Context(), f.pool))
	u, h := f.handle(t, agent)
	expected, err := h.LoadExecution(t.Context())
	require.NoError(t, err)
	_, err = u.DB().
		Exec(t.Context(), `UPDATE agent_execution_state SET normal_context_id=NULL,pending_output_limit_id=NULL,
 max_normal_input_sequence=0,max_context_input_sequence=0,logical_ready_at=NULL WHERE
agent_id=$1
`, agent)
	require.NoError(t, err)
	for range 2 {
		rebuilt, rebuildErr := h.ReconstructExecution(t.Context())
		require.NoError(t, rebuildErr)
		require.Equal(t, expected.Head, rebuilt.Head)
		require.Equal(t, output, rebuilt.Head.PendingOutputLimitID)
		require.Equal(t, []uuid.UUID{input}, rebuilt.Selection.Model.Opening.InputIDs)
	}
	var unchanged bool
	require.NoError(
		t,
		u.DB().
			QueryRow(t.Context(), `
SELECT normal_context_id IS NULL FROM agent_execution_state WHERE agent_id=$1
`, agent).
			Scan(&unchanged),
	)
	require.True(t, unchanged)
	require.NoError(t, u.Commit(t.Context(), "corrupt head"))
	u, h = f.handle(t, agent)
	_, err = u.DB().Exec(t.Context(), `
INSERT INTO agent_inputs(project_id,agent_id,state,input_kind,delivery_mode,queued_at)
 VALUES($1,$2,'received','content','steering',$3)`, f.project, agent, f.now)
	require.NoError(t, err)
	rebuilt, err := h.ReconstructExecution(t.Context())
	require.NoError(t, err)
	require.Equal(t, agentexecution.AdmitAllSteering, rebuilt.Selection.Admission)
	require.NoError(t, u.Rollback(t.Context()))
	u, h = f.handle(t, agent)
	rebuilt, err = h.ReconstructExecution(t.Context())
	require.NoError(t, err)
	require.Equal(t, agentexecution.WorkModel, rebuilt.Selection.Work)
	_, err = u.DB().Exec(t.Context(), `DELETE FROM agent_execution_state WHERE agent_id=$1`, agent)
	require.NoError(t, err)
	require.NoError(t, u.Commit(t.Context(), "corrupt head"))
	_, h = f.handle(t, agent)
	_, err = h.LoadExecution(t.Context())
	require.ErrorIs(t, err, pgx.ErrNoRows)
	rebuilt, err = h.ReconstructExecution(t.Context())
	require.NoError(t, err)
	require.Equal(t, expected.Head, rebuilt.Head)
}

func TestExecutionFactPlans(t *testing.T) {
	f := releasedExecutionFixture(t)
	require.NoError(t, applyExecutionCutover(t.Context(), f.pool))
	entries, err := os.ReadDir("internal/executiondb")
	require.NoError(t, err)
	connection, err := f.pool.Acquire(t.Context())
	require.NoError(t, err)
	defer connection.Release()
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql.go") && entry.Name() != "batch.go" {
			continue
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), "internal/executiondb/"+entry.Name(), nil, 0)
		require.NoError(t, parseErr)
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			query, unquoteErr := strconv.Unquote(literal.Value)
			require.NoError(t, unquoteErr)
			if !strings.HasPrefix(query, "-- name: Load") && !strings.HasPrefix(query, "-- name: Capture") &&
				!strings.HasPrefix(
					query,
					"-- name: PublishExecution ",
				) &&
				!strings.HasPrefix(query,
					"-- name: ListExecutionIntegrationTargetAgents ") &&
				!strings.HasPrefix(query,
					"-- name: ClearExecutionIntegrationTargets ") {
				return true
			}
			name := strings.Fields(query)[2]
			t.Run(name, func(t *testing.T) {
				results, explainErr := connection.Conn().
					PgConn().
					Exec(t.Context(), "EXPLAIN (GENERIC_PLAN, FORMAT JSON) "+query).
					ReadAll()
				require.NoError(t, explainErr)
				require.Len(t, results, 1)
				plan := string(results[0].Rows[0][0])
				require.NotContains(t, plan, "Function Scan")
				limit := 22
				if name == "LoadExecutionFacts" {
					limit = 100
				}
				if name == "PublishExecution" {
					limit = 35
				}
				if name == "LoadExecutionBase" {
					limit = 28
				}
				require.LessOrEqual(t, strings.Count(plan, `"Node Type"`), limit)
				if name == "LoadExecutionBase" || name == "LoadExecutionFacts" ||
					name == "ListExecutionIntegrationTargetAgents" ||
					name == "ClearExecutionIntegrationTargets" {
					_, err := connection.Exec(t.Context(), "SET enable_seqscan=off")
					t.Cleanup(func() {
						_, err := connection.Exec(context.Background(), "RESET enable_seqscan")
						require.NoError(t, err)
					})
					require.NoError(t, err)
					indexed, err := connection.Conn().PgConn().
						Exec(t.Context(), "EXPLAIN (GENERIC_PLAN, FORMAT JSON) "+query).ReadAll()
					require.NoError(t, err)
					require.Len(t, indexed, 1)
					require.NotContains(t, string(indexed[0].Rows[0][0]), `"Node Type": "Seq Scan"`)

					t.Logf("generic plan: %d nodes", strings.Count(plan, `"Node Type"`))
				}
			})
			return true
		})
	}
}

func TestExecutionRootBackfill(t *testing.T) {
	f := releasedExecutionFixture(t)
	root := f.agent(t)
	child, grandchild := uuid.New(), uuid.New()
	_, err := f.pool.Exec(t.Context(), `INSERT INTO agents(id,parent_agent_id,subagent_key,
 org_id,project_id,state,current_config_id,created_at,updated_at)
 VALUES($1,$3,'child',$4,$5,'active',$6,$7,$7),($2,$1,'grandchild',$4,$5,'active',$6,$7,$7)`,
		child, grandchild, root, f.org, f.project, f.config, f.now)
	require.NoError(t, err)
	require.NoError(t, applyExecutionCutover(t.Context(), f.pool))
	var roots []uuid.UUID
	require.NoError(
		t,
		f.pool.QueryRow(t.Context(), `SELECT array_agg(root_agent_id) FROM agents`).Scan(&roots),
	)
	require.Equal(t, []uuid.UUID{root, root, root}, roots)
	_, h := f.handle(t, grandchild)
	snapshot, err := h.ReconstructExecution(t.Context())
	require.NoError(t, err)
	require.Equal(t, grandchild, snapshot.Head.AgentID)
}

func TestExecutionRootBackfillRejectsCycle(t *testing.T) {
	f := releasedExecutionFixture(t)
	a, b := uuid.New(), uuid.New()
	_, err := f.pool.Exec(t.Context(), `INSERT INTO agents(id,parent_agent_id,subagent_key,
 org_id,project_id,state,current_config_id,created_at,updated_at)
 VALUES($1,$2,'a',$3,$4,'active',$5,$6,$6),($2,$1,'b',$3,$4,'active',$5,$6,$6)`,
		a, b, f.org, f.project, f.config, f.now)
	require.NoError(t, err)
	require.ErrorContains(t, applyExecutionCutover(t.Context(), f.pool), "invalid agent ancestry")
}

func TestExecutionOpeningMembersAreAgentScopedAndOrdered(t *testing.T) {
	f := releasedExecutionFixture(t)
	a, b := f.agent(t), f.agent(t)
	_, first := f.opening(t, a, 1, uuid.Nil, "content")
	turn, second := f.opening(t, a, 2, uuid.Nil, "content")
	_, foreign := f.opening(t, b, 1, uuid.Nil, "content")
	normal := f.modelContext(t, a, 2, "normal", nil, 1)
	f.fail(t, normal, "compact")
	compact := f.modelContext(t, a, 2, "compaction", new(int64(2)), 1)
	f.checkpoint(t, a, turn, compact, 3, 2)
	require.NoError(t, applyExecutionCutover(t.Context(), f.pool))
	for _, table := range []string{"model_call_contexts", "context_checkpoints", "agent_inputs"} {
		for _, tc := range []struct {
			name  string
			ids   []uuid.UUID
			valid bool
		}{
			{"foreign", []uuid.UUID{foreign}, false},
			{"missing", []uuid.UUID{uuid.New()}, false},
			{"reversed", []uuid.UUID{second, first}, false},
			{"mixed", []uuid.UUID{first, foreign}, false},
			{"valid", []uuid.UUID{first, second}, true},
		} {
			t.Run(table+"/"+tc.name, func(t *testing.T) {
				tx, err := f.pool.Begin(t.Context())
				require.NoError(t, err)
				defer func() { _ = tx.Rollback(t.Context()) }()
				switch table {
				case "agent_inputs":
					id, event := uuid.New(), uuid.New()
					_, err = tx.Exec(t.Context(), `INSERT INTO agent_inputs(
 id,project_id,agent_id,state,input_kind,delivery_mode,agent_config_id,queued_at)
 VALUES($1,$2,$3,'received','config_change','immediate',$4,now())`, id, f.project, a, f.config)
					require.NoError(t, err)
					_, err = tx.Exec(t.Context(), `INSERT INTO agent_events(
 id,agent_id,turn_id,sequence,event_kind,agent_input_id,is_opening_event,idempotency_key,created_at)
 VALUES($1,$2,$3,4,'agent_input',$4,true,'agent_input:'||($4::uuid)::text,now())`, event, a, turn, id)
					require.NoError(t, err)
					_, err = tx.Exec(t.Context(), `UPDATE agent_inputs
 SET state='resolved',admitted_event_id=$2,admitted_at=now(),resolved_at=now(),
 opening_input_ids=$3,opening_event_sequence=1 WHERE id=$1`, id, event, tc.ids)
				default:
					source, id, ids := normal, uuid.New(), tc.ids
					if table == "context_checkpoints" {
						source, ids = compact, []uuid.UUID{first, second}
					}
					_, err = tx.Exec(t.Context(), `INSERT INTO model_call_contexts(
 id,org_id,project_id,agent_id,operation_kind,attempt_number,agent_config_id,configured_model_revision_id,
 input_event_sequence,source_event_sequence_end,runtime_lock_id,created_at,turn_id,
 opening_input_ids,opening_event_sequence)
 SELECT $1,org_id,project_id,agent_id,operation_kind,2,agent_config_id,configured_model_revision_id,
 input_event_sequence,source_event_sequence_end,runtime_lock_id,now(),turn_id,$3,1
 FROM model_call_contexts WHERE id=$2`, id, source, ids)
					if table == "context_checkpoints" {
						require.NoError(t, err)
						_, err = tx.Exec(t.Context(), `INSERT INTO context_checkpoints(
 agent_id,summarized_through_event_sequence,producer_model_call_context_id,summary,created_at,
 opening_input_ids,opening_event_sequence) VALUES($1,1,$2,'summary',now(),$3,1)`, a, id, tc.ids)
					}
				}
				if tc.valid {
					require.NoError(t, err)
				} else {
					require.ErrorContains(t, err, "opening inputs must belong to the agent in event sequence order")
				}
			})
		}
	}
}
