//go:build integration && servicee2e

package e2e

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/integrationlock"
	"github.com/omnara-ai/omnara/internal/testutil/integrationredis"
)

type serviceE2EEnvironment struct {
	seed                  string
	root                  string
	repoRoot              string
	apiURL                string
	publicURL             string
	publicURLHost         string
	apiListenAddr         string
	apiMetricsURL         string
	apiMetricsListenAddr  string
	workerURL             string
	workerListenAddr      string
	maintenanceURL        string
	maintenanceListenAddr string
	containerAPIURL       string
	baseDatabaseURL       string
	databaseURL           string
	databaseSchema        string
	redisURL              string
	db                    *pgxpool.Pool
}

func newServiceE2EEnvironment(t *testing.T, ctx context.Context, seed string) *serviceE2EEnvironment {
	return newServiceE2EEnvironmentWithOptions(t, ctx, seed, true)
}

func newDaemonOnlyServiceE2EEnvironment(t *testing.T, ctx context.Context, seed string) *serviceE2EEnvironment {
	return newServiceE2EEnvironmentWithOptions(t, ctx, seed, false)
}

func newServiceE2EEnvironmentWithOptions(
	t *testing.T,
	ctx context.Context,
	seed string,
	requireDocker bool,
) *serviceE2EEnvironment {
	t.Helper()
	integrationlock.Acquire(t)
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	uniqueSeed := fmt.Sprintf("%s-%d", seed, time.Now().UnixNano())
	root, err := os.MkdirTemp("/tmp", "omnara-service-e2e-"+uniqueSeed+"-")
	if err != nil {
		t.Fatalf("create service e2e root: %v", err)
	}
	if requireDocker {
		output, err := exec.CommandContext(ctx, "docker", "version").CombinedOutput()
		if err != nil {
			detail := strings.TrimSpace(string(output))
			if detail != "" {
				err = fmt.Errorf("%w: %s", err, detail)
			}
			if os.Getenv("OMNARA_REQUIRE_SERVICE_E2E") == "1" {
				t.Fatalf("docker is required for service E2E: %v", err)
			}
			t.Skipf("docker unavailable: %v", err)
		}
	}
	databaseURL := os.Getenv("OMNARA_TEST_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://omnara:omnara@127.0.0.1:55432/omnara?sslmode=disable"
	}
	redisURL := integrationredis.URL(t)
	integrationdb.AssertTestDatabaseURL(t, databaseURL)
	schema := serviceE2ESchemaName(uniqueSeed)
	adminDB, err := storage.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open integration db: %v", err)
	}
	if _, err := adminDB.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE; CREATE SCHEMA `+schema+`;`); err != nil {
		adminDB.Close()
		t.Fatalf("reset integration schema: %v", err)
	}
	var extensionSchema string
	if err := adminDB.QueryRow(ctx, `
SELECT coalesce((
    SELECT namespace.nspname
    FROM pg_extension extension
    JOIN pg_namespace namespace ON namespace.oid = extension.extnamespace
    WHERE extension.extname = 'pg_trgm'
), '')`).Scan(&extensionSchema); err != nil {
		adminDB.Close()
		t.Fatalf("load integration extension schema: %v", err)
	}
	adminDB.Close()
	searchPath := schema
	if extensionSchema != "" && extensionSchema != schema {
		searchPath += "," + extensionSchema
	}
	schemaDatabaseURL := databaseURLWithSearchPath(t, databaseURL, searchPath)
	db, err := storage.Open(ctx, schemaDatabaseURL)
	if err != nil {
		t.Fatalf("open schema integration db: %v", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		db.Close()
		t.Fatalf("create service e2e root directory: %v", err)
	}
	ports := freePorts(t, 3)
	apiPort := ports[0]
	apiMetricsPort := ports[1]
	maintenancePort := ports[2]
	containerPublicURL := "http://host.docker.internal:" + apiPort
	containerAPIURL := containerPublicURL + "/api/v1"
	env := &serviceE2EEnvironment{
		seed:                  uniqueSeed,
		root:                  root,
		repoRoot:              repoRoot,
		apiURL:                "http://127.0.0.1:" + apiPort,
		publicURL:             containerPublicURL,
		publicURLHost:         "host.docker.internal:" + apiPort,
		apiListenAddr:         "0.0.0.0:" + apiPort,
		apiMetricsURL:         "http://127.0.0.1:" + apiMetricsPort,
		apiMetricsListenAddr:  "0.0.0.0:" + apiMetricsPort,
		maintenanceURL:        "http://127.0.0.1:" + maintenancePort,
		maintenanceListenAddr: "0.0.0.0:" + maintenancePort,
		containerAPIURL:       containerAPIURL,
		baseDatabaseURL:       databaseURL,
		databaseURL:           schemaDatabaseURL,
		databaseSchema:        schema,
		redisURL:              redisURL,
		db:                    db,
	}
	t.Cleanup(env.cleanup)
	env.runMigrations(t, ctx)
	return env
}

func (e *serviceE2EEnvironment) cleanup() {
	if e.db != nil {
		e.db.Close()
	}
	if e.baseDatabaseURL != "" && e.databaseSchema != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if db, err := storage.Open(ctx, e.baseDatabaseURL); err == nil {
			_, _ = db.Exec(ctx, `DROP SCHEMA IF EXISTS `+e.databaseSchema+` CASCADE;`)
			db.Close()
		}
	}
	if e.root != "" {
		_ = os.RemoveAll(e.root)
	}
}

func serviceE2ESchemaName(seed string) string {
	re := regexp.MustCompile(`[^a-zA-Z0-9_]`)
	name := strings.ToLower(re.ReplaceAllString(seed, "_"))
	if len(name) > 48 {
		name = name[:48]
	}
	return "e2e_" + name
}

func databaseURLWithSearchPath(t *testing.T, databaseURL, schema string) string {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
