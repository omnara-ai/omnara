//go:build integration

package dbmigrate_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/dbmigrate"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestPostgresMigrationsReplayIdempotently(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, db := openPostgresMigrationTestDB(t, ctx)
	if err := dbmigrate.ApplyPostgres(
		ctx,
		db,
		os.DirFS("../../migrations"),
	); err != nil {
		t.Fatalf("idempotent migration replay: %v", err)
	}

	var applied int
	if err := db.QueryRowContext(
		ctx,
		`SELECT count(*)
		 FROM goose_db_version
		 WHERE version_id > 0 AND is_applied`,
	).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	migrationFiles, err := filepath.Glob("../../migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if applied != len(migrationFiles) {
		t.Fatalf(
			"applied production migrations = %d, want %d",
			applied,
			len(migrationFiles),
		)
	}
	for _, constraint := range []string{
		"orgs_name_policy",
		"projects_name_policy",
		"personal_access_tokens_name_policy",
		"auth_device_flows_client_name_policy",
		"auth_device_flows_token_name_policy",
		"org_api_keys_name_policy",
		"secrets_name_policy",
		"model_provider_configs_name_policy",
		"configured_models_name_policy",
		"agent_profiles_name_policy",
		"agents_name_policy",
		"machine_pools_name_policy",
		"machines_display_name_policy",
		"machine_daemon_tokens_name_policy",
		"cron_triggers_name_policy",
		"agent_configs_source_required",
		"skills_name_policy",
	} {
		var validated bool
		if err := db.QueryRowContext(
			ctx,
			`SELECT convalidated FROM pg_constraint WHERE conname = $1`,
			constraint,
		).Scan(&validated); err != nil {
			t.Fatalf("load %s validation state: %v", constraint, err)
		}
		if !validated {
			t.Fatalf("constraint %s was not validated", constraint)
		}
	}
	var agentConfigSourceDefaultDropped bool
	if err := db.QueryRowContext(
		ctx,
		`SELECT column_default IS NULL
		 FROM information_schema.columns
		 WHERE table_schema = current_schema()
		   AND table_name = 'agent_configs'
		   AND column_name = 'source'`,
	).Scan(&agentConfigSourceDefaultDropped); err != nil {
		t.Fatalf("load agent_configs.source default: %v", err)
	}
	if !agentConfigSourceDefaultDropped {
		t.Fatal("agent_configs.source retained an invalid empty default")
	}
}

func TestResourceNameMigrationRequiresExistingRowsToBeValid(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	if err := dbmigrate.ApplyPostgres(ctx, db, migrationFilesThrough(t, 20)); err != nil {
		t.Fatalf("apply migrations before resource-name policy: %v", err)
	}
	orgIDs := make([]string, 0, 22)
	for index := range 22 {
		var orgID string
		if err := pool.QueryRow(
			ctx,
			`INSERT INTO orgs(name, created_at, updated_at)
			 VALUES ($1, now(), now())
			 RETURNING id`,
			fmt.Sprintf("\u00a0legacy-%02d", index),
		).Scan(&orgID); err != nil {
			t.Fatalf("insert simulated legacy organization %d: %v", index, err)
		}
		orgIDs = append(orgIDs, orgID)
	}
	slices.Sort(orgIDs)
	err := dbmigrate.ApplyPostgres(ctx, db, os.DirFS("../../migrations"))
	for _, want := range []string{
		"resource-name migration blocked; migrate these invalid stored values (22)",
		"orgs/" + orgIDs[0] + ".name",
		"orgs/" + orgIDs[19] + ".name",
		"and 2 more",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("resource-name migration error = %v, want %q", err, want)
		}
	}
	if got := strings.Count(err.Error(), "orgs/"); got != 20 {
		t.Fatalf("reported organization IDs = %d, want 20: %v", got, err)
	}
	for _, omittedID := range orgIDs[20:] {
		if strings.Contains(err.Error(), omittedID) {
			t.Fatalf("resource-name migration error included truncated ID %s: %v", omittedID, err)
		}
	}
	if got := currentPostgresMigrationVersion(t, ctx, db); got != 21 {
		t.Fatalf("migration version after rejected resource name = %d, want 21", got)
	}
	for _, orgID := range orgIDs {
		if _, err := pool.Exec(ctx, `UPDATE orgs SET name = 'valid' WHERE id = $1`, orgID); err != nil {
			t.Fatalf("repair organization %s name: %v", orgID, err)
		}
	}
	if err := dbmigrate.ApplyPostgres(ctx, db, os.DirFS("../../migrations")); err != nil {
		t.Fatalf("apply resource-name migration after repair: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO orgs(name, created_at, updated_at) VALUES ($1, now(), now())`,
		strings.Repeat("x", 65),
	); err == nil {
		t.Fatal("oversized new organization name succeeded")
	}
}

func TestResourceNameMigrationRequiresStoredAgentConfigsToUseCurrentNamesAndSchema(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	if err := dbmigrate.ApplyPostgres(ctx, db, migrationFilesThrough(t, 20)); err != nil {
		t.Fatalf("apply migrations before resource-name policy: %v", err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE agent_configs DISABLE TRIGGER ALL`); err != nil {
		t.Fatalf("disable agent config triggers: %v", err)
	}
	triggersDisabled := true
	t.Cleanup(func() {
		if triggersDisabled {
			_, _ = pool.Exec(context.Background(), `ALTER TABLE agent_configs ENABLE TRIGGER ALL`)
		}
	})
	const invalidSource = `
instruction: Test migration preflight.
model:
  provider_config: Provider
  name: Model
machine_sources:
  - machine_pool_name: " Build Pool"
`
	const insertInvalidSourceConfig = `
INSERT INTO agent_configs(
    id,
    org_id,
    project_id,
    configured_model_id,
    definition,
    source,
    source_format,
    source_hash,
    compiled_definition,
    compiler_version,
    effective_definition_hash,
    created_at
) VALUES (
    $1::uuid,
    uuidv7(),
    uuidv7(),
    uuidv7(),
    '{}'::jsonb,
    $2,
    'yaml',
    $3,
    '{}'::jsonb,
    '',
    $3,
    now()
)
`
	const invalidSourceConfigID = "00000000-0000-0000-0000-000000000001"
	configIDs := []string{invalidSourceConfigID}
	if _, err := pool.Exec(
		ctx,
		insertInvalidSourceConfig,
		invalidSourceConfigID,
		invalidSource,
		"invalid-resource-name-source",
	); err != nil {
		t.Fatalf("insert stored agent config with invalid resource reference: %v", err)
	}
	const validSource = `
instruction: Test migration preflight.
model:
  provider_config: Provider
  name: Model
`
	const legacyCompiledDefinition = `{"name":"legacy-agent-name"}`
	legacyDefinitionHash := fmt.Sprintf("%x", sha256.Sum256([]byte(legacyCompiledDefinition)))
	const legacyCompiledConfigID = "00000000-0000-0000-0000-000000000002"
	configIDs = append(configIDs, legacyCompiledConfigID)
	if _, err := pool.Exec(ctx, `
INSERT INTO agent_configs(
    id,
    org_id,
    project_id,
    configured_model_id,
    definition,
    source,
    source_format,
    source_hash,
    compiled_definition,
    compiler_version,
    effective_definition_hash,
    created_at
) VALUES (
    $1::uuid,
    uuidv7(),
    uuidv7(),
    uuidv7(),
    '{}'::jsonb,
    $2,
    'yaml',
    'legacy-compiled-name',
    $3::jsonb,
    '',
    $4,
    now()
)
`, legacyCompiledConfigID, validSource, legacyCompiledDefinition, legacyDefinitionHash); err != nil {
		t.Fatalf("insert stored agent config with legacy compiled name: %v", err)
	}
	for index := 3; index <= 22; index++ {
		configID := fmt.Sprintf("00000000-0000-0000-0000-%012d", index)
		configIDs = append(configIDs, configID)
		if _, err := pool.Exec(
			ctx,
			insertInvalidSourceConfig,
			configID,
			invalidSource,
			fmt.Sprintf("invalid-resource-name-source-%02d", index),
		); err != nil {
			t.Fatalf("insert extra invalid stored agent config %d: %v", index, err)
		}
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE agent_configs ENABLE TRIGGER ALL`); err != nil {
		t.Fatalf("enable agent config triggers: %v", err)
	}
	triggersDisabled = false

	err := dbmigrate.ApplyPostgres(ctx, db, os.DirFS("../../migrations"))
	for _, want := range []string{
		"22 agent configs must be migrated",
		invalidSourceConfigID,
		"machine_pool_name",
		legacyCompiledConfigID,
		`unknown field "name"`,
		configIDs[19],
		"and 2 more",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("resource-name migration error = %v, want %q", err, want)
		}
	}
	if got := strings.Count(err.Error(), "00000000-0000-0000-0000-"); got != 20 {
		t.Fatalf("reported agent config IDs = %d, want 20: %v", got, err)
	}
	for _, omittedID := range configIDs[20:] {
		if strings.Contains(err.Error(), omittedID) {
			t.Fatalf("agent config migration error included truncated ID %s: %v", omittedID, err)
		}
	}
	if got := currentPostgresMigrationVersion(t, ctx, db); got != 20 {
		t.Fatalf("migration version after rejected agent config source = %d, want 20", got)
	}

	if _, err := pool.Exec(ctx, `ALTER TABLE agent_configs DISABLE TRIGGER ALL`); err != nil {
		t.Fatalf("disable agent config triggers for repair: %v", err)
	}
	triggersDisabled = true
	if _, err := pool.Exec(
		ctx,
		`DELETE FROM agent_configs
		 WHERE source_hash = 'legacy-compiled-name'
		    OR source_hash LIKE 'invalid-resource-name-source%'`,
	); err != nil {
		t.Fatalf("remove invalid stored agent configs: %v", err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE agent_configs ENABLE TRIGGER ALL`); err != nil {
		t.Fatalf("enable agent config triggers after repair: %v", err)
	}
	triggersDisabled = false
	if err := dbmigrate.ApplyPostgres(ctx, db, os.DirFS("../../migrations")); err != nil {
		t.Fatalf("apply resource-name migration after agent config repair: %v", err)
	}
}

func TestResourceNameMigrationPreflightUsesConfiguredSearchPath(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	for _, statement := range []string{
		`DROP EXTENSION IF EXISTS pg_trgm`,
		`CREATE SCHEMA extensions`,
		`CREATE EXTENSION pg_trgm WITH SCHEMA extensions`,
		`CREATE SCHEMA agents`,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	productConfig := pool.Config().ConnConfig.Copy()
	productConfig.RuntimeParams["search_path"] = "agents,extensions"
	productDB := stdlib.OpenDB(*productConfig)
	t.Cleanup(func() { _ = productDB.Close() })
	if err := dbmigrate.ApplyPostgres(ctx, productDB, migrationFilesThrough(t, 20)); err != nil {
		t.Fatalf("apply migrations in product schema: %v", err)
	}

	if _, err := productDB.ExecContext(ctx, `ALTER TABLE agent_configs DISABLE TRIGGER ALL`); err != nil {
		t.Fatalf("disable agent config triggers: %v", err)
	}
	triggersDisabled := true
	t.Cleanup(func() {
		if triggersDisabled {
			_, _ = productDB.ExecContext(
				context.Background(),
				`ALTER TABLE agent_configs ENABLE TRIGGER ALL`,
			)
		}
	})
	const configID = "00000000-0000-0000-0000-000000000001"
	if _, err := productDB.ExecContext(ctx, `
INSERT INTO agent_configs(
    id,
    org_id,
    project_id,
    configured_model_id,
    definition,
    source,
    source_format,
    source_hash,
    compiled_definition,
    compiler_version,
    effective_definition_hash,
    created_at
) VALUES (
    $1::uuid,
    uuidv7(),
    uuidv7(),
    uuidv7(),
    '{}'::jsonb,
    '',
    'yaml',
    'search-path-invalid',
    '{}'::jsonb,
    '',
    'search-path-invalid',
    now()
)
`, configID); err != nil {
		t.Fatalf("insert invalid stored agent config: %v", err)
	}
	if _, err := productDB.ExecContext(ctx, `ALTER TABLE agent_configs ENABLE TRIGGER ALL`); err != nil {
		t.Fatalf("enable agent config triggers: %v", err)
	}
	triggersDisabled = false

	migrationConfig := pool.Config().ConnConfig.Copy()
	migrationConfig.RuntimeParams["search_path"] = "agents,extensions"
	migrationDB := stdlib.OpenDB(*migrationConfig)
	t.Cleanup(func() { _ = migrationDB.Close() })
	err := dbmigrate.ApplyPostgres(ctx, migrationDB, os.DirFS("../../migrations"))
	for _, want := range []string{"1 agent config must be migrated", configID, "source"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("resource-name migration error = %v, want %q", err, want)
		}
	}
	if got := currentPostgresMigrationVersion(t, ctx, migrationDB); got != 20 {
		t.Fatalf("migration version after search-path preflight = %d, want 20", got)
	}
}

func TestPostgresResourceNamePolicyMatchesGo(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool, _ := openPostgresMigrationTestDB(t, ctx)
	wantForbidden := make([]int, 0)
	for codepoint := rune(0); codepoint <= unicode.MaxRune; codepoint++ {
		if unicode.IsControl(codepoint) || unicode.In(
			codepoint,
			unicode.Cf,
			unicode.Other_Default_Ignorable_Code_Point,
			unicode.Variation_Selector,
		) || codepoint == '\u2800' || codepoint == '\ufffd' ||
			(unicode.IsSpace(codepoint) && codepoint != ' ') {
			wantForbidden = append(wantForbidden, int(codepoint))
		}
	}
	rows, err := pool.Query(ctx, `
SELECT codepoint
FROM generate_series(0, $1) AS codepoints(codepoint)
WHERE resource_name_codepoint_is_forbidden(codepoint)
ORDER BY codepoint
`, int(unicode.MaxRune))
	if err != nil {
		t.Fatalf("query forbidden resource-name code points: %v", err)
	}
	defer rows.Close()
	gotForbidden := make([]int, 0, len(wantForbidden))
	for rows.Next() {
		var codepoint int
		if err := rows.Scan(&codepoint); err != nil {
			t.Fatalf("scan forbidden resource-name code point: %v", err)
		}
		gotForbidden = append(gotForbidden, codepoint)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate forbidden resource-name code points: %v", err)
	}
	if !slices.Equal(gotForbidden, wantForbidden) {
		t.Fatalf("PostgreSQL forbidden code points differ from Go\n got: %v\nwant: %v", gotForbidden, wantForbidden)
	}

	tests := []struct {
		value      string
		allowEmpty bool
		max        int
	}{
		{value: "", max: resourcename.MaxCodePoints},
		{value: "", allowEmpty: true, max: resourcename.MaxCodePoints},
		{value: "Studio  54", max: resourcename.MaxCodePoints},
		{value: "研究開発 شركة برمجيات", max: resourcename.MaxCodePoints},
		{value: "🚀 Lab", max: resourcename.MaxCodePoints},
		{value: strings.Repeat("界", resourcename.MaxCodePoints), max: resourcename.MaxCodePoints},
		{value: strings.Repeat("界", resourcename.MaxCodePoints+1), max: resourcename.MaxCodePoints},
		{value: " Acme", max: resourcename.MaxCodePoints},
		{value: "Acme ", max: resourcename.MaxCodePoints},
		{value: "\u00a0Acme", max: resourcename.MaxCodePoints},
		{value: "Acme\u00a0Labs", max: resourcename.MaxCodePoints},
		{value: "Acme\u200dLabs", max: resourcename.MaxCodePoints},
		{value: "Acme\u202eLabs", max: resourcename.MaxCodePoints},
		{value: strings.Repeat("a", 128), max: 128},
		{value: strings.Repeat("a", 129), max: 128},
	}
	for _, test := range tests {
		want := (test.allowEmpty || test.value != "") &&
			resourcename.ValidateWithMax("name", test.value, test.max) == nil
		var got bool
		if err := pool.QueryRow(
			ctx,
			`SELECT resource_name_is_valid_with_max($1, $2, $3)`,
			test.value,
			test.allowEmpty,
			test.max,
		).Scan(&got); err != nil {
			t.Fatalf("validate resource name %q in PostgreSQL: %v", test.value, err)
		}
		if got != want {
			t.Errorf(
				"PostgreSQL resource-name validity for %q (allow_empty=%t, max=%d) = %t, want %t",
				test.value,
				test.allowEmpty,
				test.max,
				got,
				want,
			)
		}
	}
}

func TestPostgresStoredOrgScopeColumnsMatchOwnershipBoundaries(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, db := openPostgresMigrationTestDB(t, ctx)
	const expected = "agent_configs,agent_machine_bindings,agents,configured_model_revisions,configured_models,daemon_runtimes,integration_installs,machine_daemon_tokens,machine_online_intervals,machine_pools,machines,model_call_contexts,model_provider_configs,org_api_keys,org_invitations,org_managed_work_admission,org_memberships,org_resource_limit_overrides,process_actions,processes,project_machine_grants,project_machine_pool_grants,project_memberships,project_model_grants,projects,secret_grants,secret_oauth_refresh_leases,secret_versions,secrets,skill_grants,skills"
	var actual string
	if err := db.QueryRowContext(ctx, `
SELECT coalesce(string_agg(column_info.table_name, ',' ORDER BY column_info.table_name), '')
FROM information_schema.columns column_info
JOIN information_schema.tables table_info
  ON table_info.table_schema = column_info.table_schema
 AND table_info.table_name = column_info.table_name
WHERE column_info.table_schema = current_schema()
  AND column_info.column_name = 'org_id'
  AND table_info.table_type = 'BASE TABLE'
`).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual != expected {
		t.Fatalf("stored org_id columns = %q, want %q", actual, expected)
	}
}

func TestPostgresStoredProjectScopeColumnsMatchOwnershipBoundaries(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, db := openPostgresMigrationTestDB(t, ctx)
	const expected = "actors,agent_configs,agent_inputs,agent_machine_bindings,agent_profile_versions,agent_profiles,agents,cron_triggers,integration_installs,integration_targets,model_call_contexts,process_actions,processes,project_machine_grants,project_machine_pool_grants,project_memberships,project_model_grants"
	var actual string
	if err := db.QueryRowContext(ctx, `
	SELECT coalesce(string_agg(column_info.table_name, ',' ORDER BY column_info.table_name), '')
	FROM information_schema.columns column_info
	JOIN information_schema.tables table_info
	  ON table_info.table_schema = column_info.table_schema
	 AND table_info.table_name = column_info.table_name
	WHERE column_info.table_schema = current_schema()
	  AND column_info.column_name = 'project_id'
	  AND table_info.table_type = 'BASE TABLE'
	`).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual != expected {
		t.Fatalf("stored project_id columns = %q, want %q", actual, expected)
	}
}

func TestPostgresProjectOrganizationIsImmutable(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, db := openPostgresMigrationTestDB(t, ctx)
	var originalOrgID, otherOrgID, projectID string
	for name, destination := range map[string]*string{
		"original": &originalOrgID,
		"other":    &otherOrgID,
	} {
		if err := db.QueryRowContext(ctx, `
INSERT INTO orgs(name, created_at, updated_at)
VALUES ($1, statement_timestamp(), statement_timestamp())
RETURNING id::text
`, name).Scan(destination); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.QueryRowContext(ctx, `
INSERT INTO projects(org_id, name, created_at, updated_at)
VALUES ($1, 'immutable-project', statement_timestamp(), statement_timestamp())
RETURNING id::text
`, originalOrgID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE projects SET org_id = $1 WHERE id = $2`, otherOrgID, projectID); err == nil {
		t.Fatal("project organization update succeeded")
	}
	var retainedOrgID string
	if err := db.QueryRowContext(ctx, `SELECT org_id::text FROM projects WHERE id = $1`, projectID).Scan(&retainedOrgID); err != nil {
		t.Fatal(err)
	}
	if retainedOrgID != originalOrgID {
		t.Fatalf("project org_id = %s, want %s", retainedOrgID, originalOrgID)
	}
}

func TestPostgresAuthoredSchemaIdentifiersStayBelowTruncationLimit(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, db := openPostgresMigrationTestDB(t, ctx)
	var identifiers string
	if err := db.QueryRowContext(ctx, `
SELECT coalesce(string_agg(kind || ':' || owner || '.' || name, ',' ORDER BY kind, owner, name), '')
FROM (
  SELECT 'relation' AS kind, namespace.nspname AS owner, relation.relname AS name
  FROM pg_class relation
  JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace
  WHERE namespace.oid = current_schema()::regnamespace
    AND NOT EXISTS (
      SELECT 1
      FROM pg_constraint constraint_info
      WHERE constraint_info.conindid = relation.oid
    )
  UNION ALL
  SELECT 'column', column_info.attrelid::regclass::text, column_info.attname
  FROM pg_attribute column_info
  WHERE column_info.attrelid IN (
    SELECT relation.oid
    FROM pg_class relation
    WHERE relation.relnamespace = current_schema()::regnamespace
  )
    AND column_info.attnum > 0
    AND NOT column_info.attisdropped
  UNION ALL
  SELECT 'trigger', trigger_info.tgrelid::regclass::text, trigger_info.tgname
  FROM pg_trigger trigger_info
  WHERE trigger_info.tgrelid IN (
    SELECT relation.oid
    FROM pg_class relation
    WHERE relation.relnamespace = current_schema()::regnamespace
  )
    AND NOT trigger_info.tgisinternal
  UNION ALL
  SELECT 'function', current_schema(), function_info.proname
  FROM pg_proc function_info
  WHERE function_info.pronamespace = current_schema()::regnamespace
  UNION ALL
  SELECT 'policy', policy_info.polrelid::regclass::text, policy_info.polname
  FROM pg_policy policy_info
  WHERE policy_info.polrelid IN (
    SELECT relation.oid
    FROM pg_class relation
    WHERE relation.relnamespace = current_schema()::regnamespace
  )
) identifiers
WHERE octet_length(name) >= 63
`).Scan(&identifiers); err != nil {
		t.Fatal(err)
	}
	if identifiers != "" {
		t.Fatalf("authored schema identifiers at PostgreSQL's 63-byte truncation limit: %s", identifiers)
	}
}

func TestPostgresExplicitConstraintNamesStayBelowTruncationLimit(t *testing.T) {
	t.Parallel()
	migrationFiles, err := filepath.Glob("../../migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	constraintNamePattern := regexp.MustCompile(
		`(?is)\b(?:ADD\s+)?CONSTRAINT\s+("(?:[^"]|"")*"|[a-z_][a-z0-9_$]*)\s+` +
			`(?:CHECK\b|UNIQUE\b|PRIMARY\s+KEY\b|EXCLUDE\b|FOREIGN\s+KEY\b|` +
			`REFERENCES\b|NOT\s+NULL\b|NULL\b|DEFAULT\b|GENERATED\b)`,
	)
	columnReference := `owner_id uuid CONSTRAINT owner_reference_contract REFERENCES owners(id)`
	if match := constraintNamePattern.FindStringSubmatch(columnReference); len(match) != 2 || match[1] != "owner_reference_contract" {
		t.Fatalf("explicit constraint matcher missed column-level reference: %q", match)
	}
	for _, migrationFile := range migrationFiles {
		body, readErr := os.ReadFile(migrationFile)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, match := range constraintNamePattern.FindAllSubmatch(body, -1) {
			name := string(match[1])
			if strings.HasPrefix(name, `"`) {
				name = strings.ReplaceAll(strings.Trim(name, `"`), `""`, `"`)
			}
			if len([]byte(name)) >= 63 {
				t.Errorf("explicit constraint name %q in %s reaches PostgreSQL's 63-byte truncation limit", name, migrationFile)
			}
		}
	}
}

func TestPostgresSchemaHasNoExactDuplicateIndexes(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, db := openPostgresMigrationTestDB(t, ctx)
	var duplicates string
	if err := db.QueryRowContext(ctx, `
WITH index_info AS (
  SELECT index_catalog.indrelid,
         index_catalog.indexrelid,
         index_catalog.indisexclusion,
         access_method.amname,
         index_catalog.indnkeyatts,
         index_catalog.indnatts,
         index_catalog.indkey::text AS keys,
         index_catalog.indcollation::text AS collations,
         index_catalog.indclass::text AS classes,
         index_catalog.indoption::text AS options,
         coalesce(pg_get_expr(index_catalog.indexprs, index_catalog.indrelid), '') AS expressions,
         coalesce(pg_get_expr(index_catalog.indpred, index_catalog.indrelid), '') AS predicate
  FROM pg_index index_catalog
  JOIN pg_class index_relation ON index_relation.oid = index_catalog.indexrelid
  JOIN pg_am access_method ON access_method.oid = index_relation.relam
  WHERE index_relation.relnamespace = current_schema()::regnamespace
    AND index_catalog.indisvalid
    AND index_catalog.indisready
), duplicate_groups AS (
  SELECT indrelid,
         string_agg(indexrelid::regclass::text, '+' ORDER BY indexrelid::regclass::text) AS names
  FROM index_info
  GROUP BY indrelid, indisexclusion, amname, indnkeyatts, indnatts,
           keys, collations, classes, options, expressions, predicate
  HAVING count(*) > 1
)
SELECT coalesce(string_agg(indrelid::regclass::text || ':' || names, ',' ORDER BY indrelid::regclass::text), '')
FROM duplicate_groups
`).Scan(&duplicates); err != nil {
		t.Fatal(err)
	}
	if duplicates != "" {
		t.Fatalf("exact duplicate indexes: %s", duplicates)
	}
}

func TestPostgresUniqueConstraintsDoNotRedundantlySupersetPrimaryKeys(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, db := openPostgresMigrationTestDB(t, ctx)
	var redundant string
	if err := db.QueryRowContext(ctx, `
WITH primary_keys AS (
  SELECT primary_key.conrelid, primary_key.conkey
  FROM pg_constraint primary_key
  WHERE primary_key.connamespace = current_schema()::regnamespace
    AND primary_key.contype = 'p'
), redundant_unique_constraints AS (
  SELECT unique_constraint.conrelid,
         unique_constraint.conname
  FROM pg_constraint unique_constraint
  JOIN primary_keys
    ON primary_keys.conrelid = unique_constraint.conrelid
  WHERE unique_constraint.connamespace = current_schema()::regnamespace
    AND unique_constraint.contype = 'u'
    AND unique_constraint.conkey @> primary_keys.conkey
    AND cardinality(unique_constraint.conkey) > cardinality(primary_keys.conkey)
    AND NOT EXISTS (
      SELECT 1
      FROM pg_constraint foreign_key
      WHERE foreign_key.contype = 'f'
        AND foreign_key.confrelid = unique_constraint.conrelid
        AND foreign_key.confkey = unique_constraint.conkey
    )
)
SELECT coalesce(string_agg(conrelid::regclass::text || '.' || conname, ',' ORDER BY conrelid::regclass::text, conname), '')
FROM redundant_unique_constraints
`).Scan(&redundant); err != nil {
		t.Fatal(err)
	}
	if redundant != "" {
		t.Fatalf("unique constraints redundantly contain primary keys without a referencing foreign key: %s", redundant)
	}
}

const mutatingForeignKeysWithoutSupportingIndexesQuery = `
WITH mutating_foreign_keys AS (
  SELECT constraint_info.conrelid,
         constraint_info.conname,
         constraint_info.conkey,
         CASE count(*) FILTER (WHERE NOT child_column.attnotnull)
           WHEN 0 THEN NULL
           WHEN 1 THEN string_agg(format('(%I IS NOT NULL)', child_column.attname), '')
             FILTER (WHERE NOT child_column.attnotnull)
           ELSE '(' || string_agg(
             format('(%I IS NOT NULL)', child_column.attname),
             ' AND ' ORDER BY foreign_key_column.ordinal
           ) FILTER (WHERE NOT child_column.attnotnull) || ')'
         END AS nullable_key_predicate
  FROM pg_constraint constraint_info
  CROSS JOIN LATERAL unnest(constraint_info.conkey) WITH ORDINALITY
    AS foreign_key_column(attnum, ordinal)
  JOIN pg_attribute child_column
    ON child_column.attrelid = constraint_info.conrelid
   AND child_column.attnum = foreign_key_column.attnum
  WHERE constraint_info.connamespace = current_schema()::regnamespace
    AND constraint_info.contype = 'f'
    AND constraint_info.confdeltype IN ('c', 'n', 'd')
  GROUP BY constraint_info.conrelid, constraint_info.conname, constraint_info.conkey
), unsupported_foreign_keys AS (
  SELECT foreign_key.conrelid,
         foreign_key.conname
  FROM mutating_foreign_keys foreign_key
  WHERE NOT EXISTS (
    SELECT 1
    FROM pg_index index_info
    WHERE index_info.indrelid = foreign_key.conrelid
      AND index_info.indisvalid
      AND index_info.indisready
      AND index_info.indnkeyatts >= cardinality(foreign_key.conkey)
      AND (
        SELECT array_agg(index_column.attnum ORDER BY index_column.attnum)
        FROM unnest(index_info.indkey) WITH ORDINALITY index_column(attnum, ordinal)
        WHERE index_column.ordinal <= cardinality(foreign_key.conkey)
      ) = (
          SELECT array_agg(foreign_key_column.attnum ORDER BY foreign_key_column.attnum)
          FROM unnest(foreign_key.conkey) foreign_key_column(attnum)
        )
      AND (
        index_info.indpred IS NULL
        OR (
          foreign_key.nullable_key_predicate IS NOT NULL
          AND pg_get_expr(index_info.indpred, index_info.indrelid) =
            foreign_key.nullable_key_predicate
        )
      )
  )
)
SELECT coalesce(string_agg(conrelid::regclass::text || '.' || conname, ',' ORDER BY conrelid::regclass::text, conname), '')
FROM unsupported_foreign_keys
`

func TestPostgresMutatingForeignKeysHaveSupportingIndexes(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, db := openPostgresMigrationTestDB(t, ctx)
	unsupported := mutatingForeignKeysWithoutSupportingIndexes(t, ctx, db)
	if unsupported != "" {
		t.Fatalf("mutating foreign keys without full-key indexes: %s", unsupported)
	}
}

func TestPostgresMutatingForeignKeyIndexesRejectIncompletePredicates(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, db := openPostgresMigrationTestDB(t, ctx)
	if _, err := db.ExecContext(ctx, `
DROP INDEX user_auth_tokens_email_idx;
CREATE INDEX user_auth_tokens_incomplete_email_idx
    ON user_auth_tokens(user_id, user_email_id)
    WHERE consumed_at IS NULL;
`); err != nil {
		t.Fatal(err)
	}
	unsupported := mutatingForeignKeysWithoutSupportingIndexes(t, ctx, db)
	if !strings.Contains(unsupported, "user_auth_tokens.") {
		t.Fatalf("incomplete partial index unexpectedly supported user-auth-token cascade: %s", unsupported)
	}
}

func mutatingForeignKeysWithoutSupportingIndexes(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) string {
	t.Helper()
	var unsupported string
	if err := db.QueryRowContext(ctx, mutatingForeignKeysWithoutSupportingIndexesQuery).Scan(&unsupported); err != nil {
		t.Fatal(err)
	}
	return unsupported
}

func TestOpenPostgresAcceptsPoolParameters(t *testing.T) {
	t.Parallel()

	databaseURL := os.Getenv("OMNARA_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("OMNARA_TEST_DATABASE_URL is not set")
	}
	integrationdb.AssertTestDatabaseURL(t, databaseURL)
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("pool_max_conns", "5")
	parsed.RawQuery = query.Encode()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := dbmigrate.OpenPostgres(ctx, parsed.String())
	if err != nil {
		t.Fatalf("open migration database with pool parameter: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenPostgresAppliesSessionDefaultsToEveryConnection(t *testing.T) {
	t.Parallel()
	databaseURL := testDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	db, err := dbmigrate.OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open migration database: %v", err)
	}
	defer func() { _ = db.Close() }()

	connections := make([]*sql.Conn, 0, 3)
	defer func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	}()
	for range 3 {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("acquire migration connection: %v", err)
		}
		connections = append(connections, conn)
		var applicationName, lockTimeout, statementTimeout string
		if err := conn.QueryRowContext(ctx, `
SELECT current_setting('application_name'),
       current_setting('lock_timeout'),
       current_setting('statement_timeout')
`).Scan(&applicationName, &lockTimeout, &statementTimeout); err != nil {
			t.Fatalf("read migration session settings: %v", err)
		}
		if applicationName != "omnara-migrate" || lockTimeout != "30s" || statementTimeout != "15min" {
			t.Fatalf(
				"migration session settings = (%q, %q, %q)",
				applicationName,
				lockTimeout,
				statementTimeout,
			)
		}
	}
}

func TestOpenPostgresPreservesExplicitSessionSettings(t *testing.T) {
	t.Parallel()
	parsed, err := url.Parse(testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("application_name", "operator-migrator")
	query.Set("lock_timeout", "4s")
	query.Set("statement_timeout", "9s")
	parsed.RawQuery = query.Encode()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := dbmigrate.OpenPostgres(ctx, parsed.String())
	if err != nil {
		t.Fatalf("open migration database: %v", err)
	}
	defer func() { _ = db.Close() }()

	var applicationName, lockTimeout, statementTimeout string
	if err := db.QueryRowContext(ctx, `
SELECT current_setting('application_name'),
       current_setting('lock_timeout'),
       current_setting('statement_timeout')
`).Scan(&applicationName, &lockTimeout, &statementTimeout); err != nil {
		t.Fatal(err)
	}
	if applicationName != "operator-migrator" || lockTimeout != "4s" || statementTimeout != "9s" {
		t.Fatalf(
			"migration session settings = (%q, %q, %q)",
			applicationName,
			lockTimeout,
			statementTimeout,
		)
	}
}

func TestRunPostgresHonorsWholeRunTimeout(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	databaseURL := generatedDatabaseURL(t, pool)

	slow := fstest.MapFS{
		"000001_slow.sql": {Data: []byte(`-- +goose Up
CREATE TABLE slow_probe(id bigint PRIMARY KEY);
SELECT pg_sleep(10);
`)},
	}
	started := time.Now()
	err := dbmigrate.RunPostgres(ctx, databaseURL, slow, 200*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded migration error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("bounded migration returned after %s", elapsed)
	}

	var tableExists bool
	if err := pool.QueryRow(
		ctx,
		`SELECT to_regclass('slow_probe') IS NOT NULL`,
	).Scan(&tableExists); err != nil {
		t.Fatal(err)
	}
	if tableExists {
		t.Fatal("timed-out migration left schema changes behind")
	}
}

func TestPostgresPopulatedVersion14UpgradesToLatest(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	latestVersion := latestPostgresMigrationVersion(t)

	if err := dbmigrate.ApplyPostgres(ctx, db, migrationFilesThrough(t, 14)); err != nil {
		t.Fatalf("apply migrations through version 14: %v", err)
	}
	if got := currentPostgresMigrationVersion(t, ctx, db); got != 14 {
		t.Fatalf("pre-upgrade schema version = %d, want 14", got)
	}
	var orgID, projectID, poolID, grantID, machineID string
	if err := db.QueryRowContext(ctx, `
INSERT INTO orgs(name, created_at, updated_at)
VALUES ('v14 populated org', statement_timestamp(), statement_timestamp())
RETURNING id::text
`).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
INSERT INTO projects(org_id, name, created_at, updated_at)
VALUES ($1, 'v14 populated project', statement_timestamp(), statement_timestamp())
RETURNING id::text
`, orgID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
INSERT INTO machine_pools(
    org_id, name, management_kind, provider,
    default_machine_cpu, default_machine_memory_mb,
    provider_auth_env_var, max_total_machines,
    max_machine_cpu, max_machine_memory_mb, metadata,
    created_at, updated_at
)
VALUES (
    $1, 'v14 populated pool', 'cluster', 'test',
    4, 8192, 'V14_PROVIDER_TOKEN', 10, 8, 16384,
    '{"fixture":"v14"}'::jsonb,
    statement_timestamp(), statement_timestamp()
)
RETURNING id::text
`, orgID).Scan(&poolID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
INSERT INTO project_machine_pool_grants(
    org_id, project_id, machine_pool_id,
    default_machine_cpu, default_machine_memory_mb,
    max_machine_cpu, max_machine_memory_mb, metadata,
    created_at, updated_at
)
VALUES (
    $1, $2, $3, 2, 4096, 6, 8192,
    '{"fixture":"v14"}'::jsonb,
    statement_timestamp(), statement_timestamp()
)
RETURNING id::text
`, orgID, projectID, poolID).Scan(&grantID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
INSERT INTO machines(
    org_id, source_kind, display_name, provider,
    lifecycle_state, lifecycle_changed_at, metadata,
    created_at, updated_at
)
VALUES (
    $1, 'byo', 'v14 populated machine', 'byo',
    'active', statement_timestamp(),
    '{"fixture":"v14","observed_platform":{"os":"linux","arch":"amd64"}}'::jsonb,
    statement_timestamp(), statement_timestamp()
)
RETURNING id::text
`, orgID).Scan(&machineID); err != nil {
		t.Fatal(err)
	}

	if err := dbmigrate.ApplyPostgres(ctx, db, os.DirFS("../../migrations")); err != nil {
		t.Fatalf("upgrade populated version 14 database: %v", err)
	}
	if got := currentPostgresMigrationVersion(t, ctx, db); got != latestVersion {
		t.Fatalf("upgraded schema version = %d, want %d", got, latestVersion)
	}
	var joinedRows int
	if err := db.QueryRowContext(ctx, `
SELECT count(*)
FROM project_machine_pool_grants grant_record
JOIN projects project_record
  ON project_record.org_id = grant_record.org_id
 AND project_record.id = grant_record.project_id
JOIN machine_pools pool_record
  ON pool_record.org_id = grant_record.org_id
 AND pool_record.id = grant_record.machine_pool_id
WHERE grant_record.id = $1
  AND project_record.id = $2
  AND pool_record.id = $3
  AND pool_record.metadata = '{"fixture":"v14"}'::jsonb
  AND grant_record.metadata = '{"fixture":"v14"}'::jsonb
  AND pool_record.min_machine_cpu IS NULL
  AND pool_record.min_machine_memory_mb IS NULL
  AND grant_record.min_machine_cpu IS NULL
  AND grant_record.min_machine_memory_mb IS NULL
`, grantID, projectID, poolID).Scan(&joinedRows); err != nil {
		t.Fatal(err)
	}
	if joinedRows != 1 {
		t.Fatalf("preserved v14 relationship rows = %d, want 1", joinedRows)
	}
	var observedPlatform string
	if err := db.QueryRowContext(
		ctx,
		`SELECT metadata->>'observed_platform' FROM machines WHERE id = $1`,
		machineID,
	).Scan(&observedPlatform); err != nil {
		t.Fatal(err)
	}
	if observedPlatform != "linux/amd64" {
		t.Fatalf("normalized observed platform = %q, want linux/amd64", observedPlatform)
	}

	if _, err := db.ExecContext(ctx, `
UPDATE machine_pools
SET min_machine_cpu = 2, min_machine_memory_mb = 4096
WHERE id = $1
`, poolID); err != nil {
		t.Fatalf("set valid pool minimums: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
UPDATE project_machine_pool_grants
SET min_machine_cpu = 1, min_machine_memory_mb = 2048
WHERE id = $1
`, grantID); err != nil {
		t.Fatalf("set valid grant minimums: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE machine_pools SET min_machine_cpu = 9 WHERE id = $1`, poolID); err == nil {
		t.Fatal("pool minimum above maximum unexpectedly succeeded")
	}
	if _, err := db.ExecContext(
		ctx,
		`UPDATE project_machine_pool_grants SET min_machine_memory_mb = 9000 WHERE id = $1`,
		grantID,
	); err == nil {
		t.Fatal("grant minimum above maximum unexpectedly succeeded")
	}

	if err := dbmigrate.ApplyPostgres(ctx, db, os.DirFS("../../migrations")); err != nil {
		t.Fatalf("replay upgraded populated database: %v", err)
	}
}

func TestPostgresMigrationsUseConfiguredPgTrgmSchema(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	for _, statement := range []string{
		`DROP EXTENSION IF EXISTS pg_trgm`,
		`CREATE SCHEMA extensions`,
		`CREATE EXTENSION pg_trgm WITH SCHEMA extensions`,
		`CREATE SCHEMA agents`,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}

	config := pool.Config().ConnConfig.Copy()
	config.RuntimeParams["search_path"] = "agents,extensions"
	db := stdlib.OpenDB(*config)
	defer func() { _ = db.Close() }()
	if err := dbmigrate.ApplyPostgres(
		ctx,
		db,
		os.DirFS("../../migrations"),
	); err != nil {
		t.Fatalf("migrate with configured pg_trgm schema: %v", err)
	}

	var extensionSchema string
	if err := db.QueryRowContext(
		ctx,
		`SELECT namespace.nspname
		 FROM pg_catalog.pg_extension AS extension
		 JOIN pg_catalog.pg_namespace AS namespace
		   ON namespace.oid = extension.extnamespace
		 WHERE extension.extname = 'pg_trgm'`,
	).Scan(&extensionSchema); err != nil {
		t.Fatal(err)
	}
	if extensionSchema != "extensions" {
		t.Fatalf("pg_trgm schema = %q, want extensions", extensionSchema)
	}

	var indexes int
	if err := db.QueryRowContext(
		ctx,
		`SELECT count(*)
		 FROM pg_catalog.pg_indexes
		 WHERE schemaname = 'agents'
		   AND indexname IN (
		       'projects_name_trgm_idx',
		       'secrets_name_trgm_idx',
		       'agent_profiles_name_trgm_idx',
		       'agents_name_trgm_idx',
		       'machines_display_name_trgm_idx',
		       'skills_name_trgm_idx'
		   )`,
	).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if indexes != 6 {
		t.Fatalf("trigram indexes in product schema = %d, want 6", indexes)
	}
}

func TestPostgresMigrationsInstallPgTrgmInProductSchema(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	for _, statement := range []string{
		`DROP EXTENSION IF EXISTS pg_trgm`,
		`DROP SCHEMA public CASCADE`,
		`CREATE SCHEMA agents`,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}

	config := pool.Config().ConnConfig.Copy()
	config.RuntimeParams["search_path"] = "agents"
	db := stdlib.OpenDB(*config)
	defer func() { _ = db.Close() }()
	if err := dbmigrate.ApplyPostgres(
		ctx,
		db,
		os.DirFS("../../migrations"),
	); err != nil {
		t.Fatalf("migrate without public schema: %v", err)
	}

	var extensionSchema string
	if err := db.QueryRowContext(
		ctx,
		`SELECT namespace.nspname
		 FROM pg_catalog.pg_extension AS extension
		 JOIN pg_catalog.pg_namespace AS namespace
		   ON namespace.oid = extension.extnamespace
		 WHERE extension.extname = 'pg_trgm'`,
	).Scan(&extensionSchema); err != nil {
		t.Fatal(err)
	}
	if extensionSchema != "agents" {
		t.Fatalf("pg_trgm schema = %q, want agents", extensionSchema)
	}
}

func TestPostgresMigrationFailureRollsBack(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, db := openPostgresMigrationTestDB(t, ctx)
	nextVersion := currentPostgresMigrationVersion(t, ctx, db) + 1
	failing := fstest.MapFS{
		fmt.Sprintf("%06d_failing.sql", nextVersion): {
			Data: []byte(`-- +goose Up
CREATE TABLE migration_transaction_probe(id bigint PRIMARY KEY);
SELECT missing_migration_function();
`),
		},
	}
	if err := dbmigrate.ApplyPostgres(ctx, db, failing); err == nil {
		t.Fatal("failing migration unexpectedly succeeded")
	}

	var probeExists bool
	if err := db.QueryRowContext(
		ctx,
		`SELECT to_regclass('public.migration_transaction_probe') IS NOT NULL`,
	).Scan(&probeExists); err != nil {
		t.Fatal(err)
	}
	if probeExists {
		t.Fatal("failed migration left its schema change behind")
	}
}

func TestPostgresMigrationsSerializeConcurrentRunners(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, db := openPostgresMigrationTestDB(t, ctx)
	nextVersion := currentPostgresMigrationVersion(t, ctx, db) + 1
	locked := fstest.MapFS{
		fmt.Sprintf("%06d_locked.sql", nextVersion): {
			Data: []byte(`-- +goose Up
SELECT pg_sleep(0.2);
CREATE TABLE migration_lock_probe(id bigint PRIMARY KEY);
`),
		},
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			errs <- dbmigrate.ApplyPostgres(ctx, db, locked)
		}()
	}
	ready.Wait()
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent migration: %v", err)
		}
	}

	var applied int
	if err := db.QueryRowContext(
		ctx,
		`SELECT count(*)
		 FROM goose_db_version
		 WHERE version_id = $1 AND is_applied`,
		nextVersion,
	).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("concurrent migration records = %d, want 1", applied)
	}
}

func TestPostgresRejectsNewerDatabaseVersion(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, db := openPostgresMigrationTestDB(t, ctx)
	newerVersion := currentPostgresMigrationVersion(t, ctx, db) + 1
	if _, err := db.ExecContext(
		ctx,
		`INSERT INTO goose_db_version(version_id, is_applied)
		 VALUES($1, true)`,
		newerVersion,
	); err != nil {
		t.Fatal(err)
	}
	err := dbmigrate.ApplyPostgres(ctx, db, os.DirFS("../../migrations"))
	if err == nil || !strings.Contains(err.Error(), "newer than binary target") {
		t.Fatalf("newer database migration error = %v", err)
	}
}

func TestPostgresOlderMigratorSeesConcurrentNewerVersion(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool, db := openPostgresMigrationTestDB(t, ctx)
	nextVersion := currentPostgresMigrationVersion(t, ctx, db) + 1
	newerVersion := nextVersion + 1
	nextMigration := []byte(`-- +goose Up
SELECT pg_sleep(1) /* omnara_concurrent_migration_probe */;
CREATE TABLE concurrent_migration_next(id bigint PRIMARY KEY);
`)
	older := fstest.MapFS{
		fmt.Sprintf("%06d_concurrent.sql", nextVersion): {Data: nextMigration},
	}
	newer := fstest.MapFS{
		fmt.Sprintf("%06d_concurrent.sql", nextVersion): {Data: nextMigration},
		fmt.Sprintf("%06d_newer.sql", newerVersion): {
			Data: []byte(`-- +goose Up
CREATE TABLE concurrent_migration_newer(id bigint PRIMARY KEY);
`),
		},
	}

	newerErr := make(chan error, 1)
	go func() {
		newerErr <- dbmigrate.ApplyPostgres(ctx, db, newer)
	}()

	for {
		var active bool
		if err := pool.QueryRow(
			ctx,
			`SELECT EXISTS (
				SELECT 1
				FROM pg_stat_activity
				WHERE datname = current_database()
				  AND pid <> pg_backend_pid()
				  AND state = 'active'
				  AND query LIKE '%omnara_concurrent_migration_probe%'
			)`,
		).Scan(&active); err != nil {
			t.Fatal(err)
		}
		if active {
			break
		}
		select {
		case err := <-newerErr:
			t.Fatalf("newer migrator finished before concurrency probe: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}

	err := dbmigrate.ApplyPostgres(ctx, db, older)
	if err == nil || !strings.Contains(err.Error(), "newer than binary target") {
		t.Fatalf("older concurrent migrator error = %v", err)
	}
	if err := <-newerErr; err != nil {
		t.Fatalf("newer concurrent migrator: %v", err)
	}
}

func openPostgresMigrationTestDB(
	t *testing.T,
	ctx context.Context,
) (*pgxpool.Pool, *sql.DB) {
	t.Helper()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../migrations")
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	return pool, db
}

func currentPostgresMigrationVersion(t *testing.T, ctx context.Context, db *sql.DB) int64 {
	t.Helper()
	var version int64
	if err := db.QueryRowContext(
		ctx,
		`SELECT coalesce(max(version_id), 0)
		 FROM goose_db_version
		 WHERE is_applied`,
	).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func latestPostgresMigrationVersion(t *testing.T) int64 {
	t.Helper()
	entries, err := os.ReadDir("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	var latest int64
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			t.Fatalf("migration file lacks version prefix: %s", entry.Name())
		}
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			t.Fatalf("parse migration version %s: %v", entry.Name(), err)
		}
		latest = max(latest, version)
	}
	if latest == 0 {
		t.Fatal("no PostgreSQL migrations found")
	}
	return latest
}

func migrationFilesThrough(t *testing.T, maximum int) fstest.MapFS {
	t.Helper()
	entries, err := os.ReadDir("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	migrations := make(fstest.MapFS)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			t.Fatalf("migration file lacks version prefix: %s", entry.Name())
		}
		version, err := strconv.Atoi(prefix)
		if err != nil {
			t.Fatalf("parse migration version %s: %v", entry.Name(), err)
		}
		if version > maximum {
			continue
		}
		data, err := os.ReadFile(filepath.Join("../../migrations", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		migrations[entry.Name()] = &fstest.MapFile{Data: data}
	}
	return migrations
}

func testDatabaseURL(t *testing.T) string {
	t.Helper()
	databaseURL := os.Getenv("OMNARA_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("OMNARA_TEST_DATABASE_URL is not set")
	}
	integrationdb.AssertTestDatabaseURL(t, databaseURL)
	return databaseURL
}

func generatedDatabaseURL(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	parsed, err := url.Parse(testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		t.Skip("whole-run URL test requires a PostgreSQL URL connection string")
	}
	parsed.Path = "/" + pool.Config().ConnConfig.Database
	return parsed.String()
}
