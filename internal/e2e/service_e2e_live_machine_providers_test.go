//go:build integration && servicee2e && live

package e2e

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

const liveMachineDaemonVersion = "999.0.0"

type liveMachineProvider struct {
	name         string
	env          []string
	prepareStack func(t *testing.T, keys []string) error
	pool         func(keys []string) (credential string, pool map[string]any)
}

type liveMachineStack struct {
	env         *serviceE2EEnvironment
	worker      serviceProcess
	maintenance serviceProcess
}

func liveMachineProviders() []liveMachineProvider {
	return []liveMachineProvider{
		{name: "arker", env: []string{"ARKER_API_KEY"}, pool: func(keys []string) (string, map[string]any) {
			return keys[0], map[string]any{
				"default_machine_provider_options": map[string]any{"source": "ubuntu-base", "region": "aws-us-west-2"},
				"default_machine_cpu":              2,
				"default_machine_memory_mb":        4096,
				"max_machine_cpu":                  2,
				"max_machine_memory_mb":            4096,
				"max_total_cpu":                    2,
				"max_total_memory_mb":              4096,
			}
		}},
		{name: "blaxel", env: []string{"BL_API_KEY", "BL_WORKSPACE"}, pool: func(keys []string) (string, map[string]any) {
			return keys[0], map[string]any{
				"provider_config": map[string]any{"workspace": keys[1]},
				"default_machine_provider_options": map[string]any{
					"image": "blaxel/base-image:latest", "region": "us-pdx-1", "startup_script": "apk add --no-cache curl",
				},
				"default_machine_memory_mb": 4096,
				"max_machine_memory_mb":     4096,
				"max_total_memory_mb":       4096,
			}
		}},
		{name: "daytona", env: []string{"DAYTONA_API_KEY"}, pool: func(keys []string) (string, map[string]any) {
			return keys[0], map[string]any{
				"default_machine_provider_options": map[string]any{"snapshot": "daytona-small", "target": "us"},
				"max_machine_cpu":                  4,
				"max_machine_memory_mb":            8192,
				"max_total_cpu":                    4,
				"max_total_memory_mb":              8192,
			}
		}},
		{
			name:         "docker",
			env:          []string{"OMNARA_DOCKER_TEST_HOST", "OMNARA_DOCKER_TEST_CREDENTIAL"},
			prepareStack: allowLiveDockerHost,
			pool: func(keys []string) (string, map[string]any) {
				return keys[1], map[string]any{
					"provider_config":                  map[string]any{"api_base_url": keys[0]},
					"default_machine_provider_options": map[string]any{"image": "buildpack-deps:24.04-curl"},
					"default_machine_cpu":              2,
					"default_machine_memory_mb":        2048,
					"max_machine_cpu":                  2,
					"max_machine_memory_mb":            2048,
					"max_total_cpu":                    2,
					"max_total_memory_mb":              2048,
				}
			},
		},
		{name: "e2b", env: []string{"E2B_API_KEY"}, pool: func(keys []string) (string, map[string]any) {
			return keys[0], map[string]any{
				"default_machine_provider_options": map[string]any{"template": "base"},
				"max_machine_cpu":                  4,
				"max_machine_memory_mb":            8192,
				"max_total_cpu":                    4,
				"max_total_memory_mb":              8192,
			}
		}},
		{name: "modal", env: []string{"MODAL_TOKEN_ID", "MODAL_TOKEN_SECRET"}, pool: func(
			keys []string,
		) (string, map[string]any) {
			return string(mustJSON(map[string]string{"token_id": keys[0], "token_secret": keys[1]})), map[string]any{
				"provider_config":                  map[string]any{"app": "omnara-live-tests"},
				"default_machine_provider_options": map[string]any{"image": "buildpack-deps:24.04-curl"},
				"default_machine_cpu":              2,
				"default_machine_memory_mb":        2048,
				"max_machine_cpu":                  2,
				"max_machine_memory_mb":            2048,
				"max_total_cpu":                    2,
				"max_total_memory_mb":              2048,
			}
		}},
		{name: "superserve", env: []string{"SUPERSERVE_API_KEY"}, pool: func(keys []string) (string, map[string]any) {
			return keys[0], map[string]any{
				"default_machine_provider_options": map[string]any{"template": "superserve/base"},
				"max_machine_cpu":                  4,
				"max_machine_memory_mb":            8192,
				"max_total_cpu":                    4,
				"max_total_memory_mb":              8192,
			}
		}},
	}
}

//nolint:tparallel // providers share one stack whose setup sets process env, so only the subtests run in parallel
func TestServiceE2ELiveMachineProviders(t *testing.T) {
	providers := liveMachineProviders()
	if !slices.ContainsFunc(providers, func(provider liveMachineProvider) bool {
		_, ok := liveMachineProviderKeys(provider.env)
		return ok
	}) {
		t.Skip("no machine provider credentials are configured")
	}
	prepareErrs := make(map[string]error)
	for _, provider := range providers {
		if keys, ok := liveMachineProviderKeys(provider.env); ok && provider.prepareStack != nil {
			prepareErrs[provider.name] = provider.prepareStack(t, keys)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	t.Cleanup(cancel)
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "live-machines")
	exposeLiveMachineAPI(t, ctx, env)
	env.startAPI(t, ctx)
	stack := liveMachineStack{
		env:         env,
		worker:      env.startWorker(t, ctx, "", serviceWorkerOptions{}),
		maintenance: env.startMaintenance(t, ctx),
	}
	requireLiveMachineTunnelOnlyServesDaemonRoutes(t, ctx, env)
	for _, provider := range providers {
		t.Run(provider.name, func(t *testing.T) {
			t.Parallel()
			keys, ok := liveMachineProviderKeys(provider.env)
			if !ok {
				t.Skipf("needs %s", strings.Join(provider.env, " and "))
			}
			require.NoError(t, prepareErrs[provider.name], "prepare the stack for %s", provider.name)
			credential, pool := provider.pool(keys)
			runLiveMachineProviderJourney(t, ctx, stack, provider.name, credential, pool)
		})
	}
}

func allowLiveDockerHost(t *testing.T, keys []string) error {
	apiBaseURL, err := url.Parse(keys[0])
	if err != nil {
		return err
	}
	port := apiBaseURL.Port()
	if port == "" {
		port = "443"
	}
	addresses, err := net.DefaultResolver.LookupHost(t.Context(), apiBaseURL.Hostname())
	if err != nil {
		return err
	}
	cidrs := []string{os.Getenv("OMNARA_PRIVATE_ENDPOINT_CIDRS")}
	for _, address := range addresses {
		cidrs = append(cidrs, net.JoinHostPort(address, port))
	}
	t.Setenv("OMNARA_PRIVATE_ENDPOINT_CIDRS", strings.Join(cidrs, ","))
	t.Setenv("OMNARA_DOCKER_POOLS_ENABLED", "1")
	return nil
}

func liveMachineProviderKeys(names []string) ([]string, bool) {
	values := make([]string, len(names))
	for i, name := range names {
		values[i] = strings.TrimSpace(os.Getenv(name))
		if values[i] == "" {
			return nil, false
		}
	}
	return values, true
}

func runLiveMachineProviderJourney(
	t *testing.T, ctx context.Context, stack liveMachineStack, provider, credential string, pool map[string]any,
) {
	env, worker, maintenance := stack.env, stack.worker, stack.maintenance
	seed := "live-machine-" + provider
	project := env.bootstrapProjectViaAPI(t, ctx, seed, "openai-prod", "service-e2e-local")

	nonce := "omnara-live-" + provider + "-" + env.seed
	uploadPath := "/tmp/" + nonce + "-upload.txt"
	downloadPath := "/tmp/" + nonce + "-download.txt"
	finalText := "LIVE_MACHINE_DONE_" + nonce
	var requests atomic.Int64
	modelFailures := make(chan string, 1)
	fail := func(w http.ResponseWriter, status int, format string, args ...any) {
		message := fmt.Sprintf(format, args...)
		select {
		case modelFailures <- message:
		default:
		}
		http.Error(w, message, status)
	}
	artifactPathPattern := regexp.MustCompile(`/artifacts/art_[A-Za-z0-9]+`)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			fail(w, http.StatusBadRequest, "decode model request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch requests.Add(1) {
		case 1:
			if !fakeModelRequestContainsTools(w, body, fail, "run_command", "storage_upload", "storage_download") {
				return
			}
			writeOpenAIFunctionCall(w, fail, "resp_write", "write", "run_command", map[string]any{
				"command": fmt.Sprintf("printf %%s '%s' > '%s'", nonce, uploadPath),
			})
		case 2:
			if !fakeModelProcessToolResult(w, body, fail, "write", "exited") {
				return
			}
			writeOpenAIFunctionCall(w, fail, "resp_upload", "upload", "storage_upload", map[string]any{
				"path": "/artifacts", "source": uploadPath,
			})
		case 3:
			output := toolResultOutputForCall(body, "upload")
			artifactPath := artifactPathPattern.FindString(output)
			if artifactPath == "" {
				fail(w, http.StatusBadRequest, "upload did not return an artifact path: %s", output)
				return
			}
			writeOpenAIFunctionCall(w, fail, "resp_download", "download", "storage_download", map[string]any{
				"path": artifactPath, "destination": downloadPath,
			})
		case 4:
			if !fakeModelToolOutputContains(w, body, fail, "download", downloadPath) {
				return
			}
			writeOpenAIFunctionCall(w, fail, "resp_read", "read", "run_command", map[string]any{
				"command": fmt.Sprintf("cat '%s'", downloadPath),
			})
		case 5:
			result, ok := fakeModelProcessResult(w, body, fail, "read")
			if !ok {
				return
			}
			if result.State != "exited" || result.Output != nonce {
				fail(w, http.StatusBadRequest, "downloaded file read back as state=%q output=%q", result.State, result.Output)
				return
			}
			writeOpenAIMessage(w, fail, "resp_done", finalText)
		default:
			fail(w, http.StatusBadRequest, "unexpected model request: %s", mustJSONString(body))
		}
	}))
	defer model.Close()
	env.updateServiceE2EProviderBaseURL(t, ctx, project.projectID, "openai-prod", model.URL)

	secret := env.requestJSON(t, ctx, http.MethodPost, "/api/v1/orgs/"+project.orgID+"/secrets", map[string]any{
		"owner":    map[string]any{"kind": "org"},
		"name":     seed + "-credential",
		"material": map[string]any{"kind": "generic", "value": credential},
	}, "", project.adminToken, http.StatusCreated)
	poolName := seed + "-pool"
	pool["name"] = poolName
	pool["provider"] = provider
	pool["provider_auth_secret_id"] = testutil.RequireType[string](t, secret["id"])
	pool["max_total_machines"] = 1
	created := env.requestJSON(t, ctx, http.MethodPost, "/api/v1/orgs/"+project.orgID+"/machine-pools",
		pool, "", project.adminToken, http.StatusCreated)
	poolID := testutil.RequireType[string](t, created["id"])
	poolUUID := mustDecodeServiceE2EPublicID(t, publicid.KindMachinePool, poolID)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		request, err := env.newAPIRequest(ctx, http.MethodDelete,
			"/api/v1/orgs/"+project.orgID+"/machine-pools/"+poolID, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+project.adminToken)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("delete live machine pool: %v", err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("delete live machine pool status = %d", response.StatusCode)
		}
		waitForServiceE2EConditionUntil(t, ctx, time.Now().Add(5*time.Minute), func() (bool, string) {
			var remaining int
			var detail string
			err := env.db.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE lifecycle_state <> 'deleted'),
       coalesce(string_agg(lifecycle_state || ' ' || coalesce(provider_resource_id, '') || ' ' ||
         coalesce(lifecycle_reason_message, ''), '; '), '')
FROM machines WHERE machine_pool_id = $1`, poolUUID).Scan(&remaining, &detail)
			if err != nil {
				return false, err.Error()
			}
			return remaining == 0, "pool machines are not deleted yet: " + detail + "; maintenance=" +
				maintenance.logExcerpt()
		})
	})
	env.requestJSON(t, ctx, http.MethodPost, project.projectPath+"/machine-pool-grants",
		map[string]any{"machine_pool_id": poolID}, "", project.adminToken, http.StatusCreated)
	config := env.requestJSON(t, ctx, http.MethodPost, project.projectPath+"/agent-configs", map[string]any{
		"source_format": "yaml",
		"source": `instruction: Move the file with the tools you are given.
model:
  provider_config: openai-prod
  name: service-e2e-local
machine_sources:
  - machine_pool_name: ` + poolName + `
tools:
  run_command:
    permission:
      mode: always_allow
  storage_upload:
    permission:
      mode: always_allow
  storage_download:
    permission:
      mode: always_allow
`,
	}, "", project.adminToken, http.StatusCreated)
	profile := env.requestJSON(t, ctx, http.MethodPost,
		project.projectPath+"/agent-profiles/"+project.agentID+"/config",
		map[string]any{
			"config":                     testutil.RequireType[string](t, config["id"]),
			"expected_current_config_id": project.configID,
		}, "", project.adminToken, http.StatusOK)
	project.configID = testutil.RequireType[string](
		t, testutil.RequireType[map[string]any](t, profile["current_config"])["id"],
	)
	agentID := project.createAgent(t, ctx)

	waitForServiceE2EConditionUntil(t, ctx, time.Now().Add(5*time.Minute), func() (bool, string) {
		var state, reason string
		var online bool
		err := env.db.QueryRow(ctx, `
SELECT machine.lifecycle_state,
       coalesce(machine.lifecycle_reason_code, '') || ' ' || coalesce(machine.lifecycle_reason_message, '') ||
         ' ' || coalesce(machine.failure_report::text, ''),
       EXISTS (
         SELECT 1 FROM online_daemon_runtimes runtime
         WHERE runtime.org_id = machine.org_id AND runtime.machine_id = machine.id
       )
FROM machines machine WHERE machine.machine_pool_id = $1`, poolUUID).Scan(&state, &reason, &online)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, "the pool has no machine yet"
		}
		if err != nil {
			return false, err.Error()
		}
		detail := fmt.Sprintf("machine state=%s online=%t reason=%s; worker=%s; maintenance=%s",
			state, online, reason, worker.logExcerpt(), maintenance.logExcerpt())
		if state == "provision_failed" || state == "deleted" {
			t.Fatal(detail)
		}
		return state == "active" && online, detail
	})

	project.createInput(t, ctx, agentID, "Write a file, upload it, download it, and read it back.")
	waitForServiceE2ETextOutput(t, ctx, env,
		mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID),
		mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID), finalText, modelFailures, worker)
	if requests.Load() != 5 {
		t.Fatalf("model requests = %d, want 5", requests.Load())
	}
}

func requireLiveMachineTunnelOnlyServesDaemonRoutes(t *testing.T, ctx context.Context, env *serviceE2EEnvironment) {
	t.Helper()
	project := env.bootstrapProjectViaAPI(t, ctx, "live-machines-tunnel", "openai-prod", "service-e2e-local")
	secrets := "/orgs/" + project.orgID + "/secrets"
	admin := map[string]string{"Authorization": "Bearer " + project.adminToken}
	for _, check := range []struct {
		method, path string
		headers      map[string]string
		want         int
	}{
		{http.MethodGet, "/api/v1" + secrets, admin, http.StatusNotFound},
		{http.MethodGet, "/api/v1" + secrets, map[string]string{
			"Cookie": httpauth.BrowserSessionHostCookieName + "=" + project.adminSession,
		}, http.StatusNotFound},
		{http.MethodGet, "/api/v1/daemon/%2e%2e" + secrets, admin, http.StatusNotFound},
		{http.MethodPost, "/api/v1/daemon/bootstrap", nil, http.StatusUnauthorized},
		{http.MethodPost, "/api/v1/daemon/bootstrap", map[string]string{
			"Authorization": "Bearer invalid-machine-token",
		}, http.StatusUnauthorized},
	} {
		request, err := http.NewRequestWithContext(ctx, check.method, env.publicURL+check.path, nil)
		require.NoError(t, err)
		for name, value := range check.headers {
			request.Header.Set(name, value)
		}
		response, err := http.DefaultClient.Do(request)
		require.NoError(t, err, "%s %s", check.method, check.path)
		_ = response.Body.Close()
		require.Equal(t, check.want, response.StatusCode, "%s %s through the tunnel", check.method, check.path)
	}
}

func exposeLiveMachineAPI(t *testing.T, ctx context.Context, env *serviceE2EEnvironment) {
	t.Helper()
	cloudflared, err := exec.LookPath("cloudflared")
	if err != nil {
		t.Fatalf("cloudflared is required so sandboxes can reach the local API: %v", err)
	}
	var publicURL atomic.Value
	publicURL.Store("")
	mux := http.NewServeMux()
	for _, arch := range []string{"amd64", "arm64"} {
		binary := filepath.Join(env.root, "omnarad-linux-"+arch)
		build := exec.CommandContext(ctx, goBin(env.repoRoot), "build", "-ldflags",
			"-X github.com/omnara-ai/omnara/internal/omnarad.version="+liveMachineDaemonVersion, "-o", binary, "./cmd/daemon")
		build.Dir = env.repoRoot
		build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build linux/%s omnarad: %v\n%s", arch, err, output)
		}
		daemon, err := os.ReadFile(binary)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(daemon)
		mux.HandleFunc("GET /omnarad-releases/linux-"+arch+".txt", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintf(w, "version=%s\nurl=%s/omnarad-releases/omnarad-linux-%s\nsha256=%x\n",
				liveMachineDaemonVersion, publicURL.Load(), arch, digest)
		})
		mux.HandleFunc("GET /omnarad-releases/omnarad-linux-"+arch, func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, binary)
		})
	}
	apiURL, err := url.Parse(env.apiURL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(apiURL)
	mux.Handle("/api/v1/daemon/", proxy)
	mux.Handle("GET /install/omnarad.sh", proxy)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawPath != "" || path.Clean(r.URL.Path) != r.URL.Path {
			http.NotFound(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)

	tunnel := exec.Command(cloudflared, "tunnel", "--no-autoupdate", "--url", front.URL)
	stderr, err := tunnel.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := tunnel.Start(); err != nil {
		t.Fatalf("start cloudflared: %v", err)
	}
	t.Cleanup(func() {
		_ = tunnel.Process.Kill()
		_ = tunnel.Wait()
	})
	tunnelURLs := make(chan string, 1)
	tunnelLogs := &safeLogBuffer{}
	go func() {
		pattern := regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			_, _ = fmt.Fprintln(tunnelLogs, scanner.Text())
			if found := pattern.FindString(scanner.Text()); found != "" {
				select {
				case tunnelURLs <- found:
				default:
				}
			}
		}
	}()
	var tunnelURL string
	select {
	case tunnelURL = <-tunnelURLs:
	case <-time.After(time.Minute):
		t.Fatalf("cloudflared did not report a tunnel URL: %s", tunnelLogs.String())
	}
	publicURL.Store(tunnelURL)
	waitForServiceE2EConditionUntil(t, ctx, time.Now().Add(3*time.Minute), func() (bool, string) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, tunnelURL+"/omnarad-releases/linux-amd64.txt", nil)
		if err != nil {
			return false, err.Error()
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return false, err.Error()
		}
		_ = response.Body.Close()
		return response.StatusCode == http.StatusOK, fmt.Sprintf("tunnel status %d", response.StatusCode)
	})
	env.publicURL = tunnelURL
	env.publicURLHost = strings.TrimPrefix(tunnelURL, "https://")
	env.apiListenAddr = strings.TrimPrefix(env.apiURL, "http://")
	env.apiMetricsListenAddr = strings.TrimPrefix(env.apiMetricsURL, "http://")
	env.maintenanceListenAddr = strings.TrimPrefix(env.maintenanceURL, "http://")
	secretEncryptionKey := make([]byte, 32)
	_, _ = rand.Read(secretEncryptionKey)
	env.secretEncryptionKey = base64.StdEncoding.EncodeToString(secretEncryptionKey)
	t.Setenv("OMNARA_PUBLIC_URL", tunnelURL)
	t.Setenv("OMNARA_PUBLIC_API_URL", tunnelURL+"/api/v1")
	t.Setenv("OMNARA_DAEMON_RELEASE_URL", tunnelURL+"/omnarad-releases")
}
