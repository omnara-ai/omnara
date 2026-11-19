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
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/testutil/mcptest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const (
	// Each batch may take up to 24 hours. The test waits this long per model
	// call (override with OMNARA_LIVE_OPENROUTER_BATCH_WAIT) and skips if
	// OpenRouter has not finished by then.
	defaultLiveOpenRouterBatchWait = 15 * time.Minute
	// The turn asks for this many tool calls in one model response.
	liveBatchTurnToolCalls = 3
	// Two model calls if the model batches its tool calls as asked (tool
	// requests, then the answer); allow for a model that spreads them out.
	liveBatchTurnMinModelCalls = 2
	liveBatchTurnMaxModelCalls = 4
)

type liveToolTurnOptions struct {
	Seed           string
	ProviderConfig string
	// Batch runs the turn through the Batch API variant, resumed by callbacks.
	Batch  bool
	Budget time.Duration
}

type liveToolTurnMetrics struct {
	ProviderConfig string
	Elapsed        time.Duration
	ModelCalls     int
	ToolCalls      int
	InputTokens    int64
	OutputTokens   int64
	// CostUSD is the sum the provider reported; CostedCalls is how many of the
	// model calls reported one.
	CostUSD     float64
	CostedCalls int
	Callbacks   int64
}

// TestServiceE2ELiveOpenRouterBatchTurn is deliberately the only live batch
// test, because batch inference is slow. It runs the same full agent turn
// through the real services twice against OpenRouter, on the same model:
//
//	input -> model asks for three tool calls -> MCP tools run -> model answers
//
// first on the synchronous provider as a baseline, then on the Batch API
// provider, and reports wall time and cost for both.
//
// On the batch run the worker's fallback poll is pushed out of reach, so the
// turn can only advance when OpenRouter calls the API's batch callback route,
// which is exposed through a cloudflared quick tunnel. Between callbacks the
// agent's runtime lock must stay held with no worker attached.
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

	var baseline, batch *liveToolTurnMetrics
	t.Run("sync_baseline", func(t *testing.T) {
		baseline = runLiveOpenRouterToolTurn(t, liveToolTurnOptions{
			Seed:           "live-openrouter-sync-turn",
			ProviderConfig: "openrouter-prod",
			Budget:         5 * time.Minute,
		})
	})
	t.Run("batch", func(t *testing.T) {
		batch = runLiveOpenRouterToolTurn(t, liveToolTurnOptions{
			Seed:           "live-openrouter-batch-turn",
			ProviderConfig: serviceE2EOpenRouterBatchProviderConfig,
			Batch:          true,
			Budget:         liveBatchTurnMaxModelCalls * wait,
		})
	})

	t.Logf("same turn on model %s (%d tool calls requested):", serviceE2EOpenRouterBatchModel(), liveBatchTurnToolCalls)
	for _, metrics := range []*liveToolTurnMetrics{baseline, batch} {
		if metrics != nil {
			t.Log(metrics.summary())
		}
	}
	if baseline == nil || batch == nil {
		t.Log("comparison incomplete: one of the runs did not finish")
		return
	}
	t.Logf("batch took %.1fx the wall time of the synchronous turn", batch.Elapsed.Seconds()/baseline.Elapsed.Seconds())
	if baseline.CostedCalls == baseline.ModelCalls && batch.CostedCalls == batch.ModelCalls && baseline.CostUSD > 0 {
		t.Logf("batch cost %.0f%% of the synchronous turn ($%.6f vs $%.6f)",
			100*batch.CostUSD/baseline.CostUSD, batch.CostUSD, baseline.CostUSD)
	} else {
		t.Log("cost comparison unavailable: not every model call reported a cost")
	}
}

func (m liveToolTurnMetrics) summary() string {
	cost := fmt.Sprintf("$%.6f", m.CostUSD)
	if m.CostedCalls != m.ModelCalls {
		cost += fmt.Sprintf(" (reported by %d of %d model calls)", m.CostedCalls, m.ModelCalls)
	}
	line := fmt.Sprintf("  %-16s wall=%-8s model_calls=%d tool_calls=%d input_tokens=%d output_tokens=%d cost=%s",
		m.ProviderConfig, m.Elapsed.Round(time.Second), m.ModelCalls, m.ToolCalls, m.InputTokens, m.OutputTokens, cost)
	if m.Callbacks > 0 {
		line += " callbacks=" + strconv.FormatInt(m.Callbacks, 10)
	}
	return line
}

func runLiveOpenRouterToolTurn(t *testing.T, opts liveToolTurnOptions) *liveToolTurnMetrics {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), opts.Budget+15*time.Minute)
	defer cancel()

	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, opts.Seed)
	toolResult := "OPAQUE_TOOL_RESULT_" + rand.Text()
	mcpServer := mcptest.NewJSONServerWithGreetResult(t, toolResult)
	callbacks := &atomic.Int64{}
	if opts.Batch {
		callbacks = exposeLiveBatchCallbackRoute(t, ctx, env)
	}
	env.startAPI(t, ctx)
	if opts.Batch {
		waitForLiveBatchTunnel(t, ctx, env)
	}

	modelSlug := serviceE2EOpenRouterBatchModel()
	project := env.bootstrapProjectViaAPIWithSourceAndModelOptions(t, ctx, opts.Seed, strings.Join([]string{
		"instruction: Use tools only when explicitly requested. Preserve exact tool results.",
		"model:",
		"  provider_config: " + opts.ProviderConfig,
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
	marker := "TOOL_TURN_DONE_" + nonce
	project.createInput(t, ctx, agentID, strings.Join([]string{
		"Call the " + toolName + " tool exactly three times, with name set to ALPHA_" + nonce +
			", BRAVO_" + nonce + ", and CHARLIE_" + nonce + ".",
		"Make all three calls together in a single response, in parallel, not one after another.",
		"The exact result is generated inside the tool server and cannot be guessed.",
		"After the tools return, do not call another tool.",
		"Reply on one line beginning with " + marker + " followed by the exact text the tool returned.",
	}, "\n"))
	workerOptions := serviceWorkerOptions{
		ProviderConfig: opts.ProviderConfig,
		BaseURL:        os.Getenv("OPENROUTER_BASE_URL"),
	}
	if opts.Batch {
		// Out of reach on purpose: only a real callback may resume the turn.
		workerOptions.ExtraEnv = []string{"OMNARA_MODEL_BATCH_POLL_INTERVAL=12h"}
	}
	worker := env.startWorker(t, ctx, project.projectID, workerOptions)

	started := time.Now()
	deadline := started.Add(opts.Budget)
	sawParkedLock := false
	var output string
	for !strings.Contains(output, marker) {
		failure, err := latestTerminalLiveModelFailure(ctx, env, projectUUID, agentUUID, 0)
		if err != nil {
			t.Fatalf("query model failures: %v", err)
		}
		if failure != "" {
			t.Fatalf("terminal live model failure: %s worker_logs=%s", failure, worker.logExcerpt())
		}
		parked := 0
		if opts.Batch {
			parked = liveParkedBatchLocks(t, ctx, env, agentUUID)
			sawParkedLock = sawParkedLock || parked > 0
		}
		if time.Now().After(deadline) {
			if parked > 0 {
				// The batch is still running at OpenRouter; that is its schedule,
				// not a defect. Everything up to the parked lock was verified.
				t.Skipf("turn still waiting on an OpenRouter batch after %s (%d callbacks so far); "+
					"raise OMNARA_LIVE_OPENROUTER_BATCH_WAIT to wait longer", opts.Budget, callbacks.Load())
			}
			t.Fatalf("turn did not finish within %s; callbacks=%d output=%q worker_logs=%s",
				opts.Budget, callbacks.Load(), output, worker.logExcerpt())
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for the tool turn: %v", ctx.Err())
		case <-time.After(time.Second):
		}
		output, err = latestModelOutputTextAfterSequence(ctx, env, projectUUID, agentUUID, 0)
		if err != nil {
			t.Fatalf("query model output: %v", err)
		}
	}
	metrics := &liveToolTurnMetrics{
		ProviderConfig: opts.ProviderConfig,
		Elapsed:        time.Since(started),
		Callbacks:      callbacks.Load(),
	}
	if !strings.Contains(output, toolResult) {
		t.Fatalf("final output %q does not carry the tool result %q", output, toolResult)
	}
	waitForLiveAgentIdle(t, ctx, env, project.projectID, agentID, worker)

	wantVariant := "openrouter"
	if opts.Batch {
		wantVariant = "openrouter-batch"
	}
	var costUSD *float64
	if err := env.db.QueryRow(ctx, `
SELECT count(*),
       coalesce(sum(context.input_tokens_total), 0),
       coalesce(sum(context.output_tokens_total), 0),
       sum(context.provider_reported_cost_usd)::float8,
       count(context.provider_reported_cost_usd)
FROM model_call_contexts context
JOIN configured_model_revisions revision
  ON revision.org_id = context.org_id AND revision.id = context.configured_model_revision_id
JOIN model_provider_configs provider_config
  ON provider_config.org_id = revision.org_id AND provider_config.id = revision.model_provider_config_id
WHERE context.project_id = $1 AND context.agent_id = $2
  AND context.operation_kind = 'normal' AND context.state = 'succeeded'
  AND provider_config.name = $3 AND provider_config.api_variant = $4
  AND provider_config.deleted_at IS NULL`,
		projectUUID, agentUUID, opts.ProviderConfig, wantVariant).Scan(
		&metrics.ModelCalls, &metrics.InputTokens, &metrics.OutputTokens, &costUSD, &metrics.CostedCalls,
	); err != nil {
		t.Fatalf("query model call usage: %v", err)
	}
	if costUSD != nil {
		metrics.CostUSD = *costUSD
	}
	if metrics.ModelCalls < liveBatchTurnMinModelCalls || metrics.ModelCalls > liveBatchTurnMaxModelCalls {
		t.Fatalf("succeeded model calls on %s = %d, want %d to %d",
			opts.ProviderConfig, metrics.ModelCalls, liveBatchTurnMinModelCalls, liveBatchTurnMaxModelCalls)
	}
	if metrics.InputTokens == 0 || metrics.OutputTokens == 0 {
		t.Fatalf("model usage was not recorded: %+v", metrics)
	}

	var resultBlocks int
	if err := env.db.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE call.state = 'completed'), count(block.id)
FROM tool_call_read_projection call
LEFT JOIN tool_call_results result
  ON result.agent_id = call.agent_id AND result.tool_call_id = call.id AND result.outcome = 'succeeded'
LEFT JOIN content_blocks block
  ON block.agent_id = result.agent_id AND block.owner_tool_call_result_id = result.id
 AND block.block_kind = 'text' AND block.text_content = $4
WHERE call.project_id = $1 AND call.agent_id = $2 AND call.name = $3`,
		projectUUID, agentUUID, toolName, toolResult).Scan(&metrics.ToolCalls, &resultBlocks); err != nil {
		t.Fatalf("query tool calls: %v", err)
	}
	if metrics.ToolCalls < liveBatchTurnToolCalls || resultBlocks != metrics.ToolCalls {
		t.Fatalf("completed %s calls = %d with %d matching results, want at least %d, all returning the tool result",
			toolName, metrics.ToolCalls, resultBlocks, liveBatchTurnToolCalls)
	}

	if opts.Batch {
		if !sawParkedLock {
			t.Fatal("never observed a parked runtime lock held past the worker lease while a batch was running")
		}
		// With the fallback poll disabled, each model call needs its own callback.
		if metrics.Callbacks < int64(metrics.ModelCalls) {
			t.Fatalf("API received %d batch callbacks for %d model calls", metrics.Callbacks, metrics.ModelCalls)
		}
		var leftover int
		if err := env.db.QueryRow(ctx,
			`SELECT count(*) FROM model_batch_requests WHERE agent_id = $1`, agentUUID).Scan(&leftover); err != nil {
			t.Fatalf("query leftover batch requests: %v", err)
		}
		if leftover != 0 {
			t.Fatalf("%d batch requests left behind after the turn", leftover)
		}
	}
	t.Logf("%s finished: %s", opts.ProviderConfig, output)
	return metrics
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
