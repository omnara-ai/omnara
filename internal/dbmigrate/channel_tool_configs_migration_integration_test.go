//go:build integration

package dbmigrate_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestPostgresChannelToolConfigMigrationPreservesPinnedReferences(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 37))
	fixture := seedLegacyChannelMigrationFixture(t, ctx, db)
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 38))
	unchangedSource := "instruction: Unchanged config.\n" +
		"model: {provider_config: migration provider, name: migration model}\n"
	unchangedCompiled, err := agentconfig.Compile(
		agentconfig.SourceFormatYAML, []byte(unchangedSource), agentconfig.CompileOptions{})
	require.NoError(t, err)
	unchangedHash := sha256.Sum256([]byte(unchangedSource))
	var unchangedID, unchangedBefore string
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO agent_configs(
  org_id, project_id, configured_model_id, definition, source, source_format,
  source_hash, compiled_definition, compiler_version, effective_definition_hash, created_at
) SELECT org_id, project_id, configured_model_id, $2::jsonb, $3, 'yaml',
  $4, $2::jsonb, '', $5, created_at FROM agent_configs
WHERE id = (SELECT current_config_id FROM agents WHERE id = $1)
RETURNING id::text, to_jsonb(agent_configs)::text`, fixture.agentID,
		unchangedCompiled.CanonicalJSON, unchangedSource, hex.EncodeToString(unchangedHash[:]), unchangedCompiled.Hash).
		Scan(&unchangedID, &unchangedBefore))

	var contextID, configID string
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO model_call_contexts(
  org_id, project_id, agent_id, operation_kind, attempt_number, agent_config_id,
  configured_model_revision_id, input_event_sequence, runtime_lock_id, created_at
) SELECT agent.org_id, agent.project_id, agent.id, 'normal', 1, config.id,
  model.current_revision_id, 1, uuidv7(), statement_timestamp()
FROM agents agent
JOIN agent_configs config ON config.id = agent.current_config_id
JOIN configured_models model ON model.id = config.configured_model_id
WHERE agent.id = $1 RETURNING id::text, agent_config_id::text`, fixture.agentID).
		Scan(&contextID, &configID))
	store := storage.NewStore(pool)
	projectID := uuid.MustParse(fixture.projectID)
	original, found, err := store.Execution().GetAgentConfig(ctx, projectID, uuid.MustParse(configID))
	require.NoError(t, err)
	require.True(t, found)
	_, err = agentconfig.RuntimeContractFromCompiled(
		original.CompiledDefinition, original.CompilerVersion, original.EffectiveDefinitionHash)
	require.ErrorContains(t, err, "not registered", "pin the actual pre-migration failure")
	before := channelConfigReferences(t, ctx, db)
	require.NoError(t, applyProductionPostgresMigrations(ctx, db))
	require.JSONEq(t, before, channelConfigReferences(t, ctx, db))
	var unchangedAfter string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT to_jsonb(config)::text FROM agent_configs config WHERE id = $1`, unchangedID).Scan(&unchangedAfter))
	require.Equal(t, unchangedBefore, unchangedAfter, "unaffected configs remain byte-identical")

	var pinnedID string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT agent_config_id::text FROM model_call_contexts WHERE id = $1`, contextID).Scan(&pinnedID))
	require.Equal(t, configID, pinnedID)
	migrated, found, err := store.Execution().GetAgentConfig(ctx, projectID, uuid.MustParse(pinnedID))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, original.ID, migrated.ID)
	require.Equal(t, original.ConfiguredModelID, migrated.ConfiguredModelID)
	require.Equal(t, original.CreatedAt, migrated.CreatedAt)
	runtime, err := agentconfig.RuntimeContractFromCompiled(
		migrated.CompiledDefinition, migrated.CompilerVersion, migrated.EffectiveDefinitionHash)
	require.NoError(t, err, "unfinished context loads its original config ID through the normal reader")
	require.Equal(t, "Preserve the integration migration fixture.", runtime.Instruction)
	require.Len(t, runtime.Tools, 3)
	require.Equal(t, "read_file", runtime.Tools[1].Name)
	require.Equal(t, "search_files", runtime.Tools[2].Name)
	require.Equal(t, "custom_task", runtime.Tools[0].Name)
	require.JSONEq(t, `{"type":"object","properties":{"count":{"type":"number","maximum":1e30}}}`,
		string(runtime.Tools[0].InputSchema), "jsonb exponent formatting cannot change the compiled hash contract")
	require.NotContains(t, migrated.Source, "send_integration_message")
	require.NotContains(t, migrated.Source, "set_integration_target")
	_, err = agentconfig.ParseSource(agentconfig.SourceFormat(migrated.SourceFormat), []byte(migrated.Source))
	require.NoError(t, err, "migrated source remains editable through the ordinary API")
	sourceHash := sha256.Sum256([]byte(migrated.Source))
	require.Equal(t, hex.EncodeToString(sourceHash[:]), migrated.SourceHash)
	require.NoError(t, applyProductionPostgresMigrations(ctx, db))
	replayed, found, err := store.Execution().GetAgentConfig(ctx, projectID, migrated.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, migrated, replayed)
	assertChannelConfigImmutabilityRestored(t, ctx, db, configID)
}

func TestPostgresChannelToolConfigMigrationRollsBackDuplicates(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 37))
	fixture := seedLegacyChannelMigrationFixture(t, ctx, db)
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 38))
	var configID, source string
	var compiled []byte
	require.NoError(t, db.QueryRowContext(ctx, `SELECT config.id::text, config.source,
  config.compiled_definition::text FROM agent_configs config
JOIN agents agent ON agent.current_config_id = config.id WHERE agent.id = $1`, fixture.agentID).
		Scan(&configID, &source, &compiled))
	// A distinct valid old config becomes identical when the retired builtin
	// settings disappear. Let the normal unique constraint reject it atomically.
	source = strings.Replace(source, "send_integration_message: {permission:",
		"send_integration_message: {enabled: false, permission:", 1)
	var value map[string]any
	require.NoError(t, json.Unmarshal(compiled, &value))
	tools, ok := value["tools"].(map[string]any)
	require.True(t, ok)
	legacyTool, ok := tools["send_integration_message"].(map[string]any)
	require.True(t, ok)
	legacyTool["enabled"] = false
	compiled, err := json.Marshal(value)
	require.NoError(t, err)
	sourceHash, compiledHash := sha256.Sum256([]byte(source)), sha256.Sum256(compiled)
	_, err = db.ExecContext(ctx, `INSERT INTO agent_configs(
  org_id, project_id, configured_model_id, definition, source, source_format,
  source_hash, compiled_definition, compiler_version, effective_definition_hash, created_at
) SELECT org_id, project_id, configured_model_id, $2::jsonb, $3, source_format,
  $4, $2::jsonb, compiler_version, $5, created_at FROM agent_configs WHERE id = $1`,
		configID, compiled, source, hex.EncodeToString(sourceHash[:]), hex.EncodeToString(compiledHash[:]))
	require.NoError(t, err)
	var before, after string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT jsonb_agg(to_jsonb(config) ORDER BY id)::text FROM agent_configs config`).Scan(&before))
	err = applyProductionPostgresMigrations(ctx, db)
	require.ErrorContains(t, err, "duplicate key", "operator must resolve this observed case before retrying")
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT jsonb_agg(to_jsonb(config) ORDER BY id)::text FROM agent_configs config`).Scan(&after))
	require.JSONEq(t, before, after, "no partially rewritten configs or remapped references")
	assertChannelConfigImmutabilityRestored(t, ctx, db, configID)
	var applied bool
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM goose_db_version WHERE version_id = 39 AND is_applied)`).Scan(&applied))
	require.False(t, applied)
}

func channelConfigReferences(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	var result string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT jsonb_build_object(
  'agents', (SELECT jsonb_agg(to_jsonb(agent) ORDER BY id) FROM agents agent),
  'profiles', (SELECT jsonb_agg(to_jsonb(profile) ORDER BY id) FROM agent_profiles profile),
  'versions', (SELECT jsonb_agg(to_jsonb(version) ORDER BY id) FROM agent_profile_versions version),
  'inputs', (SELECT jsonb_agg(to_jsonb(input) ORDER BY id) FROM agent_inputs input),
  'contexts', (SELECT jsonb_agg(to_jsonb(context) ORDER BY id) FROM model_call_contexts context)
)::text`).Scan(&result))
	return result
}

func assertChannelConfigImmutabilityRestored(t *testing.T, ctx context.Context, db *sql.DB, configID string) {
	t.Helper()
	_, err := db.ExecContext(ctx, `UPDATE agent_configs SET source = source WHERE id = $1`, configID)
	require.ErrorContains(t, err, "agent_configs are immutable")
}
