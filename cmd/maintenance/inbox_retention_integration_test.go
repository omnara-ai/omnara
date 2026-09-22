//go:build integration

package main

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
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
	store := newMaintenanceInboxStore(t, pool, ids)
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
	live := createMaintenanceInboxApp(t, store, ids, "live").ID
	disconnected := createMaintenanceInboxApp(t, store, ids, "disconnected").ID
	deleted := createMaintenanceInboxApp(t, store, ids, "deleted").ID
	applied, err := store.Integrations().DisconnectProjectApp(ctx, integrationstore.DisconnectProjectAppInput{
		ProjectID: ids.ProjectID, AppID: disconnected,
	})
	require.NoError(t, err)
	require.True(t, applied)
	require.NoError(t, store.Integrations().DeleteProjectApp(ctx, ids.OrgID, ids.ProjectID, deleted))
	_, err = pool.Exec(ctx, `INSERT INTO integration_inbox
 (project_id,app_id,receipt_key,payload,state,completed_at)
 SELECT $1,$2,'old:'||n,'verified raw callback'::bytea,'completed',statement_timestamp()-interval '8 days'
 FROM generate_series(1,102) n`, ids.ProjectID, live)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO integration_inbox
 (project_id,app_id,receipt_key,payload,state,completed_at)
 SELECT $1,$2,'deleted:'||n,'obsolete raw callback'::bytea,'failed',statement_timestamp()
 FROM generate_series(1,102) n`,
		ids.ProjectID, deleted)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO integration_inbox
 (project_id,app_id,receipt_key,payload,state,completed_at,plan,progress)
 VALUES ($1,$2,'recent','recent callback'::bytea,'completed',statement_timestamp()-interval '6 days','{}','{}'),
        ($1,$2,'failed','failed callback'::bytea,'failed',statement_timestamp(),'{"slot":{"identity":"frozen"}}',
         '{"slot":{"prepared":{"digest":"frozen"}}}'),
        ($1,$3,'disabled-failed','disabled callback'::bytea,'failed',statement_timestamp(),'{}','{}'),
        ($1,$2,'pending','pending callback'::bytea,'pending',NULL,NULL,'{}')`, ids.ProjectID, live, disconnected)
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
	until := time.Now().Add(10 * time.Second)
	for {
		runCoreMaintenanceTick(ctx, logger, store)
		var obsolete int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM integration_inbox
 WHERE (app_id=$1 AND receipt_key LIKE 'old:%') OR app_id=$2`, live, deleted).Scan(&obsolete))
		if obsolete == 0 {
			break
		}
		require.True(t, time.Now().Before(until), "retention must make progress across ticks")
	}
	for range 2 {
		runCoreMaintenanceTick(ctx, logger, store)
		var completedCount, deletedCount int
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM integration_inbox WHERE app_id=$1 AND receipt_key LIKE 'old:%'`,
			live).Scan(&completedCount))
		require.Zero(t, completedCount, "completed receipts stay deleted")
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM integration_inbox WHERE app_id=$1`, deleted).Scan(&deletedCount))
		require.Zero(t, deletedCount, "deleted-scope receipts stay deleted")
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

func TestCoreMaintenanceInboxRetentionResumesAfterBatchTimeout(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../migrations")
	ids := storagefixture.ProjectIDs{
		OrgID: uuid.New(), ProjectID: uuid.New(), ProviderAdminUserID: uuid.New(),
		ProviderSecretID: uuid.New(), ProviderSecretVersionID: uuid.New(), ProviderConfigID: uuid.New(),
	}
	storagefixture.SeedProject(t, ctx, pool, ids, time.Now())
	store := newMaintenanceInboxStore(t, pool, ids)
	app := createMaintenanceInboxApp(t, store, ids, "retention-timeout").ID
	_, err := pool.Exec(ctx, `INSERT INTO integration_inbox
 (project_id,app_id,receipt_key,payload,state,completed_at)
 SELECT $1,$2,'old:'||n,'x'::bytea,'completed',statement_timestamp()-interval '8 days'
 FROM generate_series(1,202) n`, ids.ProjectID, app)
	require.NoError(t, err)
	count, err := store.Integrations().CleanupTerminalIntegrationInbox(ctx, integrationInboxRetention, 100)
	require.NoError(t, err)
	require.EqualValues(t, 100, count)
	// Sequence increments survive rollback, so only the first maintenance attempt stalls.
	_, err = pool.Exec(ctx, `CREATE SEQUENCE inbox_cleanup_deletes;
 CREATE FUNCTION slow_inbox_cleanup() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
   IF nextval('inbox_cleanup_deletes') = 50 THEN PERFORM pg_sleep(10); END IF;
   RETURN OLD;
 END $$;
 CREATE TRIGGER slow_inbox_cleanup BEFORE DELETE ON integration_inbox
 FOR EACH ROW EXECUTE FUNCTION slow_inbox_cleanup()`)
	require.NoError(t, err)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	runCoreMaintenanceTick(ctx, logger, store)
	var remaining int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM integration_inbox`).Scan(&remaining))
	require.Equal(t, 102, remaining, "timed-out batch rolls back every delete; the first batch stays committed")
	require.Contains(t, logs.String(), `"level":"ERROR"`, "hard deadline failure must be logged")
	require.Contains(t, logs.String(), "cleanup completed integration inbox")
	logs.Reset()
	until := time.Now().Add(10 * time.Second)
	for {
		runCoreMaintenanceTick(ctx, logger, store)
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM integration_inbox`).Scan(&remaining))
		if remaining == 0 {
			break
		}
		require.True(t, time.Now().Before(until), "later ticks must retry and drain the timed-out batch")
	}
	require.NotContains(t, logs.String(), `"level":"ERROR"`)
}

func newMaintenanceInboxStore(t *testing.T, pool *pgxpool.Pool, ids storagefixture.ProjectIDs) *storage.Store {
	t.Helper()
	wrapper, err := secrets.NewLocalKeyWrapper("maintenance-test", map[string][]byte{
		"maintenance-test": []byte("0123456789abcdef0123456789abcdef"),
	})
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), `INSERT INTO org_memberships(org_id,user_id,role,created_at)
        VALUES($1,$2,'owner',now())`, ids.OrgID, ids.ProviderAdminUserID)
	require.NoError(t, err)
	return storage.NewStore(pool, storage.WithSecretKeyWrapper(wrapper))
}

func createMaintenanceInboxApp(
	t *testing.T, store *storage.Store, ids storagefixture.ProjectIDs, name string,
) integrationstore.ProjectAppRecord {
	t.Helper()
	ctx := t.Context()
	input := integrationstore.ConfigureProjectAppInput{
		OrgID: ids.OrgID, ProjectID: ids.ProjectID, InstalledByUserID: ids.ProviderAdminUserID,
	}
	input.Provider, input.ProviderTenantID, input.ProviderAccountRef = "github", "123", "456"
	input.CredentialAppID = 123
	material := secrets.GitHubAppCredentialsMaterial{
		AppID: "123", PrivateKey: "fixture-key-verified-by-caller", WebhookSecret: "fixture-webhook",
	}
	credential, version, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: ids.OrgID, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: ids.ProjectID,
		Name: name + "-credentials", Actor: identitystore.NewUserPrincipal(ids.ProviderAdminUserID), Material: material,
	})
	require.NoError(t, err)
	app, err := store.Integrations().CreateProjectApp(ctx, integrationstore.SaveProjectAppInput{
		OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: name, AppType: appdefinition.GitHubPR,
	})
	require.NoError(t, err)
	input.AppID, input.ExpectedSetupRevision = app.ID, app.SetupRevision
	input.CredentialSecretID, input.CredentialVersionID = credential.ID, version.ID
	app, err = store.Integrations().ConfigureProjectApp(ctx, input)
	require.NoError(t, err)
	require.Equal(t, integrationstore.ProjectAppStateActive, app.State)
	require.Equal(t, credential.ID, app.CredentialSecretID)
	return app
}
