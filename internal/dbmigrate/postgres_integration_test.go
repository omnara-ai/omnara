//go:build integration

package dbmigrate_test

import (
	"context"
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

	"github.com/google/uuid"
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
	migrationFiles, err := productionMigrationFiles()
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

func TestPostgresMigrationSetCannotSkipAgentConfigNameMigration(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	if err := dbmigrate.ApplyPostgres(ctx, db, migrationFilesThrough(t, 24)); err != nil {
		t.Fatalf("apply migrations before agent config name migration: %v", err)
	}
	migrations := migrationFilesThrough(t, 26)
	delete(migrations, "000026_migrate_agent_config_names.go")
	err := dbmigrate.ApplyPostgres(ctx, db, migrations)
	if err == nil || !strings.Contains(err.Error(), "requires missing 000026_migrate_agent_config_names.go") {
		t.Fatalf("missing Go migration error = %v", err)
	}
	if got := currentPostgresMigrationVersion(t, ctx, db); got != 24 {
		t.Fatalf("migration version after missing Go migration = %d, want 24", got)
	}
}

func TestResourceNameMigrationRepairsKnownLegacyOrgNames(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	if err := dbmigrate.ApplyPostgres(ctx, db, migrationFilesThrough(t, 20)); err != nil {
		t.Fatalf("apply migrations before resource-name policy: %v", err)
	}
	tests := []struct {
		input string
		want  string
	}{
		{input: " Legacy Org ", want: "Legacy Org"},
		{input: strings.Repeat("x", 372), want: strings.Repeat("x", 64)},
	}
	orgIDs := make([]string, len(tests))
	for index, test := range tests {
		if err := pool.QueryRow(
			ctx,
			`INSERT INTO orgs(name, created_at, updated_at)
			 VALUES ($1, now(), now())
			 RETURNING id`,
			test.input,
		).Scan(&orgIDs[index]); err != nil {
			t.Fatalf("insert simulated legacy organization %d: %v", index, err)
		}
	}
	if err := dbmigrate.ApplyPostgres(ctx, db, os.DirFS("../../migrations")); err != nil {
		t.Fatalf("apply resource-name migration: %v", err)
	}
	for index, test := range tests {
		var got string
		if err := pool.QueryRow(ctx, `SELECT name FROM orgs WHERE id = $1`, orgIDs[index]).Scan(&got); err != nil {
			t.Fatalf("load repaired organization %d: %v", index, err)
		}
		if got != test.want {
			t.Fatalf("repaired organization %d name = %q, want %q", index, got, test.want)
		}
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
WHERE resource_name_codepoint_is_forbidden_v1(codepoint)
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
		{value: "Café", max: resourcename.MaxCodePoints},
		{value: "Cafe\u0301", max: resourcename.MaxCodePoints},
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
		canonicalize := resourcename.CanonicalizeRequiredWithMax
		if test.allowEmpty {
			canonicalize = resourcename.CanonicalizeOptionalWithMax
		}
		normalized, validationErr := canonicalize("name", test.value, test.max)
		want := (test.allowEmpty || test.value != "") && validationErr == nil && normalized == test.value
		var got bool
		if err := pool.QueryRow(
			ctx,
			`SELECT resource_name_is_valid_with_max_v1($1, $2, $3)`,
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
	var maxValid, overMaxValid bool
	if err := pool.QueryRow(
		ctx,
		`SELECT resource_name_is_valid_v1($1, false), resource_name_is_valid_v1($2, false)`,
		strings.Repeat("x", resourcename.MaxCodePoints),
		strings.Repeat("x", resourcename.MaxCodePoints+1),
	).Scan(&maxValid, &overMaxValid); err != nil {
		t.Fatalf("validate default PostgreSQL resource-name maximum: %v", err)
	}
	if !maxValid || overMaxValid {
		t.Fatalf(
			"default PostgreSQL resource-name maximum accepted max=%t over-max=%t",
			maxValid,
			overMaxValid,
		)
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

func TestPostgresPopulatedVersion20BackfillsConfiguredModelManagementKind(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()

	if err := dbmigrate.ApplyPostgres(ctx, db, migrationFilesThrough(t, 20)); err != nil {
		t.Fatalf("apply migrations through version 20: %v", err)
	}
	if got := currentPostgresMigrationVersion(t, ctx, db); got != 20 {
		t.Fatalf("pre-upgrade schema version = %d, want 20", got)
	}

	orgID := uuid.NewString()
	secretID := uuid.NewString()
	secretVersionID := uuid.NewString()
	tenantProviderID := uuid.NewString()
	clusterProviderID := uuid.NewString()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO orgs(id, name, created_at, updated_at)
VALUES ($1, 'v19 populated org', statement_timestamp(), statement_timestamp())
`, orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO secrets(
    id, org_id, management_kind, owner_kind, name, kind, metadata,
    current_version_id, created_at, updated_at
)
VALUES (
    $1, $2, 'tenant', 'org', 'v19 tenant provider key', 'generic', '{}'::jsonb,
    $3, statement_timestamp(), statement_timestamp()
)
`, secretID, orgID, secretVersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO secret_versions(
    id, org_id, secret_id, version_number, payload_keys, encryption_scheme,
    key_id, dek_wrapped_by, encrypted_dek, encrypted_dek_nonce, nonce, ciphertext, created_at
)
VALUES (
    $1, $2, $3, 1, ARRAY['value'], 'aes-256-gcm-envelope-v1',
    'test-key', 'local', decode(repeat('01', 48), 'hex'), decode(repeat('02', 12), 'hex'),
    decode(repeat('03', 12), 'hex'), decode(repeat('04', 32), 'hex'), statement_timestamp()
)
`, secretVersionID, orgID, secretID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO model_provider_configs(
    id, org_id, management_kind, name, api_format, base_url, endpoint_path,
    auth_kind, credential_secret_id, created_at, updated_at
)
VALUES (
    $1, $2, 'tenant', 'v19 tenant provider', 'openai-responses',
    'https://tenant.example.test/v1', '/responses', 'bearer_token', $3,
    statement_timestamp(), statement_timestamp()
)
`, tenantProviderID, orgID, secretID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO model_provider_configs(
    id, org_id, management_kind, name, api_format, base_url, endpoint_path,
    auth_kind, credential_secret_id, deleted_at, created_at, updated_at
)
VALUES (
    $1, $2, 'cluster', 'v19 deleted cluster provider', 'openai-responses',
    'https://cluster.example.test/v1', '/responses', 'bearer_token', NULL,
    statement_timestamp(), statement_timestamp(), statement_timestamp()
)
`, clusterProviderID, orgID); err != nil {
		t.Fatal(err)
	}
	for _, model := range []struct {
		name       string
		providerID string
		deleted    bool
	}{
		{name: "cluster legacy model", providerID: clusterProviderID, deleted: true},
		{name: "tenant legacy model", providerID: tenantProviderID},
	} {
		if _, err := tx.ExecContext(ctx, `
WITH configured_model AS (
	INSERT INTO configured_models(
		id, org_id, model_provider_config_id, name, current_revision_id,
		deleted_at, created_at, updated_at
	)
	VALUES (
		$1, $2, $3, $4, $5,
		CASE WHEN $6::boolean THEN statement_timestamp() END,
		statement_timestamp(), statement_timestamp()
	)
	RETURNING id, org_id, model_provider_config_id, current_revision_id
)
INSERT INTO configured_model_revisions(
    id, org_id, configured_model_id, model_provider_config_id,
    provider_model_slug, context_window_tokens, max_output_tokens, created_at
)
SELECT current_revision_id, org_id, id, model_provider_config_id,
       $4, 128000, 8192, statement_timestamp()
FROM configured_model
`, uuid.NewString(), orgID, model.providerID, model.name, uuid.NewString(), model.deleted); err != nil {
			t.Fatalf("insert %s: %v", model.name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if err := dbmigrate.ApplyPostgres(ctx, db, os.DirFS("../../migrations")); err != nil {
		t.Fatalf("upgrade populated version 19 database: %v", err)
	}
	var backfilled string
	if err := db.QueryRowContext(ctx, `
SELECT string_agg(
    configured_model.name || ':' || configured_model.management_kind || ':' || provider_config.management_kind,
    ',' ORDER BY configured_model.name
)
FROM configured_models configured_model
JOIN model_provider_configs provider_config
  ON provider_config.org_id = configured_model.org_id
 AND provider_config.id = configured_model.model_provider_config_id
WHERE configured_model.org_id = $1
`, orgID).Scan(&backfilled); err != nil {
		t.Fatal(err)
	}
	const expected = "cluster legacy model:cluster:cluster,tenant legacy model:tenant:tenant"
	if backfilled != expected {
		t.Fatalf("backfilled configured model authority = %q, want %q", backfilled, expected)
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
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("open empty-search-path connection: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `SET search_path TO ''`); err != nil {
		t.Fatalf("clear search path: %v", err)
	}
	if _, err := conn.ExecContext(
		ctx,
		`INSERT INTO agents.orgs(name, created_at, updated_at)
		 VALUES ('Qualified Organization', now(), now())`,
	); err != nil {
		t.Fatalf("insert through qualified table with empty search path: %v", err)
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
		if entry.IsDir() || !isProductionMigrationFile(entry.Name()) {
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
		if entry.IsDir() || !isProductionMigrationFile(entry.Name()) {
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

func productionMigrationFiles() ([]string, error) {
	entries, err := os.ReadDir("../../migrations")
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !isProductionMigrationFile(entry.Name()) {
			continue
		}
		files = append(files, filepath.Join("../../migrations", entry.Name()))
	}
	return files, nil
}

func isProductionMigrationFile(name string) bool {
	extension := filepath.Ext(name)
	return extension == ".sql" || extension == ".go" && !strings.HasSuffix(name, "_test.go")
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
