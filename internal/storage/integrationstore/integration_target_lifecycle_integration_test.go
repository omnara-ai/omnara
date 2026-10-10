//go:build integration

package integrationstore_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
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
 interaction_auto_select=false, state='archived', archived_at=now() WHERE id=$1`,
		historical.ID,
		historicalTarget)
	f.exec(t, `UPDATE agents SET interaction_target_id=$2, interaction_handler_key='other',
 interaction_auto_select=false WHERE id=$1`, moved.ID, retainedTarget)
	f.exec(
		t,
		`WITH identities AS MATERIALIZED (SELECT gen_random_uuid() AS id FROM generate_series(1,10000)), inserted AS (
INSERT INTO agents(id,root_agent_id,org_id,project_id,state,name,current_config_id,created_at,updated_at)
SELECT identity.id,identity.id,agent.org_id,agent.project_id,'active','unrelated',
agent.current_config_id,statement_timestamp(),statement_timestamp()
FROM identities identity CROSS JOIN agents agent WHERE agent.id=$1 RETURNING id)
INSERT INTO agent_execution_state(agent_id,turn_continuable,incomplete_tools) SELECT id,false,false FROM inserted`,
		current.ID,
	)
	cell := agentexecution.NewCell("test", f.pool, nil, nil)
	require.NoError(t, cell.Transact(f.ctx, func(u *agentexecution.Unit) error {
		return u.ClearIntegrationTargets(f.ctx, uuid.New(), f.integrationID)
	}))
	require.NoError(t, cell.Transact(f.ctx, func(u *agentexecution.Unit) error {
		return u.ClearIntegrationTargets(f.ctx, f.project, f.integrationID)
	}))
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
	require.NoError(t, cell.Transact(f.ctx, func(u *agentexecution.Unit) error {
		return u.ClearIntegrationTargets(f.ctx, f.project, f.integrationID)
	}))
	var selected int
	require.NoError(
		t,
		f.pool.QueryRow(f.ctx,
			`SELECT count(*) FROM agents a JOIN integration_targets t ON t.id=a.interaction_target_id WHERE t.integration_id=$1`,
			f.integrationID).
			Scan(&selected),
	)
	require.Zero(t, selected)
}
