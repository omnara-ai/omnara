//go:build integration

package dbmigrate_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestPostgresIntegrationChannelsMigrationPreservesSlackConversations(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 37))
	fixture := seedLegacyChannelMigrationFixture(t, ctx, db)
	deletedProject := seedLegacyChannelMigrationFixture(t, ctx, db)
	deletedOrg := seedLegacyChannelMigrationFixture(t, ctx, db)
	_, err := db.ExecContext(ctx, `UPDATE projects SET deleted_at = statement_timestamp() WHERE id = $1`,
		deletedProject.projectID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE orgs SET deleted_at = statement_timestamp() WHERE id = $1`, deletedOrg.orgID)
	require.NoError(t, err)
	before := channelMigrationHistory(t, ctx, db)
	require.NoError(t, applyProductionPostgresMigrations(ctx, db))
	require.JSONEq(t, before, channelMigrationHistory(t, ctx, db),
		"cutover must preserve conversations, inputs and credentials")

	var apps, routes, bindings, definitions, workflows int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT
  (SELECT count(*) FROM integration_apps WHERE owner_project_id = $1),
  (SELECT count(*) FROM integration_routes WHERE project_id = $1),
  (SELECT count(*) FROM integration_target_bindings WHERE project_id = $1),
  (SELECT count(*) FROM integration_channel_definitions WHERE project_id = $1),
  (SELECT count(*) FROM integration_workflows WHERE project_id = $1)`, fixture.projectID).
		Scan(&apps, &routes, &bindings, &definitions, &workflows))
	require.Equal(t, []int{2, 2, 3, 4, 2}, []int{apps, routes, bindings, definitions, workflows})

	var mapped bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT
  app.connector_key = 'chat_sdk' AND app.provider_app_ref = install.provider_account_ref
  AND app.owner_project_id = install.project_id AND app.credential_secret_id IS NULL
  AND app.installation_credential_kind = 'slack_app_credentials'
  AND install.integration_kind = 'managed' AND install.installed_by_user_id = $2
  AND install.installed_by_org_api_key_id IS NULL AND install.credential_secret_id = $3
  AND install.last_oauth_flow_id = $4 AND install.provider_identity = '{"bot_user_id":"U_BOT"}'::jsonb
  AND route.deployment_key = 'slack' AND route.behavior_key = 'slack_conversation'
  AND route.agent_profile_id = $5 AND route.state = 'active' AND route.deleted_at IS NULL
FROM integration_installs install
JOIN integration_apps app ON app.id = install.integration_app_id
JOIN integration_routes route ON route.integration_install_id = install.id
WHERE install.id = $1`,
		fixture.installID, fixture.userID, fixture.secretID, fixture.flowID, fixture.profileID).Scan(&mapped))
	require.True(t, mapped, "OAuth identity, attribution, credentials and selected profile must survive")
	for _, targetID := range []string{fixture.targetID, fixture.dmTargetID} {
		var preserved bool
		require.NoError(t, db.QueryRowContext(ctx, `SELECT
  workflow.agent_id = agent.id AND workflow.instance_key = target.provider_ref
  AND binding.agent_id = agent.id AND binding.integration_route_id = workflow.integration_route_id
  AND binding.source = 'channel' AND binding.receive_allowed AND binding.send_allowed
  AND NOT binding.read_allowed AND binding.reply_receive_allowed IS NULL
  AND binding.reply_read_allowed IS NULL AND binding.reply_send_allowed IS NULL
  AND binding.revoked_at IS NULL AND definition.capabilities -> 'send' = 'true'::jsonb
  AND definition.kind = CASE target.provider_ref_kind WHEN 'dm' THEN 'SLACK_CHANNEL' ELSE 'SLACK_THREAD' END
FROM integration_targets target
JOIN agents agent ON agent.integration_target_id = target.id
JOIN integration_workflows workflow ON workflow.integration_install_id = target.integration_install_id
  AND workflow.instance_key = target.provider_ref
JOIN integration_target_bindings binding ON binding.integration_target_id = target.id
JOIN integration_channel_definitions definition ON definition.id = target.channel_definition_id
WHERE target.id = $1`, targetID).Scan(&preserved))
		require.True(t, preserved, "preserve both active thread and archived DM without widening grants")
	}
	var historicalBinding sql.NullString
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT integration_target_binding_id::text FROM agent_inputs WHERE id = $1`,
		fixture.inputID).Scan(&historicalBinding))
	require.False(t, historicalBinding.Valid, "do not invent provenance for historical input")
	var revoked bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT revoked_at IS NOT NULL
FROM integration_target_bindings WHERE integration_target_id = $1`, fixture.deletedTargetID).Scan(&revoked))
	require.True(t, revoked)
	for _, projectID := range []string{deletedProject.projectID, deletedOrg.projectID} {
		var retired bool
		require.NoError(t, db.QueryRowContext(ctx, `SELECT
  (SELECT bool_and(state = 'disabled' AND deleted_at IS NOT NULL) FROM integration_apps WHERE owner_project_id = $1)
  AND (SELECT bool_and(state = 'disabled' AND deleted_at IS NOT NULL) FROM integration_routes WHERE project_id = $1)
  AND (SELECT bool_and(revoked_at IS NOT NULL) FROM integration_target_bindings WHERE project_id = $1)`,
			projectID).Scan(&retired))
		require.True(t, retired, "deleted owners cannot regain live routes or grants")
	}
	var removed bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT
  to_regclass('public.integration_deliveries') IS NULL AND to_regclass('public.model_call_tool_contracts') IS NULL
  AND NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND (
    (table_name = 'integration_installs' AND column_name IN ('agent_id', 'agent_profile_id'))
    OR (table_name = 'integration_targets' AND column_name = 'agent_id')
    OR (table_name = 'integration_routes' AND column_name IN ('handler_key', 'handler_version'))))
  AND NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname IN (
    'integration_installs_00_fill_compatibility_app', 'integration_targets_create_legacy_binding',
    'integration_target_bindings_validate_legacy_shape', 'agent_inputs_fill_integration_binding'))
  AND (SELECT array_agg(sweep_kind) = ARRAY['event_unprocessable'] FROM integration_sweep_cursors)`).Scan(&removed))
	require.True(t, removed, "final migration cannot leave native shims, snapshots or an outgoing outbox")

	_, err = db.ExecContext(ctx, `INSERT INTO agent_inputs(
  project_id, agent_id, state, input_kind, delivery_mode, integration_target_id,
  idempotency_scope, input_idempotency_key, queued_at, metadata
) VALUES ($1, $2, 'received', 'content', 'queued', $3, 'migration-new', 'no-binding', statement_timestamp(), '{}')`,
		fixture.projectID, fixture.agentID, fixture.targetID)
	require.ErrorContains(t, err, "requires an explicit binding", "new input must not inherit guessed authority")
	_, err = db.ExecContext(ctx, `INSERT INTO integration_targets(
  project_id, integration_install_id, target_ref, provider_ref, provider_ref_kind,
  channel_definition_id, created_at, updated_at
) SELECT project_id, integration_install_id, 'standalone', 'D_STANDALONE', 'dm', channel_definition_id,
  statement_timestamp(), statement_timestamp() FROM integration_targets WHERE id = $1`, fixture.dmTargetID)
	require.NoError(t, err, "registration no longer requires an agent")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM integration_target_bindings WHERE project_id = $1`,
		fixture.projectID).Scan(&bindings))
	require.Equal(t, 3, bindings, "standalone target creation never grants authority")
	assertMigratedSlackWorkflow(t, ctx, pool, fixture)
}

// Exercise production workflow admission on the converted conversation itself.
// A gateway's requested read grant cannot replace the migrated binding's denial.
func assertMigratedSlackWorkflow(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture legacyChannelMigrationFixture,
) {
	t.Helper()
	store := storage.NewStore(pool)
	projectID, installID := uuid.MustParse(fixture.projectID), uuid.MustParse(fixture.installID)
	target, err := store.Integrations().GetIntegrationTarget(ctx, projectID, uuid.MustParse(fixture.targetID))
	require.NoError(t, err)
	routes, err := store.Integrations().ListActiveIntegrationRoutes(ctx, projectID, installID)
	require.NoError(t, err)
	require.Len(t, routes, 1)
	var originalBindingID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM integration_target_bindings
  WHERE integration_target_id = $1 AND revoked_at IS NULL`, target.ID).Scan(&originalBindingID))
	capability := channelconnector.Capability{
		Provider: integrationstore.IntegrationProviderSlack, ConnectorKey: channelconnector.BuiltInConnectorKey,
	}
	identity := executionstore.ChannelWorkflowIdentity{
		ProjectID: projectID, IntegrationInstallID: installID, IntegrationRouteID: routes[0].ID,
		InstanceKey: target.ProviderRef, Capabilities: []channelconnector.Capability{capability},
	}
	prepared, err := store.Execution().PrepareChannelWorkflow(ctx, identity)
	require.NoError(t, err)
	require.Equal(t, uuid.MustParse(fixture.agentID), prepared.AgentID())
	for _, inputKey := range []string{"after-cutover", "legacy-input"} {
		receipt, err := store.Integrations().ReceiveIntegrationEvent(ctx, integrationstore.ReceiveIntegrationEventInput{
			ProjectID: projectID, IntegrationInstallID: installID, EventID: inputKey,
			Payload: json.RawMessage(`{"text":"after cutover"}`), Capabilities: identity.Capabilities,
		})
		require.NoError(t, err)
		lease, found, err := store.Integrations().ClaimNextIntegrationEvent(
			ctx, integrationstore.ClaimNextIntegrationEventInput{
				Capability: capability, LeaseDuration: time.Minute,
			})
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, receipt.ID, lease.ID)
		result, err := store.Execution().DeliverChannelWorkflow(ctx, executionstore.DeliverChannelWorkflowInput{
			Prepared: prepared, InputKey: inputKey,
			Receipt: executionstore.ChannelEventLease{
				ReceiptID: lease.ID, LeaseToken: lease.LeaseToken, LeaseGeneration: lease.LeaseGeneration,
			},
			Target: integrationstore.CreateIntegrationTargetInput{
				ChannelDefinitionID: target.ChannelDefinitionID,
				ProviderRef:         target.ProviderRef, ProviderRefKind: target.ProviderRefKind,
			},
			ReadAllowed: true, SendAllowed: true, ProviderUserID: "U_MIGRATION",
			Content: executionstore.PreparedInputContent{Blocks: json.RawMessage(`[{"type":"text","text":"after cutover"}]`)},
		})
		require.NoError(t, err)
		require.Equal(t, prepared.AgentID(), result.AgentInput.AgentID)
		require.False(t, result.CreatedAgent)
		require.Equal(t, target.ID, result.ChannelID)
		if inputKey == "after-cutover" {
			require.True(t, result.CreatedInput)
			require.NotEqual(t, uuid.MustParse(fixture.inputID), result.AgentInput.ID)
			require.Equal(t, originalBindingID, result.BindingID)
			require.Equal(t, originalBindingID, result.AgentInput.IntegrationTargetBindingID)
		} else {
			require.False(t, result.CreatedInput)
			require.Equal(t, uuid.MustParse(fixture.inputID), result.AgentInput.ID)
			require.Equal(t, uuid.Nil, result.BindingID, "historical replay cannot invent a provenance pin")
			require.Equal(t, uuid.Nil, result.AgentInput.IntegrationTargetBindingID)
			require.JSONEq(t, `[{"type":"text","text":"before cutover"}]`, string(result.ContentBlocks))
		}
	}
	binding, err := store.Integrations().GetIntegrationTargetBinding(ctx, projectID, originalBindingID)
	require.NoError(t, err)
	require.False(t, binding.ReadAllowed, "current behavior requests cannot widen a migrated binding")
	require.True(t, binding.ReceiveAllowed)
	require.True(t, binding.SendAllowed)
	var inputs, bindings int
	require.NoError(t, pool.QueryRow(ctx, `SELECT
  (SELECT count(*) FROM agent_inputs WHERE agent_id = $1),
  (SELECT count(*) FROM integration_target_bindings WHERE integration_target_id = $2)`,
		prepared.AgentID(), target.ID).Scan(&inputs, &bindings))
	require.Equal(t, 2, inputs, "new content is appended once and old input is replayed unchanged")
	require.Equal(t, 1, bindings, "admission preserves the original grant identity")
}

func TestPostgresIntegrationChannelsMigrationRejectsUnsupportedLegacyShape(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"fixed_agent", "unknown_address"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			pool := integrationdb.OpenUnmigratedPool(t, ctx)
			db := stdlib.OpenDBFromPool(pool)
			defer func() { _ = db.Close() }()
			require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 37))
			fixture := seedLegacyChannelMigrationFixture(t, ctx, db)
			if kind == "fixed_agent" {
				_, err := db.ExecContext(ctx,
					`UPDATE integration_installs SET agent_profile_id = NULL, agent_id = $2 WHERE id = $1`,
					fixture.installID, fixture.agentID)
				require.NoError(t, err)
			} else {
				_, err := db.ExecContext(ctx, `UPDATE integration_targets SET provider_ref_kind = 'unknown' WHERE id = $1`,
					fixture.targetID)
				require.NoError(t, err)
			}
			err := applyProductionPostgresMigrations(ctx, db)
			require.ErrorContains(t, err, "channel cutover", "unexpected ownership/address needs an explicit decision")
			var rolledBack bool
			require.NoError(t, db.QueryRowContext(ctx, `SELECT to_regclass('public.integration_apps') IS NULL
  AND EXISTS (SELECT 1 FROM information_schema.columns
    WHERE table_name = 'integration_installs' AND column_name = 'agent_id')`).
				Scan(&rolledBack))
			require.True(t, rolledBack, "failed cutover must leave the old schema recoverable")
		})
	}
}

// Compare immutable history and opaque encrypted credentials without decrypting,
// rewriting old inputs, or treating new provenance columns as historical facts.
func channelMigrationHistory(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	var history string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT jsonb_build_object(
  'agents', (SELECT jsonb_agg(to_jsonb(agent) ORDER BY id) FROM agents agent),
  'inputs', (SELECT jsonb_agg(to_jsonb(input) - 'integration_target_binding_id' ORDER BY id) FROM agent_inputs input),
  'content', (SELECT jsonb_agg(to_jsonb(block) ORDER BY id) FROM content_blocks block),
  'targets', (SELECT jsonb_agg(
    to_jsonb(target) - ARRAY['agent_id', 'channel_definition_id', 'parent_channel_id'] ORDER BY id)
    FROM integration_targets target),
  'secrets', (SELECT jsonb_agg(to_jsonb(secret) ORDER BY id) FROM secrets secret),
  'versions', (SELECT jsonb_agg(to_jsonb(version) ORDER BY id) FROM secret_versions version)
)::text`).Scan(&history))
	return history
}

type legacyChannelMigrationFixture struct {
	userID, orgID, projectID, agentID            string
	installID, targetID, inputID                 string
	deletedTargetID, dmTargetID, dmAgentID       string
	profileID, secretID, secretVersionID, flowID string
}

func seedLegacyChannelMigrationFixture(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) legacyChannelMigrationFixture {
	t.Helper()
	fixture := legacyChannelMigrationFixture{
		userID: uuid.NewString(), orgID: uuid.NewString(), projectID: uuid.NewString(),
		agentID: uuid.NewString(), installID: uuid.NewString(), targetID: uuid.NewString(),
		inputID: uuid.NewString(), deletedTargetID: uuid.NewString(),
		dmTargetID: uuid.NewString(), dmAgentID: uuid.NewString(), profileID: uuid.NewString(),
		secretID: uuid.NewString(), secretVersionID: uuid.NewString(), flowID: uuid.Must(uuid.NewV7()).String(),
	}
	providerConfigID := uuid.NewString()
	configuredModelID := uuid.NewString()
	configuredModelRevisionID := uuid.NewString()
	agentConfigID := uuid.NewString()
	profileVersionID := uuid.NewString()
	deletedInstallID := uuid.NewString()
	source := `instruction: Preserve the integration migration fixture.
model:
  provider_config: migration provider
  name: migration model
`
	compiled, err := agentconfig.Compile(
		agentconfig.SourceFormatYAML,
		[]byte(source),
		agentconfig.CompileOptions{},
	)
	if err != nil {
		t.Fatalf("compile migration fixture agent config: %v", err)
	}
	// Freeze the actual pre-cutover builtin shape; the current compiler correctly
	// rejects these names, and migration 39 must repair their stored definitions.
	source += `tools:
  send_integration_message: {permission: {mode: always_allow}}
  set_integration_target: {enabled: false}
  custom_task:
    type: custom
    description: Preserve this custom schema.
    permission: {mode: always_ask}
    input_schema: {type: object, properties: {count: {type: number, maximum: 1e30}}}
`
	var compiledObject map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(compiled.CanonicalJSON, &compiledObject))
	compiledObject["tools"] = json.RawMessage(`{
  "send_integration_message":{"enabled":true,"permission":{"mode":"always_allow"}},
  "set_integration_target":{"enabled":false,"permission":{"mode":"always_allow"}},
  "custom_task":{"type":"custom","enabled":true,"permission":{"mode":"always_ask"},
    "description":"Preserve this custom schema.",
    "input_schema":{"type":"object","properties":{"count":{"type":"number","maximum":1e30}}}}
}`)
	compiled.CanonicalJSON, err = json.Marshal(compiledObject)
	require.NoError(t, err)
	var canonicalValue any
	require.NoError(t, json.Unmarshal(compiled.CanonicalJSON, &canonicalValue))
	compiled.CanonicalJSON, err = json.Marshal(canonicalValue)
	require.NoError(t, err)
	compiledDigest := sha256.Sum256(compiled.CanonicalJSON)
	compiled.Hash = hex.EncodeToString(compiledDigest[:])
	sourceDigest := sha256.Sum256([]byte(source))

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	statements := []struct {
		name string
		sql  string
		args []any
	}{
		{"user", `INSERT INTO users(id, display_name, created_at, updated_at)
VALUES ($1, 'migration user', statement_timestamp(), statement_timestamp())`, []any{fixture.userID}},
		{"organization", `INSERT INTO orgs(id, name, created_at, updated_at)
VALUES ($1, 'Migration org', statement_timestamp(), statement_timestamp())`, []any{fixture.orgID}},
		{
			"project", `INSERT INTO projects(id, org_id, name, created_at, updated_at)
VALUES ($1, $2, 'Migration project', statement_timestamp(), statement_timestamp())`,
			[]any{fixture.projectID, fixture.orgID},
		},
		{"provider", `INSERT INTO model_provider_configs(
  id, org_id, management_kind, name, api_format, base_url, endpoint_path,
  auth_kind, deleted_at, created_at, updated_at
) VALUES (
  $1, $2, 'cluster', 'Migration provider', 'openai-responses',
  'https://provider.example.test/v1', '/responses', 'bearer_token',
  statement_timestamp(), statement_timestamp(), statement_timestamp()
)`, []any{providerConfigID, fixture.orgID}},
		{"model", `WITH model AS (
  INSERT INTO configured_models(
    id, org_id, model_provider_config_id, management_kind, name,
    current_revision_id, created_at, updated_at
  ) VALUES (
    $1, $2, $3, 'cluster', 'Migration model', $4,
    statement_timestamp(), statement_timestamp()
  ) RETURNING id, org_id, model_provider_config_id, current_revision_id
)
INSERT INTO configured_model_revisions(
  id, org_id, configured_model_id, model_provider_config_id,
  provider_model_slug, context_window_tokens, max_output_tokens, created_at
)
SELECT current_revision_id, org_id, id, model_provider_config_id,
       'migration-model', 128000, 8192, statement_timestamp()
FROM model`, []any{configuredModelID, fixture.orgID, providerConfigID, configuredModelRevisionID}},
		{"agent config", `INSERT INTO agent_configs(
  id, org_id, project_id, configured_model_id, definition, source,
  source_format, source_hash, compiled_definition, compiler_version,
  effective_definition_hash, created_at
) VALUES (
  $1, $2, $3, $4, $5::jsonb, $6, 'yaml', $7,
  $5::jsonb, '', $8, statement_timestamp()
)`, []any{
			agentConfigID, fixture.orgID, fixture.projectID, configuredModelID,
			string(compiled.CanonicalJSON), source, hex.EncodeToString(sourceDigest[:]), compiled.Hash,
		}},
		{"agent", `INSERT INTO agents(
  id, org_id, project_id, state, name, current_config_id, created_at, updated_at
) VALUES (
  $1, $2, $3, 'active', 'Migration agent', $4,
  statement_timestamp(), statement_timestamp()
)`, []any{fixture.agentID, fixture.orgID, fixture.projectID, agentConfigID}},
		{"profile", `WITH profile AS (
  INSERT INTO agent_profiles(id, project_id, name, current_version_id, created_at, updated_at)
  VALUES ($1, $2, 'Migration profile', $3, statement_timestamp(), statement_timestamp())
  RETURNING id, project_id
)
INSERT INTO agent_profile_versions(id, project_id, profile_id, generation, agent_config_id, created_at)
SELECT $3, project_id, id, 1, $4, statement_timestamp() FROM profile`,
			[]any{fixture.profileID, fixture.projectID, profileVersionID, agentConfigID}},
		{"archived DM agent", `INSERT INTO agents(
  id, org_id, project_id, state, name, current_config_id, created_at, updated_at, archived_at
) VALUES ($1, $2, $3, 'archived', 'Archived DM agent', $4,
  statement_timestamp(), statement_timestamp(), statement_timestamp())`,
			[]any{fixture.dmAgentID, fixture.orgID, fixture.projectID, agentConfigID}},
		{"credential", `INSERT INTO secrets(
  id, org_id, management_kind, owner_kind, owner_project_id, name, kind, current_version_id, created_at, updated_at
) VALUES ($1, $2, 'tenant', 'project', $3, 'Slack credential', 'slack_app_credentials', $4,
  statement_timestamp(), statement_timestamp())`,
			[]any{fixture.secretID, fixture.orgID, fixture.projectID, fixture.secretVersionID}},
		{"credential version", `INSERT INTO secret_versions(
  id, org_id, secret_id, version_number, payload_keys, encryption_scheme, key_id,
  dek_wrapped_by, encrypted_dek, encrypted_dek_nonce, nonce, ciphertext, created_at
) VALUES ($1, $2, $3, 1, ARRAY['access_token', 'client_id', 'client_secret', 'signing_secret'],
  'aes-256-gcm-envelope-v1', 'migration-fixture', 'local', decode(repeat('ab', 48), 'hex'),
  decode(repeat('cd', 12), 'hex'), decode(repeat('ef', 12), 'hex'), decode(repeat('42', 32), 'hex'),
  statement_timestamp())`, []any{fixture.secretVersionID, fixture.orgID, fixture.secretID}},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("seed migration fixture %s: %v", statement.name, err)
		}
	}

	channelStatements := []struct {
		name string
		sql  string
		args []any
	}{
		{"installations", `
INSERT INTO integration_installs(
  id, org_id, project_id, agent_profile_id, installed_by_user_id, provider,
  integration_kind, connection_mode, state, provider_tenant_id,
  provider_account_ref, provider_agent_display_name, deleted_at,
  credential_secret_id, last_oauth_flow_id, provider_identity, created_at, updated_at
)
VALUES
  ($1, $2, $3, $4, $5, 'slack', 'agent_profile', 'webhook', 'active',
   $7, $8, 'Legacy bot', NULL, $11, $12, '{"bot_user_id":"U_BOT"}',
   statement_timestamp(), statement_timestamp()),
  ($6, $2, $3, $4, $5, 'slack', 'agent_profile', 'webhook', 'active',
	 $9, $10, 'Deleted bot', statement_timestamp(), NULL, NULL, '{}',
	   statement_timestamp(), statement_timestamp())`, []any{
			fixture.installID, fixture.orgID, fixture.projectID, fixture.profileID,
			fixture.userID, deletedInstallID,
			"legacy-workspace-" + fixture.projectID,
			"legacy-bot-" + fixture.projectID,
			"deleted-workspace-" + fixture.projectID,
			"deleted-bot-" + fixture.projectID, fixture.secretID, fixture.flowID,
		}},
		{"targets", `
INSERT INTO integration_targets(
  id, project_id, agent_id, integration_install_id, target_ref,
  provider_ref, provider_ref_kind, display_name, deleted_at, created_at, updated_at
)
VALUES
  ($5, $2, $3, $1, 'legacy-thread', 'C_EXISTING:1730000000.000001', 'thread',
   'Legacy thread', NULL, statement_timestamp(), statement_timestamp()),
  ($6, $2, $3, $4, 'deleted-thread', 'C_REMOVED:1730000000.000001', 'thread',
	   'Deleted thread', statement_timestamp(), statement_timestamp(), statement_timestamp())`, []any{
			fixture.installID, fixture.projectID, fixture.agentID, deletedInstallID,
			fixture.targetID, fixture.deletedTargetID,
		}},
		{"DM target", `INSERT INTO integration_targets(
  id, project_id, agent_id, integration_install_id, target_ref, provider_ref, provider_ref_kind, created_at, updated_at
) VALUES ($1, $2, $3, $4, 'existing-dm', 'D_EXISTING', 'dm', statement_timestamp(), statement_timestamp())`,
			[]any{fixture.dmTargetID, fixture.projectID, fixture.dmAgentID, fixture.installID}},
		{"current pointers", `UPDATE agents SET integration_target_id = CASE id WHEN $1::uuid THEN $2::uuid ELSE $4::uuid END
WHERE id IN ($1::uuid, $3::uuid)`, []any{fixture.agentID, fixture.targetID, fixture.dmAgentID, fixture.dmTargetID}},
		{"input", `
INSERT INTO agent_inputs(
  id, project_id, agent_id, state, input_kind, delivery_mode,
  integration_target_id, idempotency_scope, input_idempotency_key,
  queued_at, metadata
)
VALUES (
  $1, $2, $3, 'received', 'content', 'queued', $4,
  'integration:slack:' || $5::text, 'legacy-input', statement_timestamp(), '{}'::jsonb
	)`, []any{
			fixture.inputID, fixture.projectID, fixture.agentID, fixture.targetID, fixture.installID,
		}},
	}
	channelStatements = append(channelStatements, struct {
		name string
		sql  string
		args []any
	}{
		"historical input content", `INSERT INTO content_blocks(
   agent_id, owner_kind, owner_agent_input_id, ordinal, block_kind, text_content, created_at
  ) VALUES ($1, 'agent_input', $2, 0, 'text', 'before cutover', statement_timestamp())`,
		[]any{fixture.agentID, fixture.inputID},
	})
	for _, statement := range channelStatements {
		if _, err := tx.ExecContext(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("seed legacy channel %s: %v", statement.name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit migration fixture: %v", err)
	}
	return fixture
}
