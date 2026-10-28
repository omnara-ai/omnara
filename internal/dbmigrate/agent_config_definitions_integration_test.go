//go:build integration

package dbmigrate_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

const agentConfigDefinitionsMigration = 61

type agentConfigDefinitionsFixture struct {
	db                *sql.DB
	orgID             uuid.UUID
	projectID         uuid.UUID
	configuredModelID uuid.UUID
}

func openAgentConfigDefinitionsFixture(t *testing.T, ctx context.Context) agentConfigDefinitionsFixture {
	t.Helper()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, agentConfigDefinitionsMigration-1))

	f := agentConfigDefinitionsFixture{
		db:                db,
		orgID:             uuid.New(),
		projectID:         uuid.New(),
		configuredModelID: uuid.New(),
	}
	secretID, secretVersionID, providerConfigID, revisionID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC()
	// Secrets and configured models point at their current versions through
	// deferred foreign keys, so the fixture is seeded in one transaction.
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []struct {
		label string
		query string
		args  []any
	}{
		{"org", `INSERT INTO orgs(id, name, created_at, updated_at) VALUES ($1, 'Definitions Org', $2, $2)`,
			[]any{f.orgID, now}},
		{"project", `INSERT INTO projects(id, org_id, name, created_at, updated_at)
VALUES ($1, $2, 'Definitions Project', $3, $3)`, []any{f.projectID, f.orgID, now}},
		{"secret", `INSERT INTO secrets(id, org_id, management_kind, owner_kind, name, kind, metadata,
    current_version_id, created_at, updated_at)
VALUES ($1, $2, 'tenant', 'org', 'definitions-provider-key', 'generic', '{}'::jsonb, $3, $4, $4)`,
			[]any{secretID, f.orgID, secretVersionID, now}},
		{"secret version", `INSERT INTO secret_versions(id, org_id, secret_id, version_number, payload_keys,
    encryption_scheme, key_id, dek_wrapped_by, encrypted_dek, encrypted_dek_nonce, nonce, ciphertext, created_at)
VALUES ($1, $2, $3, 1, ARRAY['value'], 'aes-256-gcm-envelope-v1', 'test-key', 'local',
    decode(repeat('01', 48), 'hex'), decode(repeat('02', 12), 'hex'), decode(repeat('03', 12), 'hex'),
    decode(repeat('04', 32), 'hex'), $4)`, []any{secretVersionID, f.orgID, secretID, now}},
		{"model provider config", `INSERT INTO model_provider_configs(id, org_id, management_kind, name, api_format,
    api_variant, base_url, endpoint_path, auth_kind, credential_secret_id, created_at, updated_at)
VALUES ($1, $2, 'tenant', 'definitions-provider', 'openai-responses', 'default', 'https://api.openai.com/v1',
    '/responses', 'bearer_token', $3, $4, $4)`, []any{providerConfigID, f.orgID, secretID, now}},
		{"configured model", `WITH configured_model AS (
INSERT INTO configured_models(id, org_id, model_provider_config_id, management_kind, name, current_revision_id,
    created_at, updated_at)
VALUES ($1, $2, $3, 'tenant', 'definitions-model', $4, $5, $5)
RETURNING id, org_id, model_provider_config_id, current_revision_id
)
INSERT INTO configured_model_revisions(id, org_id, configured_model_id, model_provider_config_id,
    provider_model_slug, context_window_tokens, max_output_tokens, created_at)
SELECT current_revision_id, org_id, id, model_provider_config_id, 'definitions-model', 128000, 8192, $5
FROM configured_model`, []any{f.configuredModelID, f.orgID, providerConfigID, revisionID, now}},
	} {
		_, err := tx.ExecContext(ctx, statement.query, statement.args...)
		require.NoError(t, err, statement.label)
	}
	require.NoError(t, tx.Commit())
	return f
}

func (f agentConfigDefinitionsFixture) insertConfig(
	t *testing.T,
	ctx context.Context,
	source *string,
	compiled, hash string,
	createdAt time.Time,
) uuid.UUID {
	t.Helper()
	var sourceFormat, sourceHash *string
	if source != nil {
		format, digest := "yaml", "source-"+*source
		sourceFormat, sourceHash = &format, &digest
	}
	id := uuid.New()
	_, err := f.db.ExecContext(ctx, `INSERT INTO agent_configs(id, org_id, project_id, configured_model_id,
    source, source_format, source_hash, compiled_definition, effective_definition_hash, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10)`,
		id, f.orgID, f.projectID, f.configuredModelID, source, sourceFormat, sourceHash, compiled, hash, createdAt)
	require.NoError(t, err)
	return id
}

func TestAgentConfigDefinitionsMigrationSharesCompiledConfigs(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := openAgentConfigDefinitionsFixture(t, ctx)

	sourceA, sourceAFormatted := "instruction: a\n", "# same meaning\ninstruction:   a\n"
	compiledA, compiledB := `{"instruction":"a"}`, `{"instruction":"b"}`
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	firstA := f.insertConfig(t, ctx, &sourceA, compiledA, "hash-a", base)
	formattedA := f.insertConfig(t, ctx, &sourceAFormatted, compiledA, "hash-a", base.Add(time.Minute))
	generatedA := f.insertConfig(t, ctx, nil, compiledA, "hash-a", base.Add(2*time.Minute))
	onlyB := f.insertConfig(t, ctx, &sourceA, compiledB, "hash-b", base.Add(3*time.Minute))

	require.NoError(t, applyProductionPostgresMigrations(ctx, f.db))

	var definitions int
	require.NoError(t, f.db.QueryRowContext(ctx,
		`SELECT count(*) FROM agent_config_definitions WHERE project_id = $1`, f.projectID).Scan(&definitions))
	require.Equal(t, 2, definitions)

	definitionOf := func(configID uuid.UUID) (uuid.UUID, string, sql.NullString, time.Time) {
		t.Helper()
		var definitionID uuid.UUID
		var hash string
		var source sql.NullString
		var definitionCreatedAt time.Time
		require.NoError(t, f.db.QueryRowContext(ctx, `
SELECT definition.id, definition.effective_definition_hash, config.source, definition.created_at
FROM agent_configs config
JOIN agent_config_definitions definition ON definition.project_id = config.project_id
  AND definition.id = config.definition_id
WHERE config.id = $1`, configID).Scan(&definitionID, &hash, &source, &definitionCreatedAt))
		return definitionID, hash, source, definitionCreatedAt
	}
	sharedID, sharedHash, firstSource, sharedCreatedAt := definitionOf(firstA)
	require.Equal(t, "hash-a", sharedHash)
	require.True(t, sharedCreatedAt.Equal(base), "definition should take its earliest version's created_at")
	require.Equal(t, sourceA, firstSource.String)
	for configID, wantSource := range map[uuid.UUID]sql.NullString{
		formattedA: {String: sourceAFormatted, Valid: true},
		generatedA: {},
	} {
		definitionID, _, source, _ := definitionOf(configID)
		require.Equal(t, sharedID, definitionID, "equivalent configs should share one definition")
		require.Equal(t, wantSource, source, "migration must keep each version's source as it was")
	}
	var staleRevisions int
	require.NoError(t, f.db.QueryRowContext(ctx,
		`SELECT count(*) FROM agent_configs WHERE project_id = $1 AND source_revision <> 1`, f.projectID).Scan(&staleRevisions))
	require.Zero(t, staleRevisions, "existing versions should be saved under revision 1")
	otherID, otherHash, _, _ := definitionOf(onlyB)
	require.NotEqual(t, sharedID, otherID)
	require.Equal(t, "hash-b", otherHash)

	_, err := f.db.ExecContext(ctx, `UPDATE agent_configs SET source = 'rewritten' WHERE id = $1`, firstA)
	require.ErrorContains(t, err, "agent_configs are immutable")
	_, err = f.db.ExecContext(ctx, `UPDATE agent_configs SET definition_id = $2 WHERE id = $1`, firstA, otherID)
	require.ErrorContains(t, err, "agent_configs are immutable")
	_, err = f.db.ExecContext(ctx, `DELETE FROM agent_configs WHERE id = $1`, firstA)
	require.ErrorContains(t, err, "agent_configs are immutable")
	_, err = f.db.ExecContext(ctx, `UPDATE agent_config_definitions SET created_at = now() WHERE id = $1`, sharedID)
	require.ErrorContains(t, err, "agent_config_definitions are immutable")

	tx, err := f.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `SET LOCAL omnara.agent_config_definition_rewrite = 'on'`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE agent_configs SET definition_id = $2 WHERE id = $1`, firstA, otherID)
	require.NoError(t, err, "a migration that opts in may repoint a version at another definition")
	_, err = tx.ExecContext(ctx, `UPDATE agent_configs SET source = 'rewritten' WHERE id = $1`, firstA)
	require.ErrorContains(t, err, "agent_configs are immutable", "opting in never permits rewriting source")
}

func TestAgentConfigDefinitionsMigrationRejectsHashCollisions(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := openAgentConfigDefinitionsFixture(t, ctx)

	sourceA, sourceB := "instruction: a\n", "instruction: b\n"
	now := time.Now().UTC()
	f.insertConfig(t, ctx, &sourceA, `{"instruction":"a"}`, "colliding-hash", now)
	f.insertConfig(t, ctx, &sourceB, `{"instruction":"b"}`, "colliding-hash", now)

	err := applyProductionPostgresMigrations(ctx, f.db)
	require.ErrorContains(t, err, "share effective definition hash colliding-hash but differ in compiled content")
	require.Equal(t, int64(agentConfigDefinitionsMigration-1), currentPostgresMigrationVersion(t, ctx, f.db))
}
