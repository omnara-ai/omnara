//go:build integration

package executionstore_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestProjectRecoveryPlans(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, t.Name())
	profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, f.Store,
		"recovery-profile", "Recovery", testAgentConfigYAML())
	otherProfile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, f.Store,
		"other-recovery-profile", "Other recovery", testAgentConfigYAML())
	type source struct {
		agentID, outputID uuid.UUID
	}
	sources := make([]source, 0, 3)
	for i, profileID := range []uuid.UUID{profile.ID, otherProfile.ID, profile.ID} {
		launch, err := f.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
			ProjectID: testProjectID, ProfileID: profileID,
			LaunchedBy: systemPrincipalForTest(testOrgID), IdempotencyKey: fmt.Sprintf("recovery-agent-%d", i),
		})
		require.NoError(t, err)
		fixture := f
		fixture.AgentID = launch.Agent.ID
		fixture.Lock, err = f.Store.Execution().AcquireAgentRuntimeLock(ctx, testProjectID, fixture.AgentID,
			testWorkerProcessID, testAgentRuntimeLockLeaseDuration)
		require.NoError(t, err)
		callID := createTypedToolCallForProcessTest(t, ctx, fixture, fmt.Sprintf("recovery-seed-%d", i),
			"seed", "custom", true)
		var outputID uuid.UUID
		require.NoError(t, f.Store.pool.QueryRow(ctx,
			"SELECT model_output_id FROM tool_calls WHERE agent_id=$1 AND id=$2", fixture.AgentID, callID).Scan(&outputID))
		sources = append(sources, source{fixture.AgentID, outputID})
	}

	tx := integrationdb.BeginTx(t, ctx, f.Store.pool)
	for i, source := range sources {
		_, err := tx.Exec(ctx, `
INSERT INTO tool_calls(id, project_id, agent_id, model_output_id, provider_call_id,
                      name, input, type, state, created_at)
SELECT md5($2::uuid::text || ':recovery:' || n)::uuid, $1::uuid, $2::uuid, $3::uuid, 'mixed-' || n,
       CASE WHEN $4::int = 0 AND n % 4 = 1 THEN 'other_tool' ELSE 'target' END, '{}',
       CASE WHEN $4::int = 0 AND n % 4 = 2 THEN 'built_in' ELSE 'custom' END,
       'awaiting_authorization', '2026-01-01'::timestamptz + n * interval '1 second'
FROM generate_series(1, 2000) n`, testProjectID, source.agentID, source.outputID, i)
		require.NoError(t, err)
		_, err = tx.Exec(ctx, `UPDATE tool_calls SET state='ready'
WHERE agent_id=$1 AND provider_call_id LIKE 'mixed-%'
  AND ($2::int <> 0 OR substring(provider_call_id from 7)::int % 4 <> 3)`, source.agentID, i)
		require.NoError(t, err)
	}
	_, err := tx.Exec(ctx, "UPDATE agents SET state='archived', archived_at=now() WHERE id=$1", sources[2].agentID)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, "ANALYZE tool_calls, agents, model_outputs, agent_events")
	require.NoError(t, err)

	query := integrationdb.GeneratedQuery(t, "../internal/dbsqlc/tool_calls.sql.go", "ListUnfinishedToolCallsForProject")
	_, err = tx.Exec(ctx, "PREPARE recovery AS "+query)
	require.NoError(t, err)
	defer func() { _, _ = tx.Exec(ctx, "DEALLOCATE recovery") }()
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		_, err = tx.Exec(ctx, "SET LOCAL plan_cache_mode="+mode)
		require.NoError(t, err)
		var firstPageBuffers int
		for _, offset := range []int{0, 1600} {
			createdAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(offset) * time.Second)
			cursorID := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
			params := dbsqlc.ListUnfinishedToolCallsForProjectParams{
				ProjectID: testProjectID, AgentProfileID: &profile.ID, State: "ready", Type: "custom",
				Names: []string{"target"}, CursorCreatedAt: &createdAt, CursorID: &cursorID, RowLimit: 4,
			}
			rows, err := dbsqlc.New(tx).ListUnfinishedToolCallsForProject(ctx, params)
			require.NoError(t, err)
			require.Len(t, rows, 4)
			for i, row := range rows {
				require.Equal(t, sources[0].agentID, row.AgentID)
				require.Equal(t, fmt.Sprintf("mixed-%d", offset+4*(i+1)), row.ProviderCallID)
			}
			var raw json.RawMessage
			statement := fmt.Sprintf("EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+
				"EXECUTE recovery('%s','ready','custom',ARRAY['target'],'%s','%s','%s',4)",
				testProjectID, profile.ID, createdAt.Format(time.RFC3339), cursorID)
			require.NoError(t, tx.QueryRow(ctx, statement).Scan(&raw))
			plan := integrationdb.ReadPlan(t, raw)
			require.EqualValues(t, 4, plan.Rows)
			var orderedIndex bool
			plan.Walk(func(node integrationdb.PlanNode) {
				orderedIndex = orderedIndex || node.Index == "tool_calls_project_unfinished_idx"
				if node.Relation == "model_outputs" || node.Relation == "agent_events" {
					require.LessOrEqual(t, node.Rows*node.Loops, float64(4), "%s", raw)
				}
				require.NotEqual(t, "content_blocks", node.Relation, "%s", raw)
			})
			require.True(t, orderedIndex, "%s", raw)
			buffers := integrationdb.PlanBuffers(t, raw)
			if offset == 0 {
				firstPageBuffers = buffers
			} else {
				// A cursor must seek past older calls even with a generic prepared
				// plan, rather than scan and discard every preceding page again.
				require.LessOrEqual(t, buffers, firstPageBuffers+64, "%s: %s", mode, raw)
			}
			t.Logf("%s offset=%d buffers=%d", mode, offset, buffers)
		}
	}

	// Interactions use the same project-first cursor contract. Reuse this mixed
	// active/archived, multi-profile history to check deep pages in both plans.
	_, err = tx.Exec(ctx, `
INSERT INTO agent_interactions(id, project_id, agent_id, tool_call_id, interaction_kind, state, created_at)
SELECT id, project_id, agent_id, id,
       CASE WHEN substring(provider_call_id from 7)::int % 4 = 0 THEN 'question' ELSE 'permission' END,
       'open', created_at
FROM tool_calls WHERE provider_call_id LIKE 'mixed-%'`)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, "ANALYZE agent_interactions")
	require.NoError(t, err)
	interactionQuery := integrationdb.GeneratedQuery(t,
		"../internal/dbsqlc/subagents.sql.go", "ListOpenAgentInteractionsForProject")
	_, err = tx.Exec(ctx, "PREPARE interaction_recovery AS "+interactionQuery)
	require.NoError(t, err)
	defer func() { _, _ = tx.Exec(ctx, "DEALLOCATE interaction_recovery") }()
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		_, err = tx.Exec(ctx, "SET LOCAL plan_cache_mode="+mode)
		require.NoError(t, err)
		var firstPageBuffers int
		for _, offset := range []int{0, 1600} {
			createdAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(offset) * time.Second)
			cursorID := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
			rows, err := dbsqlc.New(tx).ListOpenAgentInteractionsForProject(ctx,
				dbsqlc.ListOpenAgentInteractionsForProjectParams{
					ProjectID: testProjectID, AgentProfileID: &profile.ID, InteractionKind: "question",
					CursorCreatedAt: &createdAt, CursorID: &cursorID, RowLimit: 4,
				})
			require.NoError(t, err)
			require.Len(t, rows, 4)
			for i, row := range rows {
				require.Equal(t, sources[0].agentID, row.AgentID)
				require.Equal(t, fmt.Sprintf("mixed-%d", offset+4*(i+1)), row.ProviderCallID)
			}
			var raw json.RawMessage
			statement := fmt.Sprintf("EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+
				"EXECUTE interaction_recovery('%s','question','%s','%s','%s',4)",
				testProjectID, profile.ID, createdAt.Format(time.RFC3339), cursorID)
			require.NoError(t, tx.QueryRow(ctx, statement).Scan(&raw))
			plan := integrationdb.ReadPlan(t, raw)
			require.EqualValues(t, 4, plan.Rows)
			var orderedIndex bool
			plan.Walk(func(node integrationdb.PlanNode) {
				orderedIndex = orderedIndex || node.Index == "agent_interactions_project_open_idx"
				if node.Relation == "model_outputs" || node.Relation == "agent_events" {
					require.LessOrEqual(t, node.Rows*node.Loops, float64(4), "%s", raw)
				}
			})
			require.True(t, orderedIndex, "%s", raw)
			buffers := integrationdb.PlanBuffers(t, raw)
			if offset == 0 {
				firstPageBuffers = buffers
			} else {
				require.LessOrEqual(t, buffers, firstPageBuffers+64, "%s: %s", mode, raw)
			}
			t.Logf("interactions %s offset=%d buffers=%d", mode, offset, buffers)
		}
	}

	// The broad endpoint still lists unfinished built-ins and authorization waits;
	// the narrower worker filters must not become an implicit API restriction.
	rows, err := tx.Query(ctx, query, testProjectID, "", "", nil, profile.ID, nil, nil, 4)
	require.NoError(t, err)
	listed, err := pgx.CollectRows(rows, pgx.RowToMap)
	require.NoError(t, err)
	require.Len(t, listed, 4)
	for i, call := range listed {
		require.Equal(t, fmt.Sprintf("mixed-%d", i+1), call["provider_call_id"])
	}
}
