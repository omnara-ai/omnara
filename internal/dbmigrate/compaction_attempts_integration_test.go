//go:build integration

package dbmigrate_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func TestCompactionRecoveryMigrationOrdersLegacyAttemptsPerParent(t *testing.T) {
	ctx := t.Context()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 51))
	ids := storagefixture.ProjectIDs{
		OrgID: uuid.New(), ProjectID: uuid.New(), ProviderAdminUserID: uuid.New(),
		ProviderSecretID: uuid.New(), ProviderSecretVersionID: uuid.New(), ProviderConfigID: uuid.New(),
	}
	storagefixture.SeedProject(t, ctx, pool, ids, time.Now())
	models := modelstore.New(pool)
	execution := executionstore.New(pool, executionstore.Config{})
	config := storagefixture.SeedAgentConfig(t, ctx, models, execution, ids.OrgID, ids.ProjectID,
		"instruction: Test compaction recovery.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
	configuredModel, err := models.GetConfiguredModelByName(ctx, ids.OrgID, ids.ProviderConfigID, "gpt-test")
	require.NoError(t, err)
	agentID, parentID := uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO agents
		(id,org_id,project_id,state,current_config_id,next_event_sequence,created_at,updated_at)
		VALUES($1,$2,$3,'active',$4,101,now(),now())`, agentID, ids.OrgID, ids.ProjectID, config.ID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO model_call_contexts
		(id,org_id,project_id,agent_id,operation_kind,attempt_number,agent_config_id,
		configured_model_revision_id,input_event_sequence,runtime_lock_id,state,created_at)
		VALUES($1,$2,$3,$4,'normal',1,$5,$6,100,$7,'started',now())`,
		parentID, ids.OrgID, ids.ProjectID, agentID, config.ID, configuredModel.CurrentRevisionID, uuid.New())
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE model_call_contexts SET state='failed',recovery_kind='compact',
		error_kind='context_window',error_message='overflow',completed_at=now() WHERE id=$1`, parentID)
	require.NoError(t, err)
	var children []uuid.UUID
	for _, source := range []struct{ end, attempts int }{{100, 2}, {50, 4}} {
		for attempt := 1; attempt <= source.attempts; attempt++ {
			childID := uuid.New()
			_, err = pool.Exec(ctx, `INSERT INTO model_call_contexts
				(id,org_id,project_id,agent_id,operation_kind,attempt_number,agent_config_id,
				configured_model_revision_id,input_event_sequence,source_event_sequence_end,runtime_lock_id,state,created_at)
				SELECT $2,org_id,project_id,agent_id,'compaction',$3,agent_config_id,
				configured_model_revision_id,input_event_sequence,$4,runtime_lock_id,'started',statement_timestamp()
				FROM model_call_contexts WHERE id=$1`, parentID, childID, attempt, source.end)
			require.NoError(t, err)
			recovery := "retry"
			if attempt == source.attempts {
				recovery = "reduce_compaction_source"
			}
			_, err = pool.Exec(ctx, `UPDATE model_call_contexts SET state='failed',
				recovery_kind=$2,error_kind='context_window',error_message='reduce source',
				retry_at=CASE WHEN $2='retry' THEN statement_timestamp() END,
				completed_at=statement_timestamp() WHERE id=$1`, childID, recovery)
			require.NoError(t, err)
			children = append(children, childID)
		}
	}
	require.NoError(t, applyProductionPostgresMigrations(ctx, db))
	for index, childID := range children {
		var actualParent uuid.UUID
		var attempt, sourceEnd int
		require.NoError(t, pool.QueryRow(ctx, `SELECT parent_normal_model_call_context_id,
			attempt_number,source_event_sequence_end FROM model_call_contexts WHERE id=$1`, childID).
			Scan(&actualParent, &attempt, &sourceEnd))
		require.Equal(t, parentID, actualParent)
		require.Equal(t, index+1, attempt)
		if index < 2 {
			require.Equal(t, 100, sourceEnd)
		} else {
			require.Equal(t, 50, sourceEnd)
		}
	}
}
