//go:build integration

package dbmigrate_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func TestIntegrationOwnedSchemaKeepsIndependentSetupAndImmutableIdentity(t *testing.T) {
	ctx := t.Context()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 48))
	var integrationKindHasDefault bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT column_default IS NOT NULL
        FROM information_schema.columns WHERE table_schema='public'
            AND table_name='integrations' AND column_name='integration_kind'`).Scan(&integrationKindHasDefault))
	require.False(t, integrationKindHasDefault, "the legacy backfill must not silently type new integrations")
	var obsolete bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT
        EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public'
            AND table_name='integration_targets' AND column_name='target_ref')
        OR EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='public'
            AND indexname='integration_targets_agent_target_ref_idx')
        OR EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='integration_targets'::regclass
            AND pg_get_constraintdef(oid) LIKE '%target_ref%')`).Scan(&obsolete))
	require.False(t, obsolete, "database-only aliases and their constraints are removed")
	var legacyObjects []string
	require.NoError(t, pool.QueryRow(ctx, `SELECT ARRAY(
        SELECT relname FROM pg_class WHERE relnamespace=current_schema()::regnamespace
            AND relname ~ '^integration_installs'
        UNION ALL
        SELECT conname FROM pg_constraint WHERE connamespace=current_schema()::regnamespace
            AND conname ~ '(integration_installs|integration_install_id)'
        UNION ALL
        SELECT table_name || '.' || column_name FROM information_schema.columns
            WHERE table_schema=current_schema()
                AND column_name = 'integration_install_id'
        UNION ALL
        SELECT proname FROM pg_proc WHERE pronamespace=current_schema()::regnamespace
            AND prosrc ~ '\mintegration_installs\M'
        ORDER BY 1)`).Scan(&legacyObjects))
	require.Empty(t, legacyObjects, "live relations, constraints, views and functions use integration names")
	ids := storagefixture.ProjectIDs{
		OrgID: uuid.New(), ProjectID: uuid.New(), ProviderAdminUserID: uuid.New(),
		ProviderSecretID: uuid.New(), ProviderSecretVersionID: uuid.New(), ProviderConfigID: uuid.New(),
	}
	storagefixture.SeedProject(t, ctx, pool, ids, time.Now())
	create := func(name string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		err := pool.QueryRow(ctx, `INSERT INTO integrations
			(org_id,project_id,name,integration_kind,state,created_at,updated_at)
			VALUES ($1,$2,$3,'slack_thread','disconnected',now(),now()) RETURNING id`,
			ids.OrgID, ids.ProjectID, name).Scan(&id)
		require.NoError(t, err)
		return id
	}
	first, second := create("engineering"), create("support")
	for _, id := range []uuid.UUID{first, second} {
		_, err := pool.Exec(ctx,
			`UPDATE integrations SET provider_tenant_id='T123',provider_account_ref='A123' WHERE id=$1`, id)
		require.NoError(t, err)
	}
	for _, statement := range []string{
		`UPDATE integrations SET name='renamed' WHERE id=$1`,
		`UPDATE integrations SET integration_kind='discord_thread' WHERE id=$1`,
		`UPDATE integrations SET provider_tenant_id='T456' WHERE id=$1`,
		`UPDATE integrations SET provider_account_ref=NULL,provider_tenant_id=NULL WHERE id=$1`,
	} {
		_, err := pool.Exec(ctx, statement, first)
		require.ErrorContains(t, err, "project integration identity is immutable")
	}
	_, err := pool.Exec(ctx, `UPDATE integrations SET state='active' WHERE id=$1`, first)
	require.ErrorContains(t, err, "check constraint", "activation requires credentials and installer attribution")
	_, err = pool.Exec(ctx, `UPDATE integrations SET settings='{"launcher":null}' WHERE id=$1`, first)
	require.NoError(t, err)
	var revision int64
	require.NoError(
		t,
		pool.QueryRow(ctx, `SELECT setup_revision FROM integrations WHERE id=$1`, first).Scan(&revision),
	)
	require.Equal(t, int64(1), revision, "behavior edits must not invalidate provider sessions")
	_, err = pool.Exec(ctx, `UPDATE integrations SET deleted_at=now() WHERE id=$1`, first)
	require.NoError(t, err)
	replacement := create("engineering")
	require.NotEqual(t, first, replacement, "reusing a name must create a different identity")
	var otherDeleted bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT deleted_at IS NOT NULL FROM integrations WHERE id=$1`, second).Scan(&otherDeleted))
	require.False(t, otherDeleted, "deleting one setup must not delete another setup using the same bot")
}

func TestIntegrationActorMigrationPreservesLegacySlackAttribution(t *testing.T) {
	ctx := t.Context()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 46))
	ids := storagefixture.ProjectIDs{
		OrgID: uuid.New(), ProjectID: uuid.New(), ProviderAdminUserID: uuid.New(),
		ProviderSecretID: uuid.New(), ProviderSecretVersionID: uuid.New(), ProviderConfigID: uuid.New(),
	}
	storagefixture.SeedProject(t, ctx, pool, ids, time.Now())
	id := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO actors
		(id,project_id,provider,provider_tenant_id,provider_user_id,display_name,metadata,created_at,updated_at)
		VALUES($1,$2,'slack','T_OLD','U_OLD','Historical sender','{"retained":"history"}',now(),now())`, id, ids.ProjectID)
	require.NoError(t, err)
	execution := executionstore.New(pool, executionstore.Config{})
	modelID, revisionID, configID := uuid.New(), uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `WITH model AS (
		INSERT INTO configured_models
		(id,org_id,model_provider_config_id,name,current_revision_id,management_kind,created_at,updated_at)
		VALUES($1,$2,$3,'history',$4,'tenant',now(),now()))
		INSERT INTO configured_model_revisions(id,org_id,configured_model_id,model_provider_config_id,provider_model_slug,
		context_window_tokens,max_output_tokens,created_at) VALUES($4,$2,$1,$3,'test',10000,1000,now())`,
		modelID, ids.OrgID, ids.ProviderConfigID, revisionID)
	require.NoError(t, err)
	compiled := fmt.Sprintf(`{"instruction":"History","model":{"configured_model_id":%q}}`, modelID)
	_, err = pool.Exec(ctx, `INSERT INTO agent_configs(id,org_id,project_id,configured_model_id,compiled_definition,
		effective_definition_hash,created_at) VALUES($1,$2,$3,$4,$5,$6,now())`,
		configID, ids.OrgID, ids.ProjectID, modelID, compiled, fmt.Sprintf("%x", sha256.Sum256([]byte(compiled))))
	require.NoError(t, err)
	agentID, inputID, eventID, turnID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	tx := integrationdb.BeginTx(t, ctx, pool)
	_, err = tx.Exec(ctx, `INSERT INTO agents
		(id,org_id,project_id,state,current_config_id,next_event_sequence,created_at,updated_at)
		VALUES($1,$2,$3,'active',$4,2,now(),now())`, agentID, ids.OrgID, ids.ProjectID, configID)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO agent_inputs(id,project_id,agent_id,actor_id,state,queued_at)
		VALUES($1,$2,$3,$4,'received',now())`, inputID, ids.ProjectID, agentID, id)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO agent_events
		(id,agent_id,turn_id,sequence,event_kind,idempotency_key,agent_input_id,is_opening_event,created_at)
		VALUES($1,$2,$3,1,'agent_input',$4,$5,true,now())`,
		eventID, agentID, turnID, "agent_input:"+inputID.String(), inputID)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO agent_turns(id,agent_id,turn_sequence,latest_event_id,latest_semantic_event_id)
		VALUES($1,$2,1,$3,$3)`, turnID, agentID, eventID)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE agent_inputs
		SET state='resolved',admitted_event_id=$2,admitted_at=now(),resolved_at=now()
		WHERE id=$1`, inputID, eventID)
	require.NoError(t, err)
	stopInputID, stopEventID := uuid.New(), uuid.New()
	_, err = tx.Exec(ctx, `WITH input AS (
		INSERT INTO agent_inputs(id,project_id,agent_id,state,input_kind,control_type,delivery_mode,queued_at)
		VALUES($1,$2,$3,'received','control','cancel_current','immediate',now()))
		INSERT INTO agent_events
		(id,agent_id,turn_id,sequence,event_kind,idempotency_key,agent_input_id,is_opening_event,created_at)
		VALUES($4,$3,$5,2,'agent_input',$6,$1,false,now())`,
		stopInputID, ids.ProjectID, agentID, stopEventID, turnID, "agent_input:"+stopInputID.String())
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE agent_inputs
		SET state='resolved',admitted_event_id=$2,admitted_at=now(),resolved_at=now()
		WHERE id=$1`, stopInputID, stopEventID)
	require.NoError(t, err)
	_, err = tx.Exec(ctx,
		`UPDATE agent_turns SET latest_event_id=$2,latest_semantic_event_id=$2 WHERE id=$1`, turnID, stopEventID)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE agents SET next_event_sequence=3 WHERE id=$1`, agentID)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	var before, after []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_jsonb(a) FROM actors a WHERE id=$1`, id).Scan(&before))
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 49))
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_jsonb(a) FROM actors a WHERE id=$1`, id).Scan(&after))
	var expected, actual map[string]any
	require.NoError(t, json.Unmarshal(before, &expected))
	require.NoError(t, json.Unmarshal(after, &actual))
	expected["provider"], expected["provider_tenant_id"] = "integration", "slack:T_OLD"
	expected["metadata"] = map[string]any{"retained": "history", "source_label": "Slack"}
	delete(expected, "updated_at")
	delete(actual, "updated_at")
	require.Equal(t, expected, actual, "only the namespace and saved label change")
	var historicalActorID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `SELECT i.actor_id FROM agent_events e
		JOIN agent_inputs i ON i.id=e.agent_input_id WHERE e.id=$1`, eventID).Scan(&historicalActorID))
	require.Equal(t, id, historicalActorID, "historical inputs/events retain their actor UUID")
	for range 2 {
		actor, err := executionstore.IntegrationActorParams(integrationstore.IntegrationRecord{
			ID: uuid.New(), ProjectID: ids.ProjectID, IntegrationKind: integrationdefinition.SlackThread,
			Provider: "slack", ProviderTenantID: "T_OLD", ProviderAccountRef: uuid.NewString(),
		}, "U_OLD", nil)
		require.NoError(t, err)
		input, _, _, err := execution.CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
			ProjectID: ids.ProjectID, AgentID: agentID, Actor: &actor,
			ContentBlocks: json.RawMessage(`[{"type":"text","text":"After migration"}]`),
		})
		require.NoError(t, err)
		require.Equal(t, id, input.ActorID, "new inputs from different bots reuse historical actor IDs")
	}
}
