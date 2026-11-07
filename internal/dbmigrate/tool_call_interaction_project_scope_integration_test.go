//go:build integration

package dbmigrate_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

const toolCallInteractionProjectScopeMigration = 69

func TestToolCallInteractionProjectScopeMigration(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, toolCallInteractionProjectScopeMigration-1))
	ids := storagefixture.ProjectIDs{
		OrgID: uuid.New(), ProjectID: uuid.New(), ProviderAdminUserID: uuid.New(),
		ProviderSecretID: uuid.New(), ProviderSecretVersionID: uuid.New(), ProviderConfigID: uuid.New(),
	}
	storagefixture.SeedProject(t, ctx, pool, ids, time.Now())
	exec := func(statement string, args ...any) {
		t.Helper()
		_, err := db.ExecContext(ctx, statement, args...)
		require.NoError(t, err)
	}
	modelID, revisionID, definitionID, configID, agentID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec(`WITH model AS (
		INSERT INTO configured_models(id,org_id,model_provider_config_id,name,current_revision_id,
			management_kind,created_at,updated_at) VALUES($1,$2,$3,'test',$4,'tenant',now(),now()))
		INSERT INTO configured_model_revisions(id,org_id,configured_model_id,model_provider_config_id,
			provider_model_slug,context_window_tokens,max_output_tokens,created_at)
		VALUES($4,$2,$1,$3,'test',10000,1000,now())`, modelID, ids.OrgID, ids.ProviderConfigID, revisionID)
	exec(`INSERT INTO agent_config_definitions(id,org_id,project_id,configured_model_id,compiled_definition,
		effective_definition_hash,created_at)
		VALUES($1,$2,$3,$4,jsonb_build_object('instruction','Review','model',
			jsonb_build_object('configured_model_id',$5::text)),'project-scope-hash',now())`,
		definitionID, ids.OrgID, ids.ProjectID, modelID, modelID.String())
	exec(`INSERT INTO agent_configs(id,org_id,project_id,definition_id,created_at) VALUES($1,$2,$3,$4,now())`,
		configID, ids.OrgID, ids.ProjectID, definitionID)
	exec(`INSERT INTO agents(id,root_agent_id,org_id,project_id,state,current_config_id,created_at,updated_at)
		VALUES($1,$1,$2,$3,'active',$4,now(),now())`, agentID, ids.OrgID, ids.ProjectID, configID)

	inputID, inputEventID, turnID, contextID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	outputID, pendingCallID, doneCallID, resultID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	write := func(statement string, args ...any) {
		t.Helper()
		_, err := tx.ExecContext(ctx, statement, args...)
		require.NoError(t, err)
	}
	write(`INSERT INTO agent_inputs(id,project_id,agent_id,state,input_kind,delivery_mode,queued_at)
		VALUES($1,$2,$3,'received','content','queued',now())`, inputID, ids.ProjectID, agentID)
	write(`INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,idempotency_key,
		agent_input_id,is_opening_event,created_at)
		VALUES($1,$2,$3,1,'agent_input','agent_input:'||$4::text,$4::uuid,true,now())`,
		inputEventID, agentID, turnID, inputID)
	write(`INSERT INTO agent_turns(id,agent_id,turn_sequence,latest_event_id,latest_semantic_event_id)
		VALUES($1,$2,1,$3,$3)`, turnID, agentID, inputEventID)
	write(`UPDATE agent_inputs SET state='resolved',admitted_event_id=$2,admitted_at=now(),resolved_at=now()
		WHERE id=$1`, inputID, inputEventID)
	require.NoError(t, tx.Commit())
	exec(`INSERT INTO model_call_contexts(id,org_id,project_id,agent_id,operation_kind,attempt_number,
		agent_config_id,configured_model_revision_id,input_event_sequence,runtime_lock_id,state,created_at,
		turn_id,opening_input_ids,opening_event_sequence)
		VALUES($1,$2,$3,$4,'normal',1,$5,$6,1,$7,'started',now(),$8,ARRAY[$9::uuid],1)`,
		contextID, ids.OrgID, ids.ProjectID, agentID, configID, revisionID, uuid.New(), turnID, inputID)

	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	write(`INSERT INTO model_outputs(id,agent_id,model_call_context_id,stop_reason,created_at)
		VALUES($1,$2,$3,'tool_use',now())`, outputID, agentID, contextID)
	write(`INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,idempotency_key,
		model_output_id,is_opening_event,created_at)
		VALUES($1,$2,$3,2,'model_output','model_output:'||$4::text,$4::uuid,false,now())`,
		uuid.New(), agentID, turnID, outputID)
	for ordinal, callID := range []uuid.UUID{pendingCallID, doneCallID} {
		write(`INSERT INTO tool_calls(id,agent_id,model_output_id,provider_call_id,name,input,type,state,created_at)
			VALUES($1,$2,$3,$4,'lookup_customer','{}','custom','awaiting_authorization',now())`,
			callID, agentID, outputID, callID.String())
		write(`INSERT INTO content_blocks(agent_id,owner_kind,owner_model_output_id,ordinal,block_kind,
			tool_call_id,created_at) VALUES($1,'model_output',$2,$3,'tool_call',$4,now())`,
			agentID, outputID, ordinal, callID)
	}
	write(`UPDATE model_call_contexts SET state='succeeded',api_format='openai-chat-completions',
		api_variant='openai',completed_at=now() WHERE id=$1`, contextID)
	require.NoError(t, tx.Commit())

	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	write(`UPDATE tool_calls SET state='completed' WHERE id=$1`, doneCallID)
	write(`INSERT INTO tool_call_results(id,agent_id,tool_call_id,outcome,completed_at)
		VALUES($1,$2,$3,'succeeded',now())`, resultID, agentID, doneCallID)
	write(`INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,idempotency_key,
		tool_call_result_id,is_opening_event,created_at)
		VALUES($1,$2,$3,3,'tool_result','tool_result:'||$4::text,$4::uuid,false,now())`,
		uuid.New(), agentID, turnID, resultID)
	require.NoError(t, tx.Commit())

	openID, resolvedID := uuid.New(), uuid.New()
	exec(`INSERT INTO agent_interactions(id,agent_id,tool_call_id,interaction_kind,state,request,created_at)
		VALUES($1,$2,$3,'permission','open','{}',now()),($4,$2,$5,'question','open','{}',now())`,
		openID, agentID, pendingCallID, resolvedID, doneCallID)
	exec(`UPDATE agent_interactions SET state='resolved',resolved_at=now() WHERE id=$1`, resolvedID)

	require.NoError(t, applyProductionPostgresMigrations(ctx, db))

	for _, table := range []string{"tool_calls", "agent_interactions"} {
		var rows, scoped int
		var nullable string
		require.NoError(t, db.QueryRowContext(ctx, `
SELECT count(*), count(*) FILTER (WHERE project_id = $1),
       (SELECT is_nullable FROM information_schema.columns WHERE table_name = $2 AND column_name = 'project_id')
FROM `+table, ids.ProjectID, table).Scan(&rows, &scoped, &nullable))
		require.Equal(t, 2, rows, table)
		require.Equal(t, rows, scoped, table)
		require.Equal(t, "NO", nullable, table)

		var definition string
		var validated, plainKeyExists bool
		require.NoError(t, db.QueryRowContext(ctx, `
SELECT pg_get_constraintdef(oid), convalidated,
       EXISTS (SELECT 1 FROM pg_constraint WHERE conname = $2)
FROM pg_constraint
WHERE conname = $1
`, table+"_project_id_agent_id_fkey", table+"_agent_id_fkey").Scan(&definition, &validated, &plainKeyExists))
		require.Equal(t, "FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id)", definition, table)
		require.True(t, validated, table)
		require.False(t, plainKeyExists, table)
	}

	var guardEnabled string
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT tgenabled::text FROM pg_trigger WHERE tgname = 'agent_interactions_transition_guard'
`).Scan(&guardEnabled))
	require.Equal(t, "O", guardEnabled)

	_, err = db.ExecContext(ctx, `UPDATE tool_calls SET project_id = $2 WHERE id = $1`, pendingCallID, uuid.New())
	require.ErrorContains(t, err, "tool call facts are immutable")
	_, err = db.ExecContext(ctx, `
UPDATE agent_interactions SET state = 'canceled', resolved_at = now(), project_id = $2 WHERE id = $1
`, openID, uuid.New())
	require.ErrorContains(t, err, "agent_interaction lineage is immutable")
}
