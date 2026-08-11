//go:build integration

package dbschema_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/dbschema"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestRequireVersionDoesNotCreateMissingVersionTable(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)

	err := dbschema.RequireVersion(ctx, pool, 16)
	if err == nil || !strings.Contains(err.Error(), "run omnara-migrate") {
		t.Fatalf("missing version table error = %v", err)
	}
	var exists bool
	if err := pool.QueryRow(
		ctx,
		`SELECT to_regclass('goose_db_version') IS NOT NULL`,
	).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("readiness check created goose_db_version")
	}
}

func TestRequireVersionAcceptsNewerAdditiveSchemaVersion(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../migrations")
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO goose_db_version(version_id, is_applied) VALUES (17, true)`,
	); err != nil {
		t.Fatal(err)
	}
	if err := dbschema.RequireVersion(ctx, pool, 16); err != nil {
		t.Fatalf("newer additive schema readiness: %v", err)
	}
}
