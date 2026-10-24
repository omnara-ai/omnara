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

// A background command's matching output reaches the agent as a message after
// its run_command call has returned and the agent has finished its turn.
func TestServiceE2EDaemonOutputMatchNotifiesAgent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	const seed = "daemon-output-match"
	env := newServiceE2EEnvironment(t, ctx, seed)
	api := env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithTools(t, ctx, seed, "openai-prod", "service-e2e-local", "run_command")
	machine := project.bootstrapDockerMachine(t, ctx, seed+"-machine")
	project.updateAgentProfileConfigWithMachine(
		t, ctx, seed+"-config", "openai-prod", "service-e2e-local",
		machine.machineName, "/work", nil, nil, "run_command",
	)
	agentID := project.createAgent(t, ctx)
	daemon := env.startDaemonContainer(t, ctx, machine.daemonToken, machine.workdir)
	waitForDaemonRuntime(t, ctx, env, project.orgID, machine.machineID)
	waitForDockerMachineOnline(t, ctx, env, project.orgID, machine.machineID)

	nonce := strings.ToUpper(strings.ReplaceAll(env.seed, "-", "_"))
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
			writeOpenAIFunctionCall(w, failModelRequest, "resp_server", "call_server", "run_command", map[string]any{
				"command": "printf 'booting\\n'; sleep 12; printf 'server listening on 3000\\n'; " +
					"printf 'request ok\\n'; sleep 120",
				"shell":        "sh",
				"cwd":          "/work",
				"detach":       true,
				"notify_match": "listening on [0-9]+",
			})
		case 2:
			result, ok := fakeModelProcessResult(w, body, failModelRequest, "call_server")
			if !ok {
				return
			}
			if result.State != "running" || !strings.Contains(result.Output, "booting") {
				failModelRequest(w, http.StatusBadRequest, "server result = %+v body=%s", result, mustJSONString(body))
				return
			}
			writeOpenAIMessage(w, failModelRequest, "resp_waiting", "WAITING_"+nonce)
		case 3:
			// Only the matching line is sent, not the output around it.
			request := mustJSONString(body)
			if !strings.Contains(request, "printed lines matching `listening on [0-9]+`") ||
				!strings.Contains(request, "```\\nserver listening on 3000\\n```") {
				failModelRequest(w, http.StatusBadRequest, "match notification missing: %s", request)
				return
			}
			writeOpenAIMessage(w, failModelRequest, "resp_seen", "MATCH_SEEN_"+nonce)
		default:
			failModelRequest(w, http.StatusTeapot, "unexpected OpenAI request %d: %s", step, mustJSONString(body))
		}
	}))
	defer openai.Close()

	project.createInput(t, ctx, agentID, "start the server and tell me when it is listening")
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
JOIN agents agent ON agent.id = event.agent_id
JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id
WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.event_kind = 'model_output'
  AND block.block_kind = 'text' AND block.text_content = $3`,
			projectUUID, agentUUID, "MATCH_SEEN_"+nonce,
		).Scan(&count); err != nil {
			return false, err.Error()
		}
		var processes, notifications string
		_ = env.db.QueryRow(ctx, `
SELECT coalesce(string_agg(state || ':match=' || notify_match || ':exit=' || notify_on_exit::text, ','), '')
FROM processes WHERE project_id = $1 AND agent_id = $2`, projectUUID, agentUUID).Scan(&processes)
		_ = env.db.QueryRow(ctx, `
SELECT count(*)::text FROM agent_inputs
WHERE project_id = $1 AND agent_id = $2 AND idempotency_scope = 'process_notification'`,
			projectUUID, agentUUID).Scan(&notifications)
		return count == 1, fmt.Sprintf(
			"match was not seen; requests=%d processes=%s notifications=%s api=%s worker=%s daemon=%s",
			requestCount.Load(), processes, notifications, api.logExcerpt(), worker.logExcerpt(), daemon.logExcerpt(),
		)
	})
	if got := requestCount.Load(); got != 3 {
		t.Fatalf("fake model saw %d requests, want 3", got)
	}
}
