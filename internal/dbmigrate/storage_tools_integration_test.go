//go:build integration

package dbmigrate_test

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"testing"
	"testing/fstest"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/dbmigrate"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	schemamigrations "github.com/omnara-ai/omnara/migrations"
	"github.com/stretchr/testify/require"
)

func TestStorageToolsMigrationSourceIdentity(t *testing.T) {
	t.Parallel()
	const oldCompiled = `{"instruction":"Example.","tools":{"read_file":{}}}`
	const newCompiled = `{"instruction":"Example.","tools":{"storage_read":{}}}`
	const oldSource = `{"tools":{"read_file":{}},"instruction":"Example."}`
	const newSource = `{"tools":{"storage_read":{}},"instruction":"Example."}`
	type config struct {
		source, compiled, wantSource string
	}
	for _, test := range []struct {
		name             string
		configs          []config
		separateProjects bool
		collision        bool
	}{
		{
			name: "preserve field order",
			configs: []config{
				{oldSource, oldCompiled, newSource},
				{oldCompiled, oldCompiled, newCompiled},
			},
		},
		{
			name: "collision with unchanged config",
			configs: []config{
				{oldSource, oldCompiled, oldSource},
				{newSource, newCompiled, newSource},
			},
			collision: true,
		},
		{
			name: "collision without source",
			configs: []config{
				{"", oldCompiled, ""},
				{"", newCompiled, ""},
			},
			collision: true,
		},
		{
			name: "equivalent configs in different projects",
			configs: []config{
				{oldSource, oldCompiled, newSource},
				{newSource, newCompiled, newSource},
			},
			separateProjects: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			db := openStorageToolsMigrationTestDB(t)
			projectID := uuid.New()
			ids := make([]uuid.UUID, len(test.configs))
			for i, config := range test.configs {
				ids[i] = uuid.New()
				if test.separateProjects {
					projectID = uuid.New()
				}
				var source, format, sourceHash any
				if config.source != "" {
					source, format, sourceHash = config.source, "json", storageToolFixtureHash(config.source)
				}
				_, err := db.ExecContext(ctx, `INSERT INTO agent_configs
 (id,project_id,source,source_format,source_hash,compiled_definition,effective_definition_hash)
 VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7)`, ids[i], projectID, source, format, sourceHash,
					config.compiled, storageToolFixtureHash(config.compiled))
				require.NoError(t, err)
			}
			err := applyStorageToolsMigration(t, db)
			if test.collision {
				require.ErrorContains(t, err, "would make configs")
				require.ErrorContains(t, err, "duplicates")
				// Sequence changes survive rollback, so this proves no UPDATE was attempted.
				var attempted bool
				require.NoError(t, db.QueryRowContext(ctx, `SELECT is_called FROM config_update_attempts`).Scan(&attempted))
				require.False(t, attempted)
				require.EqualValues(t, 0, currentPostgresMigrationVersion(t, ctx, db))
			} else {
				require.NoError(t, err)
				require.EqualValues(t, 55, currentPostgresMigrationVersion(t, ctx, db))
				var valid bool
				require.NoError(t, db.QueryRowContext(ctx, `SELECT indisvalid FROM pg_index
 WHERE indexrelid='tool_calls_memory_file_idx'::regclass`).Scan(&valid))
				require.True(t, valid)
			}
			for i, config := range test.configs {
				var source, sourceHash sql.NullString
				var hash string
				require.NoError(t, db.QueryRowContext(ctx, `SELECT source,source_hash,effective_definition_hash
 FROM agent_configs WHERE id=$1`, ids[i]).Scan(&source, &sourceHash, &hash))
				require.Equal(t, config.wantSource, source.String)
				require.Equal(t, config.wantSource != "", source.Valid)
				require.Equal(t, source.Valid, sourceHash.Valid)
				if source.Valid {
					require.Equal(t, storageToolFixtureHash(config.wantSource), sourceHash.String)
				}
				wantCompiled := newCompiled
				if test.collision {
					wantCompiled = config.compiled
				}
				require.Equal(t, storageToolFixtureHash(wantCompiled), hash)
			}
			var trigger string
			require.NoError(t, db.QueryRowContext(ctx, `SELECT tgenabled FROM pg_trigger
 WHERE tgrelid='agent_configs'::regclass AND tgname='agent_configs_immutable'`).Scan(&trigger))
			require.Equal(t, "O", trigger)
		})
	}
}

func openStorageToolsMigrationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	pool := integrationdb.OpenUnmigratedPool(t, t.Context())
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	_, err := db.ExecContext(t.Context(), `
CREATE TABLE agent_configs (
 id uuid PRIMARY KEY, project_id uuid NOT NULL,
 source text, source_format text, source_hash text,
 compiled_definition jsonb NOT NULL, effective_definition_hash text NOT NULL,
 UNIQUE NULLS NOT DISTINCT (project_id,effective_definition_hash,source_format,source_hash)
);
CREATE FUNCTION reject_config_update() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'immutable config'; END $$;
CREATE TRIGGER agent_configs_immutable BEFORE UPDATE ON agent_configs
 FOR EACH ROW EXECUTE FUNCTION reject_config_update();
CREATE SEQUENCE config_update_attempts;
CREATE FUNCTION record_config_update() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN PERFORM nextval('config_update_attempts'); RETURN NEW; END $$;
CREATE TRIGGER record_config_update BEFORE UPDATE ON agent_configs
 FOR EACH ROW EXECUTE FUNCTION record_config_update();
CREATE TABLE tool_calls (agent_id uuid, type text, name text, input jsonb);
`)
	require.NoError(t, err)
	return db
}

func applyStorageToolsMigration(t *testing.T, db *sql.DB) error {
	t.Helper()
	for _, migration := range schemamigrations.GoMigrations() {
		if migration.Version == 55 {
			return dbmigrate.ApplyPostgres(t.Context(), db, fstest.MapFS{}, migration)
		}
	}
	t.Fatal("storage tools migration is missing")
	return nil
}

func storageToolFixtureHash(raw string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
}
