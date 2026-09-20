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
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 40))
	ids := storagefixture.ProjectIDs{
		OrgID: uuid.New(), ProjectID: uuid.New(), ProviderAdminUserID: uuid.New(),
		ProviderSecretID: uuid.New(), ProviderSecretVersionID: uuid.New(), ProviderConfigID: uuid.New(),
	}
	storagefixture.SeedProject(t, ctx, pool, ids, time.Now())
	create := func(name string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		err := pool.QueryRow(ctx, `INSERT INTO project_apps
			(org_id,project_id,name,definition_id,provider,state,created_at,updated_at)
			VALUES ($1,$2,$3,'omnara.slack','slack','disconnected',now(),now()) RETURNING id`,
			ids.OrgID, ids.ProjectID, name).Scan(&id)
		require.NoError(t, err)
		return id
	}
	first, second := create("engineering"), create("support")
	// Separate saved apps can attach the same physical bot. An unconnected app
	// has no verified identity yet; once attached, that identity cannot change.
	for _, id := range []uuid.UUID{first, second} {
		_, err := pool.Exec(ctx,
			`UPDATE project_apps SET provider_tenant_id='T123',provider_account_ref='A123' WHERE id=$1`, id)
		require.NoError(t, err)
	}
	for _, statement := range []string{
		`UPDATE project_apps SET name='renamed' WHERE id=$1`,
		`UPDATE project_apps SET definition_id='omnara.discord' WHERE id=$1`,
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
