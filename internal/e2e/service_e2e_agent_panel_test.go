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

func TestServiceE2EAssistantUsesOwnMCPWithoutApprovals(t *testing.T) {
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
	const openedPage = "It opened /models."
	const pointed = "The user can see it marked."
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
			if !strings.Contains(string(mustJSON(body["input"])), "This first message is automatic") {
				t.Error("creation did not ask the assistant to introduce itself")
			}
			_, _ = w.Write([]byte(`{"id":"greeting","status":"completed","output":[{"id":"hello",` +
				`"type":"message","content":[{"type":"output_text","text":"Hi, I am your Omnara assistant."}]}],` +
				`"usage":{"input_tokens":10,"output_tokens":5}}`))
			return
		case 2:
			if !strings.Contains(string(mustJSON(body["tools"])), `"type":"tool_search"`) {
				t.Error("expected deferred Omnara discovery")
			}
			_, _ = w.Write([]byte(`{"id":"search_response","status":"completed","output":[` +
				`{"id":"tsc_search","type":"tool_search_call","execution":"client","call_id":"search",` +
				`"status":"completed","arguments":{"pattern":"whoami|projects_create"}}],` +
				`"usage":{"input_tokens":10,"output_tokens":5}}`))
			return
		case 3:
			if !strings.Contains(string(mustJSON(body["input"])), `"type":"tool_search_output"`) ||
				!strings.Contains(string(mustJSON(body["input"])), "whoami") {
				t.Error("discovery did not return authenticated Omnara tools")
			}
			name, callID, arguments = "mcp__omnara__whoami", "identity", map[string]any{}
		case 4:
			if !requestContainsToolResult(body, "identity", project.adminUserID) {
				t.Error("MCP read did not authenticate as the assistant creator")
			}
			name, callID = "mcp__omnara__projects_create", "create"
			arguments = map[string]any{"orgID": project.orgID, "name": projectName}
		case 5:
			if !requestContainsToolResult(body, "create", projectName) {
				t.Error("MCP write did not return created project")
			}
			name, callID, arguments = "dashboard_click", "click", map[string]any{"target": "Models"}
		case 6:
			if !requestContainsToolResult(body, "click", openedPage) {
				t.Error("the dashboard's answer did not reach the assistant")
			}
			name, callID, arguments = "dashboard_point_at", "point", map[string]any{"target": "New provider"}
		case 7:
			if !requestContainsToolResult(body, "point", pointed) {
				t.Error("the dashboard's answer to pointing did not reach the assistant")
			}
			name, callID, arguments = "dashboard_click", "unanswered", map[string]any{"target": "Usage"}
		case 8:
			if !requestContainsToolResult(body, "unanswered", "No result was submitted within 5 seconds.") {
				t.Error("a click with no dashboard to answer did not time out")
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
	worker := env.startWorker(t, ctx, project.projectID,
		serviceWorkerOptions{ProviderConfig: "openai-prod", BaseURL: model.URL, LogLevel: "info"})
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var idle bool
		err := env.db.QueryRow(ctx, `SELECT logical_ready_at IS NULL FROM agent_execution_state
            WHERE agent_id=$1`, agentUUID).Scan(&idle)
		return err == nil && idle && requests.Load() == 1, "assistant did not greet and settle: " + worker.logExcerpt()
	})
	env.startMaintenance(t, ctx)
	project.createInput(t, ctx, agentID, "Check my identity, create a project and show me where models are.")
	// The dashboard answers the first click and the pointing; nothing answers the second click.
	for _, answered := range [][2]string{{"dashboard_click", openedPage}, {"dashboard_point_at", pointed}} {
		name, answer := answered[0], answered[1]
		var call uuid.UUID
		waitForServiceE2ECondition(t, ctx, func() (bool, string) {
			err := env.db.QueryRow(ctx, `SELECT id FROM tool_calls
                WHERE agent_id=$1 AND name=$2 AND state='ready'`, agentUUID, name).Scan(&call)
			return err == nil, "assistant did not call " + name + ": " + worker.logExcerpt()
		})
		callID, err := publicid.Encode(publicid.KindToolCall, call)
		require.NoError(t, err)
		env.requestBrowserJSON(t, ctx, "POST", project.projectPath+"/agents/"+agentID+"/tool-calls/"+callID+"/result",
			map[string]any{
				"outcome":        "succeeded",
				"content_blocks": []map[string]string{{"type": "text", "text": answer}},
			}, "", project.adminSession, project.adminCSRF, 201)
	}
	var count int
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		err := env.db.QueryRow(ctx, `SELECT count(*) FROM projects WHERE name=$1`, projectName).Scan(&count)
		return err == nil && count == 1 && requests.Load() == 8,
			fmt.Sprintf("journey incomplete: projects=%d requests=%d: %s", count, requests.Load(), worker.logExcerpt())
	})
	interactions := env.requestJSON(t, ctx, "GET", project.projectPath+"/agents/"+agentID+"/interactions",
		nil, "", project.adminToken, 200)
	require.Empty(t, testutil.RequireType[[]any](t, interactions["data"]), "no tool asks for approval")
	var machines int
	require.NoError(t, env.db.QueryRow(ctx, `SELECT count(*) FROM machines`).Scan(&machines))
	require.Zero(t, machines)
}
