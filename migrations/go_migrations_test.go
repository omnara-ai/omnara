package migrations_test

import (
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	schemamigrations "github.com/omnara-ai/omnara/migrations"
	"github.com/pressly/goose/v3"
)

func TestGoMigrationsMatchDirectory(t *testing.T) {
	config, err := pgx.ParseConfig("postgres://unused@localhost/unused?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })

	if _, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		os.DirFS("."),
		goose.WithDisableGlobalRegistry(true),
		goose.WithGoMigrations(schemamigrations.GoMigrations()...),
	); err != nil {
		t.Fatalf("construct scoped migration provider: %v", err)
	}
}
