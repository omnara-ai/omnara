//go:build integration

package main

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) { integrationdb.RunTestMain(m) }

func TestCoreMaintenanceTickCleansInboxInBoundedBatchesAndPreservesHistory(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../migrations")
	ids := storagefixture.ProjectIDs{
		OrgID: uuid.New(), ProjectID: uuid.New(), ProviderAdminUserID: uuid.New(),
		ProviderSecretID: uuid.New(), ProviderSecretVersionID: uuid.New(), ProviderConfigID: uuid.New(),
	}
	storagefixture.SeedProject(t, ctx, pool, ids, time.Now())
	store := storage.NewStore(pool)
	config := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
		"instruction: Keep accepted history\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
	profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: "history", CurrentConfigID: config.ID,
	})
	require.NoError(t, err)
	launched, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: ids.ProjectID, ProfileID: profile.ID, AgentConfigID: config.ID,
		LaunchedBy: identitystore.NewUserPrincipal(ids.ProviderAdminUserID), Message: "Accepted user input survives",
	})
	require.NoError(t, err)
	live, disabled, deleted := uuid.New(), uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO integration_connections
 (id,org_id,project_id,installed_by_user_id,provider,state,provider_tenant_id,provider_account_ref,
  deleted_at,created_at,updated_at)
 VALUES ($1,$4,$5,$6,'github','active','123','456',NULL,now(),now()),
        ($2,$4,$5,$6,'github','disabled','123','457',NULL,now(),now()),
        ($3,$4,$5,$6,'github','disabled','123','458',statement_timestamp(),now(),now())`,
		live, disabled, deleted, ids.OrgID, ids.ProjectID, ids.ProviderAdminUserID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO integration_inbox
 (project_id,connection_id,receipt_key,payload,state,completed_at)
 SELECT $1,$2,'old:'||n,'verified raw callback'::bytea,'completed',statement_timestamp()-interval '8 days'
 FROM generate_series(1,102) n`, ids.ProjectID, live)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO integration_inbox
 (project_id,connection_id,receipt_key,payload,state)
 SELECT $1,$2,'deleted:'||n,'obsolete raw callback'::bytea,'failed' FROM generate_series(1,102) n`,
		ids.ProjectID, deleted)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO integration_inbox
 (project_id,connection_id,receipt_key,payload,state,completed_at,plan,progress)
 VALUES ($1,$2,'recent','recent callback'::bytea,'completed',statement_timestamp()-interval '6 days','{}','{}'),
        ($1,$2,'failed','failed callback'::bytea,'failed',NULL,'{"slot":{"identity":"frozen"}}',
         '{"slot":{"prepared":{"digest":"frozen"}}}'),
        ($1,$3,'disabled-failed','disabled callback'::bytea,'failed',NULL,'{}','{}'),
        ($1,$2,'pending','pending callback'::bytea,'pending',NULL,NULL,'{}')`, ids.ProjectID, live, disabled)
	require.NoError(t, err)
	type retainedReceipt struct {
		key, state, payload, plan, progress string
	}
	readRetained := func() []retainedReceipt {
		rows, err := pool.Query(ctx, `SELECT receipt_key,state,convert_from(payload,'UTF8'),
 coalesce(plan::text,''),progress::text FROM integration_inbox
 WHERE receipt_key IN ('recent','failed','disabled-failed','pending') ORDER BY receipt_key`)
		require.NoError(t, err)
		defer rows.Close()
		var result []retainedReceipt
		for rows.Next() {
			var row retainedReceipt
			require.NoError(t, rows.Scan(&row.key, &row.state, &row.payload, &row.plan, &row.progress))
			result = append(result, row)
		}
		require.NoError(t, rows.Err())
		return result
	}
	retained := readRetained()
	require.Len(t, retained, 4)
	var originalEvents int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM agent_events WHERE agent_id=$1`,
		launched.Agent.ID).Scan(&originalEvents))
	require.Positive(t, originalEvents)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	for _, remaining := range []int{2, 0, 0} {
		runCoreMaintenanceTick(ctx, logger, store)
		var completedCount, deletedCount int
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM integration_inbox WHERE connection_id=$1 AND receipt_key LIKE 'old:%'`,
			live).Scan(&completedCount))
		require.Equal(t, remaining, completedCount, "one completed batch per real maintenance tick")
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM integration_inbox WHERE connection_id=$1`, deleted).Scan(&deletedCount))
		require.Equal(t, remaining, deletedCount, "one deleted-scope batch per real maintenance tick")
		require.Equal(t, retained, readRetained())
		var inputID, agentID uuid.UUID
		var eventCount int
		require.NoError(t, pool.QueryRow(ctx, `SELECT id,agent_id FROM agent_inputs WHERE id=$1`,
			launched.AgentInput.ID).Scan(&inputID, &agentID))
		require.Equal(t, launched.AgentInput.ID, inputID)
		require.Equal(t, launched.Agent.ID, agentID)
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM agent_events WHERE agent_id=$1`,
			launched.Agent.ID).Scan(&eventCount))
		require.Equal(t, originalEvents, eventCount, "agent event history survives raw receipt retention")
	}
	require.Contains(t, logs.String(), "cleaned completed integration inbox")
	require.Contains(t, logs.String(), "cleaned deleted integration inbox")
	require.NotContains(t, logs.String(), `"level":"ERROR"`)
}
