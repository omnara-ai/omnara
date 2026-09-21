//go:build integration

package dbmigrate_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	schemamigrations "github.com/omnara-ai/omnara/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestInternalConfigIDsMigration(t *testing.T) {
	const snapshot = `SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM agent_configs c`
	for _, failure := range []string{"", "hash", "reference", "process"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			pool := integrationdb.OpenUnmigratedPool(t, ctx)
			db := stdlib.OpenDBFromPool(pool)
			defer func() { _ = db.Close() }()
			_, err := db.ExecContext(ctx, `
				CREATE TABLE agent_configs (
					id uuid PRIMARY KEY, configured_model_id uuid NOT NULL,
					source text, source_format text, source_hash text,
					compiled_definition jsonb NOT NULL, effective_definition_hash text NOT NULL,
					compiler_version text NOT NULL, definition jsonb NOT NULL DEFAULT '{}',
					created_at timestamptz NOT NULL DEFAULT now(),
					UNIQUE NULLS NOT DISTINCT (effective_definition_hash, source_format, source_hash));
				CREATE FUNCTION reject_config_update() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN RAISE EXCEPTION 'immutable'; END $$;
				CREATE TRIGGER agent_configs_immutable BEFORE UPDATE OR DELETE ON agent_configs
				FOR EACH ROW EXECUTE FUNCTION reject_config_update();
				CREATE TABLE config_references (id uuid PRIMARY KEY, config_id uuid REFERENCES agent_configs(id));`)
			require.NoError(t, err)
			secretID, skillID, modelID := uuid.New(), uuid.New(), uuid.New()
			secretPublic, err := publicid.Encode(publicid.KindSecret, secretID)
			require.NoError(t, err)
			skillPublic, err := publicid.Encode(publicid.KindSkill, skillID)
			require.NoError(t, err)
			if failure == "reference" {
				skillPublic = "invalid"
			}
			legacy := `{"model":{"configured_model_id":"` + modelID.String() + `"},"skills":[{"public_id":"` + skillPublic + `"}]} `
			hash := func(raw []byte) string {
				var value any
				require.NoError(t, json.Unmarshal(raw, &value))
				canonical, err := json.Marshal(value)
				require.NoError(t, err)
				return fmt.Sprintf("%x", sha256.Sum256(canonical))
			}
			oldHash := hash([]byte(legacy))
			if failure == "hash" {
				oldHash = "invalid"
			}
			ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
			sources := []*string{
				new("# untouched YAML\nskills: [" + skillPublic + "]\n"),
				new(`{"skills":["` + skillPublic + `"]}`), nil,
			}
			for index, source := range sources {
				var format, sourceHash *string
				if source != nil {
					format = new([]string{"yaml", "json"}[index])
					sourceHash = new(fmt.Sprintf("%x", sha256.Sum256([]byte(*source))))
				}
				_, err := db.ExecContext(ctx, `INSERT INTO agent_configs
					(id, configured_model_id, source, source_format, source_hash,
compiled_definition, effective_definition_hash, compiler_version)
					VALUES ($1,$2,$3,$4,$5,$6,$7,'')`, ids[index], modelID, source, format, sourceHash, legacy, oldHash)
				require.NoError(t, err)
				_, err = db.ExecContext(ctx, `INSERT INTO config_references VALUES ($1,$1)`, ids[index])
				require.NoError(t, err)
			}
			targets := [][2]string{
				{"machine_pools", "default_machine_secret_env"},
				{"project_machine_pool_grants", "default_machine_secret_env_overlay"},
				{"machines", "secret_env"}, {"agent_machine_bindings", "secret_env_overlay"}, {"processes", "secret_env"},
			}
			for _, target := range targets {
				_, err := db.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE %s (id uuid PRIMARY KEY,
%s jsonb NOT NULL, updated_at timestamptz NOT NULL DEFAULT '2020-01-01Z')`, target[0], target[1]))
				require.NoError(t, err)
				value := secretPublic
				if failure == "process" && target[0] == "processes" {
					value = "invalid"
				}
				_, err = db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (id, %s) VALUES ($1, $2)`, target[0], target[1]),
					ids[0], `{"TOKEN":"`+value+`","REMOVE":null}`)
				require.NoError(t, err)
			}
			var before []byte
			require.NoError(t, db.QueryRowContext(ctx, snapshot).Scan(&before))
			var migration *goose.Migration
			for _, candidate := range schemamigrations.GoMigrations() {
				if candidate.Version == 42 {
					migration = candidate
				}
			}
			require.NotNil(t, migration)
			provider, err := goose.NewProvider(goose.DialectPostgres, db, fstest.MapFS{},
				goose.WithDisableGlobalRegistry(true), goose.WithGoMigrations(migration))
			require.NoError(t, err)
			_, err = provider.Up(ctx)
			if failure != "" {
				require.Error(t, err)
				var after []byte
				require.NoError(t, db.QueryRowContext(ctx, snapshot).Scan(&after))
				require.Equal(t, before, after)
			} else {
				require.NoError(t, err)
				for _, id := range ids {
					var compiled []byte
					var effectiveHash, version string
					require.NoError(t, db.QueryRowContext(ctx, `
SELECT compiled_definition, effective_definition_hash, compiler_version
FROM agent_configs WHERE id=$1`, id).Scan(&compiled, &effectiveHash, &version))
					require.Equal(t, "1", version)
					require.Equal(t, hash(compiled), effectiveHash)
					contract, err := agentconfig.RuntimeContractFromCompiled(compiled, effectiveHash)
					require.NoError(t, err)
					require.Equal(t, skillID, contract.Skills[0].ID)
				}
				var after []byte
				require.NoError(t, db.QueryRowContext(ctx, snapshot).Scan(&after))
				var oldRows, newRows []map[string]any
				require.NoError(t, json.Unmarshal(before, &oldRows))
				require.NoError(t, json.Unmarshal(after, &newRows))
				for index := range oldRows {
					for _, key := range []string{"compiled_definition", "effective_definition_hash", "compiler_version"} {
						delete(oldRows[index], key)
						delete(newRows[index], key)
					}
				}
				require.Equal(t, oldRows, newRows)
				var count int
				require.NoError(t, db.QueryRowContext(ctx, `
SELECT count(*) FROM config_references r JOIN agent_configs c ON r.config_id=c.id`).Scan(&count))
				require.Equal(t, 3, count)
				_, err = db.ExecContext(ctx, `DELETE FROM goose_db_version WHERE version_id=40`)
				require.NoError(t, err)
				_, err = provider.Up(ctx)
				require.NoError(t, err)
				var rerun []byte
				require.NoError(t, db.QueryRowContext(ctx, snapshot).Scan(&rerun))
				require.Equal(t, after, rerun)
			}
			for _, target := range targets {
				var raw []byte
				var updatedAt time.Time
				query := fmt.Sprintf(`SELECT %s, updated_at FROM %s WHERE id=$1`, target[1], target[0])
				require.NoError(t, db.QueryRowContext(ctx, query, ids[0]).Scan(&raw, &updatedAt))
				want := secretID.String()
				if failure != "" {
					want = secretPublic
				}
				if failure == "process" && target[0] == "processes" {
					want = "invalid"
				}
				require.JSONEq(t, `{"TOKEN":"`+want+`","REMOVE":null}`, string(raw))
				require.True(t, updatedAt.Equal(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)))
			}
			_, err = db.ExecContext(ctx, `UPDATE agent_configs SET source=source WHERE id=$1`, ids[0])
			require.ErrorContains(t, err, "immutable")
		})
	}
}
