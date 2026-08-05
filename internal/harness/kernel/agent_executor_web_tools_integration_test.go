//go:build integration

package kernel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
	"github.com/omnara-ai/omnara/internal/webaccess"
)

const kernelTestExaSecret = "kernel-test-exa-secret-key"

func TestAgentExecutorWebToolsAsyncLifecycle(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)

	// Fake Exa endpoint (keyed path) and a fetchable page.
	exaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != kernelTestExaSecret {
			t.Errorf("missing x-api-key header")
		}
		_, _ = w.Write(
			[]byte(`{"results":[{"url":"https://go.dev/blog/release","title":"Go Release Notes","text":"release snippet"}]}`),
		)
	}))
	defer exaServer.Close()
	pageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("fetched page body"))
	}))
	defer pageServer.Close()

	agentID, userID := fixture.createAgent(t, ctx, "openai/kernel-test", fixture.Now, "web_search", "web_fetch")
	fetchInput, err := json.Marshal(map[string]string{"url": pageServer.URL})
	if err != nil {
		t.Fatalf("marshal fetch input: %v", err)
	}
	modelClient := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		responses: []model.Response{
			{
				ID:         "resp_web_tools",
				StopReason: model.StopReasonToolUse,
				Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{
					{ID: "call_web_search", Name: "web_search", Input: json.RawMessage(`{"query":"go release notes"}`)},
					{ID: "call_web_fetch", Name: "web_fetch", Input: fetchInput},
				}),
			},
			{
				ID:         "resp_web_tools_final",
				Content:    []model.ResponsePart{{Type: "text", Text: "web research finished"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor: tools.Executor{
			Store:      fixture.Store,
			WebSearch:  kernelTestExaProvider(t, exaServer),
			WebFetcher: webaccess.NewFetcher(webaccess.FetcherOptions{AllowLoopback: true}),
		},
		Now: func() time.Time { return fixture.Now.Add(2 * time.Second) },
	}
	turn := fixture.admitContentInputTurn(t, ctx, agentID, userID, "research go releases", fixture.Now.Add(time.Second))
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("persist web tool output: %v", err)
	}
	scope := executeNextToolWork(t, ctx, fixture, executor, turn)
	select {
	case <-scope.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("web tool work did not finish")
	}
	if err := scope.Err(); err != nil {
		t.Fatalf("execute web tool work: %v", err)
	}
	executeNextModelWork(t, ctx, fixture, executor, turn)

	// Both tool calls completed with text + structured_data content parts.
	completed, err := storagetest.ListCompletedToolCallsForTurn(
		ctx,
		fixture.Store,
		kernelTestProjectID,
		agentID,
		turn.TurnID,
	)
	if err != nil {
		t.Fatalf("list completed tool calls: %v", err)
	}
	partsByCall := map[string]string{}
	for _, call := range completed {
		partsByCall[call.ProviderCallID] = string(call.ResultContentParts)
	}
	for _, providerCallID := range []string{"call_web_search", "call_web_fetch"} {
		resultParts, ok := partsByCall[providerCallID]
		if !ok {
			t.Fatalf("%s missing from completed tool calls: %v", providerCallID, partsByCall)
		}
		if !strings.Contains(resultParts, `"structured_data"`) || !strings.Contains(resultParts, `"text"`) {
			t.Fatalf("%s content parts missing text/structured_data: %s", providerCallID, resultParts)
		}
		if strings.Contains(resultParts, kernelTestExaSecret) {
			t.Fatalf("%s content parts leak the provider API key", providerCallID)
		}
	}
	if searchParts := partsByCall["call_web_search"]; !strings.Contains(searchParts, "go.dev/blog/release") ||
		!strings.Contains(searchParts, "Go Release Notes") {
		t.Fatalf("search parts missing results: %s", searchParts)
	}
	if fetchParts := partsByCall["call_web_fetch"]; !strings.Contains(fetchParts, "fetched page body") {
		t.Fatalf("fetch parts missing page content: %s", fetchParts)
	}

	// The API key never appears in durable model-visible records.
	var leaked int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT
  count(*)
FROM content_blocks block
JOIN agents agent ON agent.id = block.agent_id
WHERE agent.project_id = $1
  AND block.agent_id = $2
  AND (
    coalesce(block.text_content, '') LIKE '%' || $3 || '%'
    OR coalesce(block.structured_data, 'null'::jsonb)::text LIKE '%' || $3 || '%'
  )
`, kernelTestProjectID, agentID, kernelTestExaSecret).Scan(&leaked); err != nil {
		t.Fatalf("scan for key leak: %v", err)
	}
	if leaked > 0 {
		t.Fatalf("provider API key leaked into %d durable records", leaked)
	}

	// Final model output produced after the wake.
	var finalOutputs int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM agent_events event
JOIN content_blocks block
  ON block.agent_id = event.agent_id
 AND block.owner_model_output_id = event.model_output_id
JOIN agents agent ON agent.id = event.agent_id
WHERE agent.project_id = $1 AND event.agent_id = $2
  AND event.event_kind = 'model_output'
  AND block.block_kind = 'text'
  AND block.text_content = 'web research finished'
`, kernelTestProjectID, agentID).Scan(&finalOutputs); err != nil {
		t.Fatalf("count final output: %v", err)
	}
	if finalOutputs != 1 {
		t.Fatalf("final outputs = %d, want 1", finalOutputs)
	}
}

func kernelTestExaProvider(t *testing.T, server *httptest.Server) webaccess.ExaProvider {
	t.Helper()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse Exa test server URL: %v", err)
	}
	client := server.Client()
	return webaccess.ExaProvider{
		APIKey: kernelTestExaSecret,
		HTTPClient: &http.Client{Transport: kernelExaTransport{
			target: target,
			base:   client.Transport,
		}},
	}
}

type kernelExaTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (t kernelExaTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	return t.base.RoundTrip(clone)
}

// gatedSearchProvider blocks Search until released, so the test can hold a
// tool call in running state across a crash-replay re-dispatch.
type gatedSearchProvider struct {
	release chan struct{}
	started chan struct{}
	calls   atomic.Int32
}

func (g *gatedSearchProvider) Search(
	ctx context.Context,
	req webaccess.SearchRequest,
) (webaccess.SearchResponse, error) {
	g.calls.Add(1)
	select {
	case g.started <- struct{}{}:
	default:
	}
	select {
	case <-g.release:
	case <-ctx.Done():
		return webaccess.SearchResponse{}, ctx.Err()
	}
	return webaccess.SearchResponse{
		Provider: "gated",
		Results:  []webaccess.SearchResult{{URL: "https://example.org/gated", Title: "Gated", Snippet: "gated result"}},
	}, nil
}

func TestAgentExecutorWebSearchRemovesRunningToolCallFromToolWork(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)

	provider := &gatedSearchProvider{
		release: make(chan struct{}),
		started: make(chan struct{}, 1),
	}
	agentID, userID := fixture.createAgent(t, ctx, "openai/kernel-test", fixture.Now, "web_search")
	modelClient := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		responses: []model.Response{
			{
				ID:         "resp_gated_search",
				StopReason: model.StopReasonToolUse,
				Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{
					{ID: "call_gated_search", Name: "web_search", Input: json.RawMessage(`{"query":"gated"}`)},
				}),
			},
			{ID: "resp_gated_final", Content: []model.ResponsePart{{Type: "text", Text: "gated search finished"}}, StopReason: model.StopReasonEndTurn},
		},
	}
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:  tools.Executor{Store: fixture.Store, WebSearch: provider},
		Now:           func() time.Time { return fixture.Now.Add(2 * time.Second) },
	}
	turn := fixture.admitContentInputTurn(t, ctx, agentID, userID, "gated search", fixture.Now.Add(time.Second))

	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("persist gated search output: %v", err)
	}
	scope := executeNextToolWork(t, ctx, fixture, executor, turn)
	var state string
	if err := fixture.Pool.QueryRow(ctx, `
SELECT state
FROM tool_call_read_projection
WHERE project_id = $1
  AND agent_id = $2
  AND id = (
    SELECT id
    FROM tool_call_read_projection
    WHERE project_id = $1 AND agent_id = $2 AND provider_call_id = 'call_gated_search'
  )
`, kernelTestProjectID, agentID).
		Scan(&state); err != nil {
		t.Fatalf("load running call: %v", err)
	}
	if state != "running" {
		t.Fatalf("state after first dispatch = %q, want running", state)
	}
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("web search did not start")
	}

	var runnable int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM agent_tool_work_frontiers($1, $2)
`, kernelTestProjectID, agentID).Scan(&runnable); err != nil {
		t.Fatalf("count runnable tool work: %v", err)
	}
	if runnable != 0 {
		t.Fatalf("running web search exposed %d tool-work frontiers, want 0", runnable)
	}
	if calls := provider.calls.Load(); calls != 1 {
		t.Fatalf("web search calls = %d, want exactly 1", calls)
	}
	close(provider.release)
	select {
	case <-scope.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("gated web search did not finish")
	}
	if err := scope.Err(); err != nil {
		t.Fatalf("gated web search completion: %v", err)
	}
	executeNextModelWork(t, ctx, fixture, executor, turn)

	var terminalResults int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM tool_call_results result
JOIN tool_call_read_projection call ON call.id = result.tool_call_id
  AND call.agent_id = result.agent_id
WHERE call.project_id = $1 AND call.agent_id = $2 AND call.provider_call_id = 'call_gated_search'
`, kernelTestProjectID, agentID).Scan(&terminalResults); err != nil {
		t.Fatalf("count terminal results: %v", err)
	}
	if terminalResults != 1 {
		t.Fatalf("terminal results = %d, want exactly 1", terminalResults)
	}
	var toolResultEvents int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM agent_events event
JOIN tool_call_results result ON result.id = event.tool_call_result_id
  AND result.agent_id = event.agent_id
JOIN tool_call_read_projection call ON call.id = result.tool_call_id
  AND call.agent_id = result.agent_id
WHERE call.project_id = $1
  AND event.agent_id = $2
  AND event.event_kind = 'tool_result'
  AND call.provider_call_id = 'call_gated_search'
`, kernelTestProjectID, agentID).Scan(&toolResultEvents); err != nil {
		t.Fatalf("count tool result events: %v", err)
	}
	if toolResultEvents != 1 {
		t.Fatalf("tool result events = %d, want exactly 1", toolResultEvents)
	}
}
