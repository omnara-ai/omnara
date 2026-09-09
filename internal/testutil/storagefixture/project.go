// Package storagefixture uses leaf stores so same-package storage tests can
// import it without a cycle.
package storagefixture

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type ProjectIDs struct {
	OrgID                   uuid.UUID
	ProjectID               uuid.UUID
	ProviderAdminUserID     uuid.UUID
	ProviderSecretID        uuid.UUID
	ProviderSecretVersionID uuid.UUID
	ProviderConfigID        uuid.UUID
}

// SeedProject uses a placeholder secret that cannot be decrypted.
func SeedProject(t testing.TB, ctx context.Context, pool *pgxpool.Pool, ids ProjectIDs, now time.Time) {
	t.Helper()
	_, err := pool.Exec(
		ctx,
		`INSERT INTO users(id, display_name, created_at, updated_at)
		 VALUES ($1, 'Default Provider Admin', $2, $2)`,
		ids.ProviderAdminUserID,
		now,
	)
	require.NoError(t, err, "seed default-provider admin")
	_, err = pool.Exec(
		ctx,
		`
INSERT INTO orgs(id, name, idempotency_key, created_at, updated_at)
VALUES ($1, 'Test Org', 'idem-test-org', $2, $2)
`,
		ids.OrgID,
		now,
	)
	require.NoError(t, err, "seed org")
	InsertProject(t, ctx, pool, ids.OrgID, ids.ProjectID, "Test Project", "idem-test-project", now)
	tx, err := pool.Begin(ctx)
	require.NoError(t, err, "begin default provider fixture")
	defer func() { _ = tx.Rollback(ctx) }()
	// Secret-reference guards must see the secret before the provider is inserted.
	_, err = tx.Exec(
		ctx,
		`
INSERT INTO secrets(
    id, org_id, management_kind, owner_kind, name, kind, metadata, current_version_id, created_at, updated_at
  )
  VALUES ($2, $1, 'tenant', 'org', 'default-provider-key', 'generic', '{}'::jsonb, $3, $4, $4)
ON CONFLICT (id) DO NOTHING`,
		ids.OrgID, ids.ProviderSecretID, ids.ProviderSecretVersionID, now,
	)
	require.NoError(t, err, "seed default provider secret")
	_, err = tx.Exec(ctx, `
INSERT INTO secret_versions(
    id, org_id, secret_id, version_number, payload_keys, encryption_scheme, key_id, dek_wrapped_by,
    encrypted_dek, encrypted_dek_nonce, nonce, ciphertext, created_at
  )
  VALUES (
    $3, $1, $2, 1, ARRAY['value'], 'aes-256-gcm-envelope-v1', 'test-key', 'local',
    decode(repeat('01', 48), 'hex'), decode(repeat('02', 12), 'hex'),
    decode(repeat('03', 12), 'hex'), decode(repeat('04', 32), 'hex'), $4
  )
ON CONFLICT (id) DO NOTHING`,
		ids.OrgID, ids.ProviderSecretID, ids.ProviderSecretVersionID, now,
	)
	require.NoError(t, err, "seed default provider secret version")
	_, err = tx.Exec(ctx, `
INSERT INTO model_provider_configs(
  id, org_id, management_kind, name, api_format, api_variant, base_url, endpoint_path, auth_kind,
  credential_secret_id, created_at, updated_at
)
VALUES (
  $3, $1, 'tenant', 'openai-prod', 'openai-responses', 'default', 'https://api.openai.com/v1',
  '/responses', 'bearer_token', $2, $4, $4
)
ON CONFLICT (id) DO NOTHING`,
		ids.OrgID,
		ids.ProviderSecretID,
		ids.ProviderConfigID,
		now,
	)
	require.NoError(t, err, "seed default model provider config")
	require.NoError(t, tx.Commit(ctx), "commit default provider fixture")
}

func InsertProject(
	t testing.TB,
	ctx context.Context,
	pool *pgxpool.Pool,
	orgID, projectID uuid.UUID,
	name, idempotencyKey string,
	now time.Time,
) {
	t.Helper()
	_, err := pool.Exec(ctx, `
INSERT INTO projects(id, org_id, name, idempotency_key, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $5)
`, projectID, orgID, name, idempotencyKey, now)
	require.NoError(t, err, "insert project %q (%s)", name, projectID)
}
