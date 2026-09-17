//go:build integration

package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	schemamigrations "github.com/omnara-ai/omnara/migrations"
)

func TestMigratedChannelConfigResumesPinnedMCPApprovalWithOriginalArguments(t *testing.T) {
	testUnfinishedMCPApproval(t, func(fixture kernelFixture, pinned executionstore.ModelCallContextRecord) {
		ctx := t.Context()
		before, found, err := fixture.Store.Execution().GetModelCallContext(ctx, pinned.ProjectID, pinned.AgentID, pinned.ID)
		require.NoError(t, err)
		require.True(t, found)
		config, found, err := fixture.Store.Execution().GetAgentConfig(ctx, pinned.ProjectID, pinned.AgentConfigID)
		require.NoError(t, err)
		require.True(t, found)
		seedRetiredChannelConfigForKernelTest(t, fixture, config)
		executor := AgentExecutor{Store: storage.NewStore(fixture.Pool)}
		_, err = executor.modelContextToolRuntime(ctx, pinned.ProjectID, pinned.AgentID, before, fixture.Now)
		require.ErrorContains(t, err, "send_integration_message",
			"a legacy config must fail current reconstruction before the actual migration runs")

		applyChannelConfigMigrationForKernelTest(t, fixture)
		migrated, found, err := fixture.Store.Execution().GetAgentConfig(ctx, pinned.ProjectID, pinned.AgentConfigID)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, config.ID, migrated.ID)
		require.Equal(t, config.ConfiguredModelID, migrated.ConfiguredModelID)
		require.Equal(t, config.CreatedAt, migrated.CreatedAt)
		require.Equal(t, config.EffectiveDefinitionHash, migrated.EffectiveDefinitionHash)
		require.JSONEq(t, string(config.CompiledDefinition), string(migrated.CompiledDefinition))
		after, found, err := fixture.Store.Execution().GetModelCallContext(ctx, pinned.ProjectID, pinned.AgentID, pinned.ID)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, before, after, "migration must leave the existing producer context and watermark intact")
		captured, err := fixture.Store.Execution().CaptureAgentConfigForEventWatermark(
			ctx, pinned.ProjectID, pinned.AgentID, pinned.InputEventSequence,
		)
		require.NoError(t, err)
		require.Equal(t, migrated.ID, captured.AgentConfig.ID)
		require.Equal(t, migrated.EffectiveDefinitionHash, captured.AgentConfig.EffectiveDefinitionHash)
		_, err = executor.modelContextToolRuntime(ctx, pinned.ProjectID, pinned.AgentID, after, fixture.Now)
		require.NoError(t, err)
		// The shared journey now approves and executes this pending MCP call with a
		// fresh worker, checking unchanged arguments, permission identity and replay.
	})
}

func seedRetiredChannelConfigForKernelTest(
	t *testing.T,
	fixture kernelFixture,
	config executionstore.AgentConfigRecord,
) {
	t.Helper()
	ctx := t.Context()
	// Recreate the pre-cutover row after pinning the ordinary MCP context, keeping
	// the retrieval tools materialized by compilation. Only test setup temporarily
	// overrides immutability; no current compiler accepts the retired declarations.
	var compiled map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(config.CompiledDefinition, &compiled))
	tools := make(map[string]json.RawMessage)
	if raw := compiled["tools"]; len(raw) > 0 {
		require.NoError(t, json.Unmarshal(raw, &tools))
	}
	for _, name := range []string{"send_integration_message", "set_integration_target"} {
		tools[name] = json.RawMessage(`{"enabled":true,"permission":{"mode":"always_allow"}}`)
	}
	toolJSON, err := json.Marshal(tools)
	require.NoError(t, err)
	compiled["tools"] = toolJSON
	encoded, err := json.Marshal(compiled)
	require.NoError(t, err)
	legacyCompiled, err := jsoncanonical.Normalize(encoded)
	require.NoError(t, err)
	legacySource := config.Source + `
tools:
  send_integration_message:
    permission:
      mode: always_allow
  set_integration_target:
    permission:
      mode: always_allow
`
	sourceHash := sha256.Sum256([]byte(legacySource))
	compiledHash := sha256.Sum256(legacyCompiled)
	tx, err := fixture.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `ALTER TABLE agent_configs DISABLE TRIGGER agent_configs_immutable`)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE agent_configs
SET source = $2, source_hash = $3, definition = $4::jsonb,
    compiled_definition = $4::jsonb, effective_definition_hash = $5
WHERE id = $1`, config.ID, legacySource, hex.EncodeToString(sourceHash[:]),
		legacyCompiled, hex.EncodeToString(compiledHash[:]))
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `ALTER TABLE agent_configs ENABLE TRIGGER agent_configs_immutable`)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
}

func applyChannelConfigMigrationForKernelTest(t *testing.T, fixture kernelFixture) {
	t.Helper()
	var cutover *goose.Migration
	for _, migration := range schemamigrations.GoMigrations() {
		if migration.Version == 41 {
			cutover = migration
			break
		}
	}
	require.NotNil(t, cutover, "exercise the production registered migration, not a copied converter")
	db := stdlib.OpenDBFromPool(fixture.Pool)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	// The fixture already has the latest schema. Run exactly this data migration
	// without altering the fixture's migration ledger or replaying earlier DDL.
	provider, err := goose.NewProvider(goose.DialectPostgres, db, nil,
		goose.WithDisableGlobalRegistry(true), goose.WithDisableVersioning(true), goose.WithGoMigrations(cutover))
	require.NoError(t, err)
	results, err := provider.Up(t.Context())
	require.NoError(t, err)
	require.Len(t, results, 1)
}
