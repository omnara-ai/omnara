//go:build integration

package dbmigrate_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/dbmigrate"
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
}

func TestPostgresStoredOrgScopeColumnsMatchOwnershipBoundaries(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, db := openPostgresMigrationTestDB(t, ctx)
	const expected = "agent_configs,agent_machine_bindings,agents,configured_model_revisions,configured_models,daemon_runtimes,integration_installs,machine_daemon_tokens,machine_online_intervals,machine_pools,machines,model_call_contexts,model_provider_configs,org_api_keys,org_invitations,org_managed_work_admission,org_memberships,process_actions,processes,project_machine_grants,project_machine_pool_grants,project_memberships,project_model_grants,projects,secret_grants,secret_oauth_refresh_leases,secret_versions,secrets,skill_grants,skills"
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
	const expected = "actors,agent_configs,agent_inputs,agent_machine_bindings,agent_profile_versions,agent_profiles,agents,integration_installs,integration_targets,model_call_contexts,process_actions,processes,project_machine_grants,project_machine_pool_grants,project_memberships,project_model_grants"
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
