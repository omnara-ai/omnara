package integrationdb

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/dbmigrate"
)

func TestMigrateLocalDatabase(t *testing.T) {
	ctx := context.Background()
	dsn := os.Getenv(databaseURLEnv)
	if dsn == "" {
		t.Skip(databaseURLEnv + " is not set")
	}
	AssertTestDatabaseURL(t, dsn)

	db, err := dbmigrate.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("open integration database: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := dbmigrate.ApplyPostgres(
		ctx,
		db,
		os.DirFS("../../../migrations"),
	); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
}

func TestCleanStaleGeneratedDatabases(t *testing.T) {
	ctx := context.Background()
	dsn := os.Getenv(databaseURLEnv)
	if dsn == "" {
		t.Skip(databaseURLEnv + " is not set")
	}
	olderThan := time.Hour
	if value := os.Getenv(cleanOlderThanEnv); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil {
			t.Fatalf("parse %s: %v", cleanOlderThanEnv, err)
		}
		olderThan = parsed
	}
	runID, runIDSet := os.LookupEnv(cleanDatabaseRunIDEnv)
	if runIDSet && sanitizeRunID(runID) == "" {
		t.Fatalf("refusing to clean generated integration databases with empty %s", cleanDatabaseRunIDEnv)
	}
	if !runIDSet && olderThan == 0 {
		t.Fatalf("refusing to clean all inactive generated integration databases without %s", cleanDatabaseRunIDEnv)
	}
	dropped := dropStaleGeneratedDatabases(t, ctx, dsn, olderThan, runID)
	for _, databaseName := range dropped {
		t.Logf("dropped stale integration database %s", databaseName)
	}
}
