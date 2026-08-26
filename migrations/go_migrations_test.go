package migrations_test

import (
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	schemamigrations "github.com/omnara-ai/omnara/migrations"
	"github.com/pressly/goose/v3"
)

func TestMigrations(t *testing.T) {
	for _, migration := range schemamigrations.GoMigrations() {
		if !migration.UseTx {
			t.Fatalf("Go migration %d must be transactional", migration.Version)
		}
	}
	config, err := pgx.ParseConfig("postgres://unused@localhost/unused?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })

	embeddedProvider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		schemamigrations.Files,
		goose.WithDisableGlobalRegistry(true),
		goose.WithGoMigrations(schemamigrations.GoMigrations()...),
	)
	if err != nil {
		t.Fatalf("construct embedded migration provider: %v", err)
	}
	filesystemProvider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		os.DirFS("."),
		goose.WithDisableGlobalRegistry(true),
		goose.WithGoMigrations(schemamigrations.GoMigrations()...),
	)
	if err != nil {
		t.Fatalf("construct filesystem migration provider: %v", err)
	}
	embeddedSources := embeddedProvider.ListSources()
	filesystemSources := filesystemProvider.ListSources()
	if len(embeddedSources) != len(filesystemSources) {
		t.Fatalf("embedded migrations = %d, filesystem migrations = %d", len(embeddedSources), len(filesystemSources))
	}
	for index, embedded := range embeddedSources {
		filesystem := filesystemSources[index]
		if embedded.Version != filesystem.Version || embedded.Type != filesystem.Type {
			t.Fatalf("embedded migration = %+v, filesystem migration = %+v", embedded, filesystem)
		}
	}
}
