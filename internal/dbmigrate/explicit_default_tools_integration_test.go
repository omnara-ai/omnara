//go:build integration

package dbmigrate_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"testing/fstest"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	schemamigrations "github.com/omnara-ai/omnara/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestExplicitDefaultToolsMigration(t *testing.T) {
	for _, collision := range []bool{false, true} {
		t.Run(fmt.Sprintf("collision_%v", collision), func(t *testing.T) {
			ctx := context.Background()
			pool := integrationdb.OpenUnmigratedPool(t, ctx)
			db := stdlib.OpenDBFromPool(pool)
			defer func() { _ = db.Close() }()
			_, err := db.ExecContext(ctx, `
				CREATE TABLE agent_configs (
					id uuid PRIMARY KEY, project_id uuid NOT NULL, source text NOT NULL,
					source_format text NOT NULL, source_hash text NOT NULL,
					definition jsonb NOT NULL, compiled_definition jsonb NOT NULL,
					effective_definition_hash text NOT NULL,
					UNIQUE(project_id, effective_definition_hash, source_format, source_hash));
				CREATE FUNCTION reject_config_update() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN RAISE EXCEPTION 'immutable'; END $$;
				CREATE TRIGGER agent_configs_immutable BEFORE UPDATE OR DELETE ON agent_configs
				FOR EACH ROW EXECUTE FUNCTION reject_config_update();
				CREATE TABLE config_references (kind text PRIMARY KEY, config_id uuid REFERENCES agent_configs(id));`)
			require.NoError(t, err)
			projectID, configID := uuid.New(), uuid.MustParse("00000000-0000-0000-0000-000000000002")
			insert := func(id uuid.UUID, source, compiled string) error {
				_, err := db.ExecContext(ctx, `
					INSERT INTO agent_configs VALUES ($1,$2,$3,'json',$4,$5::jsonb,$5::jsonb,$6)`,
					id, projectID, source, fmt.Sprintf("%x", sha256.Sum256([]byte(source))),
					compiled, fmt.Sprintf("%x", sha256.Sum256([]byte(compiled))))
				return err
			}
			legacySource := `{"skills":["skill"]}`
			legacyCompiled := `{"skills":[{"public_id":"skill"}]}`
			require.NoError(t, insert(configID, legacySource, legacyCompiled))
			for _, kind := range []string{"agent", "profile_version", "model_context"} {
				_, err = db.ExecContext(ctx, "INSERT INTO config_references VALUES ($1,$2)", kind, configID)
				require.NoError(t, err)
			}
			disabledID := uuid.New()
			disabled := `{"skills":[{"public_id":"skill"}],"tools":{"skill":{"enabled":false}}}`
			require.NoError(t, insert(disabledID, disabled, disabled))
			skillID, err := publicid.Encode(publicid.KindSkill, uuid.New())
			require.NoError(t, err)
			numericSource := `{"instruction":"Help","model":{"provider_config":"openai","name":"test"},` +
				`"subagents":{"worker":{"type":"self"}},"skills":["` + skillID + `"],"tools":{"custom":{"type":"custom","description":"Custom",` +
				`"input_schema":{"type":"object","properties":{"x":{"type":"number","minimum":1e-7,"maximum":1e21}}}}}}`
			opts := agentconfig.CompileOptions{
				ResolveSkillID: func(id string) (agentconfig.SkillResolution, error) {
					return agentconfig.SkillResolution{PublicID: id, Name: "test"}, nil
				},
			}
			normalized, err := agentconfig.Compile(agentconfig.SourceFormatJSON, []byte(numericSource), opts)
			require.NoError(t, err)
			var numericLegacy agentconfig.Compiled
			require.NoError(t, json.Unmarshal(normalized.CanonicalJSON, &numericLegacy))
			delete(numericLegacy.Tools, "skill")
			for _, name := range toolcatalog.SubagentToolNames() {
				delete(numericLegacy.Tools, name)
			}
			numericEncoded, err := agentconfig.EncodeCompiled(numericLegacy)
			require.NoError(t, err)
			numericID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
			require.NoError(t, insert(numericID, numericSource, string(numericEncoded.CanonicalJSON)))
			subagentID := uuid.New()
			subagentSource := `{"subagents":{"worker":{"type":"self"}},"tools":{"spawn_agent":{"enabled":false},"read_agent":{"permission":{"mode":"always_ask"}}}}`
			subagentCompiled := `{"subagents":{"worker":{"type":"self"}},"tools":{"read_agent":{"enabled":true,"permission":{"mode":"always_ask","parameters":{}}},"spawn_agent":{"enabled":false,"permission":{"mode":"always_allow","parameters":{}}}}}`
			require.NoError(t, insert(subagentID, subagentSource, subagentCompiled))
			if collision {
				source, err := agentconfig.AddSourceTools(agentconfig.SourceFormatJSON, []byte(legacySource), []string{"skill"})
				require.NoError(t, err)
				require.NoError(t, insert(uuid.New(), string(source),
					`{"skills":[{"public_id":"skill"}],"tools":{"skill":`+
						`{"enabled":true,"permission":{"mode":"always_allow","parameters":{}}}}}`))
			}
			var migration *goose.Migration
			for _, candidate := range schemamigrations.GoMigrations() {
				if candidate.Version == 38 {
					migration = candidate
				}
			}
			require.NotNil(t, migration)
			provider, err := goose.NewProvider(goose.DialectPostgres, db, fstest.MapFS{},
				goose.WithDisableGlobalRegistry(true), goose.WithGoMigrations(migration))
			require.NoError(t, err)
			_, err = provider.Up(ctx)
			if collision {
				require.ErrorContains(t, err, "duplicate key")
			} else {
				require.NoError(t, err)
			}
			var source, sourceHash, effectiveHash string
			var compiled, definition []byte
			require.NoError(t, db.QueryRowContext(ctx, `
				SELECT source, source_hash, definition, compiled_definition, effective_definition_hash
				FROM agent_configs WHERE id=$1`, configID).Scan(&source, &sourceHash, &definition, &compiled, &effectiveHash))
			require.JSONEq(t, string(compiled), string(definition))
			require.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte(source))), sourceHash)
			if collision {
				require.Equal(t, legacySource, source)
				require.JSONEq(t, legacyCompiled, string(compiled))
				require.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte(legacyCompiled))), effectiveHash)
			} else {
				contract, err := agentconfig.RuntimeContractFromCompiled(compiled, agentconfig.CompilerVersion, effectiveHash)
				require.NoError(t, err)
				require.Len(t, contract.Tools, 1)
				require.Equal(t, "skill", contract.Tools[0].Name)
				require.Equal(t, "always_allow", contract.Tools[0].Permission.Mode)
				var references int
				require.NoError(t, db.QueryRowContext(ctx, `
					SELECT count(*) FROM config_references r JOIN agent_configs c ON c.id=r.config_id
					WHERE c.id=$1 AND c.compiled_definition->'tools' ? 'skill'`, configID).Scan(&references))
				require.Equal(t, 3, references)
				require.ErrorContains(t, insert(uuid.New(), legacySource, legacyCompiled), "agent_configs_explicit_default_tools")
				require.ErrorContains(t, insert(uuid.New(), subagentSource, subagentCompiled),
					"agent_configs_explicit_default_tools")
				for _, missing := range toolcatalog.SubagentToolNames() {
					incomplete := numericLegacy
					incomplete.Tools = make(map[string]agentconfig.ToolCompiled)
					for name, tool := range normalized.Compiled.Tools {
						if name != missing {
							incomplete.Tools[name] = tool
						}
					}
					encoded, err := agentconfig.EncodeCompiled(incomplete)
					require.NoError(t, err)
					require.ErrorContains(t, insert(uuid.New(), numericSource, string(encoded.CanonicalJSON)),
						"agent_configs_explicit_default_tools")
				}
				require.NoError(t, insert(uuid.New(), `{}`, `{}`))
				_, err = provider.Up(ctx)
				require.NoError(t, err)
			}
			require.NoError(t, db.QueryRowContext(ctx, "SELECT source FROM agent_configs WHERE id=$1", disabledID).Scan(&source))
			require.Equal(t, disabled, source)
			require.NoError(t, db.QueryRowContext(ctx, `
				SELECT source, compiled_definition, effective_definition_hash FROM agent_configs WHERE id=$1`,
				subagentID).Scan(&source, &compiled, &effectiveHash))
			if collision {
				require.Equal(t, subagentSource, source)
				require.JSONEq(t, subagentCompiled, string(compiled))
			} else {
				contract, err := agentconfig.RuntimeContractFromCompiled(compiled, agentconfig.CompilerVersion, effectiveHash)
				require.NoError(t, err)
				require.Len(t, contract.Tools, 4)
				for _, tool := range contract.Tools {
					require.NotEqual(t, "spawn_agent", tool.Name)
					if tool.Name == "read_agent" {
						require.Equal(t, "always_ask", tool.Permission.Mode)
					}
				}
				require.Contains(t, source, `"spawn_agent":{"enabled":false}`)
			}
			require.NoError(t, db.QueryRowContext(ctx, `
				SELECT source, compiled_definition, effective_definition_hash FROM agent_configs WHERE id=$1`,
				numericID).Scan(&source, &compiled, &effectiveHash))
			_, err = agentconfig.RuntimeContractFromCompiled(compiled, agentconfig.CompilerVersion, effectiveHash)
			require.NoError(t, err)
			if collision {
				require.Equal(t, numericSource, source)
				require.Equal(t, numericEncoded.Hash, effectiveHash)
			} else {
				recompiled, err := agentconfig.Compile(agentconfig.SourceFormatJSON, []byte(source), opts)
				require.NoError(t, err)
				require.Equal(t, normalized.Hash, effectiveHash)
				require.Equal(t, recompiled.Hash, effectiveHash)
				require.JSONEq(t, string(normalized.CanonicalJSON), string(compiled))
			}
			_, err = db.ExecContext(ctx, "UPDATE agent_configs SET source=source WHERE id=$1", configID)
			require.ErrorContains(t, err, "immutable")
		})
	}
}
