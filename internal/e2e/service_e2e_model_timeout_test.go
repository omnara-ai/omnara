//go:build integration && servicee2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/publicid"
)

func TestServiceE2EProviderIdleTimeoutRetriesAndCompletes(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	for _, key := range []string{"OPENAI_API_KEY", "OPENROUTER_API_KEY", "ANTHROPIC_API_KEY"} {
		t.Setenv(key, "service-e2e-test-key")
	}
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "provider-idle-retry")
	const finalText = "Completed after a provider retry."
	var requests atomic.Int64
	failures := make(chan string, 1)
	initialMessages := make(chan string, 1)
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		write := func(delta map[string]any, finish any) {
			chunk := map[string]any{
				"id":      "timeout-response",
				"model":   "service-e2e-openrouter",
				"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
			}
			if finish != nil {
				chunk["usage"] = map[string]any{"prompt_tokens": 10, "completion_tokens": 5}
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: %s\n\n", mustJSONString(chunk))
			w.(http.Flusher).Flush()
		}
		switch requests.Add(1) {
		case 1:
			initialMessages <- string(body["messages"])
			write(
				map[string]any{
					"role": "assistant",
					"tool_calls": []any{
						map[string]any{
							"index":    0,
							"id":       "unfinished-tool",
							"type":     "function",
							"function": map[string]any{"name": "list_machines", "arguments": "{"},
						},
					},
				},
				nil,
			)
			<-r.Context().Done()
		case 2:
			if string(body["messages"]) != <-initialMessages {
				select {
				case failures <- "transport retry altered model history":
				default:
				}
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			// A response with heartbeat bytes can outlast its configured idle window.
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			timer := time.NewTimer(2 * time.Second)
			defer timer.Stop()
			for {
				select {
				case <-ticker.C:
					_, _ = fmt.Fprint(w, ": heartbeat\n\n")
					w.(http.Flusher).Flush()
				case <-timer.C:
					write(map[string]any{"role": "assistant", "content": finalText}, "stop")
					_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
					return
				case <-r.Context().Done():
					return
				}
			}
		default:
			http.Error(w, "unexpected retry", 400)
		}
	}))
	defer router.Close()
	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithToolsAndModelOptions(
		t,
		ctx,
		"provider-idle-retry",
		"openrouter-prod",
		"service-e2e-openrouter",
		nil,
		"list_machines",
	)
	path := "/api/v1/orgs/" + project.orgID + "/model-provider-configs"
	listed := env.requestJSON(t, ctx, http.MethodGet, path, nil, "", project.adminToken, http.StatusOK)
	providerID := ""
	for _, item := range listed["data"].([]any) {
		provider := item.(map[string]any)
		if provider["name"] == "openrouter-prod" {
			providerID = provider["id"].(string)
		}
	}
	if providerID == "" {
		t.Fatal("provider missing")
	}
	updated := env.requestJSON(
		t,
		ctx,
		http.MethodPut,
		path+"/"+providerID,
		map[string]any{"request_timeout_ms": 30000, "idle_timeout_ms": 1000},
		"",
		project.adminToken,
		http.StatusOK,
	)
	if updated["request_timeout_ms"] != float64(30000) || updated["idle_timeout_ms"] != float64(1000) {
		t.Fatalf("timeouts=%v", updated)
	}
	agentID := project.createAgent(t, ctx)
	project.createInput(t, ctx, agentID, "Check available machines.")
	worker := env.startWorker(
		t,
		ctx,
		project.projectID,
		serviceWorkerOptions{ProviderConfig: "openrouter-prod", BaseURL: router.URL},
	)
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	waitForServiceE2ETextOutput(t, ctx, env, projectUUID, agentUUID, finalText, failures, worker)
	waitForServiceE2EAgentIdle(t, ctx, env, projectUUID, agentUUID)
	var failed, succeeded, continued, toolCalls int
	if err := env.db.QueryRow(ctx, `SELECT count(*) FILTER(WHERE state='failed' AND recovery_kind='retry' AND error_kind='transient' AND error_code='provider_idle_timeout'),count(*) FILTER(WHERE state='succeeded') FROM model_call_contexts WHERE agent_id=$1`, agentUUID).
		Scan(&failed, &succeeded); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(ctx, `SELECT count(*) FROM model_outputs WHERE agent_id=$1 AND (continue_after_truncation OR stop_reason='max_tokens')`, agentUUID).
		Scan(&continued); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(ctx, `SELECT count(*) FROM tool_calls WHERE agent_id=$1`, agentUUID).
		Scan(&toolCalls); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || failed != 1 || succeeded != 1 || continued != 0 || toolCalls != 0 {
		t.Fatalf(
			"requests=%d failed=%d succeeded=%d continued=%d tools=%d",
			requests.Load(),
			failed,
			succeeded,
			continued,
			toolCalls,
		)
	}
}
