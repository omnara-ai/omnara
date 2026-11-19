//go:build integration && servicee2e && live

package e2e

import (
	"bufio"
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/testutil/mcptest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

// Each batch may take up to 24 hours. The test waits this long per model call
// (override with OMNARA_LIVE_OPENROUTER_BATCH_WAIT) and skips if OpenRouter has
// not finished by then.
const defaultLiveOpenRouterBatchWait = 15 * time.Minute

// TestServiceE2ELiveOpenRouterBatchTurn is deliberately the only live batch
// test, because batch inference is slow. It runs one full agent turn through
// the real services against the real OpenRouter Batch API:
//
//	input -> batch 1 (model asks for a tool) -> MCP tool runs -> batch 2 (model answers)
//
// The worker's fallback poll is pushed out of reach, so the turn can only
// advance when OpenRouter calls the API's batch callback route, which is
// exposed through a cloudflared quick tunnel. Between callbacks the agent's
// runtime lock must stay held with no worker attached.
func TestServiceE2ELiveOpenRouterBatchTurn(t *testing.T) {
	if strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")) == "" {
		t.Fatal("OPENROUTER_API_KEY is required for the live OpenRouter batch service E2E")
	}
	wait := defaultLiveOpenRouterBatchWait
	if raw := strings.TrimSpace(os.Getenv("OMNARA_LIVE_OPENROUTER_BATCH_WAIT")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			t.Fatalf("OMNARA_LIVE_OPENROUTER_BATCH_WAIT %q is not a positive duration", raw)
		}
		wait = parsed
	}
	const modelCalls = 2
	turnBudget := modelCalls * wait
	ctx, cancel := context.WithTimeout(context.Background(), turnBudget+15*time.Minute)
	defer cancel()

	const seed = "live-openrouter-batch-turn"
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, seed)
	toolResult := "OPAQUE_TOOL_RESULT_" + rand.Text()
	mcpServer := mcptest.NewJSONServerWithGreetResult(t, toolResult)
	callbacks := exposeLiveBatchCallbackRoute(t, ctx, env)
	env.startAPI(t, ctx)
	waitForLiveBatchTunnel(t, ctx, env)

	modelSlug := serviceE2EOpenRouterBatchModel()
	project := env.bootstrapProjectViaAPIWithSourceAndModelOptions(t, ctx, seed, strings.Join([]string{
		"instruction: Use tools only when explicitly requested. Preserve exact tool results.",
		"model:",
		"  provider_config: " + serviceE2EOpenRouterBatchProviderConfig,
		"  name: " + modelSlug,
		"mcp:",
		"  docs:",
		"    url: " + mcpServer.URL,
		"    permission:",
		"      mode: always_allow",
		"",
	}, "\n"), nil)
	agentID := project.createAgent(t, ctx)
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)

	nonce := strings.ToUpper(strings.ReplaceAll(env.seed, "-", "_"))
	toolName := toolcatalog.MCPRuntimeToolName("docs", "greet")
	marker := "BATCH_TURN_DONE_" + nonce
	project.createInput(t, ctx, agentID, strings.Join([]string{
		"Call the " + toolName + " tool exactly once with name set to TOOL_FACT_" + nonce + ".",
		"The exact result is generated inside the tool server and cannot be guessed.",
		"After the tool returns, do not call another tool.",
		"Reply on one line beginning with " + marker + " followed by the exact text the tool returned.",
	}, "\n"))
	worker := env.startWorker(t, ctx, project.projectID, serviceWorkerOptions{
		ProviderConfig: serviceE2EOpenRouterBatchProviderConfig,
		BaseURL:        os.Getenv("OPENROUTER_BASE_URL"),
		// Out of reach on purpose: only a real callback may resume the turn.
		ExtraEnv: []string{"OMNARA_MODEL_BATCH_POLL_INTERVAL=12h"},
	})

	started := time.Now()
	deadline := started.Add(turnBudget)
	sawParkedLock := false
	var output string
	for !strings.Contains(output, marker) {
		failure, err := latestTerminalLiveModelFailure(ctx, env, projectUUID, agentUUID, 0)
		if err != nil {
			t.Fatalf("query model failures: %v", err)
		}
		if failure != "" {
			t.Fatalf("terminal live batch model failure: %s worker_logs=%s", failure, worker.logExcerpt())
		}
		parked := liveParkedBatchLocks(t, ctx, env, agentUUID)
		sawParkedLock = sawParkedLock || parked > 0
		if time.Now().After(deadline) {
			if parked > 0 {
				// The batch is still running at OpenRouter; that is its schedule,
				// not a defect. Everything up to the parked lock was verified.
				t.Skipf("turn still waiting on an OpenRouter batch after %s (%d callbacks so far); "+
					"raise OMNARA_LIVE_OPENROUTER_BATCH_WAIT to wait longer", turnBudget, callbacks.Load())
			}
			t.Fatalf("turn did not finish within %s and no batch is parked; callbacks=%d output=%q worker_logs=%s",
				turnBudget, callbacks.Load(), output, worker.logExcerpt())
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for the batch turn: %v", ctx.Err())
		case <-time.After(2 * time.Second):
		}
		output, err = latestModelOutputTextAfterSequence(ctx, env, projectUUID, agentUUID, 0)
		if err != nil {
			t.Fatalf("query model output: %v", err)
		}
	}
	t.Logf("turn finished in %s with %d callbacks: %s", time.Since(started).Round(time.Second), callbacks.Load(), output)
	if !strings.Contains(output, toolResult) {
		t.Fatalf("final output %q does not carry the tool result %q from the first batch", output, toolResult)
	}
	waitForLiveAgentIdle(t, ctx, env, project.projectID, agentID, worker)

	if !sawParkedLock {
		t.Fatal("never observed a parked runtime lock held past the worker lease while a batch was running")
	}
	// With the fallback poll disabled, each model call needs its own callback.
	if got := callbacks.Load(); got < modelCalls {
		t.Fatalf("API received %d batch callbacks, want at least %d", got, modelCalls)
	}
	var succeeded, leftover int
	if err := env.db.QueryRow(ctx, `
SELECT count(*) FROM model_call_contexts context
JOIN configured_model_revisions revision
  ON revision.org_id = context.org_id AND revision.id = context.configured_model_revision_id
JOIN model_provider_configs provider_config
  ON provider_config.org_id = revision.org_id AND provider_config.id = revision.model_provider_config_id
WHERE context.project_id = $1 AND context.agent_id = $2
  AND context.operation_kind = 'normal' AND context.state = 'succeeded'
  AND provider_config.name = $3 AND provider_config.api_variant = 'openrouter-batch'
  AND provider_config.deleted_at IS NULL`,
		projectUUID, agentUUID, serviceE2EOpenRouterBatchProviderConfig).Scan(&succeeded); err != nil {
		t.Fatalf("query batch model contexts: %v", err)
	}
	if succeeded != modelCalls {
		t.Fatalf("succeeded batch model calls = %d, want %d (tool request, then final answer)", succeeded, modelCalls)
	}
	if err := env.db.QueryRow(ctx,
		`SELECT count(*) FROM model_batch_requests WHERE agent_id = $1`, agentUUID).Scan(&leftover); err != nil {
		t.Fatalf("query leftover batch requests: %v", err)
	}
	if leftover != 0 {
		t.Fatalf("%d batch requests left behind after the turn", leftover)
	}
	assertLiveToolResultEvidence(t, ctx, env, projectUUID, agentUUID, toolName, toolResult)
}

// liveParkedBatchLocks counts the agent's parked batch requests whose runtime
// lock is held well past any worker lease, which only a parked lock is.
func liveParkedBatchLocks(t *testing.T, ctx context.Context, env *serviceE2EEnvironment, agentUUID string) int {
	t.Helper()
	var parked int
	if err := env.db.QueryRow(ctx, `
SELECT count(*) FROM model_batch_requests request
JOIN agent_runtime_locks runtime_lock ON runtime_lock.id = request.runtime_lock_id
WHERE request.agent_id = $1 AND request.state = 'parked'
  AND runtime_lock.lease_expires_at > now() + interval '1 hour'`, agentUUID).Scan(&parked); err != nil {
		t.Fatalf("query parked batch locks: %v", err)
	}
	return parked
}

// exposeLiveBatchCallbackRoute makes only the API's batch callback route
// reachable from OpenRouter through a cloudflared quick tunnel and points the
// services' public URL at it. It returns the number of callbacks forwarded.
func exposeLiveBatchCallbackRoute(t *testing.T, ctx context.Context, env *serviceE2EEnvironment) *atomic.Int64 {
	t.Helper()
	cloudflared, err := exec.LookPath("cloudflared")
	if err != nil {
		t.Fatalf("cloudflared is required so OpenRouter can reach the API's batch callback route: %v", err)
	}
	apiURL, err := url.Parse(env.apiURL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(apiURL)
	callbacks := &atomic.Int64{}
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", proxy)
	mux.HandleFunc("POST /api/model-batches/openrouter/", func(w http.ResponseWriter, r *http.Request) {
		callbacks.Add(1)
		proxy.ServeHTTP(w, r)
	})
	front := httptest.NewServer(mux)
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
	select {
	case tunnelURL := <-tunnelURLs:
		env.publicURL = tunnelURL
		env.publicURLHost = strings.TrimPrefix(tunnelURL, "https://")
	case <-ctx.Done():
		t.Fatalf("waiting for cloudflared: %v", ctx.Err())
	case <-time.After(time.Minute):
		t.Fatalf("cloudflared did not report a tunnel URL: %s", tunnelLogs.String())
	}
	return callbacks
}

// waitForLiveBatchTunnel waits until the public URL reaches the running API.
// OpenRouter rejects a callback host that does not resolve publicly yet.
func waitForLiveBatchTunnel(t *testing.T, ctx context.Context, env *serviceE2EEnvironment) {
	t.Helper()
	waitForServiceE2EConditionUntil(t, ctx, time.Now().Add(3*time.Minute), func() (bool, string) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, env.publicURL+"/healthz", nil)
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
	t.Logf("API batch callback route is public at %s", env.publicURL)
}
