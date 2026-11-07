//go:build integration

package dbmigrate_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestSecretConsumerNormalizationMigration(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 68))
	var orgID, liveProjectID, deletedProjectID, secretID, configID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO orgs(name, created_at, updated_at)
VALUES ('Secret migration', statement_timestamp(), statement_timestamp()) RETURNING id`).Scan(&orgID))
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO projects(org_id, name, created_at, updated_at)
VALUES ($1, 'Live', statement_timestamp(), statement_timestamp()) RETURNING id`, orgID).Scan(&liveProjectID))
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO projects(org_id, name, created_at, updated_at, deleted_at)
VALUES ($1, 'Deleted', statement_timestamp(), statement_timestamp(), statement_timestamp())
RETURNING id`, orgID).Scan(&deletedProjectID))
	// Header secrets could be deleted under the former JSON-reference contract.
	// Preserve the reference so the new resolver fails closed rather than silently
	// changing the configured request's authentication.
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO secrets(
org_id, management_kind, owner_kind, name, kind, deleted_at, created_at, updated_at)
VALUES ($1, 'tenant', 'org', 'formerly-used', 'generic',
statement_timestamp(), statement_timestamp(), statement_timestamp())
RETURNING id`, orgID).Scan(&secretID))
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO model_provider_configs(
org_id, management_kind, name, api_format, base_url, endpoint_path, auth_kind,
credential_secret_id, secret_headers, created_at, updated_at)
VALUES ($1, 'tenant', 'active', 'openai-responses', 'https://example.test', '/responses', 'bearer_token',
$2, jsonb_build_object('X-Gateway-Key', ($2::uuid)::text), statement_timestamp(), statement_timestamp())
RETURNING id`, orgID, secretID).Scan(&configID))
	_, err := pool.Exec(ctx, `INSERT INTO model_provider_configs(
org_id, management_kind, name, api_format, base_url, endpoint_path, auth_kind,
secret_headers, deleted_at, created_at, updated_at)
VALUES ($1, 'tenant', 'deleted', 'openai-responses', 'https://example.test', '/responses', 'bearer_token',
jsonb_build_object('X-Gateway-Key', ($2::uuid)::text),
statement_timestamp(), statement_timestamp(), statement_timestamp())`, orgID, secretID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO event_webhooks(
org_id, project_id, url, event_types, signing_secret_id, deleted_at)
VALUES ($1, $2, 'https://example.test/live', '{tool_call_update}', $4, NULL),
($1, $2, 'https://example.test/deleted', '{tool_call_update}', $4, statement_timestamp()),
($1, $3, 'https://example.test/orphan', '{tool_call_update}', $4, NULL)`,
		orgID, liveProjectID, deletedProjectID, secretID)
	require.NoError(t, err)
	require.NoError(t, applyProductionPostgresMigrations(ctx, db))
	var headerRefs, retiredWebhooks, liveSigningRefs int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM model_provider_header_secrets").Scan(&headerRefs))
	require.Equal(t, 1, headerRefs)
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT count(*) FROM event_webhooks WHERE deleted_at IS NOT NULL AND signing_secret_id IS NULL").Scan(&retiredWebhooks))
	require.Equal(t, 2, retiredWebhooks)
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT count(*) FROM event_webhooks WHERE deleted_at IS NULL AND signing_secret_id = $1", secretID).Scan(&liveSigningRefs))
	require.Equal(t, 1, liveSigningRefs, "a live webhook retains its failed signing configuration")
	config, err := storage.NewStore(pool).Models().GetModelProviderConfig(ctx, orgID, configID)
	require.NoError(t, err)
	require.JSONEq(t, `{"X-Gateway-Key":"`+secretID.String()+`"}`, string(config.SecretHeaders))
	var oldColumn bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
WHERE table_schema = current_schema()
AND table_name = 'model_provider_configs' AND column_name = 'secret_headers')`).Scan(&oldColumn))
	require.False(t, oldColumn, "the child rows are the only persisted header-reference source")
}
