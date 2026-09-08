//go:build integration

package dbmigrate_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestModelProviderTimeoutMigrationPreservesConfiguredTotals(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 30))
	orgID := uuid.New()
	if _, err := db.ExecContext(
		ctx,
		`INSERT INTO orgs(id,name,created_at,updated_at) VALUES($1,'Timeout migration',now(),now())`,
		orgID,
	); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO model_provider_configs(
 org_id,management_kind,name,api_format,api_variant,base_url,endpoint_path,
 request_timeout_ms,auth_kind,auth_options,deleted_at,created_at,updated_at
 ) VALUES($1,'tenant',$2,'openai-responses','default','https://example.test','/responses',$3,'bearer_token','{}',
 now(),now(),now())`
	for name, total := range map[string]int{"previous-default": 600000, "custom-total": 900000} {
		if _, err := db.ExecContext(ctx, insert, orgID, name, total); err != nil {
			t.Fatal(err)
		}
	}
	require.NoError(t, applyProductionPostgresMigrations(ctx, db))
	for name, want := range map[string]int{"previous-default": 600000, "custom-total": 900000} {
		var total, idle int
		require.NoError(
			t,
			db.QueryRowContext(ctx, `SELECT request_timeout_ms,idle_timeout_ms
FROM model_provider_configs WHERE org_id=$1 AND name=$2`, orgID, name).
				Scan(&total, &idle),
		)
		if total != want || idle != 300000 {
			t.Fatalf("%s total=%d idle=%d", name, total, idle)
		}
	}
	var total, idle int
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO model_provider_configs(
 org_id,management_kind,name,api_format,api_variant,base_url,endpoint_path,
 auth_kind,auth_options,deleted_at,created_at,updated_at
 ) VALUES($1,'tenant','new-default','openai-responses','default','https://example.test','/responses',
 'bearer_token','{}',
 now(),now(),now())
 RETURNING request_timeout_ms,idle_timeout_ms`, orgID).Scan(&total, &idle))
	if total != 3600000 || idle != 300000 {
		t.Fatalf("new defaults total=%d idle=%d", total, idle)
	}
	if _, err := db.ExecContext(
		ctx,
		`UPDATE model_provider_configs SET idle_timeout_ms=0 WHERE org_id=$1`,
		orgID,
	); err == nil {
		t.Fatal("database accepted zero idle timeout")
	}
}
