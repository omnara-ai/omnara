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
)

func TestServiceE2EDockerDaemonFileToolsDeterministic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	tools := []string{"run_command", "read_file", "write_file", "list_files", "search_files"}
	env := newServiceE2EEnvironment(t, ctx, "docker-daemon-file-tools")
	api := env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithTools(
		t, ctx, "docker-daemon-file-tools", "openai-prod", "service-e2e-local", tools...,
	)
	nonce := "FILE_TOOLS_" + strings.ToUpper(strings.ReplaceAll(env.seed, "-", "_"))
	machine := project.bootstrapDockerMachine(t, ctx, "deterministic-file-tools-machine")
	project.updateAgentProfileConfigWithMachine(
		t, ctx, "docker-daemon-file-tools-machine", "openai-prod", "service-e2e-local",
		machine.machineName, "/work", nil, nil, tools...,
	)
	agentID := project.createAgent(t, ctx)
	daemon := env.startDaemonContainer(t, ctx, machine.daemonToken, machine.workdir)
	waitForDaemonRuntime(t, ctx, env, project.orgID, machine.machineID)
	waitForDockerMachineOnline(t, ctx, env, project.orgID, machine.machineID)

	const pngBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAAAAAA6fptVAAAACklEQVR4nGNgAAAAAgABSK+kcQAAAABJRU5ErkJggg=="
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
	call := func(w http.ResponseWriter, step, tool string, input map[string]any) {
		writeOpenAIFunctionCall(w, failModelRequest, "resp_"+step, "call_"+step, tool, input)
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
		contains := func(callID string, values ...string) bool {
			for _, value := range values {
				if !fakeModelToolOutputContains(w, body, failModelRequest, callID, value) {
					return false
				}
			}
			return true
		}
		switch step := requestCount.Add(1); step {
		case 1:
			if fakeModelRequestContainsTools(w, body, failModelRequest, tools...) {
				call(w, "write", "write_file", map[string]any{
					"path": "notes/todo.txt", "content": "alpha\nTODO " + nonce + "\ngamma\n",
				})
			}
		case 2:
			if contains("call_write", "notes/todo.txt", "sha256:", "mch_") {
				call(w, "read", "read_file", map[string]any{
					"path": "/work/notes/todo.txt", "offset_line": 2, "limit_lines": 1,
				})
			}
		case 3:
			if contains("call_read", "TODO "+nonce, "next_offset_line") {
				call(w, "list", "list_files", map[string]any{"pattern": "notes/*"})
			}
		case 4:
			if contains("call_list", "notes/todo.txt", "size_bytes") {
				call(w, "search", "search_files", map[string]any{
					"path": "**/*.txt", "args": []string{"-B", "1", "-e", "TODO [A-Z_0-9]+"},
				})
			}
		case 5:
			if contains("call_search", "TODO "+nonce, "alpha", "line_number") {
				call(w, "image", "run_command", map[string]any{
					"command": "printf %s " + pngBase64 + " | base64 -d > shot.png", "shell": "sh", "wait_ms": 3000,
				})
			}
		case 6:
			if contains("call_image", "exited") {
				call(w, "view", "read_file", map[string]any{"path": "shot.png"})
			}
		case 7:
			if contains("call_view", "image/png") {
				writeOpenAIMessage(w, failModelRequest, "resp_done", "FILE_TOOLS_DONE_"+nonce)
			}
		default:
			failModelRequest(w, http.StatusTeapot, "unexpected OpenAI request %d: %s", step, mustJSONString(body))
		}
	}))
	defer openai.Close()

	project.createInput(t, ctx, agentID, "exercise the machine file tools")
	worker := env.startWorker(t, ctx, project.projectID, serviceWorkerOptions{
		ProviderConfig: "openai-prod", BaseURL: openai.URL, PublicURL: env.publicURL, LogLevel: "info",
	})
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		select {
		case failure := <-modelFailures:
			t.Fatalf("deterministic fake model assertion failed: %s", failure)
		default:
		}
		var count int
		if err := env.db.QueryRow(ctx, `
			SELECT count(*)
			FROM agent_events event
			JOIN content_blocks block
			  ON block.agent_id = event.agent_id
			 AND block.owner_model_output_id = event.model_output_id
			WHERE event.agent_id = $1
			  AND event.event_kind = 'model_output'
			  AND block.block_kind = 'text'
			  AND block.text_content LIKE '%' || $2 || '%'
		`, agentUUID, "FILE_TOOLS_DONE_"+nonce).Scan(&count); err != nil {
			return false, err.Error()
		}
		if count == 1 {
			return true, ""
		}
		var toolStates string
		_ = env.db.QueryRow(ctx, `
SELECT coalesce(string_agg(call.name || ':' || call.state, ',' ORDER BY call.created_at), '')
FROM tool_call_read_projection call
WHERE call.project_id = $1 AND call.agent_id = $2
`, projectUUID, agentUUID).Scan(&toolStates)
		return false, fmt.Sprintf("requests=%d tools=%s api=%s worker=%s daemon=%s",
			requestCount.Load(), toolStates, api.logExcerpt(), worker.logExcerpt(), daemon.logExcerpt())
	})
	if got := requestCount.Load(); got != 7 {
		t.Fatalf("fake model saw %d requests, want 7", got)
	}
}
