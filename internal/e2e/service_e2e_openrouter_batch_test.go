//go:build integration && servicee2e

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/publicid"
)

// TestServiceE2EDeterministicOpenRouterBatchModelTurn runs a model turn through
// the real API and worker against a fake OpenRouter Batch API. Locally there is
// no public HTTPS URL, so no callback URL is sent and the turn is resumed by
// the fallback poll; the live batch journey covers real callbacks.
func TestServiceE2EDeterministicOpenRouterBatchModelTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "deterministic-openrouter-batch")
	configuredModelName := serviceE2EOpenRouterBatchModel()
	const modelText = "deterministic openrouter batch completed the turn"

	var mu sync.Mutex
	var submissions []map[string]any
	reads := 0
	syncCalls := 0
	openrouter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/batches":
			if auth := r.Header.Get("Authorization"); auth != "Bearer service-e2e-test-key" {
				t.Errorf("unexpected batch auth header %q", auth)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode batch request: %v", err)
			}
			submissions = append(submissions, body)
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"batch_service_e2e","object":"batch","status":"validating","expires_at":null}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/batches/batch_service_e2e":
			reads++
			if reads == 1 || len(submissions) == 0 {
				_, _ = w.Write([]byte(`{"id":"batch_service_e2e","status":"in_progress","results":null}`))
				return
			}
			requests, _ := submissions[0]["requests"].([]any)
			request, _ := requests[0].(map[string]any)
			customID, _ := request["custom_id"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "batch_service_e2e", "status": "completed",
				"results": []any{map[string]any{
					"id": "batch_req_1", "custom_id": customID, "error": nil,
					"response": map[string]any{
						"status_code": 200, "request_id": "req_service_e2e_batch",
						"body": map[string]any{
							"id": "chatcmpl_service_e2e_batch", "model": configuredModelName,
							"choices": []any{map[string]any{
								"index": 0, "finish_reason": "stop",
								"message": map[string]any{"role": "assistant", "content": modelText},
							}},
							"usage": map[string]any{"prompt_tokens": 7, "completion_tokens": 5, "cost": 0.5},
						},
					},
				}},
			})
		default:
			syncCalls++
			http.NotFound(w, r)
		}
	}))
	defer openrouter.Close()

	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithToolsAndModelOptions(
		t, ctx, "deterministic-openrouter-batch",
		serviceE2EOpenRouterBatchProviderConfig, configuredModelName, nil,
	)
	agentID := project.createAgent(t, ctx)
	project.createInput(t, ctx, agentID, "run one deterministic OpenRouter batch model turn")
	env.startWorker(t, ctx, project.projectID, serviceWorkerOptions{
		ProviderConfig: serviceE2EOpenRouterBatchProviderConfig,
		BaseURL:        openrouter.URL + "/api/v1",
		ExtraEnv:       []string{"OMNARA_MODEL_BATCH_POLL_INTERVAL=2s"},
	})
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)

	// While the batch is pending the runtime lock must stay held, parked well
	// past any worker lease.
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var parked int
		err := env.db.QueryRow(ctx, `
SELECT count(*) FROM model_batch_requests request
JOIN agent_runtime_locks runtime_lock ON runtime_lock.id = request.runtime_lock_id
WHERE request.agent_id = $1 AND request.provider_batch_id = 'batch_service_e2e'
  AND runtime_lock.lease_expires_at > now() + interval '1 hour'`, agentUUID).Scan(&parked)
		if err != nil {
			return false, err.Error()
		}
		var outputs int
		if err := env.db.QueryRow(ctx,
			`SELECT count(*) FROM agent_events WHERE agent_id = $1 AND event_kind = 'model_output'`,
			agentUUID).Scan(&outputs); err != nil {
			return false, err.Error()
		}
		if outputs > 0 {
			t.Fatal("model output was recorded before a parked batch lock was ever observed")
		}
		return parked == 1, "runtime lock not parked on the batch yet"
	})

	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var count int
		err := env.db.QueryRow(
			ctx,
			`SELECT count(*) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.event_kind = 'model_output' AND block.block_kind = 'text' AND block.text_content = $3`,
			projectUUID, agentUUID, modelText,
		).Scan(&count)
		if err != nil {
			return false, err.Error()
		}
		return count == 1, "batch assistant output not recorded yet"
	})
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var locks, requests int
		if err := env.db.QueryRow(ctx, scopedAgentRuntimeLockCountSQL, projectUUID, agentUUID).
			Scan(&locks); err != nil {
			return false, err.Error()
		}
		if err := env.db.QueryRow(ctx,
			`SELECT count(*) FROM model_batch_requests WHERE agent_id = $1`, agentUUID).Scan(&requests); err != nil {
			return false, err.Error()
		}
		return locks == 0 && requests == 0, "runtime lock or batch request still present"
	})

	mu.Lock()
	defer mu.Unlock()
	if len(submissions) != 1 || reads < 2 || syncCalls != 0 {
		t.Fatalf("fake OpenRouter saw %d batch submissions, %d reads, %d other calls; want 1, at least 2, 0",
			len(submissions), reads, syncCalls)
	}
	submission := submissions[0]
	if submission["endpoint"] != "/v1/chat/completions" || submission["model"] != configuredModelName ||
		submission["completion_window"] != "24h" {
		t.Fatalf("batch submission settings = %+v", submission)
	}
	if callbackURL, ok := submission["callback_url"]; ok {
		t.Fatalf("batch submission carried callback URL %v without a public HTTPS URL", callbackURL)
	}
	requests, _ := submission["requests"].([]any)
	if len(requests) != 1 {
		t.Fatalf("batch submission requests = %+v, want one", submission["requests"])
	}
	request, _ := requests[0].(map[string]any)
	body, _ := request["body"].(map[string]any)
	if _, streaming := body["stream"]; streaming || body["model"] != configuredModelName || body["messages"] == nil {
		t.Fatalf("batch request body = %+v", body)
	}
	if customID, _ := request["custom_id"].(string); strings.Trim(customID, "0123456789abcdef-") != "" {
		t.Fatalf("batch custom id %q is not a provider-safe UUID", customID)
	}
}
