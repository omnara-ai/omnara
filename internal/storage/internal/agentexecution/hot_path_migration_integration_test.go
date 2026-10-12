//go:build integration

package agentexecution_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestHotPathMigrationConcurrentBuildAndValidation(t *testing.T) {
	f := executionFixtureThrough(t, 53)
	ids := make([]uuid.UUID, 50000)
	for i := range ids {
		ids[i] = uuid.New()
	}
	tx, err := f.pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(t.Context()) }()
	_, err = tx.Exec(t.Context(), `
INSERT INTO agents(id,root_agent_id,org_id,project_id,state,current_config_id,created_at,updated_at)
 SELECT id,id,$2,$3,'active',$4,now(),now() FROM unnest($1::uuid[]) id
`, ids, f.org, f.project, f.config)
	require.NoError(t, err)
	_, err = tx.Exec(t.Context(), `
INSERT INTO agent_inputs(id,project_id,agent_id,state,input_kind,delivery_mode,queued_at)
 SELECT id,$2,id,'received','content','queued',now() FROM unnest($1::uuid[]) id
`, ids, f.project)
	require.NoError(t, err)
	_, err = tx.Exec(t.Context(), `
INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,agent_input_id,is_opening_event,
created_at,idempotency_key)
 SELECT id,id,id,1,'agent_input',id,true,now(),'agent_input:'||id FROM unnest($1::uuid[]) id
`, ids)
	require.NoError(t, err)
	_, err = tx.Exec(t.Context(), `
INSERT INTO agent_turns(id,agent_id,turn_sequence,latest_event_id,latest_semantic_event_id)
 SELECT id,id,1,id,id FROM unnest($1::uuid[]) id
`, ids)
	require.NoError(t, err)
	_, err = tx.Exec(t.Context(), `
UPDATE agent_inputs SET state='resolved',admitted_event_id=id,admitted_at=now(),resolved_at=now()
WHERE agent_id=ANY($1::uuid[])
`, ids)
	require.NoError(t, err)
	_, err = tx.Exec(t.Context(), `ANALYZE agents; ANALYZE agent_inputs; ANALYZE agent_events; ANALYZE agent_turns`)
	require.NoError(t, err)
	_, err = tx.Exec(t.Context(), `
INSERT INTO model_call_contexts(id,org_id,project_id,agent_id,turn_id,operation_kind,attempt_number,
agent_config_id,configured_model_revision_id,input_event_sequence,runtime_lock_id,created_at,
opening_input_ids,opening_event_sequence)
SELECT id,$2,$3,id,id,'normal',1,$4,$5,1,gen_random_uuid(),now(),ARRAY[id],1 FROM unnest($1::uuid[])
id
`, ids, f.org, f.project, f.config, f.revision)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(t.Context()))
	migration, err := os.ReadFile("../../../../migrations/000054_hot_path_references.sql")
	require.NoError(t, err)
	require.Contains(t, string(migration), "-- +goose NO TRANSACTION")
	statements := strings.Split(string(migration), ";")
	for _, statement := range statements {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		start := time.Now()
		_, err = f.pool.Exec(t.Context(), statement)
		require.NoError(t, err, statement)
		t.Logf("rows=50000 duration=%s statement=%s", time.Since(start), statement)
	}
	for _, table := range []string{"model_call_contexts", "processes", "process_actions"} {
		tx, err := f.pool.Begin(t.Context())
		require.NoError(t, err)
		defer func() { _ = tx.Rollback(t.Context()) }()
		_, err = tx.Exec(t.Context(), "ALTER TABLE "+table+" DROP CONSTRAINT "+table+"_agent_scope_fkey")
		require.NoError(t, err)
		_, err = tx.Exec(t.Context(), "ALTER TABLE "+table+" ADD CONSTRAINT "+table+
			"_agent_scope_fkey FOREIGN KEY(org_id,project_id,agent_id) REFERENCES agents(org_id,project_id,id) NOT VALID")
		require.NoError(t, err)
		require.NoError(t, tx.Commit(t.Context()))
		tx, err = f.pool.Begin(t.Context())
		require.NoError(t, err)
		defer func() { _ = tx.Rollback(t.Context()) }()
		start := time.Now()
		_, err = tx.Exec(t.Context(), "ALTER TABLE "+table+" VALIDATE CONSTRAINT "+table+"_agent_scope_fkey")
		require.NoError(t, err)
		var locks []string
		require.NoError(t, tx.QueryRow(t.Context(), `
SELECT array_agg(DISTINCT mode ORDER BY mode) FROM pg_locks
 WHERE pid=pg_backend_pid() AND relation=ANY(ARRAY['agents'::regclass,$1::regclass])
`, table).Scan(&locks))
		t.Logf("%s validation duration=%s locks=%v", table, time.Since(start), locks)
		require.NotContains(t, locks, "AccessExclusiveLock")
		require.NotContains(t, locks, "ShareRowExclusiveLock")
		require.NotContains(t, locks, "ShareLock")
		writer, err := f.pool.Begin(t.Context())
		require.NoError(t, err)
		defer func() { _ = writer.Rollback(t.Context()) }()
		_, err = writer.Exec(t.Context(), "LOCK TABLE "+table+" IN ROW EXCLUSIVE MODE NOWAIT")
		require.NoError(t, err)
		require.NoError(t, writer.Rollback(t.Context()))
		require.NoError(t, tx.Commit(t.Context()))
	}
}
