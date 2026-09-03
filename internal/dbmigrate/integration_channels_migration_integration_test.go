//go:build integration

package dbmigrate_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestPostgresIntegrationChannelsMigrationBackfillsLegacySlack(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()

	if err := applyProductionPostgresMigrationsThrough(t, ctx, db, 28); err != nil {
		t.Fatalf("apply migrations through version 28: %v", err)
	}
	fixture := seedLegacyChannelMigrationFixture(t, ctx, db)
	deletedProjectFixture := seedLegacyChannelMigrationFixture(t, ctx, db)
	deletedOrgFixture := seedLegacyChannelMigrationFixture(t, ctx, db)
	if _, err := db.ExecContext(
		ctx,
		`UPDATE projects SET deleted_at = statement_timestamp() WHERE id = $1`,
		deletedProjectFixture.projectID,
	); err != nil {
		t.Fatalf("mark migration project deleted: %v", err)
	}
	if _, err := db.ExecContext(
		ctx,
		`UPDATE orgs SET deleted_at = statement_timestamp() WHERE id = $1`,
		deletedOrgFixture.orgID,
	); err != nil {
		t.Fatalf("mark migration organization deleted: %v", err)
	}
	if err := applyProductionPostgresMigrations(ctx, db); err != nil {
		t.Fatalf("apply channel foundation migration: %v", err)
	}

	var apps, routes, bindings int
	if err := db.QueryRowContext(ctx, `
SELECT
  (SELECT count(*) FROM integration_apps WHERE owner_project_id = $1),
  (SELECT count(*) FROM integration_routes WHERE project_id = $1),
  (SELECT count(*) FROM integration_target_bindings WHERE project_id = $1)
`, fixture.projectID).Scan(&apps, &routes, &bindings); err != nil {
		t.Fatalf("load backfill counts: %v", err)
	}
	if apps != 2 || routes != 0 || bindings != 2 {
		t.Fatalf("backfill counts apps=%d routes=%d bindings=%d", apps, routes, bindings)
	}

	var connectorKey, bindingID string
	var ownerProjectID string
	var historicalBinding, routeID sql.NullString
	if err := db.QueryRowContext(ctx, `
SELECT app.connector_key, app.owner_project_id::text,
       binding.id::text, binding.integration_route_id::text,
       input.integration_target_binding_id::text
FROM integration_installs install
JOIN integration_apps app
  ON app.org_id = install.org_id AND app.id = install.integration_app_id
JOIN integration_target_bindings binding
  ON binding.project_id = install.project_id
 AND binding.integration_install_id = install.id
 AND binding.integration_target_id = $2
 AND binding.source = 'legacy_target'
JOIN agent_inputs input
  ON input.project_id = install.project_id AND input.id = $3
WHERE install.id = $1
`, fixture.installID, fixture.targetID, fixture.inputID).Scan(
		&connectorKey, &ownerProjectID, &bindingID, &routeID, &historicalBinding,
	); err != nil {
		t.Fatalf("load active legacy backfill: %v", err)
	}
	if connectorKey != "native_slack_v1" || ownerProjectID != fixture.projectID ||
		bindingID == "" || routeID.Valid || historicalBinding.Valid {
		t.Fatalf(
			"active backfill connector=%q owner=%q binding=%q route=%v historical=%v",
			connectorKey, ownerProjectID, bindingID, routeID, historicalBinding,
		)
	}

	var deletedBindingRevoked bool
	if err := db.QueryRowContext(ctx, `
SELECT binding.revoked_at IS NOT NULL
FROM integration_target_bindings binding
WHERE binding.project_id = $1 AND binding.integration_target_id = $2
`, fixture.projectID, fixture.deletedTargetID).Scan(&deletedBindingRevoked); err != nil {
		t.Fatalf("load deleted target binding: %v", err)
	}
	if !deletedBindingRevoked {
		t.Fatal("deleted legacy target binding remains active")
	}
	for _, deletedOwner := range []struct {
		name      string
		projectID string
	}{
		{name: "project", projectID: deletedProjectFixture.projectID},
		{name: "organization", projectID: deletedOrgFixture.projectID},
	} {
		var appCount int
		var allDisabled, allDeleted bool
		if err := db.QueryRowContext(ctx, `
SELECT count(*), bool_and(state = 'disabled'), bool_and(deleted_at IS NOT NULL)
FROM integration_apps
WHERE owner_project_id = $1
`, deletedOwner.projectID).Scan(&appCount, &allDisabled, &allDeleted); err != nil {
			t.Fatalf("load %s-deleted compatibility apps: %v", deletedOwner.name, err)
		}
		if appCount != 2 || !allDisabled || !allDeleted {
			t.Fatalf(
				"%s-deleted apps count=%d disabled=%t deleted=%t",
				deletedOwner.name,
				appCount,
				allDisabled,
				allDeleted,
			)
		}
	}

	compatInstallID := uuid.NewString()
	compatTargetID := uuid.NewString()
	compatInputID := uuid.NewString()
	var compatAppID string
	if err := db.QueryRowContext(ctx, `
INSERT INTO integration_installs(
  id, org_id, project_id, agent_id, installed_by_user_id, provider,
  integration_kind, connection_mode, state, provider_tenant_id,
  provider_account_ref, provider_agent_display_name, created_at, updated_at
)
VALUES (
  $1, $2, $3, $4, $5, 'slack', 'agent', 'webhook', 'disabled',
  'compat-workspace', 'compat-bot', 'Compatibility bot',
  statement_timestamp(), statement_timestamp()
)
RETURNING integration_app_id::text
`, compatInstallID, fixture.orgID, fixture.projectID, fixture.agentID, fixture.userID).
		Scan(&compatAppID); err != nil {
		t.Fatalf("insert old-shape compatibility installation: %v", err)
	}
	if compatAppID == "" {
		t.Fatal("compatibility installation has no app")
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO integration_targets(
  id, project_id, agent_id, integration_install_id, target_ref,
  provider_ref, provider_ref_kind, display_name, created_at, updated_at
)
VALUES (
  $1, $2, $3, $4, 'compat-thread', 'compat-thread', 'thread',
  'Compatibility thread', statement_timestamp(), statement_timestamp()
)
`, compatTargetID, fixture.projectID, fixture.agentID, compatInstallID); err != nil {
		t.Fatalf("insert old-shape compatibility target: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO agent_inputs(
  id, project_id, agent_id, state, input_kind, delivery_mode,
  integration_target_id, idempotency_scope, input_idempotency_key,
  queued_at, metadata
)
VALUES (
  $1, $2, $3, 'received', 'content', 'queued', $4,
  'compat-migration', 'compat-input', statement_timestamp(), '{}'::jsonb
)
`, compatInputID, fixture.projectID, fixture.agentID, compatTargetID); err != nil {
		t.Fatalf("insert old-shape compatibility input: %v", err)
	}

	var compatBindingID, compatInputBindingID string
	var compatRouteID sql.NullString
	if err := db.QueryRowContext(ctx, `
SELECT binding.id::text, input.integration_target_binding_id::text,
       binding.integration_route_id::text
FROM integration_target_bindings binding
JOIN agent_inputs input
  ON input.project_id = binding.project_id
 AND input.integration_target_id = binding.integration_target_id
WHERE binding.integration_target_id = $1 AND input.id = $2
`, compatTargetID, compatInputID).Scan(
		&compatBindingID, &compatInputBindingID, &compatRouteID,
	); err != nil {
		t.Fatalf("load compatibility provenance: %v", err)
	}
	if compatBindingID == "" || compatInputBindingID != compatBindingID ||
		compatRouteID.Valid {
		t.Fatalf(
			"compatibility binding=%q input_binding=%q route=%v",
			compatBindingID, compatInputBindingID, compatRouteID,
		)
	}

	type compatibilityInsertResult struct {
		installID string
		appID     string
		err       error
	}
	start := make(chan struct{})
	results := make(chan compatibilityInsertResult, 2)
	var writers sync.WaitGroup
	for index := range 2 {
		writers.Add(1)
		go func(index int) {
			defer writers.Done()
			<-start
			installID := uuid.NewString()
			var appID string
			err := db.QueryRowContext(ctx, `
INSERT INTO integration_installs(
  id, org_id, project_id, agent_id, installed_by_user_id, provider,
  integration_kind, connection_mode, state, provider_tenant_id,
  provider_account_ref, provider_agent_display_name, created_at, updated_at
)
VALUES (
  $1, $2, $3, $4, $5, 'slack', 'agent', 'webhook', 'disabled',
  $6, 'compat-race-bot', 'Compatibility race bot',
  statement_timestamp(), statement_timestamp()
)
RETURNING integration_app_id::text
`, installID, fixture.orgID, fixture.projectID, fixture.agentID, fixture.userID,
				"compat-race-workspace-"+string(rune('a'+index))).Scan(&appID)
			results <- compatibilityInsertResult{installID: installID, appID: appID, err: err}
		}(index)
	}
	close(start)
	writers.Wait()
	close(results)
	var sharedAppID string
	for result := range results {
		if result.err != nil {
			t.Fatalf("insert raced compatibility installation %s: %v", result.installID, result.err)
		}
		if sharedAppID == "" {
			sharedAppID = result.appID
		} else if result.appID != sharedAppID {
			t.Fatalf("raced compatibility apps = %s and %s", sharedAppID, result.appID)
		}
	}
	var racedAppCount int
	if err := db.QueryRowContext(ctx, `
SELECT count(*)
FROM integration_apps
WHERE owner_project_id = $1
  AND provider = 'slack'
  AND provider_app_ref = 'compat-race-bot'
  AND deleted_at IS NULL
`, fixture.projectID).Scan(&racedAppCount); err != nil {
		t.Fatalf("count raced compatibility apps: %v", err)
	}
	if racedAppCount != 1 || sharedAppID == "" {
		t.Fatalf("raced compatibility app count=%d id=%q", racedAppCount, sharedAppID)
	}
}

type legacyChannelMigrationFixture struct {
	userID, orgID, projectID, agentID string
	installID, targetID, inputID      string
	deletedTargetID                   string
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
	}
	providerConfigID := uuid.NewString()
	configuredModelID := uuid.NewString()
	configuredModelRevisionID := uuid.NewString()
	agentConfigID := uuid.NewString()
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
		{"project", `INSERT INTO projects(id, org_id, name, created_at, updated_at)
VALUES ($1, $2, 'Migration project', statement_timestamp(), statement_timestamp())`, []any{fixture.projectID, fixture.orgID}},
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
  id, org_id, project_id, agent_id, installed_by_user_id, provider,
  integration_kind, connection_mode, state, provider_tenant_id,
  provider_account_ref, provider_agent_display_name, deleted_at,
  created_at, updated_at
)
VALUES
  ($1, $2, $3, $4, $5, 'slack', 'agent', 'webhook', 'active',
   $7, $8, 'Legacy bot', NULL,
   statement_timestamp(), statement_timestamp()),
  ($6, $2, $3, $4, $5, 'slack', 'agent', 'webhook', 'active',
	 $9, $10, 'Deleted bot', statement_timestamp(),
	   statement_timestamp(), statement_timestamp())`, []any{
			fixture.installID, fixture.orgID, fixture.projectID, fixture.agentID,
			fixture.userID, deletedInstallID,
			"legacy-workspace-" + fixture.projectID,
			"legacy-bot-" + fixture.projectID,
			"deleted-workspace-" + fixture.projectID,
			"deleted-bot-" + fixture.projectID,
		}},
		{"targets", `
INSERT INTO integration_targets(
  id, project_id, agent_id, integration_install_id, target_ref,
  provider_ref, provider_ref_kind, display_name, deleted_at, created_at, updated_at
)
VALUES
  ($5, $2, $3, $1, 'legacy-thread', 'legacy-thread', 'thread',
   'Legacy thread', NULL, statement_timestamp(), statement_timestamp()),
  ($6, $2, $3, $4, 'deleted-thread', 'deleted-thread', 'thread',
	   'Deleted thread', statement_timestamp(), statement_timestamp(), statement_timestamp())`, []any{
			fixture.installID, fixture.projectID, fixture.agentID, deletedInstallID,
			fixture.targetID, fixture.deletedTargetID,
		}},
		{"input", `
INSERT INTO agent_inputs(
  id, project_id, agent_id, state, input_kind, delivery_mode,
  integration_target_id, idempotency_scope, input_idempotency_key,
  queued_at, metadata
)
VALUES (
  $1, $2, $3, 'received', 'content', 'queued', $4,
  'legacy-migration', 'legacy-input', statement_timestamp(), '{}'::jsonb
	)`, []any{
			fixture.inputID, fixture.projectID, fixture.agentID, fixture.targetID,
		}},
	}
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
