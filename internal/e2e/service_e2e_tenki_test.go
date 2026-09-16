//go:build integration && servicee2e && live

package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/LuxorLabs/tenki-sdk-go/sandbox"
	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestServiceE2ELiveTenki(t *testing.T) {
	token := strings.TrimSpace(os.Getenv("TENKI_API_KEY"))
	if token == "" {
		if os.Getenv("OMNARA_REQUIRE_TENKI_LIVE") == "1" {
			t.Fatal("TENKI_API_KEY is required")
		}
		t.Skip("TENKI_API_KEY is required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	defer cancel()
	require.NotEmpty(
		t,
		os.Getenv("OMNARA_TEST_REDIS_URL"),
		"OMNARA_TEST_REDIS_URL is required; use make test-live-tenki-e2e",
	)
	env := newServiceE2EEnvironment(t, ctx, "tenki-live")
	startTenkiE2EEndpoint(t, ctx, env)
	api := env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithTools(
		t,
		ctx,
		"tenki-live",
		"openai-prod",
		"service-e2e-local",
		processToolNames...)
	client, err := sdk.New(sdk.WithAuthToken(token), sdk.WithBaseURL("https://api.tenki.cloud"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	poolID := createTenkiE2EPool(t, ctx, &project, token)
	poolUUID := mustDecodeServiceE2EPublicID(t, publicid.KindMachinePool, poolID)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		rows, err := env.db.Query(cleanup, "SELECT id::text FROM machines WHERE machine_pool_id = $1", poolUUID)
		require.NoError(t, err)
		owned := map[string]bool{}
		for rows.Next() {
			var id string
			require.NoError(t, rows.Scan(&id))
			owned[id] = true
		}
		rows.Close()
		require.NoError(t, rows.Err())
		sessions, err := client.List(cleanup, sdk.WithTagFilter("omnara-managed"))
		require.NoError(t, err)
		for _, session := range sessions {
			if owned[session.Metadata["omnara-machine"]] {
				if t.Failed() {
					logs, err := session.ReadFile(cleanup, "/home/tenki/.omnara-provider/daemon.log")
					if err == nil {
						t.Logf("Tenki daemon bootstrap log: %s", logs)
					}
				}
				require.NoError(t, session.CloseIfOpen(cleanup))
			}
		}
	})
	updateTenkiE2EProfile(t, ctx, &project)
	agentID := project.createAgent(t, ctx)
	project.startPermissionAutoApprover(t, ctx, agentID)
	var machineUUID, resourceID string
	waitForServiceE2EConditionUntil(t, ctx, time.Now().Add(5*time.Minute), func() (bool, string) {
		var state, reason string
		err := env.db.QueryRow(ctx, `
SELECT id::text, coalesce(provider_resource_id, ''), lifecycle_state, lifecycle_reason_message
FROM machines WHERE machine_pool_id = $1 ORDER BY created_at DESC LIMIT 1`, poolUUID).
			Scan(&machineUUID, &resourceID, &state, &reason)
		if err != nil {
			return false, err.Error()
		}
		return resourceID != "" &&
			state == "active", "machine=" + state + " reason=" + reason + " api=" + api.logExcerpt()
	})
	parsedMachineID, err := uuid.Parse(machineUUID)
	require.NoError(t, err)
	machineID, err := publicid.Encode(publicid.KindMachine, parsedMachineID)
	require.NoError(t, err)
	waitForDaemonRuntime(t, ctx, env, project.orgID, machineID)
	waitForDockerMachineOnline(t, ctx, env, project.orgID, machineID)
	session, err := client.Get(ctx, resourceID)
	require.NoError(t, err)
	require.True(t, session.Sticky)
	require.False(t, session.InboundEnabled)
	require.True(t, session.OutboundEnabled)
	require.Empty(t, session.Metadata["TENKI_API_KEY"])
	nonce := env.seed
	var mu sync.Mutex
	step := 0
	processID := ""
	processStarted := make(chan struct{})
	resumeProcess := make(chan struct{})
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		fail := func(w http.ResponseWriter, status int, format string, args ...any) {
			http.Error(w, fmt.Sprintf(format, args...), status)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			fail(w, 400, "%v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		step++
		call := func(name string, args map[string]any) {
			writeOpenAIFunctionCall(
				w,
				fail,
				fmt.Sprintf("resp_tenki_%d", step),
				fmt.Sprintf("call_tenki_%d", step),
				name,
				args,
			)
		}
		switch (step-1)%8 + 1 {
		case 1:
			call(
				"run_command",
				map[string]any{
					"command": "printf '%s' '" + nonce + "' > nonce.txt; cat nonce.txt; printf '\\n%s' \"$TENKI_E2E_ENV\"",
					"wait_ms": 1000,
				},
			)
		case 2:
			if !fakeModelToolOutputContains(w, body, fail, fmt.Sprintf("call_tenki_%d", step-1), nonce) ||
				!fakeModelToolOutputContains(w, body, fail, fmt.Sprintf("call_tenki_%d", step-1), "from-pool") {
				return
			}
			call(
				"run_command",
				map[string]any{
					"command": "printf x >> effects.txt; while IFS= read -r line; do printf '%s\\n' \"$line\" | tee -a stdin.txt; done",
					"wait_ms": 1000,
				},
			)
		case 3:
			id, ok := fakeModelToolProcessID(w, body, fail, fmt.Sprintf("call_tenki_%d", step-1))
			if !ok {
				return
			}
			processID = id
			if step == 3 {
				close(processStarted)
				select {
				case <-resumeProcess:
				case <-ctx.Done():
					return
				}
			}
			call("write_process", map[string]any{"process_id": processID, "data": nonce + "\n"})
		case 4:
			if !fakeModelProcessActionAccepted(w, body, fail, fmt.Sprintf("call_tenki_%d", step-1)) {
				return
			}
			call(
				"read_process",
				map[string]any{"process_id": processID, "wait_ms": 1000, "cursor": 0, "max_bytes": 4096},
			)
		case 5:
			if !fakeModelToolOutputContains(w, body, fail, fmt.Sprintf("call_tenki_%d", step-1), nonce) {
				return
			}
			call("list_processes", map[string]any{})
		case 6:
			if !fakeModelToolOutputContains(w, body, fail, fmt.Sprintf("call_tenki_%d", step-1), processID) {
				return
			}
			call("stop_process", map[string]any{"process_id": processID, "mode": "terminate"})
		case 7:
			if !fakeModelProcessActionAccepted(w, body, fail, fmt.Sprintf("call_tenki_%d", step-1)) {
				return
			}
			call(
				"read_process",
				map[string]any{"process_id": processID, "wait_ms": 1000, "cursor": 0, "max_bytes": 4096},
			)
		default:
			writeOpenAIMessage(w, fail, fmt.Sprintf("resp_tenki_done_%d", step), "TENKI_E2E_DONE")
		}
	}))
	t.Cleanup(model.Close)
	worker := env.startWorker(
		t,
		ctx,
		project.projectID,
		serviceWorkerOptions{ProviderConfig: "openai-prod", BaseURL: model.URL},
	)
	project.createInput(t, ctx, agentID, "Run the sandbox verification.")
	select {
	case <-processStarted:
	case <-time.After(2 * time.Minute):
		t.Fatal("process did not start before the recovery test deadline")
	}
	api.stop()
	env.startAPI(t, ctx)
	waitForDockerMachineOnline(t, ctx, env, project.orgID, machineID)
	close(resumeProcess)
	waitForLiveModelOutputContaining(
		t,
		ctx,
		env,
		project.projectID,
		agentID,
		0,
		[]string{"TENKI_E2E_DONE"},
		worker,
	)
	for path, expected := range map[string]string{"nonce.txt": nonce, "effects.txt": "x", "stdin.txt": nonce + "\n"} {
		data, err := session.ReadFile(ctx, "/home/tenki/"+path)
		require.NoError(t, err)
		require.Equal(t, expected, string(data))
	}
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	for _, name := range processToolNames {
		var count int
		require.NoError(
			t,
			env.db.QueryRow(ctx, `SELECT count(*) FROM tool_call_read_projection
WHERE project_id=$1 AND agent_id=$2 AND name=$3 AND state='completed'`, projectUUID, agentUUID, name).
				Scan(&count),
		)
		require.Positive(t, count, "missing completed %s", name)
	}
	worker.stop()
	worker = env.startWorker(t, ctx, project.projectID,
		serviceWorkerOptions{ProviderConfig: "openai-prod", BaseURL: model.URL})
	var lastSequence int64
	require.NoError(
		t,
		env.db.QueryRow(ctx, "SELECT coalesce(max(sequence), 0) FROM agent_events WHERE agent_id=$1", agentUUID).
			Scan(&lastSequence),
	)
	project.createInput(t, ctx, agentID, "Run verification again after the worker restart.")
	waitForLiveModelOutputContaining(
		t,
		ctx,
		env,
		project.projectID,
		agentID,
		lastSequence,
		[]string{"TENKI_E2E_DONE"},
		worker,
	)
	waitForServiceE2EConditionUntil(t, ctx, time.Now().Add(3*time.Minute), func() (bool, string) {
		data, err := session.ReadFile(ctx, "/home/tenki/effects.txt")
		return err == nil && string(data) == "xx", "waiting for command after reconnect"
	})
	env.startMaintenance(t, ctx)
	deletePool, err := env.newAPIRequest(ctx, http.MethodDelete,
		"/api/v1/orgs/"+project.orgID+"/machine-pools/"+poolID, nil)
	require.NoError(t, err)
	deletePool.Header.Set("Authorization", "Bearer "+project.adminToken)
	response, err := http.DefaultClient.Do(deletePool)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusNoContent, response.StatusCode)
	waitForServiceE2EConditionUntil(t, ctx, time.Now().Add(2*time.Minute), func() (bool, string) {
		current, err := client.Get(ctx, resourceID)
		if err != nil {
			return false, err.Error()
		}
		return current.State == sdk.SessionStateTerminated, string(current.State)
	})
}

func createTenkiE2EPool(t *testing.T, ctx context.Context, p *deterministicProject, token string) string {
	t.Helper()
	secret := p.env.requestJSON(
		t,
		ctx,
		http.MethodPost,
		"/api/v1/orgs/"+p.orgID+"/secrets",
		map[string]any{
			"owner":    map[string]string{"kind": "org"},
			"name":     "tenki-api",
			"material": map[string]string{"kind": "generic", "value": token},
		},
		"",
		p.adminToken,
		http.StatusCreated,
	)
	pool := p.env.requestJSON(t, ctx, http.MethodPost, "/api/v1/orgs/"+p.orgID+"/machine-pools", map[string]any{
		"name":                             "tenki-e2e",
		"provider":                         "tenki",
		"provider_auth_secret_id":          secret["id"],
		"default_machine_cpu":              2,
		"default_machine_memory_mb":        4096,
		"default_machine_provider_options": map[string]any{},
		"provider_config":                  map[string]any{},
		"default_machine_env":              map[string]string{"TENKI_E2E_ENV": "from-pool"},
		"default_cwd":                      "/home/tenki",
		"max_total_machines":               1,
		"max_total_cpu":                    2,
		"max_total_memory_mb":              4096,
		"max_machine_cpu":                  2,
		"max_machine_memory_mb":            4096,
		"runtime_protection_enabled":       true,
	}, "", p.adminToken, http.StatusCreated)
	id := testutil.RequireType[string](t, pool["id"])
	p.env.requestJSON(
		t,
		ctx,
		http.MethodPost,
		p.projectPath+"/machine-pool-grants",
		map[string]any{"machine_pool_id": id},
		"",
		p.adminToken,
		http.StatusCreated,
	)
	return id
}

func updateTenkiE2EProfile(t *testing.T, ctx context.Context, p *deterministicProject) {
	t.Helper()
	source := `instruction: Help the user.
model:
  provider_config: openai-prod
  name: service-e2e-local
machine_sources:
  - machine_pool_name: tenki-e2e
    cwd: /home/tenki
tools:
`
	for _, name := range processToolNames {
		source += "  " + name + ": {}\n"
	}
	config := p.env.requestJSON(
		t,
		ctx,
		http.MethodPost,
		p.projectPath+"/agent-configs",
		map[string]any{"source_format": "yaml", "source": source},
		"",
		p.adminToken,
		http.StatusCreated,
	)
	p.env.requestJSON(
		t,
		ctx,
		http.MethodPost,
		p.projectPath+"/agent-profiles/"+p.agentID+"/config",
		map[string]any{"config": config["id"], "expected_current_config_id": p.configID},
		"tenki-profile",
		p.adminToken,
		http.StatusOK,
	)
	p.configID = testutil.RequireType[string](t, config["id"])
}

func startTenkiE2EEndpoint(t *testing.T, ctx context.Context, env *serviceE2EEnvironment) {
	t.Helper()
	artifact := filepath.Join(env.root, "omnarad-linux-amd64")
	build := exec.CommandContext(
		ctx,
		goBin(env.repoRoot),
		"build",
		"-ldflags",
		"-X github.com/omnara-ai/omnara/internal/omnarad.version=0.0.0",
		"-o",
		artifact,
		"./cmd/daemon",
	)
	build.Dir = env.repoRoot
	build.Env = serviceProcessEnv("GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	output, err := build.CombinedOutput()
	require.NoError(t, err, "%s", output)
	data, err := os.ReadFile(artifact)
	require.NoError(t, err)
	digest := sha256.Sum256(data)
	target, err := url.Parse(env.apiURL)
	require.NoError(t, err)
	proxy := httputil.NewSingleHostReverseProxy(target)
	var endpointMu sync.RWMutex
	endpoint := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tenki-e2e-release/linux-amd64.txt":
			endpointMu.RLock()
			defer endpointMu.RUnlock()
			fmt.Fprintf(w, "version=0.0.0\nurl=%s/tenki-e2e-release/omnarad\nsha256=%x\n", endpoint, digest)
		case "/tenki-e2e-release/omnarad":
			http.ServeFile(w, r, artifact)
		default:
			proxy.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(server.Close)
	cmd := exec.CommandContext(ctx, "cloudflared", "tunnel", "--no-autoupdate", "--url", server.URL)
	logs := &safeLogBuffer{}
	cmd.Stdout, cmd.Stderr = logs, logs
	require.NoError(t, cmd.Start())
	tunnel := newServiceProcess(cmd, logs)
	registerSubprocessCleanup(t, "tenki-e2e-tunnel", tunnel)
	pattern := regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)
	waitForServiceE2EConditionUntil(t, ctx, time.Now().Add(time.Minute), func() (bool, string) {
		match := pattern.FindString(logs.String())
		if match == "" {
			return false, logs.Excerpt(10)
		}
		endpointMu.Lock()
		endpoint = match
		endpointMu.Unlock()
		return true, ""
	})
	endpointMu.RLock()
	env.publicURL = endpoint
	endpointMu.RUnlock()
	public, err := url.Parse(env.publicURL)
	require.NoError(t, err)
	env.publicURLHost = public.Host
	t.Setenv("OMNARA_DAEMON_RELEASE_URL", env.publicURL+"/tenki-e2e-release")
}
