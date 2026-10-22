//go:build integration

package executionstore_test

import (
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func historyCostFixture(t *testing.T) (*Store, uuid.UUID) {
	t.Helper()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	config := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(),
		testOrgID, testProjectID, testAgentConfigYAML())
	agent, err := store.Execution().CreateAgentFixture(ctx, executionstore.AgentFixtureInput{
		ProjectID: testProjectID, CurrentConfigID: config.ID,
	})
	require.NoError(t, err)
	sql, err := os.ReadFile("testdata/history_cost_turn.sql")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, string(sql))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
INSERT INTO projects(id,org_id,name,created_at,updated_at)
SELECT md5('history-project-'||n)::uuid,$1,'History project '||n,now(),now() FROM generate_series(1,128) n;
INSERT INTO agent_configs(id,org_id,project_id,configured_model_id,source,source_format,
 source_hash,effective_definition_hash,
 compiled_definition,created_at)
SELECT md5('history-config-'||n)::uuid,org_id,md5('history-project-'||n)::uuid,configured_model_id,source,source_format,
 source_hash,effective_definition_hash,compiled_definition,now()
FROM agent_configs CROSS JOIN generate_series(1,128) n WHERE id=$2;
INSERT INTO agents(id,org_id,project_id,state,current_config_id,root_agent_id,created_at,updated_at)
SELECT md5('history-agent-'||n)::uuid,$1,md5('history-project-'||n)::uuid,'active',md5('history-config-'||n)::uuid,
 md5('history-agent-'||n)::uuid,now(),now() FROM generate_series(1,128) n;`,
		pgx.QueryExecModeSimpleProtocol, testOrgID, config.ID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`SELECT history_cost_turn(md5('history-agent-'||n)::uuid,0,false) FROM generate_series(1,128) n`)
	require.NoError(t, err)
	return store, agent.ID
}

func TestHistoryTriggerInsertsStayBounded(t *testing.T) {
	store, agent := historyCostFixture(t)
	ctx := t.Context()
	tx := integrationdb.BeginTx(t, ctx, store.pool)
	_, err := tx.Exec(ctx, "SET LOCAL plan_cache_mode = force_custom_plan")
	require.NoError(t, err)
	var baseline int
	for _, size := range []int{16, 2048} {
		_, err = tx.Exec(ctx, "SET LOCAL plan_cache_mode = force_custom_plan")
		require.NoError(t, err)
		start := 0
		if size > 16 {
			start = 32
		}
		_, err := tx.Exec(ctx, `SELECT history_cost_turn($1,k,false) FROM generate_series($2::int,$3::int) k`,
			agent, start, size-1)
		require.NoError(t, err)
		_, err = tx.Exec(ctx, historyCostStatistics)
		require.NoError(t, err)
		require.NoError(t, tx.Commit(ctx))
		tx = integrationdb.BeginTx(t, ctx, store.pool)
		for m, mode := range []string{"force_custom_plan", "force_generic_plan"} {
			_, err := tx.Exec(ctx, "SET LOCAL plan_cache_mode = "+mode)
			require.NoError(t, err)
			for i := range 8 {
				raw := integrationdb.ExplainPrepared(t, ctx, tx, "SELECT history_cost_turn($1,$2,false)", agent, size+m*8+i)
				buffers := integrationdb.PlanBuffers(t, raw)
				t.Logf("history=%d mode=%s turn=%d buffers=%d", size, mode, i, buffers)
				if i < 5 {
					continue
				}
				if size == 16 {
					baseline = max(baseline, buffers)
				}
				require.Less(t, buffers, 3000, "%s", raw)
				if size > 16 {
					require.LessOrEqual(t, buffers, baseline+300, "%s", raw)
				}
			}
		}
	}
}

const historyCostStatistics = `
ALTER TABLE model_outputs ALTER COLUMN agent_id SET (n_distinct = -0.02);
ALTER TABLE model_call_contexts ALTER COLUMN agent_id SET (n_distinct = -0.1),
 ALTER COLUMN project_id SET (n_distinct = -0.02);
ANALYZE agents,agent_inputs,agent_events,agent_turns,model_call_contexts,model_outputs,tool_calls,
 tool_call_results,content_blocks,context_checkpoints,agent_interactions;`

func TestInteractionPagesStayOutputBounded(t *testing.T) {
	store, agent := historyCostFixture(t)
	ctx := t.Context()
	tx := integrationdb.BeginTx(t, ctx, store.pool)
	_, err := tx.Exec(ctx, `SET LOCAL plan_cache_mode = force_custom_plan`)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `SELECT history_cost_turn($1,k,k>=2040) FROM generate_series(0,2047) k`, agent)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, historyCostStatistics)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	_, err = store.pool.Exec(ctx, "VACUUM (ANALYZE) agent_interactions")
	require.NoError(t, err)
	tx = integrationdb.BeginTx(t, ctx, store.pool)
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		_, err := tx.Exec(ctx, "SET LOCAL plan_cache_mode = "+mode)
		require.NoError(t, err)
		for _, variant := range []struct{ query, index string }{
			{"ListOpenAgentInteractionsForAgents", "agent_interactions_open_idx"},
			{"ListAllAgentInteractionsForAgents", "agent_interactions_agent_history_idx"},
		} {
			query := integrationdb.GeneratedQuery(t, "../internal/dbsqlc/subagents.sql.go", variant.query)
			raw := integrationdb.ExplainPrepared(t, ctx, tx, query, nil, nil, int64(4), []uuid.UUID{agent}, testProjectID)
			require.LessOrEqual(t, integrationdb.PlanningBuffers(t, raw), 700, "%s", raw)
			seen := false
			integrationdb.ReadPlan(t, raw).Walk(func(node integrationdb.PlanNode) {
				if node.Relation == "agent_interactions" && node.Loops > 0 {
					seen = true
					require.Equal(t, variant.index, node.Index, "%s", raw)
					require.LessOrEqual(t, node.Rows, 4.0, "%s", raw)
				}
			})
			require.True(t, seen, "%s", raw)
		}
		reads := dbsqlc.New(integrationdb.OutputBoundedPlanDB{
			Tx: tx, T: t, FixedBuffers: 20, BuffersPerRow: 20, PlanningBuffers: 700,
		})
		open, err := reads.ListOpenAgentInteractionsForAgents(ctx, dbsqlc.ListOpenAgentInteractionsForAgentsParams{
			ProjectID: testProjectID, AgentIds: []uuid.UUID{agent}, RowLimit: 4,
		})
		require.NoError(t, err)
		require.Len(t, open, 4)
		for _, row := range open {
			require.Equal(t, "open", row.State)
			require.Equal(t, testProjectID, row.ProjectID)
		}
		next, err := reads.ListOpenAgentInteractionsForAgents(ctx, dbsqlc.ListOpenAgentInteractionsForAgentsParams{
			ProjectID: testProjectID, AgentIds: []uuid.UUID{agent}, RowLimit: 4,
			CursorCreatedAt: &open[3].CreatedAt, CursorID: &open[3].ID,
		})
		require.NoError(t, err)
		require.Len(t, next, 4)
		require.NotEqual(t, open[3].ID, next[0].ID)
		all, err := reads.ListAllAgentInteractionsForAgents(ctx, dbsqlc.ListAllAgentInteractionsForAgentsParams{
			ProjectID: testProjectID, AgentIds: []uuid.UUID{agent}, RowLimit: 4,
		})
		require.NoError(t, err)
		require.Len(t, all, 4)
		filtered, err := reads.ListAgentInteractionsForAgents(ctx, dbsqlc.ListAgentInteractionsForAgentsParams{
			ProjectID: testProjectID, AgentIds: []uuid.UUID{agent}, State: "canceled", RowLimit: 4,
		})
		require.NoError(t, err)
		require.Len(t, filtered, 4)
		for i := range all {
			require.Equal(t, dbsqlc.ListAgentInteractionsForAgentsRow(all[i]), filtered[i])
		}
		wrong, err := reads.ListAllAgentInteractionsForAgents(ctx, dbsqlc.ListAllAgentInteractionsForAgentsParams{
			ProjectID: uuid.New(), AgentIds: []uuid.UUID{agent}, RowLimit: 4,
		})
		require.NoError(t, err)
		require.Empty(t, wrong)
	}
}
