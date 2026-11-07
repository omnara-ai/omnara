//go:build integration

package dbmigrate_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
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

	// Expansion must commit independently: hold a historical row while the next
	// transaction backfills it, and prove reads and active-agent writes still work.
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 70))
	blocker := integrationdb.BeginTx(t, ctx, pool)
	_, err = blocker.Exec(ctx, "SELECT id FROM tool_calls WHERE id = $1 FOR UPDATE", doneCallID)
	require.NoError(t, err)
	migrationConfig := pool.Config().ConnConfig.Copy()
	migrationConfig.RuntimeParams["lock_timeout"] = "2s"
	migrationDB := stdlib.OpenDB(*migrationConfig)
	t.Cleanup(func() { _ = migrationDB.Close() })
	migrated := integrationdb.RunAsyncError(func() error { return applyProductionPostgresMigrations(ctx, migrationDB) })
	integrationdb.WaitForLockWaiters(t, ctx, pool, "UPDATE tool_calls tool_call", 1)
	readCtx, cancelRead := context.WithTimeout(ctx, 5*time.Second)
	defer cancelRead()
	var projectID uuid.UUID
	require.NoError(t, pool.QueryRow(readCtx,
		"SELECT project_id FROM tool_call_read_projection WHERE id = $1", doneCallID).Scan(&projectID))
	require.Equal(t, ids.ProjectID, projectID)
	_, err = pool.Exec(readCtx, "UPDATE agents SET updated_at = statement_timestamp() WHERE id = $1", agentID)
	require.NoError(t, err, "the historical backfill must not take the active-agent write gate")
	agentWriter := integrationdb.BeginTx(t, ctx, pool)
	_, err = agentWriter.Exec(ctx, "LOCK TABLE agents IN ROW EXCLUSIVE MODE")
	require.NoError(t, err)
	require.NoError(t, blocker.Commit(ctx))
	integrationdb.WaitForLockWaiters(t, ctx, pool, "LOCK TABLE agents IN EXCLUSIVE MODE", 1)
	var lockErr *pgconn.PgError
	require.ErrorAs(t, integrationdb.Await(t, migrated, "finalization lock timeout"), &lockErr)
	require.Equal(t, "55P03", lockErr.Code)
	require.EqualValues(t, 71, currentPostgresMigrationVersion(t, ctx, db),
		"history must commit before finalization can fail")
	require.NoError(t, pool.QueryRow(ctx, "SELECT project_id FROM tool_calls WHERE id=$1", doneCallID).Scan(&projectID))
	require.Equal(t, ids.ProjectID, projectID, "finalization failure must not roll back the history backfill")
	require.NoError(t, agentWriter.Commit(ctx))
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

func TestCustomToolExpansionAllowsReaderWhileFirstLockWaits(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 68))
	blocker := integrationdb.BeginTx(t, ctx, pool)
	_, err := blocker.Exec(ctx, "LOCK TABLE agent_interactions IN ACCESS SHARE MODE")
	require.NoError(t, err)
	migrated := integrationdb.RunAsyncError(func() error {
		return applyProductionPostgresMigrationsThrough(t, ctx, db, 69)
	})
	integrationdb.WaitForLockWaiters(t, ctx, pool, "ALTER TABLE agent_interactions", 1)
	// The old tool view reads tool_calls before agents. Expansion must hold
	// neither while waiting for interactions, so this reader finishes immediately.
	_, err = pool.Exec(ctx, "SELECT count(*) FROM tool_call_read_projection")
	require.NoError(t, err)
	require.NoError(t, blocker.Commit(ctx))
	require.NoError(t, integrationdb.Await(t, migrated, "expansion overlapping old tool view"))
}

func TestCustomToolExpansionAllowsAgentFirstCompletion(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 68))
	writer := integrationdb.BeginTx(t, ctx, pool)
	// CompleteTool and CompleteCustomTool hold an agent FOR UPDATE lock, then
	// update tool_calls before closing interactions. Reproduce those table locks.
	_, err := writer.Exec(ctx, "LOCK TABLE agents IN ROW SHARE MODE; LOCK TABLE tool_calls IN ROW EXCLUSIVE MODE")
	require.NoError(t, err)
	migrated := integrationdb.RunAsyncError(func() error {
		return applyProductionPostgresMigrationsThrough(t, ctx, db, 70)
	})
	integrationdb.WaitForLockWaiters(t, ctx, pool, "ALTER TABLE tool_calls", 1)
	_, err = writer.Exec(ctx, "LOCK TABLE agent_interactions IN ROW EXCLUSIVE MODE")
	require.NoError(t, err, "interaction expansion must commit before migration waits for tool completion")
	require.NoError(t, writer.Commit(ctx))
	require.NoError(t, integrationdb.Await(t, migrated, "expansion after tool completion"))
}

func TestCustomToolExpansionAllowsPlanBeforeAgentLock(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 68))
	writer := integrationdb.BeginTx(t, ctx, pool)
	// Tool authorization plans read the call and interactions before apply takes
	// the agent FOR UPDATE lock and mutates either child table.
	_, err := writer.Exec(ctx, "SELECT count(*) FROM tool_call_read_projection; SELECT count(*) FROM agent_interactions")
	require.NoError(t, err)
	migrated := integrationdb.RunAsyncError(func() error {
		return applyProductionPostgresMigrationsThrough(t, ctx, db, 70)
	})
	integrationdb.WaitForLockWaiters(t, ctx, pool, "ALTER TABLE agent_interactions", 1)
	_, err = writer.Exec(ctx, `LOCK TABLE agents IN ROW SHARE MODE;
LOCK TABLE tool_calls, agent_interactions IN ROW EXCLUSIVE MODE`)
	require.NoError(t, err, "a planned tool must be able to acquire its mutation locks")
	require.NoError(t, writer.Commit(ctx))
	require.NoError(t, integrationdb.Await(t, migrated, "expansion after tool plan"))
}

func TestCustomToolProjectionMigrationsAllowReadAgentViewOrder(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 72))
	reader := integrationdb.BeginTx(t, ctx, pool)
	// read_agent reads events during planning, then its own tool call during
	// completion, retaining the event-view lock until the transaction commits.
	_, err := reader.Exec(ctx, "SELECT count(*) FROM agent_event_read_projection")
	require.NoError(t, err)
	migrated := integrationdb.RunAsyncError(func() error {
		return applyProductionPostgresMigrationsThrough(t, ctx, db, 74)
	})
	integrationdb.WaitForLockWaiters(t, ctx, pool, "CREATE OR REPLACE VIEW agent_event_read_projection", 1)
	_, err = reader.Exec(ctx, "SELECT count(*) FROM tool_call_read_projection")
	require.NoError(t, err, "read_agent must finish while event-view replacement waits")
	require.NoError(t, reader.Commit(ctx))
	require.NoError(t, integrationdb.Await(t, migrated, "projection replacement after read_agent"))
}

func TestCustomToolMigrationsAllowInteractionReader(t *testing.T) {
	t.Parallel()
	for _, phase := range []struct {
		name    string
		version int64
		waiter  string
	}{
		{"tool_expansion", 70, "ALTER TABLE tool_calls"},
		{"finalization", 72, "LOCK TABLE"},
		{"foreign_key_cleanup", 75, "LOCK TABLE"},
	} {
		t.Run(phase.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			pool := integrationdb.OpenUnmigratedPool(t, ctx)
			db := stdlib.OpenDBFromPool(pool)
			t.Cleanup(func() { _ = db.Close() })
			require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, phase.version-1))
			blocker := integrationdb.BeginTx(t, ctx, pool)
			_, err := blocker.Exec(ctx, "LOCK TABLE tool_calls IN ACCESS SHARE MODE")
			require.NoError(t, err)
			migrated := integrationdb.RunAsyncError(func() error {
				return applyProductionPostgresMigrationsThrough(t, ctx, db, phase.version)
			})
			integrationdb.WaitForLockWaiters(t, ctx, pool, phase.waiter, 1)
			// ListExecutionInteractions reads interactions before calls, then model
			// outputs and contexts. Exercise that same relation acquisition order.
			read := integrationdb.RunAsyncError(func() error {
				_, err := pool.Exec(ctx, `SELECT i.id
FROM agent_interactions i JOIN tool_calls c ON c.agent_id=i.agent_id AND c.id=i.tool_call_id
JOIN model_outputs o ON o.agent_id=c.agent_id AND o.id=c.model_output_id
JOIN model_call_contexts m ON m.agent_id=o.agent_id AND m.id=o.model_call_context_id
WHERE i.agent_id=$1 AND i.state='open' AND m.turn_id=$2
ORDER BY i.id /* custom_tool_interaction_reader */`, uuid.New(), uuid.New())
				return err
			})
			integrationdb.WaitForLockWaiters(t, ctx, pool, "custom_tool_interaction_reader", 1)
			require.NoError(t, blocker.Commit(ctx))
			require.NoError(t, integrationdb.Await(t, read, "interaction reader overlapping migration"))
			require.NoError(t, integrationdb.Await(t, migrated, "migration overlapping interaction reader"))
		})
	}
}

func TestCustomToolMigrationsAllowOverlappingViewReaders(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, toolCallInteractionProjectScopeMigration-1))

	// Stop tool-call expansion after the interaction-table expansion has committed.
	// Each reader then holds its view lock while waiting on underlying tables.
	blocker := integrationdb.BeginTx(t, ctx, pool)
	_, err := blocker.Exec(ctx, "LOCK TABLE tool_calls IN ACCESS SHARE MODE")
	require.NoError(t, err)
	migrated := integrationdb.RunAsyncError(func() error { return applyProductionPostgresMigrations(ctx, db) })
	integrationdb.WaitForLockWaiters(t, ctx, pool, "ALTER TABLE tool_calls", 1)
	readers := make([]<-chan error, 0, 2)
	for _, view := range []string{"tool_call_read_projection", "agent_event_read_projection"} {
		readers = append(readers, integrationdb.RunAsyncError(func() error {
			var count int
			return pool.QueryRow(ctx, "SELECT count(*) FROM "+view+" /* custom_tool_migration_reader */").Scan(&count)
		}))
	}
	integrationdb.WaitForLockWaiters(t, ctx, pool, "custom_tool_migration_reader", len(readers))
	var viewLocks int
	require.NoError(t, pool.QueryRow(ctx, `
SELECT count(*) FROM pg_locks
WHERE granted AND mode = 'AccessShareLock'
  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
  AND relation IN ('tool_call_read_projection'::regclass, 'agent_event_read_projection'::regclass)
`).Scan(&viewLocks))
	require.Equal(t, len(readers), viewLocks, "readers must hold the view locks that used to deadlock replacement")

	require.NoError(t, blocker.Commit(ctx))
	for _, reader := range readers {
		require.NoError(t, integrationdb.Await(t, reader, "view reader overlapping migration"))
	}
	require.NoError(t, integrationdb.Await(t, migrated, "migration overlapping view readers"))
}
