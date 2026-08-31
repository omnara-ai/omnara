package integrationdb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/multitracer"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/dbmigrate"
	schemamigrations "github.com/omnara-ai/omnara/migrations"
)

const (
	databaseURLEnv        = "OMNARA_TEST_DATABASE_URL"
	databaseRunIDEnv      = "OMNARA_TEST_DATABASE_RUN_ID"
	cleanOlderThanEnv     = "OMNARA_TEST_DATABASE_CLEAN_OLDER_THAN"
	cleanDatabaseRunIDEnv = "OMNARA_TEST_DATABASE_CLEAN_RUN_ID"
)

var (
	testDatabaseCounter uint64
	testDatabaseRun     = initTestDatabaseRun()
	templateDatabaseMu  sync.Mutex
	templateDatabase    string
	templateDatabaseKey string
	generatedDBOpened   atomic.Bool
)

type databaseRun struct {
	startedAt string
	token     string
}

type acquireTimeoutContextKey struct{}

type acquireTimeoutTracer struct {
	timeout time.Duration
}

func OpenMigratedPool(
	t testing.TB,
	ctx context.Context,
	migrationsDir string,
) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv(databaseURLEnv)
	if dsn == "" {
		t.Skip(databaseURLEnv + " is not set")
	}
	AssertTestDatabaseURL(t, dsn)
	generatedDBOpened.Store(true)

	templateName := migratedTemplateDatabase(t, ctx, dsn, migrationsDir)
	return openGeneratedPool(t, ctx, dsn, templateName)
}

func OpenUnmigratedPool(
	t testing.TB,
	ctx context.Context,
) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv(databaseURLEnv)
	if dsn == "" {
		t.Skip(databaseURLEnv + " is not set")
	}
	AssertTestDatabaseURL(t, dsn)
	generatedDBOpened.Store(true)

	return openGeneratedPool(t, ctx, dsn, "")
}

func openGeneratedPool(
	t testing.TB,
	ctx context.Context,
	dsn, templateName string,
) *pgxpool.Pool {
	t.Helper()

	databaseName := nextTestDatabaseName()
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		dropTestDatabase(t, ctx, dsn, databaseName)
	})

	if templateName == "" {
		createTestDatabase(t, ctx, dsn, databaseName)
	} else {
		createTestDatabaseFromTemplate(t, ctx, dsn, databaseName, templateName)
	}
	pool = openPoolForDatabase(t, ctx, dsn, databaseName)
	return pool
}

func openPoolForDatabase(t testing.TB, ctx context.Context, dsn, databaseName string) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse integration database url: %v", err)
	}
	cfg.ConnConfig.Database = databaseName
	cfg.MaxConns = 5
	cfg.MinConns = 0
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.ConnConfig.Tracer = appendAcquireTimeoutTracer(cfg.ConnConfig.Tracer, 5*time.Second)

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("open integration database pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping integration database: %v", err)
	}
	return pool
}

func appendAcquireTimeoutTracer(tracer pgx.QueryTracer, timeout time.Duration) pgx.QueryTracer {
	timeoutTracer := acquireTimeoutTracer{timeout: timeout}
	if tracer == nil {
		return timeoutTracer
	}
	return multitracer.New(tracer, timeoutTracer)
}

func (t acquireTimeoutTracer) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	_ pgx.TraceQueryStartData,
) context.Context {
	return ctx
}

func (t acquireTimeoutTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (t acquireTimeoutTracer) TraceAcquireStart(
	ctx context.Context,
	_ *pgxpool.Pool,
	_ pgxpool.TraceAcquireStartData,
) context.Context {
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	return context.WithValue(ctx, acquireTimeoutContextKey{}, cancel)
}

func (t acquireTimeoutTracer) TraceAcquireEnd(
	ctx context.Context,
	_ *pgxpool.Pool,
	_ pgxpool.TraceAcquireEndData,
) {
	cancel, ok := ctx.Value(acquireTimeoutContextKey{}).(context.CancelFunc)
	if ok {
		cancel()
	}
}

func migratedTemplateDatabase(
	t testing.TB,
	ctx context.Context,
	dsn, migrationsDir string,
) string {
	t.Helper()
	templateKey, err := filepath.Abs(migrationsDir)
	if err != nil {
		t.Fatalf("resolve integration migrations dir: %v", err)
	}
	templateDatabaseMu.Lock()
	defer templateDatabaseMu.Unlock()
	if templateDatabase != "" {
		if templateDatabaseKey != templateKey {
			t.Fatalf(
				"integration test process already initialized migrated template %q, got %q",
				templateDatabaseKey,
				templateKey,
			)
		}
		return templateDatabase
	}

	databaseName := nextTemplateDatabaseName()
	createTestDatabase(t, ctx, dsn, databaseName)
	pool := openPoolForDatabase(t, ctx, dsn, databaseName)
	db := stdlib.OpenDBFromPool(pool)
	err = dbmigrate.ApplyPostgres(
		ctx,
		db,
		os.DirFS(migrationsDir),
		schemamigrations.GoMigrations()...,
	)
	_ = db.Close()
	if err != nil {
		pool.Close()
		dropTestDatabase(t, ctx, dsn, databaseName)
		t.Fatalf("migrate integration template database %s: %v", databaseName, err)
	}
	pool.Close()
	setDatabaseAllowConnections(t, ctx, dsn, databaseName, false)
	templateDatabase = databaseName
	templateDatabaseKey = templateKey
	return databaseName
}

func nextTemplateDatabaseName() string {
	return fmt.Sprintf(
		"%s%s_%s_%d_template",
		generatedDatabasePrefix,
		testDatabaseRun.startedAt,
		testDatabaseRun.token,
		os.Getpid(),
	)
}

func initTestDatabaseRun() databaseRun {
	startedAt := fmt.Sprintf("%x", time.Now().UnixNano())
	runID := sanitizeRunID(os.Getenv(databaseRunIDEnv))
	if runID != "" {
		return databaseRun{startedAt: startedAt, token: runID}
	}
	return databaseRun{startedAt: startedAt, token: startedAt}
}

func sanitizeRunID(runID string) string {
	if runID == "" {
		return ""
	}
	lower := strings.ToLower(runID)
	if len(lower) <= 16 && isHexRunID(lower) {
		return lower
	}
	return ""
}

func isHexRunID(runID string) bool {
	for _, r := range runID {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func nextTestDatabaseName() string {
	return fmt.Sprintf(
		"%s%s_%s_%d_%d",
		generatedDatabasePrefix,
		testDatabaseRun.startedAt,
		testDatabaseRun.token,
		os.Getpid(),
		atomic.AddUint64(&testDatabaseCounter, 1),
	)
}

func createTestDatabase(t testing.TB, ctx context.Context, dsn, databaseName string) {
	t.Helper()
	conn := openAdminConn(t, ctx, dsn)
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{databaseName}.Sanitize()); err != nil {
		t.Fatalf("create integration database %s: %v", databaseName, err)
	}
}

func createTestDatabaseFromTemplate(t testing.TB, ctx context.Context, dsn, databaseName, templateName string) {
	t.Helper()
	conn := openAdminConn(t, ctx, dsn)
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(
		ctx,
		"CREATE DATABASE "+pgx.Identifier{databaseName}.Sanitize()+" TEMPLATE "+pgx.Identifier{templateName}.Sanitize(),
	); err != nil {
		t.Fatalf("create integration database %s from template %s: %v", databaseName, templateName, err)
	}
}

func setDatabaseAllowConnections(t testing.TB, ctx context.Context, dsn, databaseName string, allow bool) {
	t.Helper()
	conn := openAdminConn(t, ctx, dsn)
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(
		ctx,
		"ALTER DATABASE "+pgx.Identifier{databaseName}.Sanitize()+" WITH ALLOW_CONNECTIONS "+strconv.FormatBool(allow),
	); err != nil {
		t.Fatalf("set integration database %s allow_connections=%t: %v", databaseName, allow, err)
	}
}

func dropTestDatabase(t testing.TB, parentCtx context.Context, dsn, databaseName string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parentCtx), 30*time.Second)
	defer cancel()
	conn := openAdminConn(t, ctx, dsn)
	defer func() { _ = conn.Close(ctx) }()
	query := "DROP DATABASE IF EXISTS " + pgx.Identifier{databaseName}.Sanitize() + " WITH (FORCE)"
	if _, err := conn.Exec(ctx, query); err != nil {
		t.Errorf("drop integration database %s: %v", databaseName, err)
	}
}

func dropStaleGeneratedDatabases(
	t testing.TB,
	ctx context.Context,
	dsn string,
	olderThan time.Duration,
	runID string,
) []string {
	t.Helper()

	now := time.Now()
	cleanRunID := sanitizeRunID(runID)
	if runID != "" && cleanRunID == "" {
		t.Fatalf("refusing to clean generated integration databases with empty sanitized run id")
	}
	dropped, err := dropGeneratedDatabases(ctx, dsn, runID != "", func(databaseName string) bool {
		if runID != "" {
			return generatedDatabaseHasRunID(databaseName, cleanRunID)
		}
		return generatedDatabaseIsOlderThan(databaseName, now, olderThan) && !generatedDatabaseProcessIsRunning(databaseName)
	})
	if err != nil {
		t.Fatalf("drop generated integration databases: %v", err)
	}
	return dropped
}

func dropGeneratedDatabases(
	ctx context.Context,
	dsn string,
	includeActive bool,
	shouldDrop func(string) bool,
) ([]string, error) {
	if err := validateTestDatabaseURL(dsn); err != nil {
		return nil, err
	}
	conn, err := openAdminConnFromDSN(ctx, dsn)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close(ctx) }()
	rows, err := conn.Query(
		ctx,
		`SELECT d.datname
FROM pg_database d
WHERE d.datname LIKE $1
  AND ($2 OR NOT EXISTS (
    SELECT 1
    FROM pg_stat_activity a
    WHERE a.datname = d.datname
  ))
ORDER BY d.datname`,
		generatedDatabasePrefix+"%",
		includeActive,
	)
	if err != nil {
		return nil, fmt.Errorf("list generated integration databases: %w", err)
	}

	var candidates []string
	for rows.Next() {
		var databaseName string
		if err := rows.Scan(&databaseName); err != nil {
			return nil, fmt.Errorf("scan generated integration database: %w", err)
		}
		if shouldDrop(databaseName) {
			candidates = append(candidates, databaseName)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate generated integration databases: %w", err)
	}
	rows.Close()

	var dropped []string
	for _, databaseName := range candidates {
		query := "DROP DATABASE IF EXISTS " + pgx.Identifier{databaseName}.Sanitize() + " WITH (FORCE)"
		if _, err := conn.Exec(ctx, query); err != nil {
			return nil, fmt.Errorf("drop generated integration database %s: %w", databaseName, err)
		}
		dropped = append(dropped, databaseName)
	}
	return dropped, nil
}

func generatedDatabaseIsOlderThan(databaseName string, now time.Time, olderThan time.Duration) bool {
	rest, ok := strings.CutPrefix(databaseName, generatedDatabasePrefix)
	if !ok {
		return false
	}
	startedAt, _, ok := strings.Cut(rest, "_")
	if !ok {
		return false
	}
	nanos, err := strconv.ParseInt(startedAt, 16, 64)
	if err != nil {
		return false
	}
	return now.Sub(time.Unix(0, nanos)) >= olderThan
}

func generatedDatabaseHasRunID(databaseName, runID string) bool {
	parts, ok := generatedDatabaseParts(databaseName)
	if !ok {
		return false
	}
	return parts[1] == runID
}

func generatedDatabaseHasRunIDAndPID(databaseName, runID string, pid int) bool {
	if !generatedDatabaseHasRunID(databaseName, runID) {
		return false
	}
	foundPID, ok := generatedDatabasePID(databaseName)
	return ok && foundPID == pid
}

func generatedDatabasePID(databaseName string) (int, bool) {
	parts, ok := generatedDatabaseParts(databaseName)
	if !ok {
		return 0, false
	}
	pid, err := strconv.Atoi(parts[2])
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

func generatedDatabaseParts(databaseName string) ([]string, bool) {
	rest, ok := strings.CutPrefix(databaseName, generatedDatabasePrefix)
	if !ok {
		return nil, false
	}
	parts := strings.Split(rest, "_")
	if len(parts) != 4 {
		return nil, false
	}
	return parts, true
}

func generatedDatabaseProcessIsRunning(databaseName string) bool {
	pid, ok := generatedDatabasePID(databaseName)
	return ok && processIsRunning(pid)
}

func processIsRunning(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return err == syscall.EPERM
	}
	return true
}

func openAdminConn(t testing.TB, ctx context.Context, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := openAdminConnFromDSN(ctx, dsn)
	if err != nil {
		t.Fatalf("connect integration database admin connection: %v", err)
	}
	return conn
}

func openAdminConnFromDSN(ctx context.Context, dsn string) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse integration database admin url: %w", err)
	}
	cfg.Database = "postgres"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect integration database admin connection: %w", err)
	}
	return conn, nil
}

func RunTestMain(m *testing.M) {
	code := m.Run()
	dsn := os.Getenv(databaseURLEnv)
	if dsn != "" && generatedDBOpened.Load() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err := dropGeneratedDatabases(ctx, dsn, true, func(databaseName string) bool {
			return generatedDatabaseHasRunIDAndPID(databaseName, testDatabaseRun.token, os.Getpid())
		})
		cancel()
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "cleanup generated integration databases: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}
	os.Exit(code)
}
