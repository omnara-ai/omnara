package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestCheckerRules(t *testing.T) {
	type ruleTest struct {
		name    string
		command string
		sql     string
		rules   []string
	}
	reject := func(name, command, sql, rule string) ruleTest {
		return ruleTest{name: name, command: command, sql: sql, rules: []string{rule}}
	}
	rejectOutput := func(name, command, sql string) ruleTest {
		return reject(name, command, sql, ruleExplicitOutputColumns)
	}
	rejectTime := func(name, command, sql string) ruleTest {
		return reject(name, command, sql, ruleExplicitDurableTime)
	}
	rejectNowParameter := func(name, command, sql string) ruleTest {
		return reject(name, command, sql, ruleNoApplicationNow)
	}
	rejectApplicationTime := func(name, command, sql string) ruleTest {
		return reject(name, command, sql, ruleDatabaseOwnedTime)
	}
	rejectBlockingTime := func(name, command, sql string) ruleTest {
		return reject(name, command, sql, ruleBlockingLockWithTime)
	}

	tests := []ruleTest{
		rejectOutput("select wildcard", "many", "SELECT * FROM users"),
		rejectOutput("qualified wildcard", "many", "SELECT users.* FROM users"),
		rejectOutput("mixed wildcard", "many", "SELECT id, users.* FROM users"),
		rejectOutput("distinct wildcard", "many", "SELECT DISTINCT * FROM users"),
		rejectOutput("all wildcard", "many", "SELECT ALL * FROM users"),
		rejectOutput("distinct on wildcard", "many", "SELECT DISTINCT ON (id) * FROM users"),
		rejectOutput("table shorthand", "many", "TABLE users"),
		rejectOutput("row wildcard", "many", "SELECT ROW(users.*) FROM users"),
		rejectOutput("aggregate qualified wildcard", "one", "SELECT json_agg(users.*) FROM users"),
		rejectOutput("cast qualified wildcard", "many", "SELECT ROW(users.*)::record FROM users"),
		rejectOutput("nested select wildcard", "one", "SELECT EXISTS (SELECT * FROM users)"),
		rejectOutput("sqlc embed", "many", "SELECT sqlc.embed(users) FROM users"),
		rejectOutput("whole row", "many", "SELECT users FROM users"),
		rejectOutput("aliased whole row", "many", "SELECT account FROM users account"),
		rejectOutput("aggregate whole row", "one", "SELECT json_agg(users) FROM users"),
		rejectOutput("cast whole row", "many", "SELECT users::text FROM users"),
		{name: "ordinary column", command: "many", sql: "SELECT users FROM accounts"},
		{name: "qualified column", command: "many", sql: "SELECT users.id FROM users"},
		rejectOutput("returning wildcard", "one", "INSERT INTO users (id) VALUES (1) RETURNING users.*"),
		rejectOutput(
			"returning whole row",
			"one",
			"UPDATE users SET display_name = 'updated' WHERE id = 1 RETURNING users",
		),
		{name: "aggregate star", command: "one", sql: "SELECT count(*) FROM users"},
		{name: "multiplication", command: "many", sql: "SELECT quantity * price FROM line_items"},
		rejectTime("now function", "one", "SELECT now()"),
		rejectTime("qualified now function", "one", "SELECT pg_catalog.now()"),
		rejectTime("clock timestamp", "one", "SELECT clock_timestamp()"),
		rejectTime("timeofday", "one", "SELECT timeofday()"),
		rejectTime("one argument age", "one", "SELECT age(created_at) FROM users"),
		rejectTime("qualified one argument age", "one", "SELECT pg_catalog.age(created_at) FROM users"),
		{name: "two argument age", command: "one", sql: "SELECT age(ended_at, started_at) FROM jobs"},
		rejectTime("current timestamp", "one", "SELECT CURRENT_TIMESTAMP"),
		rejectTime("current time", "one", "SELECT CURRENT_TIME"),
		rejectTime("current date", "one", "SELECT CURRENT_DATE"),
		rejectTime("localtime", "one", "SELECT LOCALTIME"),
		rejectTime("localtimestamp", "one", "SELECT LOCALTIMESTAMP"),
		rejectTime("now cast", "one", "SELECT 'now'::timestamptz"),
		rejectTime("timestamp now literal", "one", "SELECT TIMESTAMP 'now'"),
		rejectTime("relative date literal", "one", "SELECT DATE 'tomorrow'"),
		rejectTime("padded relative timestamp literal", "one", "SELECT ' now '::timestamptz"),
		rejectTime("zoned relative timestamp literal", "one", "SELECT 'today UTC'::timestamptz"),
		rejectTime("timed relative timestamp literal", "one", "SELECT 'tomorrow 12:00 UTC'::timestamptz"),
		{name: "transaction timestamp", command: "one", sql: "SELECT transaction_timestamp()"},
		{name: "statement timestamp", command: "one", sql: "SELECT pg_catalog.statement_timestamp()"},
		{name: "bare now string", command: "one", sql: "SELECT 'now'"},
		{name: "now text cast", command: "one", sql: "SELECT 'now'::text"},
		{
			name:    "potentially coerced now requires schema analysis",
			command: "many",
			sql:     "SELECT id FROM jobs WHERE created_at < 'now'",
		},
		{name: "today label", command: "many", sql: "SELECT id FROM jobs WHERE label = 'today'"},
		{name: "tomorrow JSON value", command: "one", sql: "SELECT jsonb_build_object('schedule', 'tomorrow')"},
		{
			name:    "temporal conversion around text comparison",
			command: "many",
			sql:     "SELECT date(CASE WHEN label = 'now' THEN created_at::text END) FROM jobs",
		},
		{
			name:    "temporal conversion around simple case operand",
			command: "many",
			sql:     "SELECT date(CASE label WHEN 'now' THEN created_at::text END) FROM jobs",
		},
		{
			name:    "temporal conversion around JSON value",
			command: "one",
			sql:     "SELECT date(jsonb_build_object('schedule', 'tomorrow')->>'date')",
		},
		rejectTime("nested now cast", "one", "SELECT ('now'::text)::timestamptz"),
		rejectTime("nested today cast", "one", "SELECT CAST(CAST('today' AS text) AS date)"),
		rejectTime("date conversion of now string", "one", "SELECT date('now')"),
		rejectTime("date conversion of padded tomorrow string", "one", "SELECT date(' tomorrow ')"),
		rejectTime("timestamp conversion of today string", "one", "SELECT timestamptz('today')"),
		rejectTime("pg catalog date conversion", "one", "SELECT pg_catalog.date('today')"),
		rejectTime("pg catalog date cast", "one", "SELECT 'tomorrow'::pg_catalog.date"),
		rejectTime(
			"date conversion of case result",
			"one",
			"SELECT date(CASE WHEN enabled THEN 'now' ELSE '2026-01-01' END)",
		),
		rejectTime(
			"date conversion of case default",
			"one",
			"SELECT date(CASE WHEN enabled THEN '2026-01-01' ELSE 'today' END)",
		),
		rejectTime(
			"date conversion of coalesced value",
			"one",
			"SELECT date(coalesce('now', '2026-01-01'))",
		),
		{name: "custom temporal conversion", command: "one", sql: "SELECT audit.date('today')"},
		{name: "custom temporal type", command: "one", sql: "SELECT 'now'::audit.date"},
		{name: "custom now function", command: "one", sql: "SELECT audit.now()"},
		{name: "custom age function", command: "one", sql: "SELECT domain.age(created_at) FROM users"},
		rejectNowParameter("sqlc arg now", "one", "SELECT sqlc.arg(now)"),
		rejectNowParameter("sqlc nullable arg now", "one", "SELECT sqlc.narg(now)"),
		rejectNowParameter("sqlc quoted arg now", "one", "SELECT sqlc.arg('now')"),
		rejectNowParameter("sqlc quoted nullable arg now", "one", "SELECT sqlc.narg('now')"),
		rejectNowParameter("sqlc at now", "one", "SELECT @now"),
		{name: "semantic timestamp parameter", command: "one", sql: "SELECT sqlc.arg(observed_at)"},
		{name: "quoted semantic timestamp parameter", command: "one", sql: "SELECT sqlc.arg('observed_at')"},
		rejectApplicationTime(
			"update database time from renamed application parameter",
			"exec",
			"UPDATE jobs SET completed_at = sqlc.arg(completed_at)::timestamptz WHERE id = sqlc.arg(id)",
		),
		rejectApplicationTime(
			"update database time from positional parameter",
			"exec",
			"UPDATE jobs SET updated_at = $1::timestamptz WHERE id = $2",
		),
		rejectApplicationTime(
			"update database time from nullable application branch",
			"exec",
			"UPDATE jobs SET completed_at = coalesce(sqlc.narg(completed_at)::timestamptz, completed_at) WHERE id = sqlc.arg(id)",
		),
		rejectApplicationTime(
			"insert database time from application parameter",
			"exec",
			"INSERT INTO jobs (id, created_at) VALUES (sqlc.arg(id), sqlc.arg(created_at)::timestamptz)",
		),
		rejectApplicationTime(
			"insert select database time from application parameter",
			"exec",
			"INSERT INTO jobs (id, created_at) SELECT sqlc.arg(id), sqlc.arg(created_at)::timestamptz",
		),
		rejectApplicationTime(
			"upsert database time from application parameter",
			"exec",
			"INSERT INTO jobs (id, created_at) VALUES (sqlc.arg(id), transaction_timestamp()) ON CONFLICT (id) DO UPDATE SET updated_at = sqlc.arg(updated_at)::timestamptz",
		),
		{
			name:    "source evidence timestamp",
			command: "exec",
			sql:     "UPDATE jobs SET source_completed_at = sqlc.arg(source_completed_at)::timestamptz WHERE id = sqlc.arg(id)",
		},
		{
			name:    "database time derived from policy duration",
			command: "exec",
			sql: "UPDATE jobs SET expires_at = statement_timestamp() + " +
				"sqlc.arg(ttl_seconds)::int * interval '1 second' WHERE id = sqlc.arg(id)",
		},
		{
			name:    "nullable database time derived from policy duration",
			command: "exec",
			sql: "UPDATE jobs SET expires_at = CASE WHEN sqlc.arg(has_expiry)::boolean THEN " +
				"statement_timestamp() + make_interval(secs => sqlc.arg(ttl_seconds)::int) " +
				"END WHERE id = sqlc.arg(id)",
		},
		rejectBlockingTime(
			"blocking lock with statement time",
			"exec",
			"WITH locked AS (SELECT id FROM jobs WHERE id = sqlc.arg(id) FOR UPDATE) "+
				"UPDATE jobs SET updated_at = statement_timestamp() FROM locked WHERE jobs.id = locked.id",
		),
		rejectBlockingTime(
			"blocking lock with transaction time",
			"exec",
			"WITH locked AS (SELECT id FROM jobs WHERE id = sqlc.arg(id) FOR SHARE) "+
				"INSERT INTO job_events (job_id, created_at) SELECT id, transaction_timestamp() FROM locked",
		),
		{
			name:    "nonblocking skip locked with database time",
			command: "exec",
			sql: "WITH locked AS (SELECT id FROM jobs WHERE state = 'queued' FOR UPDATE SKIP LOCKED) " +
				"UPDATE jobs SET updated_at = statement_timestamp() FROM locked WHERE jobs.id = locked.id",
		},
		{
			name:    "nonblocking nowait with database time",
			command: "exec",
			sql: "WITH locked AS (SELECT id FROM jobs WHERE id = sqlc.arg(id) FOR UPDATE NOWAIT) " +
				"UPDATE jobs SET updated_at = statement_timestamp() FROM locked WHERE jobs.id = locked.id",
		},
		{
			name:    "blocking lock without database time",
			command: "exec",
			sql: "WITH locked AS (SELECT id FROM jobs WHERE id = sqlc.arg(id) FOR UPDATE) " +
				"UPDATE jobs SET state = 'done' FROM locked WHERE jobs.id = locked.id",
		},
		reject("update without predicate", "exec", "UPDATE users SET display_name = 'updated'", ruleMutationPredicate),
		reject("delete without predicate", "exec", "DELETE FROM users", ruleMutationPredicate),
		{name: "update with predicate", command: "exec", sql: "UPDATE users SET display_name = 'updated' WHERE id = 1"},
		{name: "delete with predicate", command: "exec", sql: "DELETE FROM users WHERE id = 1"},
		reject("implicit insert columns", "exec", "INSERT INTO users VALUES (1)", ruleExplicitInsertColumns),
		reject("implicit insert select columns", "exec", "INSERT INTO users SELECT 1", ruleExplicitInsertColumns),
		{name: "default values", command: "exec", sql: "INSERT INTO users DEFAULT VALUES"},
		{name: "explicit insert columns", command: "exec", sql: "INSERT INTO users (id) VALUES (1)"},
		reject("comma join", "many", "SELECT first.id FROM first, second", ruleExplicitJoins),
		reject("natural join", "many", "SELECT first.id FROM first NATURAL JOIN second", ruleExplicitJoins),
		{name: "cross join", command: "many", sql: "SELECT first.id FROM first CROSS JOIN second"},
		{name: "join on", command: "many", sql: "SELECT first.id FROM first JOIN second ON second.id = first.id"},
		{name: "join using", command: "many", sql: "SELECT first.id FROM first JOIN second USING (id)"},
		reject(
			"unaliased relation function",
			"many",
			"SELECT json_each FROM json_each('{\"a\": 1}'::json)",
			ruleExplicitDerivedAliases,
		),
		{
			name:    "aliased record relation function",
			command: "many",
			sql:     "SELECT entry.key FROM json_each('{\"a\": 1}'::json) AS entry(key, value)",
		},
		reject(
			"unaliased scalar relation function",
			"many",
			"SELECT generate_series FROM generate_series(1, 10)",
			ruleExplicitDerivedAliases,
		),
		{
			name:    "aliased scalar relation function",
			command: "many",
			sql:     "SELECT series.value FROM generate_series(1, 10) AS series(value)",
		},
		reject(
			"unaliased rows from",
			"many",
			"SELECT value FROM ROWS FROM (generate_series(1, 10))",
			ruleExplicitDerivedAliases,
		),
		{
			name:    "aliased rows from",
			command: "many",
			sql:     "SELECT series.value FROM ROWS FROM (generate_series(1, 10)) AS series(value)",
		},
		reject(
			"unaliased XML table",
			"many",
			"SELECT id FROM XMLTABLE('/rows/row' PASSING '<rows><row id=\"1\"/></rows>' COLUMNS id int PATH '@id')",
			ruleExplicitDerivedAliases,
		),
		{
			name:    "aliased XML table",
			command: "many",
			sql: "SELECT rows.id FROM XMLTABLE('/rows/row' PASSING '<rows><row id=\"1\"/></rows>' " +
				"COLUMNS id int PATH '@id') AS rows",
		},
		reject(
			"unaliased JSON table",
			"many",
			"SELECT id FROM JSON_TABLE('{\"id\": 1}'::jsonb, '$' COLUMNS (id int PATH '$.id'))",
			ruleExplicitDerivedAliases,
		),
		{
			name:    "aliased JSON table",
			command: "many",
			sql: "SELECT rows.id FROM JSON_TABLE('{\"id\": 1}'::jsonb, '$' " +
				"COLUMNS (id int PATH '$.id')) AS rows",
		},
		reject(
			"unaliased derived subquery",
			"one",
			"SELECT value FROM (SELECT 1 AS value)",
			ruleExplicitDerivedAliases,
		),
		{name: "aliased derived subquery", command: "one", sql: "SELECT derived.value FROM (SELECT 1 AS value) AS derived"},
		reject("unordered limited collection", "many", "SELECT id FROM users LIMIT 10", ruleLimitedCollectionOrder),
		{name: "ordered limited collection", command: "many", sql: "SELECT id FROM users ORDER BY id LIMIT 10"},
		reject(
			"unordered limited batch collection",
			"batchmany",
			"SELECT id FROM users WHERE org_id = ANY($1::uuid[]) LIMIT 10",
			ruleLimitedCollectionOrder,
		),
		{
			name:    "ordered limited batch collection",
			command: "batchmany",
			sql:     "SELECT id FROM users WHERE org_id = ANY($1::uuid[]) ORDER BY id LIMIT 10",
		},
		{name: "limited scalar", command: "one", sql: "SELECT id FROM users LIMIT 1"},
		{
			name:    "nested scalar limit",
			command: "many",
			sql:     "SELECT id FROM users WHERE EXISTS (SELECT 1 FROM projects LIMIT 1)",
		},
		reject("offset pagination", "many", "SELECT id FROM users ORDER BY id OFFSET 10", ruleNoOffsetPagination),
		reject("utility statement", "exec", "TRUNCATE users", ruleProductDMLOnly),
		reject("select into", "exec", "SELECT id INTO archived_users FROM users", ruleProductDMLOnly),
		{
			name:    "cte select",
			command: "many",
			sql:     "WITH selected AS (SELECT id FROM users) SELECT id FROM selected",
		},
		{
			name:    "multiple statements cannot bypass collection ordering",
			command: "many",
			sql:     "SELECT id FROM users LIMIT 1; SELECT id FROM users",
			rules:   []string{ruleLimitedCollectionOrder, ruleNamedQueryRequired},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := fmt.Appendf(nil, "-- name: Fixture :%s\n%s;\n", test.command, test.sql)
			issues := checkSource("fixture.sql", source)
			gotRules := make([]string, 0, len(issues))
			for _, issue := range issues {
				gotRules = append(gotRules, issue.Rule)
				if issue.Path != "fixture.sql" || issue.Line < 2 || issue.Column < 1 {
					t.Errorf("non-actionable diagnostic: %+v", issue)
				}
			}
			sort.Strings(gotRules)
			wantRules := append([]string(nil), test.rules...)
			sort.Strings(wantRules)
			if strings.Join(gotRules, ",") != strings.Join(wantRules, ",") {
				t.Fatalf("rules = %v, want %v; issues: %+v", gotRules, wantRules, issues)
			}
		})
	}
}

func TestBlockQueryAnnotation(t *testing.T) {
	issues := checkSource("fixture.sql", []byte("/* name: Unsafe :many */\nSELECT * FROM users;\n"))
	if len(issues) != 1 || issues[0].Rule != ruleExplicitOutputColumns {
		t.Fatalf("issues = %+v, want %s", issues, ruleExplicitOutputColumns)
	}
}

func TestUnicodeQueryAnnotation(t *testing.T) {
	issues := checkSource("fixture.sql", []byte("-- name: Caf\u00e9 :many\nSELECT id FROM users LIMIT 1;\n"))
	if len(issues) != 1 || issues[0].Rule != ruleLimitedCollectionOrder {
		t.Fatalf("issues = %+v, want %s", issues, ruleLimitedCollectionOrder)
	}
}

func TestIndentedQueryAnnotationIsNotAnOwner(t *testing.T) {
	issues := checkSource("fixture.sql", []byte("  -- name: Indented :one\nSELECT 1;\n"))
	if len(issues) != 1 || issues[0].Rule != ruleNamedQueryRequired || issues[0].Line != 2 {
		t.Fatalf("issues = %+v, want a line 2 %s violation", issues, ruleNamedQueryRequired)
	}
}

func TestAnnotationTextInsideDollarStringCannotDisagreeWithSQLC(t *testing.T) {
	issues := checkSource("fixture.sql", []byte("SELECT $$\n-- name: Fake :one\n$$;\n"))
	if len(issues) != 1 || issues[0].Rule != ruleQueryOwnership || issues[0].Line != 2 {
		t.Fatalf("issues = %+v, want a line 2 %s violation", issues, ruleQueryOwnership)
	}
}

func TestNestedLeadingBlockComment(t *testing.T) {
	source := []byte("/* outer\n  /* nested */\n*/\n-- name: Real :one\nSELECT 1;\n")
	if issues := checkSource("fixture.sql", source); len(issues) != 0 {
		t.Fatalf("issues = %+v, want none", issues)
	}
}

func TestAnnotationTextInsideNamedQueryBodyDoesNotSplitOwnership(t *testing.T) {
	source := []byte("-- name: Real :one\nSELECT $$\n-- name: Fake :one\n$$;\n")
	if issues := checkSource("fixture.sql", source); len(issues) != 0 {
		t.Fatalf("issues = %+v, want none", issues)
	}
}

func TestCheckerInspectsSQLBeforeFirstAnnotation(t *testing.T) {
	source := []byte("SELECT perform_some_work();\n-- name: Safe :many\nSELECT id FROM users;\n")
	issues := checkSource("fixture.sql", source)
	if len(issues) != 1 || issues[0].Rule != ruleNamedQueryRequired || issues[0].Line != 1 {
		t.Fatalf("issues = %+v, want a line 1 %s violation", issues, ruleNamedQueryRequired)
	}
}

func TestCheckerRejectsFileContainingOnlyUnnamedSQL(t *testing.T) {
	issues := checkSource("fixture.sql", []byte("SELECT perform_some_work();\n"))
	if len(issues) != 1 || issues[0].Rule != ruleNamedQueryRequired || issues[0].Line != 1 {
		t.Fatalf("issues = %+v, want a line 1 %s violation", issues, ruleNamedQueryRequired)
	}
}

func TestCheckerAllowsCommentOnlyPreamble(t *testing.T) {
	issues := checkSource("fixture.sql", []byte("-- product query definitions\n\n-- name: Safe :one\nSELECT 1;\n"))
	if len(issues) != 0 {
		t.Fatalf("issues = %+v, want none", issues)
	}
}

func TestParseErrorDiagnostic(t *testing.T) {
	issues := checkSource("fixture.sql", []byte("-- name: Invalid :one\nSELEC 1;\n"))
	if len(issues) != 1 || issues[0].Rule != ruleParseError || issues[0].Line != 2 || issues[0].Column < 1 {
		t.Fatalf("issues = %+v, want an actionable line 2 %s violation", issues, ruleParseError)
	}
}

func TestSecondStatementRequiresItsOwnQueryAnnotation(t *testing.T) {
	source := []byte("-- name: Multiple :many\nSELECT id FROM users ORDER BY id LIMIT 1;\nSELECT id FROM users;\n")
	issues := checkSource("fixture.sql", source)
	if len(issues) != 1 || issues[0].Rule != ruleNamedQueryRequired || issues[0].Line != 3 || issues[0].Column != 1 {
		t.Fatalf("issues = %+v, want a line 3:1 %s violation", issues, ruleNamedQueryRequired)
	}
}

func TestProductionQueryCorpus(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	queryDirectory := filepath.Join(filepath.Dir(filename), "..", "..", "..", "internal", "storage", "queries")
	issues, err := checkDirectory(queryDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Fatalf("production query violations: %+v", issues)
	}
}

func TestRunRejectsInvalidQueryDirectory(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "invalid.sql")
	source := "-- name: UnsafeProjection :many\nSELECT *\nFROM users;\n" +
		"-- name: UnsafeTimestampedMutation :exec\n" +
		"WITH locked AS (SELECT id FROM jobs WHERE id = 1 FOR UPDATE)\n" +
		"UPDATE jobs SET updated_at = statement_timestamp() FROM locked WHERE jobs.id = locked.id;\n"
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{directory}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	want := "invalid.sql:2:8: " + ruleExplicitOutputColumns
	if !strings.Contains(stdout.String(), want) {
		t.Fatalf("stdout = %q, want diagnostic containing %q", stdout.String(), want)
	}
	if !strings.Contains(stdout.String(), ruleBlockingLockWithTime) {
		t.Fatalf("stdout = %q, want %s diagnostic", stdout.String(), ruleBlockingLockWithTime)
	}
}

func TestRunRejectsRelativeTimeLiterals(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "invalid.sql")
	source := "-- name: NestedNow :one\nSELECT ('now'::text)::timestamptz;\n" +
		"-- name: NestedToday :one\nSELECT CAST(CAST('today' AS text) AS date);\n" +
		"-- name: ConvertedTomorrow :one\nSELECT date('tomorrow');\n" +
		"-- name: CaseNow :one\nSELECT date(CASE WHEN enabled THEN 'now' ELSE '2026-01-01' END);\n" +
		"-- name: CoalescedToday :one\nSELECT date(coalesce('today', '2026-01-01'));\n"
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{directory}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if count := strings.Count(stdout.String(), ruleExplicitDurableTime); count != 5 {
		t.Fatalf("stdout = %q, want five %s diagnostics", stdout.String(), ruleExplicitDurableTime)
	}
}

func TestRunRejectsSQLCOwnershipDisagreement(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "invalid.sql")
	source := "/* context\n-- name: Fake :many\n*/\n-- name: Real :one\nSELECT id FROM users LIMIT 1;\n"
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{directory}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	want := "invalid.sql:2:1: " + ruleQueryOwnership
	if !strings.Contains(stdout.String(), want) {
		t.Fatalf("stdout = %q, want diagnostic containing %q", stdout.String(), want)
	}
}

func TestRunRejectsEmptyQueryDirectory(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{t.TempDir()}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "no SQL query files found") {
		t.Fatalf("stderr = %q, want missing-query diagnostic", stderr.String())
	}
}
