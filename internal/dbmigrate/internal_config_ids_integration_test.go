//go:build integration

package dbmigrate_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
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
	for _, failure := range []string{
		"", "hash", "reference", "process", "model", "source_hash", "missing_profile", "collision",
		"partial_self_name", "partial_self_provider", "partial_profile_name", "partial_profile_provider",
	} {
		t.Run(failure, func(t *testing.T) {
			partial := strings.HasPrefix(failure, "partial_") || failure == "source_hash" ||
				failure == "missing_profile" || failure == "collision"
			wantFailure := failure != "" && !strings.HasPrefix(failure, "partial_")
			ctx := context.Background()
			pool := integrationdb.OpenUnmigratedPool(t, ctx)
			db := stdlib.OpenDBFromPool(pool)
			defer func() { _ = db.Close() }()
			_, err := db.ExecContext(ctx, `
				CREATE TABLE agent_configs (
					id uuid PRIMARY KEY, org_id uuid NOT NULL, project_id uuid NOT NULL, configured_model_id uuid NOT NULL,
					source text, source_format text, source_hash text,
					compiled_definition jsonb NOT NULL, effective_definition_hash text NOT NULL,
					compiler_version text NOT NULL, definition jsonb NOT NULL DEFAULT '{}',
					created_at timestamptz NOT NULL DEFAULT now(),
					UNIQUE NULLS NOT DISTINCT (effective_definition_hash, source_format, source_hash));
				CREATE FUNCTION reject_config_update() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN RAISE EXCEPTION 'immutable'; END $$;
				CREATE TRIGGER agent_configs_immutable BEFORE UPDATE OR DELETE ON agent_configs
				FOR EACH ROW EXECUTE FUNCTION reject_config_update();
				CREATE TABLE config_references (id uuid PRIMARY KEY, config_id uuid REFERENCES agent_configs(id));
				CREATE TABLE model_provider_configs (id uuid PRIMARY KEY, org_id uuid, name text, deleted_at timestamptz);
				CREATE TABLE configured_models (
					id uuid PRIMARY KEY, org_id uuid, model_provider_config_id uuid, name text, deleted_at timestamptz);
				CREATE TABLE agent_profiles (id uuid PRIMARY KEY, project_id uuid, current_version_id uuid, deleted_at timestamptz);
				CREATE TABLE agent_profile_versions (
					id uuid PRIMARY KEY, project_id uuid, profile_id uuid, agent_config_id uuid, deleted_at timestamptz);`)
			require.NoError(t, err)
			secretID, skillID, modelID := uuid.New(), uuid.New(), uuid.New()
			orgID, childModelID, providerID := uuid.New(), uuid.New(), uuid.New()
			projectID, profileID, versionID := uuid.New(), uuid.New(), uuid.New()
			otherProviderID, profileModelID, otherChildID, testProfileModelID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			_, err = db.ExecContext(ctx, `INSERT INTO model_provider_configs VALUES ($1,$2,'test',NULL)`, providerID, orgID)
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, `INSERT INTO model_provider_configs VALUES ($1,$2,'other',NULL)`,
				otherProviderID, orgID)
			require.NoError(t, err)
			for _, model := range []struct {
				id, provider uuid.UUID
				name         string
			}{
				{modelID, providerID, "parent"}, {profileModelID, otherProviderID, "profile-model"},
				{otherChildID, otherProviderID, "child"}, {testProfileModelID, providerID, "profile-model"},
			} {
				_, err = db.ExecContext(ctx, `INSERT INTO configured_models VALUES ($1,$2,$3,$4,NULL)`,
					model.id, orgID, model.provider, model.name)
				require.NoError(t, err)
			}
			_, err = db.ExecContext(ctx, `INSERT INTO configured_models VALUES ($1,$2,$3,'child',NULL)`,
				childModelID, orgID, providerID)
			require.NoError(t, err)
			if failure == "model" {
				_, err = db.ExecContext(ctx, `UPDATE configured_models SET org_id=$1`, uuid.New())
				require.NoError(t, err)
			}
			secretPublic, err := publicid.Encode(publicid.KindSecret, secretID)
			require.NoError(t, err)
			skillPublic, err := publicid.Encode(publicid.KindSkill, skillID)
			require.NoError(t, err)
			if failure == "reference" {
				skillPublic = "invalid"
			}
			selection := `"provider_config":"test","name":"child"`
			typeFields := `"type":"self"`
			expectedModelID, expectedProvider, expectedName := childModelID, "test", "child"
			if partial {
				selection = `"name":"child"`
			}
			if strings.HasSuffix(failure, "_provider") {
				selection = `"provider_config":"test"`
				expectedModelID, expectedName = modelID, "parent"
			}
			if strings.Contains(failure, "profile") {
				profilePublic, err := publicid.Encode(publicid.KindAgentProfile, profileID)
				require.NoError(t, err)
				typeFields = `"type":"profile","profile_id":"` + profilePublic + `"`
				if strings.HasSuffix(failure, "_provider") {
					expectedModelID, expectedName = testProfileModelID, "profile-model"
				} else {
					expectedModelID, expectedProvider = otherChildID, "other"
				}
			}
			legacy := `{"model":{"configured_model_id":"` + modelID.String() + `"},"skills":[{"public_id":"` + skillPublic +
				`"}],"subagents":{"worker":{` + typeFields + `,"model":{` + selection +
				`,"context_window_tokens":2048}},"inherit":{"type":"self"}}}`
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
			profileConfigID := uuid.New()
			profileConfig := `{"model":{"configured_model_id":"` + profileModelID.String() + `"}}`
			_, err = db.ExecContext(ctx, `INSERT INTO agent_configs
(id, org_id, project_id, configured_model_id, compiled_definition, effective_definition_hash, compiler_version)
VALUES ($1,$2,$3,$4,$5,$6,'')`,
				profileConfigID, orgID, projectID, profileModelID, profileConfig, hash([]byte(profileConfig)))
			require.NoError(t, err)
			if failure != "missing_profile" {
				_, err = db.ExecContext(ctx, `INSERT INTO agent_profiles VALUES ($1,$2,$3,NULL)`, profileID, projectID, versionID)
				require.NoError(t, err)
				_, err = db.ExecContext(ctx, `INSERT INTO agent_profile_versions VALUES ($1,$2,$3,$4,NULL)`,
					versionID, projectID, profileID, profileConfigID)
				require.NoError(t, err)
			}
			sources := []*string{
				new("# untouched YAML\nskills: [" + skillPublic + "]\n"),
				new(`{"skills":["` + skillPublic + `"]}`), nil,
			}
			if partial {
				sourceType := `"type":"self"`
				if strings.Contains(failure, "profile") {
					sourceType = `"type":"profile","profile":"helper"`
				}
				model := `"model":{` + selection + `,"context_window_tokens":2048}`
				sources[0] = new("# Keep this comment\ninstruction: Help.\nmodel: {provider_config: test, name: parent}\n" +
					"subagents:\n    worker: {" + sourceType + ", " + model + "}\n")
				sources[1] = new(`{"instruction":"Help.","model":{"provider_config":"test","name":"parent"},` +
					`"subagents":{"worker":{` + sourceType + `,` + model + `}}}`)
			}
			for index, source := range sources {
				var format, sourceHash *string
				if source != nil {
					format = new([]string{"yaml", "json"}[index])
					sourceHash = new(fmt.Sprintf("%x", sha256.Sum256([]byte(*source))))
					if failure == "source_hash" {
						sourceHash = new("invalid")
					}
				}
				_, err := db.ExecContext(ctx, `INSERT INTO agent_configs
					(id, configured_model_id, source, source_format, source_hash, org_id, project_id,
compiled_definition, effective_definition_hash, compiler_version)
					VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'')`,
					ids[index], modelID, source, format, sourceHash, orgID, projectID, legacy, oldHash)
				require.NoError(t, err)
				_, err = db.ExecContext(ctx, `INSERT INTO config_references VALUES ($1,$1)`, ids[index])
				require.NoError(t, err)
			}
			if failure == "collision" {
				fullSource := strings.Replace(*sources[0], `{"name":"child"`,
					`{"provider_config": "test", "name":"child"`, 1)
				fullCompiled := strings.Replace(legacy, selection, `"provider_config":"test","name":"child"`, 1)
				_, err = db.ExecContext(ctx, `INSERT INTO agent_configs
(id, org_id, project_id, configured_model_id, source, source_format, source_hash,
compiled_definition, effective_definition_hash, compiler_version)
VALUES ($1,$2,$3,$4,$5,'yaml',$6,$7,$8,'')`, uuid.New(), orgID, projectID, modelID, fullSource,
					fmt.Sprintf("%x", sha256.Sum256([]byte(fullSource))), fullCompiled, hash([]byte(fullCompiled)))
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
			if wantFailure {
				require.Error(t, err)
				if failure == "collision" {
					require.ErrorContains(t, err, "unique constraint")
				}
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
					require.Equal(t, expectedModelID, contract.Subagents["worker"].Model.ConfiguredModelID)
					require.Equal(t, new(2048), contract.Subagents["worker"].Model.ContextWindowTokens)
					require.Nil(t, contract.Subagents["inherit"].Model)
					if partial {
						var source, format, sourceHash *string
						require.NoError(t, db.QueryRowContext(ctx, `SELECT source, source_format, source_hash
FROM agent_configs WHERE id=$1`, id).Scan(&source, &format, &sourceHash))
						if source != nil {
							parsed, err := agentconfig.ParseSource(agentconfig.SourceFormat(*format), []byte(*source))
							require.NoError(t, err)
							require.Equal(t, expectedProvider, parsed.Subagents["worker"].Model.ProviderConfig)
							require.Equal(t, expectedName, parsed.Subagents["worker"].Model.Name)
							require.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte(*source))), *sourceHash)
						} else {
							require.Nil(t, sourceHash)
						}
					}
				}
				var after []byte
				require.NoError(t, db.QueryRowContext(ctx, snapshot).Scan(&after))
				var oldRows, newRows []map[string]any
				require.NoError(t, json.Unmarshal(before, &oldRows))
				require.NoError(t, json.Unmarshal(after, &newRows))
				for index := range oldRows {
					if partial {
						delete(oldRows[index], "source")
						delete(oldRows[index], "source_hash")
						delete(newRows[index], "source")
						delete(newRows[index], "source_hash")
					}
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
				if wantFailure {
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
