//go:build integration

package main

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func TestRunCoreMaintenanceCleansUpDespiteAnotherTaskFailure(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool, store := seedMaintenanceWebhook(t)
	_, err := pool.Exec(ctx, "DROP TABLE user_auth_tokens")
	require.NoError(t, err)
	result := runCoreMaintenance(ctx, store)
	require.ErrorContains(t, result.AuthCleanupErr, "delete inactive auth tokens")
	require.NoError(t, result.WebhookCleanupErr)
	require.NoError(t, result.ReapRuntimeLocksErr)
	require.NoError(t, result.ExpireDaemonRuntimesErr)
	require.NoError(t, result.ExpireProcessToolsErr)
	require.EqualValues(t, 1, result.DeletedWebhooks)
}

func TestRunCoreMaintenanceParallelTasksAndCancellation(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool, store := seedMaintenanceWebhook(t)
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "LOCK TABLE agent_runtime_locks IN ACCESS EXCLUSIVE MODE")
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan coreMaintenanceResult, 1)
	go func() { done <- runCoreMaintenance(runCtx, store) }()
	require.Eventually(t, func() bool {
		var count int
		err := pool.QueryRow(ctx, "SELECT count(*) FROM event_webhook_deliveries").Scan(&count)
		return err == nil && count == 0
	}, 3*time.Second, 10*time.Millisecond)
	select {
	case <-done:
		t.Fatal("maintenance returned while the runtime-lock task was still blocked")
	default:
	}
	cancel()
	select {
	case result := <-done:
		require.ErrorIs(t, result.ReapRuntimeLocksErr, context.Canceled)
		require.NoError(t, result.WebhookCleanupErr)
		require.EqualValues(t, 1, result.DeletedWebhooks)
	case <-time.After(3 * time.Second):
		t.Fatal("maintenance did not finish after cancellation")
	}
}

func seedMaintenanceWebhook(t *testing.T) (*pgxpool.Pool, *storage.Store) {
	t.Helper()
	ctx := t.Context()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../migrations")
	store := storage.NewStore(pool)
	ids := storagefixture.ProjectIDs{
		OrgID: uuid.New(), ProjectID: uuid.New(), ProviderAdminUserID: uuid.New(),
		ProviderSecretID: uuid.New(), ProviderSecretVersionID: uuid.New(), ProviderConfigID: uuid.New(),
	}
	storagefixture.SeedProject(t, ctx, pool, ids, time.Now().UTC())
	config := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
		`instruction: test
model:
  provider_config: openai-prod
  name: test
event_webhook:
  url: https://example.com/events
  events: [agent_input]
`)
	agent, err := store.Execution().CreateAgentFixture(ctx, executionstore.AgentFixtureInput{
		ProjectID: ids.ProjectID, CurrentConfigID: config.ID,
	})
	require.NoError(t, err)
	updated, err := pool.Exec(ctx, `
		UPDATE event_webhook_deliveries
		SET created_at = statement_timestamp() - interval '11 minutes'
		WHERE agent_id = $1`, agent.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, updated.RowsAffected())
	return pool, store
}
