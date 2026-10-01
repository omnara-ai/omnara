//go:build integration

package integrationstore_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func TestClearDeletedIntegrationTargetsUsesAgentIdentity(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	current := subscriptionAgent(t, f)
	historical := subscriptionAgent(t, f)
	moved := subscriptionAgent(t, f)
	other := f.addIntegration(t, "other", integrationstore.IntegrationSettings(`{}`))
	ensureTarget := func(agentID, integrationID uuid.UUID, ref string) uuid.UUID {
		t.Helper()
		target, err := f.ensureConversationTarget(integrationstore.EnsureConversationTargetInput{
			ProjectID: f.project, AgentID: agentID, IntegrationID: integrationID,
			Address: integrationstore.ConversationAddress{Kind: "thread", Ref: ref},
		})
		require.NoError(t, err)
		return target.ID
	}
	currentTarget := ensureTarget(current.ID, f.integrationID, "C123:111.222")
	oldTarget := ensureTarget(current.ID, f.integrationID, "C123:222.333")
	historicalTarget := ensureTarget(historical.ID, f.integrationID, "C123:333.444")
	ensureTarget(moved.ID, f.integrationID, "C123:444.555")
	retainedTarget := ensureTarget(moved.ID, other.ID, "C123:444.555")
	f.exec(t, `UPDATE integration_targets SET deleted_at=now() WHERE id IN ($1,$2)`,
		oldTarget, historicalTarget)
	f.exec(t, `UPDATE agents SET interaction_target_id=$2, interaction_handler_key='inbox-integration'
 WHERE id=$1`, current.ID, currentTarget)
	f.exec(t, `UPDATE agents SET interaction_target_id=$2, interaction_handler_key='inbox-integration',
 interaction_auto_select=false, state='archived', archived_at=now() WHERE id=$1`, historical.ID, historicalTarget)
	f.exec(t, `UPDATE agents SET interaction_target_id=$2, interaction_handler_key='other',
 interaction_auto_select=false WHERE id=$1`, moved.ID, retainedTarget)
	f.exec(t, `INSERT INTO agents(org_id,project_id,state,name,current_config_id,created_at,updated_at)
 SELECT org_id,project_id,'active','unrelated',current_config_id,now(),now()
 FROM agents CROSS JOIN generate_series(1,10000) WHERE id=$1`, current.ID)
	f.exec(t, "ANALYZE agents")
	f.exec(t, "ANALYZE integration_targets")
	parameters := map[string]any{"project_id": f.project, "integration_id": f.integrationID}
	plan := explainInboxQueryFile(t, f, "integration_target_lifecycle.sql",
		"ClearDeletedIntegrationTargetsFromAgents", parameters)
	var inspected float64
	var walk func(inboxQueryPlan)
	walk = func(node inboxQueryPlan) {
		if node.Relation == "agents" && node.NodeType != "ModifyTable" && node.Loops > 0 {
			inspected += (node.Rows + node.Filtered) * node.Loops
			require.NotEqual(t, "Seq Scan", node.NodeType, "deleting an integration must not scan project agents")
			t.Logf("agent lookup: %s using %s, rows=%g filtered=%g loops=%g",
				node.NodeType, node.Index, node.Rows, node.Filtered, node.Loops)
		}
		for _, child := range node.Plans {
			walk(child)
		}
	}
	walk(plan)
	require.Positive(t, inspected, "the plan must visit the selected agents")
	require.LessOrEqual(t, inspected, float64(4), "only the integration's four target owners need inspection")

	query, args := bindInboxQueryFile(t, "integration_target_lifecycle.sql",
		"ClearDeletedIntegrationTargetsFromAgents", parameters)
	foreignQuery, foreignArgs := bindInboxQueryFile(t, "integration_target_lifecycle.sql",
		"ClearDeletedIntegrationTargetsFromAgents", map[string]any{
			"project_id": uuid.New(), "integration_id": f.integrationID,
		})
	result, err := f.pool.Exec(f.ctx, foreignQuery, foreignArgs...)
	require.NoError(t, err)
	require.Zero(t, result.RowsAffected())
	result, err = f.pool.Exec(f.ctx, query, args...)
	require.NoError(t, err)
	require.EqualValues(t, 2, result.RowsAffected(), "clear live and historical selections, including archived agents")
	for _, expected := range []struct {
		agentID, targetID uuid.UUID
		handlerKey        string
		autoSelect        bool
	}{
		{agentID: current.ID, autoSelect: true},
		{agentID: historical.ID, autoSelect: false},
		{agentID: moved.ID, targetID: retainedTarget, handlerKey: "other", autoSelect: false},
	} {
		var targetID *uuid.UUID
		var handlerKey *string
		var autoSelect bool
		require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT interaction_target_id,
 interaction_handler_key, interaction_auto_select FROM agents WHERE project_id=$1 AND id=$2`,
			f.project, expected.agentID).Scan(&targetID, &handlerKey, &autoSelect))
		if expected.targetID == uuid.Nil {
			require.Nil(t, targetID)
			require.Nil(t, handlerKey)
		} else {
			require.NotNil(t, targetID)
			require.Equal(t, expected.targetID, *targetID)
			require.NotNil(t, handlerKey)
			require.Equal(t, expected.handlerKey, *handlerKey)
		}
		require.Equal(t, expected.autoSelect, autoSelect, "deletion must preserve automatic selection mode")
	}
	result, err = f.pool.Exec(f.ctx, query, args...)
	require.NoError(t, err)
	require.Zero(t, result.RowsAffected(), "clearing already removed selections is idempotent")
}
