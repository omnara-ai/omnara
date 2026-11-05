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

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestServiceE2EAssistantUsesOwnMCPAndApprovesWrites(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "assistant-panel")
	// This journey has a host worker and no containers; its configured public
	// origin must be reachable from that worker as in a real deployment.
	env.publicURL = env.apiURL
	env.publicURLHost = strings.TrimPrefix(env.apiURL, "http://")
	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPI(t, ctx, "assistant-panel", "openai-prod", "service-e2e-local")
	created := env.requestBrowserJSON(t, ctx, "POST", "/api/v1/orgs/"+project.orgID+"/assistant-agents",
		map[string]any{
			"project_id": project.projectID, "request_id": uuid.NewString(),
			"expected_revision": 0, "automatic": false,
		}, "", project.adminSession, project.adminCSRF, 200)
	agentID := testutil.RequireType[string](t, created["agent_id"])
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	var requests atomic.Int64
	const projectName = "Created by assistant"
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode model request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var name, callID string
		var arguments any
		switch requests.Add(1) {
		case 1:
			if !strings.Contains(string(mustJSON(body["tools"])), `"type":"tool_search"`) {
				t.Error("expected deferred Omnara discovery")
			}
			_, _ = w.Write([]byte(`{"id":"search_response","status":"completed","output":[` +
				`{"id":"tsc_search","type":"tool_search_call","execution":"client","call_id":"search",` +
				`"status":"completed","arguments":{"pattern":"whoami|projects_create"}}],` +
				`"usage":{"input_tokens":10,"output_tokens":5}}`))
			return
		case 2:
			if !strings.Contains(string(mustJSON(body["input"])), `"type":"tool_search_output"`) ||
				!strings.Contains(string(mustJSON(body["input"])), "whoami") {
				t.Error("discovery did not return authenticated Omnara tools")
			}
			name, callID, arguments = "mcp__omnara__whoami", "identity", map[string]any{}
		case 3:
			if !requestContainsToolResult(body, "identity", project.adminUserID) {
				t.Error("MCP read did not authenticate as the assistant creator")
			}
			name, callID = "mcp__omnara__projects_create", "create"
			arguments = map[string]any{"orgID": project.orgID, "name": projectName}
		case 4:
			if !requestContainsToolResult(body, "create", projectName) {
				t.Error("approved MCP write did not return created project")
			}
			_, _ = w.Write([]byte(`{"id":"done","status":"completed","output":[{"id":"message",` +
				`"type":"message","content":[{"type":"output_text","text":"Project created."}]}],` +
				`"usage":{"input_tokens":10,"output_tokens":5}}`))
			return
		default:
			t.Error("unexpected additional model request")
			http.Error(w, "unexpected request", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(mustJSON(map[string]any{
			"id": "response_" + callID, "status": "completed",
			"output": []map[string]any{{
				"id": "fc_" + callID, "type": "function_call", "call_id": callID,
				"name": name, "arguments": string(mustJSON(arguments)),
			}}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
		}))
	}))
	defer model.Close()
	var idle bool
	require.NoError(t, env.db.QueryRow(ctx, `SELECT logical_ready_at IS NULL FROM agent_execution_state
        WHERE agent_id=$1`, agentUUID).Scan(&idle))
	require.True(t, idle, "config-only launch resolves its input without scheduling a model turn")
	worker := env.startWorker(t, ctx, project.projectID,
		serviceWorkerOptions{ProviderConfig: "openai-prod", BaseURL: model.URL, LogLevel: "info"})
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var idle bool
		err := env.db.QueryRow(ctx, `SELECT logical_ready_at IS NULL FROM agent_execution_state
            WHERE agent_id=$1`, agentUUID).Scan(&idle)
		return err == nil && idle, "assistant did not settle idle: " + worker.logExcerpt()
	})
	require.Zero(t, requests.Load(), "creation must not start a model turn")
	project.createInput(t, ctx, agentID, "Check my identity and create a project.")
	var interactionID string
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		interactions := env.requestJSON(t, ctx, "GET", project.projectPath+"/agents/"+agentID+"/interactions",
			nil, "", project.adminToken, 200)
		items := testutil.RequireType[[]any](t, interactions["data"])
		if len(items) == 0 {
			return false, "write approval not ready: " + worker.logExcerpt()
		}
		require.Len(t, items, 1)
		interactionID = testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, items[0])["id"])
		return true, ""
	})
	require.Equal(t, int64(3), requests.Load(), "read must run without asking; write must wait")
	var count int
	require.NoError(t, env.db.QueryRow(ctx, `SELECT count(*) FROM projects WHERE name=$1`, projectName).Scan(&count))
	require.Zero(t, count, "unapproved write must not execute")
	require.True(t, project.resolvePermissionInteraction(ctx, agentID, interactionID))
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		err := env.db.QueryRow(ctx, `SELECT count(*) FROM projects WHERE name=$1`, projectName).Scan(&count)
		return err == nil && count == 1 && requests.Load() == 4,
			fmt.Sprintf("approved write incomplete: projects=%d requests=%d", count, requests.Load())
	})
	var machines int
	require.NoError(t, env.db.QueryRow(ctx, `SELECT count(*) FROM machines`).Scan(&machines))
	require.Zero(t, machines)
}
