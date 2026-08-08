//go:build integration && servicee2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/mcptest"
)

func TestServiceE2EMCPInitializesBeforeGeneration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "mcp-initializes-before-generation")
	mcpServer := mcptest.NewJSONServer(t)
	mcpURL := mcpServer.URL

	var requestCount atomic.Int64
	var modelSawReadyMCP atomic.Bool
	var projectUUID string
	var agentUUID string
	const modelText = "mcp initialization completed before generation"
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer service-e2e-test-key" {
			t.Errorf("unexpected OpenAI auth header %q", auth)
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		var state executionstore.MCPConnectionState
		var tools string
		err := env.db.QueryRow(ctx, `
SELECT connection.state, connection.tools_snapshot::text
FROM agent_mcp_connections connection
JOIN agents agent ON agent.id = connection.agent_id
WHERE agent.project_id = $1
  AND connection.agent_id = $2
  AND connection.server_key = 'docs'
`, projectUUID, agentUUID).
			Scan(&state, &tools)
		if err != nil {
			t.Errorf("query mcp connection before model request: %v", err)
			http.Error(w, "mcp connection unavailable", http.StatusServiceUnavailable)
			return
		}
		if state != executionstore.MCPConnectionStateReady || !strings.Contains(tools, `"greet"`) ||
			!strings.Contains(tools, `"noisy"`) {
			t.Errorf("model request ran before ready mcp tools snapshot: state=%q tools=%s", state, tools)
			http.Error(w, "mcp connection not ready", http.StatusServiceUnavailable)
			return
		}
		modelSawReadyMCP.Store(true)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode OpenAI request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if body["model"] != "service-e2e-local" {
			t.Errorf("unexpected model in OpenAI request: %+v", body)
		}
		requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(
			[]byte(
				`{"id":"resp_service_e2e_mcp","status":"completed","output":[` +
					`{"id":"msg_service_e2e_mcp","type":"message",` +
					`"content":[{"type":"output_text","text":"` + modelText +
					`"}]}],"usage":{"input_tokens":7,"output_tokens":5}}`,
			),
		)
	}))
	defer openai.Close()

	t.Setenv("OMNARA_E2E_API_LOG_LEVEL", "info")
	api := env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithSource(t, ctx, "mcp-initializes-before-generation", strings.Join([]string{
		"name: MCP Service E2E",
		"instruction: Initialize MCP before generation.",
		"model:",
		"  provider_config: openai-prod",
		"  name: service-e2e-local",
		"mcp:",
		"  docs:",
		"    url: " + mcpURL,
		"    permission:",
		"      mode: always_ask",
		"      parameters: {}",
	}, "\n")+"\n")
	agentID := project.createAgent(t, ctx)
	project.createInput(t, ctx, agentID, "run a turn after mcp initialization")
	projectUUID = mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID = mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	worker := env.startWorker(
		t,
		ctx,
		project.projectID,
		serviceWorkerOptions{ProviderConfig: "openai-prod", BaseURL: openai.URL, LogLevel: "info"},
	)

	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var count int
		err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.event_kind = 'model_output' AND block.block_kind = 'text' AND block.text_content = $3`, projectUUID, agentUUID, modelText).
			Scan(&count)
		if err != nil {
			return false, err.Error()
		}
		return count == 1, "assistant output not recorded yet; worker_logs=" + worker.logExcerpt()
	})
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("OpenAI server saw %d requests, want 1", got)
	}
	if !modelSawReadyMCP.Load() {
		t.Fatal("model request did not observe ready mcp connection")
	}

	var state executionstore.MCPConnectionState
	var protocolVersion, mcpSessionID, tools string
	if err := env.db.QueryRow(ctx, `
SELECT connection.state, connection.protocol_version, connection.mcp_session_id,
       connection.tools_snapshot::text
FROM agent_mcp_connections connection
JOIN agents agent ON agent.id = connection.agent_id
WHERE agent.project_id = $1
  AND connection.agent_id = $2
  AND connection.server_key = 'docs'
`, projectUUID, agentUUID).
		Scan(&state, &protocolVersion, &mcpSessionID, &tools); err != nil {
		t.Fatalf("query mcp connection: %v", err)
	}
	if state != executionstore.MCPConnectionStateReady || protocolVersion == "" || mcpSessionID == "" ||
		!strings.Contains(tools, `"greet"`) ||
		!strings.Contains(tools, `"noisy"`) {
		t.Fatalf(
			"unexpected mcp connection state=%q protocol=%q session=%q tools=%s",
			state,
			protocolVersion,
			mcpSessionID,
			tools,
		)
	}
	workerLogFields := []string{
		`"mcp.0.server_key":"docs"`,
		`"mcp.0.initialization.result":"succeeded"`,
		`"mcp.0.initialization.tools_count":2`,
	}
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		workerLogs := worker.fullLogString()
		for _, want := range workerLogFields {
			if !strings.Contains(workerLogs, want) {
				return false, "worker logs missing " + want + "\nlogs=" + workerLogs
			}
		}
		return true, ""
	})
	apiLogs := api.fullLogString()
	for _, want := range []string{`"mcp.count":1`, `"mcp.0.server_key":"docs"`} {
		if !strings.Contains(apiLogs, want) {
			t.Fatalf("api logs missing %s\nlogs=%s", want, apiLogs)
		}
	}
}

func TestServiceE2EMCPToolCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "mcp-tool-call")
	mcpServer := mcptest.NewJSONServer(t)
	mcpURL := mcpServer.URL

	var requestCount atomic.Int64
	var projectUUID string
	var agentUUID string
	const callID = "call_mcp_greet"
	const modelText = "MCP tool call completed"
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer service-e2e-test-key" {
			t.Errorf("unexpected OpenAI auth header %q", auth)
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode OpenAI request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch requestCount.Add(1) {
		case 1:
			if !requestContainsTool(body, "mcp__docs__greet") {
				t.Errorf("first model request did not expose mcp__docs__greet tool: %+v", body["tools"])
				http.Error(w, "missing mcp tool", http.StatusServiceUnavailable)
				return
			}
			_, _ = fmt.Fprintf(
				w,
				`{"id":"resp_service_e2e_mcp_tool_call_1","status":"completed","output":[`+
					`{"id":"fc_service_e2e_mcp_tool_call_1","type":"function_call",`+
					`"call_id":%q,"name":"mcp__docs__greet","arguments":"{\"name\":\"Ada\"}"}],`+
					`"usage":{"input_tokens":11,"output_tokens":4}}`,
				callID,
			)
		case 2:
			if !requestContainsToolResult(body, callID, "Hi Ada") {
				t.Errorf("second model request did not include MCP tool result for %s: %+v", callID, body["input"])
				http.Error(w, "missing mcp result", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write(
				[]byte(
					`{"id":"resp_service_e2e_mcp_tool_call_2","status":"completed","output":[` +
						`{"id":"msg_service_e2e_mcp_tool_call_2","type":"message",` +
						`"content":[{"type":"output_text","text":"` + modelText +
						`"}]}],"usage":{"input_tokens":13,"output_tokens":5}}`,
				),
			)
		default:
			t.Errorf("unexpected extra OpenAI request: %+v", body)
			http.Error(w, "too many requests", http.StatusInternalServerError)
		}
	}))
	defer openai.Close()

	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithSource(t, ctx, "mcp-tool-call", strings.Join([]string{
		"name: MCP Tool Call E2E",
		"instruction: Call the MCP greet tool.",
		"model:",
		"  provider_config: openai-prod",
		"  name: service-e2e-local",
		"mcp:",
		"  docs:",
		"    url: " + mcpURL,
		"    permission:",
		"      mode: always_allow",
		"      parameters: {}",
	}, "\n")+"\n")
	agentID := project.createAgent(t, ctx)
	project.createInput(t, ctx, agentID, "use the docs greet MCP tool for Ada")
	projectUUID = mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID = mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	worker := env.startWorker(
		t,
		ctx,
		project.projectID,
		serviceWorkerOptions{ProviderConfig: "openai-prod", BaseURL: openai.URL, LogLevel: "info"},
	)

	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var count int
		err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.event_kind = 'model_output' AND block.block_kind = 'text' AND block.text_content = $3`, projectUUID, agentUUID, modelText).
			Scan(&count)
		if err != nil {
			return false, err.Error()
		}
		return count == 1, "assistant output not recorded yet; worker_logs=" + worker.logExcerpt()
	})
	if got := requestCount.Load(); got != 2 {
		t.Fatalf("OpenAI server saw %d requests, want 2", got)
	}
	var toolCalls int
	if err := env.db.QueryRow(ctx, `
SELECT count(*)
FROM tool_call_read_projection call
WHERE call.project_id = $1
  AND call.agent_id = $2
  AND call.name = 'mcp__docs__greet'
  AND call.type = 'mcp'
  AND call.state = 'completed'
`, projectUUID, agentUUID).
		Scan(&toolCalls); err != nil {
		t.Fatalf("query mcp tool calls: %v", err)
	}
	if toolCalls != 1 {
		t.Fatalf("completed mcp__docs__greet tool calls = %d, want 1", toolCalls)
	}
}

func TestServiceE2ELiveMCPConfigChanges(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "live-mcp-config-changes")
	mcpServer := mcptest.NewJSONServer(t)

	var requestCount atomic.Int64
	const callID = "call_live_mcp_greet"
	const initialText = "initial turn completed without MCP"
	const addedText = "live MCP tool call completed"
	const removedText = "live MCP removal completed"
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer service-e2e-test-key" {
			t.Errorf("unexpected OpenAI auth header %q", auth)
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode OpenAI request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch requestCount.Add(1) {
		case 1:
			if requestContainsTool(body, "mcp__docs__greet") {
				t.Errorf("initial model request unexpectedly exposed mcp__docs__greet: %+v", body["tools"])
				http.Error(w, "unexpected mcp tool", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(
				`{"id":"resp_live_mcp_initial","status":"completed","output":[` +
					`{"id":"msg_live_mcp_initial","type":"message",` +
					`"content":[{"type":"output_text","text":"` + initialText +
					`"}]}],"usage":{"input_tokens":7,"output_tokens":5}}`,
			))
		case 2:
			if !requestContainsTool(body, "mcp__docs__greet") {
				t.Errorf("model request after live add did not expose mcp__docs__greet: %+v", body["tools"])
				http.Error(w, "missing mcp tool", http.StatusServiceUnavailable)
				return
			}
			_, _ = fmt.Fprintf(
				w,
				`{"id":"resp_live_mcp_added","status":"completed","output":[`+
					`{"id":"fc_live_mcp_added","type":"function_call",`+
					`"call_id":%q,"name":"mcp__docs__greet","arguments":"{\"name\":\"Ada\"}"}],`+
					`"usage":{"input_tokens":11,"output_tokens":4}}`,
				callID,
			)
		case 3:
			if !requestContainsToolResult(body, callID, "Hi Ada") {
				t.Errorf("model continuation did not include live MCP result for %s: %+v", callID, body["input"])
				http.Error(w, "missing mcp result", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(
				`{"id":"resp_live_mcp_result","status":"completed","output":[` +
					`{"id":"msg_live_mcp_result","type":"message",` +
					`"content":[{"type":"output_text","text":"` + addedText +
					`"}]}],"usage":{"input_tokens":13,"output_tokens":5}}`,
			))
		case 4:
			if requestContainsTool(body, "mcp__docs__greet") {
				t.Errorf("model request after live removal exposed mcp__docs__greet: %+v", body["tools"])
				http.Error(w, "stale mcp tool", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(
				`{"id":"resp_live_mcp_removed","status":"completed","output":[` +
					`{"id":"msg_live_mcp_removed","type":"message",` +
					`"content":[{"type":"output_text","text":"` + removedText +
					`"}]}],"usage":{"input_tokens":7,"output_tokens":5}}`,
			))
		default:
			t.Errorf("unexpected extra OpenAI request: %+v", body)
			http.Error(w, "too many requests", http.StatusInternalServerError)
		}
	}))
	defer openai.Close()

	initialYAML := strings.Join([]string{
		"name: Live MCP Config Changes",
		"instruction: Follow the current MCP configuration.",
		"model:",
		"  provider_config: openai-prod",
		"  name: service-e2e-local",
	}, "\n") + "\n"
	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithSource(t, ctx, "live-mcp-config-changes", initialYAML)
	agentID := project.createAgent(t, ctx)
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	waitForOutput := func(text string, worker serviceProcess) {
		t.Helper()
		waitForServiceE2ECondition(t, ctx, func() (bool, string) {
			var outputs, locks, wakeups int
			if err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.event_kind = 'model_output' AND block.block_kind = 'text' AND block.text_content = $3`, projectUUID, agentUUID, text).
				Scan(&outputs); err != nil {
				return false, err.Error()
			}
			if err := env.db.QueryRow(ctx, scopedAgentRuntimeLockCountSQL, projectUUID, agentUUID).Scan(&locks); err != nil {
				return false, err.Error()
			}
			if err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_wakeups wake JOIN agents agent ON agent.id = wake.agent_id WHERE agent.project_id = $1 AND wake.agent_id = $2`, projectUUID, agentUUID).
				Scan(&wakeups); err != nil {
				return false, err.Error()
			}
			return outputs == 1 && locks == 0 && wakeups == 0,
				fmt.Sprintf("output=%q count=%d locks=%d wakeups=%d worker_logs=%s", text, outputs, locks, wakeups, worker.logExcerpt())
		})
	}

	project.createInput(t, ctx, agentID, "complete a turn without MCP")
	worker := env.startWorker(
		t,
		ctx,
		project.projectID,
		serviceWorkerOptions{ProviderConfig: "openai-prod", BaseURL: openai.URL, LogLevel: "info"},
	)
	waitForOutput(initialText, worker)
	worker.stop()

	project.updateConfig(t, ctx, agentID, strings.Join([]string{
		"name: Live MCP Config Changes",
		"instruction: Follow the current MCP configuration.",
		"model:",
		"  provider_config: openai-prod",
		"  name: service-e2e-local",
		"mcp:",
		"  docs:",
		"    url: " + mcpServer.URL,
		"    permission:",
		"      mode: always_allow",
		"      parameters: {}",
	}, "\n")+"\n")
	project.createInput(t, ctx, agentID, "use the newly added docs greet MCP tool for Ada")
	worker = env.startWorker(
		t,
		ctx,
		project.projectID,
		serviceWorkerOptions{ProviderConfig: "openai-prod", BaseURL: openai.URL, LogLevel: "info"},
	)
	waitForOutput(addedText, worker)
	worker.stop()

	project.updateConfig(t, ctx, agentID, initialYAML)
	project.createInput(t, ctx, agentID, "complete a turn after MCP removal")
	worker = env.startWorker(
		t,
		ctx,
		project.projectID,
		serviceWorkerOptions{ProviderConfig: "openai-prod", BaseURL: openai.URL, LogLevel: "info"},
	)
	waitForOutput(removedText, worker)

	if got := requestCount.Load(); got != 4 {
		t.Fatalf("OpenAI server saw %d requests, want 4", got)
	}
	var toolCalls int
	if err := env.db.QueryRow(ctx, `SELECT count(*) FROM tool_call_read_projection WHERE project_id = $1 AND agent_id = $2 AND name = 'mcp__docs__greet' AND type = 'mcp' AND state = 'completed'`, projectUUID, agentUUID).
		Scan(&toolCalls); err != nil {
		t.Fatalf("query live MCP tool calls: %v", err)
	}
	if toolCalls != 1 {
		t.Fatalf("completed live mcp__docs__greet tool calls = %d, want 1", toolCalls)
	}
	var state executionstore.MCPConnectionState
	var protocolVersion, sessionID, capabilities, serverInfo, tools string
	if err := env.db.QueryRow(ctx, `SELECT connection.state, connection.protocol_version, connection.mcp_session_id, connection.server_capabilities::text, connection.server_info::text, connection.tools_snapshot::text FROM agent_mcp_connections connection JOIN agents agent ON agent.id = connection.agent_id WHERE agent.project_id = $1 AND connection.agent_id = $2 AND connection.server_key = 'docs'`, projectUUID, agentUUID).
		Scan(&state, &protocolVersion, &sessionID, &capabilities, &serverInfo, &tools); err != nil {
		t.Fatalf("query removed MCP connection: %v", err)
	}
	if state != executionstore.MCPConnectionStateExpired || protocolVersion != "" || sessionID != "" ||
		capabilities != "{}" || serverInfo != "{}" || tools != "[]" {
		t.Fatalf("removed MCP connection state=%q protocol=%q session=%q capabilities=%s server_info=%s tools=%s", state, protocolVersion, sessionID, capabilities, serverInfo, tools)
	}
}
