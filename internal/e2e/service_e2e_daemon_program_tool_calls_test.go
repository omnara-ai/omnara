//go:build integration && servicee2e

package e2e

import (
	"bytes"
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
)

// TestServiceE2EDaemonProgramToolCalls runs `omnarad call` inside a real
// run_command process: results reach only the command, the call is a child
// tool call with no agent event, and a machine without the flag refuses.
func TestServiceE2EDaemonProgramToolCalls(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	const seed = "daemon-program-tool-calls"
	env := newServiceE2EEnvironment(t, ctx, seed)
	api := env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithTools(
		t, ctx, seed, "openai-prod", "service-e2e-local", "run_command", "storage_list",
	)
	machine := project.bootstrapDockerMachine(t, ctx, seed+"-machine")
	// Top-level lines after the machine source enable the agent switch.
	project.updateAgentProfileConfigWithMachine(
		t, ctx, seed+"-config", "openai-prod", "service-e2e-local", machine.machineName, "/work",
		[]string{"programmatic_tool_calls:", "  enabled: true"}, nil, "run_command", "storage_list",
	)
	machinePath := "/api/v1/orgs/" + project.orgID + "/machines/" + machine.machineID
	setMachineFlag := func(ctx context.Context, enabled bool) error {
		request, err := env.newAPIRequest(ctx, http.MethodPatch, machinePath,
			bytes.NewReader(mustJSON(map[string]bool{"programmatic_tool_calls": enabled})))
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+project.adminToken)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return err
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("update machine: %s", response.Status)
		}
		return nil
	}
	if err := setMachineFlag(ctx, true); err != nil {
		t.Fatal(err)
	}
	agentID := project.createAgent(t, ctx)
	daemon := env.startDaemonContainer(t, ctx, machine.daemonToken, machine.workdir)
	waitForDaemonRuntime(t, ctx, env, project.orgID, machine.machineID)
	waitForDockerMachineOnline(t, ctx, env, project.orgID, machine.machineID)

	nonce := strings.ToUpper(strings.ReplaceAll(env.seed, "-", "_"))
	script := `"$OMNARA_BIN" call storage_list '{"pattern":"/artifacts"}'; echo "list_exit=$?"; ` +
		`"$OMNARA_BIN" call tool_search '{"pattern":"storage_l"}'; echo "search_exit=$?"`
	var requestCount atomic.Int64
	modelFailures := make(chan string, 1)
	failModelRequest := func(w http.ResponseWriter, status int, format string, args ...any) {
		message := fmt.Sprintf(format, args...)
		select {
		case modelFailures <- message:
		default:
		}
		http.Error(w, message, status)
	}
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			failModelRequest(w, http.StatusBadRequest, "decode OpenAI request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch step := requestCount.Add(1); step {
		case 1:
			writeOpenAIFunctionCall(w, failModelRequest, "resp_script", "call_script", "run_command", map[string]any{
				"command": script, "shell": "sh", "cwd": "/work", "wait_ms": 30000,
			})
		case 2:
			result, ok := fakeModelProcessResult(w, body, failModelRequest, "call_script")
			if !ok {
				return
			}
			if result.State != "exited" || !strings.Contains(result.Output, "list_exit=0") ||
				!strings.Contains(result.Output, `"entries"`) || !strings.Contains(result.Output, "search_exit=0") ||
				!strings.Contains(result.Output, `"name":"storage_list"`) {
				failModelRequest(w, http.StatusBadRequest, "script result = %+v", result)
				return
			}
			if err := setMachineFlag(r.Context(), false); err != nil {
				failModelRequest(w, http.StatusInternalServerError, "disable machine flag: %v", err)
				return
			}
			writeOpenAIFunctionCall(w, failModelRequest, "resp_disabled", "call_disabled", "run_command", map[string]any{
				// Without the flag the daemon injects no tool call environment.
				"command": `echo "bin=${OMNARA_BIN:-unset}"; /root/.omnarad/bin/omnarad call storage_list '{}'; ` +
					`echo "disabled_exit=$?"`,
				"shell": "sh", "cwd": "/work", "wait_ms": 30000,
			})
		case 3:
			result, ok := fakeModelProcessResult(w, body, failModelRequest, "call_disabled")
			if !ok {
				return
			}
			if !strings.Contains(result.Output, "bin=unset") || !strings.Contains(result.Output, "disabled_exit=2") {
				failModelRequest(w, http.StatusBadRequest, "disabled result = %+v", result)
				return
			}
			writeOpenAIMessage(w, failModelRequest, "resp_done", "DONE_"+nonce)
		default:
			failModelRequest(w, http.StatusTeapot, "unexpected OpenAI request %d: %s", step, mustJSONString(body))
		}
	}))
	defer openai.Close()

	project.createInput(t, ctx, agentID, "list my files from a script")
	project.startPermissionAutoApprover(t, ctx, agentID)
	worker := env.startWorker(t, ctx, project.projectID, serviceWorkerOptions{
		ProviderConfig: "openai-prod",
		BaseURL:        openai.URL,
	})
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		select {
		case failure := <-modelFailures:
			t.Fatalf("fake model assertion failed: %s", failure)
		default:
		}
		var count int
		if err := env.db.QueryRow(ctx, `
SELECT count(*)
FROM agent_events event
JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id
WHERE event.agent_id = $1 AND event.event_kind = 'model_output'
  AND block.block_kind = 'text' AND block.text_content = $2`,
			agentUUID, "DONE_"+nonce,
		).Scan(&count); err != nil {
			return false, err.Error()
		}
		return count == 1, fmt.Sprintf("script did not finish; requests=%d api=%s worker=%s daemon=%s",
			requestCount.Load(), api.logExcerpt(), worker.logExcerpt(), daemon.logExcerpt())
	})
	var children, childEvents int
	var outcome string
	if err := env.db.QueryRow(ctx, `
SELECT count(*), coalesce(min(result.outcome), '')
FROM tool_calls child
JOIN tool_calls parent ON parent.agent_id = child.agent_id AND parent.id = child.parent_tool_call_id
JOIN agents agent ON agent.id = child.agent_id
LEFT JOIN tool_call_results result ON result.agent_id = child.agent_id AND result.tool_call_id = child.id
WHERE agent.project_id = $1 AND child.agent_id = $2 AND child.caller = 'program'
  AND parent.name = 'run_command' AND child.name = 'storage_list'`,
		projectUUID, agentUUID).Scan(&children, &outcome); err != nil {
		t.Fatal(err)
	}
	if children != 1 || outcome != "succeeded" {
		t.Fatalf("program tool calls = %d outcome %q, want one succeeded storage_list child", children, outcome)
	}
	if err := env.db.QueryRow(ctx, `
SELECT count(*)
FROM agent_events event
JOIN tool_call_results result ON result.agent_id = event.agent_id AND result.id = event.tool_call_result_id
JOIN tool_calls call ON call.agent_id = result.agent_id AND call.id = result.tool_call_id
WHERE event.agent_id = $1 AND call.caller = 'program'`, agentUUID).Scan(&childEvents); err != nil {
		t.Fatal(err)
	}
	if childEvents != 0 {
		t.Fatalf("program tool calls wrote %d agent events, want none", childEvents)
	}
	if got := requestCount.Load(); got != 3 {
		t.Fatalf("fake model saw %d requests, want 3", got)
	}
}
