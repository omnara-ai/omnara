//go:build integration

package dbmigrate_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func TestAppOwnedSchemaKeepsIndependentSetupAndImmutableIdentity(t *testing.T) {
	ctx := t.Context()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 45))
	var appTypeHasDefault bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT column_default IS NOT NULL
        FROM information_schema.columns WHERE table_schema='public'
            AND table_name='project_apps' AND column_name='app_type'`).Scan(&appTypeHasDefault))
	require.False(t, appTypeHasDefault, "the legacy backfill must not silently type new apps")
	var obsolete bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT
        EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public'
            AND table_name='integration_targets' AND column_name='target_ref')
        OR EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='public'
            AND indexname='integration_targets_agent_target_ref_idx')
        OR EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='integration_targets'::regclass
            AND pg_get_constraintdef(oid) LIKE '%target_ref%')`).Scan(&obsolete))
	require.False(t, obsolete, "database-only aliases and their constraints are removed")
	ids := storagefixture.ProjectIDs{
		OrgID: uuid.New(), ProjectID: uuid.New(), ProviderAdminUserID: uuid.New(),
		ProviderSecretID: uuid.New(), ProviderSecretVersionID: uuid.New(), ProviderConfigID: uuid.New(),
	}
	storagefixture.SeedProject(t, ctx, pool, ids, time.Now())
	create := func(name string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		err := pool.QueryRow(ctx, `INSERT INTO project_apps
			(org_id,project_id,name,app_type,state,created_at,updated_at)
			VALUES ($1,$2,$3,'slack_thread','disconnected',now(),now()) RETURNING id`,
			ids.OrgID, ids.ProjectID, name).Scan(&id)
		require.NoError(t, err)
		return id
	}
	first, second := create("engineering"), create("support")
	for _, id := range []uuid.UUID{first, second} {
		_, err := pool.Exec(ctx,
			`UPDATE project_apps SET provider_tenant_id='T123',provider_account_ref='A123' WHERE id=$1`, id)
		require.NoError(t, err)
	}
	for _, statement := range []string{
		`UPDATE project_apps SET name='renamed' WHERE id=$1`,
		`UPDATE project_apps SET app_type='discord_thread' WHERE id=$1`,
		`UPDATE project_apps SET provider_tenant_id='T456' WHERE id=$1`,
		`UPDATE project_apps SET provider_account_ref=NULL,provider_tenant_id=NULL WHERE id=$1`,
	} {
		_, err := pool.Exec(ctx, statement, first)
		require.ErrorContains(t, err, "project app identity is immutable")
	}
	_, err := pool.Exec(ctx, `UPDATE project_apps SET state='active' WHERE id=$1`, first)
	require.ErrorContains(t, err, "check constraint", "activation requires credentials and installer attribution")
	_, err = pool.Exec(ctx, `UPDATE project_apps SET settings='{"launcher":null}' WHERE id=$1`, first)
	require.NoError(t, err)
	var revision int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT setup_revision FROM project_apps WHERE id=$1`, first).Scan(&revision))
	require.Equal(t, int64(1), revision, "behavior edits must not invalidate provider sessions")
	_, err = pool.Exec(ctx, `UPDATE project_apps SET deleted_at=now() WHERE id=$1`, first)
	require.NoError(t, err)
	replacement := create("engineering")
	require.NotEqual(t, first, replacement, "reusing a name must create a different identity")
	var otherDeleted bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT deleted_at IS NOT NULL FROM project_apps WHERE id=$1`, second).Scan(&otherDeleted))
	require.False(t, otherDeleted, "deleting one setup must not delete another setup using the same bot")
}

func TestAppActorMigrationPreservesLegacySlackAttribution(t *testing.T) {
	ctx := t.Context()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 44))
	ids := storagefixture.ProjectIDs{
		OrgID: uuid.New(), ProjectID: uuid.New(), ProviderAdminUserID: uuid.New(),
		ProviderSecretID: uuid.New(), ProviderSecretVersionID: uuid.New(), ProviderConfigID: uuid.New(),
	}
	storagefixture.SeedProject(t, ctx, pool, ids, time.Now())
	id := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO actors
		(id,project_id,provider,provider_tenant_id,provider_user_id,display_name,created_at,updated_at)
		VALUES($1,$2,'slack','T_OLD','U_OLD','Historical sender',now(),now())`, id, ids.ProjectID)
	require.NoError(t, err)
	var before, after []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_jsonb(a) FROM actors a WHERE id=$1`, id).Scan(&before))
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 46))
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_jsonb(a) FROM actors a WHERE id=$1`, id).Scan(&after))
	require.JSONEq(t, string(before), string(after), "cutover preserves actor identity and metadata")
}
