//go:build integration

package agentexecution_test

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
 AND (data_type IN ('json','jsonb') OR column_name IN ('project_id','dirty','incremental'))`).Scan(&columns))
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
 max_normal_input_sequence=0,max_context_input_sequence=0,turn_continuable=false,logical_ready_at=NULL WHERE
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
	rejected := errors.New("rollback input")
	require.ErrorIs(t, u.Savepoint(t.Context(), func(u *agentexecution.Unit) error {
		_, insertErr := u.DB().
			Exec(t.Context(), `INSERT INTO agent_inputs(project_id,agent_id,state,input_kind,delivery_mode,queued_at)
  VALUES($1,$2,'received','content','steering',$3)`, f.project, agent, f.now)
		require.NoError(t, insertErr)
		rebuilt, rebuildErr := h.ReconstructExecution(t.Context())
		require.NoError(t, rebuildErr)
		require.Equal(t, agentexecution.AdmitAllSteering, rebuilt.Selection.Admission)
		return rejected
	}), rejected)
	rebuilt, err := h.ReconstructExecution(t.Context())
	require.NoError(t, err)
	require.Equal(t, agentexecution.WorkModel, rebuilt.Selection.Work)
	_, err = u.DB().Exec(t.Context(), `DELETE FROM agent_execution_state WHERE agent_id=$1`, agent)
	require.NoError(t, err)
	_, err = h.LoadExecution(t.Context())
	require.ErrorIs(t, err, pgx.ErrNoRows)
	rebuilt, err = h.ReconstructExecution(t.Context())
	require.NoError(t, err)
	require.Equal(t, expected.Head, rebuilt.Head)
}

func TestExecutionReconstructionRejectsCorruptLineage(t *testing.T) {
	f := releasedExecutionFixture(t)
	a, b := f.agent(t), f.agent(t)
	f.opening(t, a, 1, uuid.Nil, "content")
	c := f.modelContext(t, a, 1, "normal", nil, 1)
	_, other := f.opening(t, b, 1, uuid.Nil, "content")
	require.NoError(t, applyExecutionCutover(t.Context(), f.pool))
	u, h := f.handle(t, a)
	_, err := u.DB().Exec(t.Context(), `ALTER TABLE model_call_contexts DISABLE TRIGGER USER`)
	require.NoError(t, err)
	_, err = u.DB().
		Exec(t.Context(), `UPDATE model_call_contexts SET opening_input_ids=ARRAY[$2::uuid] WHERE id=$1`, c, other)
	require.NoError(t, err)
	_, err = h.ReconstructExecution(t.Context())
	require.ErrorIs(t, err, agentexecution.ErrInvalidState)
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
				require.NotContains(t, plan, "agent_next_")
				require.NotContains(t, plan, "scheduler_")
				if name == "LoadExecutionBase" || name == "LoadExecutionFacts" ||
					name == "ListExecutionIntegrationTargetAgents" ||
					name == "ClearExecutionIntegrationTargets" {
					_, err := connection.Exec(t.Context(), "SET enable_seqscan=off")
					require.NoError(t, err)
					indexed, err := connection.Conn().PgConn().
						Exec(t.Context(), "EXPLAIN (GENERIC_PLAN, FORMAT JSON) "+query).ReadAll()
					require.NoError(t, err)
					require.Len(t, indexed, 1)
					require.NotContains(t, string(indexed[0].Rows[0][0]), `"Node Type": "Seq Scan"`)
					_, err = connection.Exec(t.Context(), "RESET enable_seqscan")
					require.NoError(t, err)
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
