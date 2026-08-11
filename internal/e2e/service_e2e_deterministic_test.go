//go:build integration && servicee2e

package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

func TestServiceE2EDeterministicWorkerRunsModelTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "deterministic-worker-model-turn")
	var requestCount atomic.Int64
	const modelText = "deterministic worker completed the turn"
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
		if body["model"] != "service-e2e-local" {
			t.Errorf("unexpected model in OpenAI request: %+v", body)
		}
		requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(
			[]byte(
				`{"id":"resp_service_e2e_deterministic","status":"completed","output":[` +
					`{"id":"msg_service_e2e_deterministic","type":"message",` +
					`"content":[{"type":"output_text","text":"` + modelText +
					`"}]}],"usage":{"input_tokens":7,"output_tokens":5}}`,
			),
		)
	}))
	defer openai.Close()

	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPI(t, ctx, "deterministic", "openai-prod", "service-e2e-local")
	agentID := project.createAgent(t, ctx)
	project.createInput(t, ctx, agentID, "run one deterministic model turn")
	env.startWorker(
		t,
		ctx,
		project.projectID,
		serviceWorkerOptions{ProviderConfig: "openai-prod", BaseURL: openai.URL},
	)
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)

	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var count int
		err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.event_kind = 'model_output' AND block.block_kind = 'text' AND block.text_content = $3`, projectUUID, agentUUID, modelText).
			Scan(&count)
		if err != nil {
			return false, err.Error()
		}
		return count == 1, "assistant output not recorded yet"
	})
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("deterministic OpenAI server saw %d requests, want 1", got)
	}
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var locks int
		if err := env.db.QueryRow(ctx, scopedAgentRuntimeLockCountSQL, projectUUID, agentUUID).
			Scan(&locks); err != nil {
			return false, err.Error()
		}
		return locks == 0, "runtime lock still present"
	})
}

func TestServiceE2EDeterministicOpenRouterRunsChatCompletionsModelTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "deterministic-openrouter-model-turn")
	var requestCount atomic.Int64
	const configuredModelName = "service-e2e-openrouter"
	const modelText = "deterministic openrouter worker completed the turn"
	openrouter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer service-e2e-test-key" {
			t.Errorf("unexpected OpenRouter auth header %q", auth)
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("HTTP-Referer") == "" ||
			r.Header.Get("X-OpenRouter-Title") == "" ||
			r.Header.Get("X-OpenRouter-Categories") == "" {
			t.Errorf("missing OpenRouter attribution headers: %+v", r.Header)
			http.Error(w, "missing attribution", http.StatusBadRequest)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode OpenRouter request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if body["model"] != configuredModelName {
			t.Errorf("unexpected model in OpenRouter request: %+v", body)
		}
		for _, field := range []string{"store", "parallel_tool_calls", "prompt_cache_retention", "max_tokens"} {
			if _, ok := body[field]; ok {
				t.Errorf("OpenRouter request should omit %s: %+v", field, body)
			}
		}
		provider, ok := body["provider"].(map[string]any)
		if !ok {
			t.Errorf("OpenRouter request omitted provider options: %+v", body)
		} else if provider["data_collection"] != "deny" || provider["sort"] != "latency" {
			t.Errorf("unexpected OpenRouter provider options: %+v", provider)
		} else if only, ok := provider["only"].([]any); !ok || len(only) != 1 || only[0] != "openai" {
			t.Errorf("unexpected OpenRouter provider.only: %+v", provider)
		}
		requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(
			[]byte(
				`{"id":"chatcmpl_service_e2e_openrouter","model":"` + configuredModelName + `","choices":[{"index":0,"message":{"role":"assistant","content":"` + modelText + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":5,"cost":0.0000125}}`,
			),
		)
	}))
	defer openrouter.Close()

	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithToolsAndModelOptions(
		t,
		ctx,
		"deterministic-openrouter",
		"openrouter-prod",
		configuredModelName,
		map[string]serviceE2EConfiguredModelOptions{
			configuredModelName: {
				APIVariantOptions: map[string]any{
					"provider": map[string]any{
						"only":            []string{"openai"},
						"data_collection": "deny",
						"sort":            "latency",
					},
				},
			},
		},
	)
	agentID := project.createAgent(t, ctx)
	project.createInput(t, ctx, agentID, "run one deterministic OpenRouter model turn")
	env.startWorker(
		t,
		ctx,
		project.projectID,
		serviceWorkerOptions{ProviderConfig: "openrouter-prod", BaseURL: openrouter.URL},
	)
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)

	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var count int
		err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.event_kind = 'model_output' AND block.block_kind = 'text' AND block.text_content = $3`, projectUUID, agentUUID, modelText).
			Scan(&count)
		if err != nil {
			return false, err.Error()
		}
		return count == 1, "assistant output not recorded yet"
	})
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("deterministic OpenRouter server saw %d requests, want 1", got)
	}
	var costRows int
	if err := env.db.QueryRow(ctx, `
SELECT count(*)
FROM model_call_contexts context
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.provider_response_id = 'chatcmpl_service_e2e_openrouter'
  AND context.provider_reported_cost_usd = 0.0000125
`, projectUUID, agentUUID).Scan(&costRows); err != nil {
		t.Fatalf("query OpenRouter provider-reported cost: %v", err)
	}
	if costRows != 1 {
		t.Fatalf("OpenRouter contexts with recorded cost = %d, want 1", costRows)
	}
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var locks int
		if err := env.db.QueryRow(ctx, scopedAgentRuntimeLockCountSQL, projectUUID, agentUUID).
			Scan(&locks); err != nil {
			return false, err.Error()
		}
		return locks == 0, "runtime lock still present"
	})
}

func TestServiceE2EConfigChangeAffectsNextModelContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "deterministic-config-change-runtime")

	type seenRequest struct {
		Model        string
		Instructions string
		Input        string
		Tools        string
	}
	var requestsMu sync.Mutex
	var requests []seenRequest
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode OpenAI request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		providerModelSlug, _ := body["model"].(string)
		instructions, _ := body["instructions"].(string)
		inputText := mustJSONString(body["input"])
		tools := mustJSONString(body["tools"])
		requestsMu.Lock()
		requests = append(requests, seenRequest{
			Model:        providerModelSlug,
			Instructions: instructions,
			Input:        inputText,
			Tools:        tools,
		})
		count := len(requests)
		requestsMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(
			[]byte(
				fmt.Sprintf(
					`{"id":"resp_config_change_%d","status":"completed","output":[`+
						`{"id":"msg_config_change_%d","type":"message",`+
						`"content":[{"type":"output_text","text":"config change response %d"}]}],`+
						`"usage":{"input_tokens":7,"output_tokens":5}}`,
					count,
					count,
					count,
				),
			),
		)
	}))
	defer openai.Close()

	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithSource(t, ctx, "deterministic-config-change-runtime", strings.Join([]string{
		"instruction: Initial runtime instruction.",
		"model:",
		"  provider_config: openai-prod",
		"  name: service-e2e-local",
	}, "\n")+"\n")
	agentID := project.createAgent(t, ctx)
	project.createInput(t, ctx, agentID, "first turn before config change")
	firstWorker := env.startWorker(
		t,
		ctx,
		project.projectID,
		serviceWorkerOptions{ProviderConfig: "openai-prod", BaseURL: openai.URL},
	)
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var count int
		err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.event_kind = 'model_output' AND block.text_content = 'config change response 1'`, projectUUID, agentUUID).
			Scan(&count)
		if err != nil {
			return false, err.Error()
		}
		return count == 1, "first config-change runtime output not recorded; worker_logs=" + firstWorker.logExcerpt()
	})
	firstWorker.stop()

	updatedYAML := strings.Join([]string{
		"instruction: Updated runtime instruction.",
		"model:",
		"  provider_config: openai-prod",
		"  name: service-e2e-alt",
		"tools:",
		"  web_search: {}",
	}, "\n") + "\n"
	project.updateConfig(t, ctx, agentID, updatedYAML)
	project.createInput(t, ctx, agentID, "second turn after config change")
	secondWorker := env.startWorker(
		t,
		ctx,
		project.projectID,
		serviceWorkerOptions{ProviderConfig: "openai-prod", BaseURL: openai.URL},
	)
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var count int
		err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.event_kind = 'model_output' AND block.text_content = 'config change response 2'`, projectUUID, agentUUID).
			Scan(&count)
		if err != nil {
			return false, err.Error()
		}
		return count == 1, "second config-change runtime output not recorded; worker_logs=" + secondWorker.logExcerpt()
	})

	requestsMu.Lock()
	gotRequests := append([]seenRequest(nil), requests...)
	requestsMu.Unlock()
	if len(gotRequests) != 2 {
		t.Fatalf("OpenAI server saw %d requests, want 2: %+v", len(gotRequests), gotRequests)
	}
	if gotRequests[0].Model != "service-e2e-local" ||
		!strings.Contains(gotRequests[0].Instructions, "Initial runtime instruction.") ||
		strings.Contains(gotRequests[0].Input, "Agent configuration changed") ||
		strings.Contains(gotRequests[0].Tools, "web_search") {
		t.Fatalf("first request did not use initial config: %+v", gotRequests[0])
	}
	if gotRequests[1].Model != "service-e2e-alt" ||
		!strings.Contains(gotRequests[1].Instructions, "Updated runtime instruction.") ||
		!strings.Contains(gotRequests[1].Tools, "web_search") {
		t.Fatalf("second request did not use updated config: %+v", gotRequests[1])
	}
	var contexts, configIDs int
	if err := env.db.QueryRow(ctx, `SELECT count(*), count(DISTINCT agent_config_id) FROM model_call_contexts WHERE project_id = $1 AND agent_id = $2 AND state = 'succeeded'`, projectUUID, agentUUID).
		Scan(&contexts, &configIDs); err != nil {
		t.Fatalf("query model contexts: %v", err)
	}
	if contexts != 2 || configIDs != 2 {
		t.Fatalf("model contexts=%d distinct configs=%d, want 2 and 2", contexts, configIDs)
	}
}

func TestServiceE2EDeterministicConfigChangeWaitsForOpenToolInteraction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "deterministic-config-change-open-tool")
	var openAIRequestCount, anthropicRequestCount atomic.Int64
	var anthropicRequestBeforeResolution atomic.Bool
	var handlerInteractionIdentity atomic.Value
	var projectUUID, agentUUID, interactionUUID string
	const finalText = "changed-format question answer continued the same turn"
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
		if body["model"] != "service-e2e-local" {
			t.Errorf("unexpected model in OpenAI request: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		switch openAIRequestCount.Add(1) {
		case 1:
			if !requestContainsTool(body, "ask_question") {
				t.Errorf("first request did not expose ask_question tool: %+v", body)
			}
			_, _ = w.Write(
				[]byte(
					`{"id":"resp_service_e2e_question","status":"completed","output":[{"type":"function_call","call_id":"call_question","name":"ask_question","arguments":"{\"questions\":[{\"prompt\":\"Ship the change?\",\"options\":[{\"label\":\"Yes\"},{\"label\":\"No\"}]}]}"}],"usage":{"input_tokens":9,"output_tokens":3}}`,
				),
			)
		default:
			t.Errorf("unexpected extra OpenAI request: %+v", body)
			http.Error(w, "unexpected request", http.StatusTeapot)
		}
	}))
	defer openai.Close()
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			http.NotFound(w, r)
			return
		}
		anthropicRequestCount.Add(1)
		if auth := r.Header.Get("x-api-key"); auth != "service-e2e-test-key" {
			t.Errorf("unexpected Anthropic auth header %q", auth)
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode Anthropic request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if body["model"] != "service-e2e-claude" {
			t.Errorf("unexpected model in Anthropic request: %+v", body)
			http.Error(w, "wrong model", http.StatusBadRequest)
			return
		}
		identity, ok := handlerInteractionIdentity.Load().([3]string)
		if !ok {
			anthropicRequestBeforeResolution.Store(true)
			http.Error(w, "interaction identity not available", http.StatusConflict)
			return
		}
		var interactionState string
		if err := env.db.QueryRow(ctx, `SELECT state FROM agent_interaction_read_projection WHERE project_id = $1 AND agent_id = $2 AND id = $3`, identity[0], identity[1], identity[2]).
			Scan(&interactionState); err != nil {
			t.Errorf("query interaction state from Anthropic request: %v", err)
			http.Error(w, "interaction unavailable", http.StatusServiceUnavailable)
			return
		}
		if interactionState != "resolved" {
			anthropicRequestBeforeResolution.Store(true)
			http.Error(w, "interaction is not resolved", http.StatusConflict)
			return
		}
		requestText := mustJSONString(body)
		if !strings.Contains(requestText, `"type":"tool_use"`) ||
			!strings.Contains(requestText, `"type":"tool_result"`) ||
			!strings.Contains(requestText, "ask_question") ||
			!strings.Contains(requestText, `"label":"Yes"`) {
			t.Errorf("Anthropic continuation did not rebuild the OpenAI tool exchange: %s", requestText)
			http.Error(w, "missing tool exchange", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		writeAnthropicMessage(w, "msg_service_e2e_question_final", finalText)
	}))
	defer anthropic.Close()

	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithTools(
		t,
		ctx,
		"deterministic-config-change-open-tool",
		"openai-prod",
		"service-e2e-local",
		"ask_question",
	)
	agentID := project.createAgent(t, ctx)
	project.createInput(t, ctx, agentID, "ask a structured question before continuing")
	projectUUID = mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID = mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	env.updateServiceE2EProviderBaseURL(t, ctx, project.projectID, "anthropic-prod", anthropic.URL)
	worker := env.startWorker(
		t,
		ctx,
		project.projectID,
		serviceWorkerOptions{ProviderConfig: "openai-prod", BaseURL: openai.URL},
	)

	var interactionID string
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		listed := env.requestJSON(
			t,
			ctx,
			http.MethodGet,
			project.projectPath+"/agents/"+agentID+"/interactions?state=open",
			nil,
			"",
			project.adminToken,
			http.StatusOK,
		)
		data, ok := listed["data"].([]any)
		if !ok {
			return false, "interaction list response did not contain data"
		}
		if len(data) == 0 {
			return false, "structured question not open yet"
		}
		item, ok := data[0].(map[string]any)
		if !ok {
			return false, "interaction item was not an object"
		}
		if item["interaction_kind"] != "question" || item["state"] != "open" {
			return false, "unexpected interaction: " + mustJSONString(item)
		}
		interactionID = item["id"].(string)
		return true, ""
	})
	interactionUUID = mustDecodeServiceE2EPublicID(t, publicid.KindAgentInteraction, interactionID)
	handlerInteractionIdentity.Store([3]string{projectUUID, agentUUID, interactionUUID})

	project.updateConfig(t, ctx, agentID, strings.Join([]string{
		"instruction: Continue only after the open question is answered.",
		"model:",
		"  provider_config: anthropic-prod",
		"  name: service-e2e-claude",
		"tools:",
		"  ask_question: {}",
		"",
	}, "\n"))
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var configEvents, openInteractions, modelContexts, locks, wakeups int
		if err := env.db.QueryRow(ctx, `
SELECT count(*)
FROM agent_events event
JOIN agent_inputs input
  ON input.agent_id = event.agent_id
 AND input.id = event.agent_input_id
WHERE input.project_id = $1
  AND event.agent_id = $2
  AND event.event_kind = 'agent_input'
  AND input.input_kind = 'config_change'`, projectUUID, agentUUID).
			Scan(&configEvents); err != nil {
			return false, err.Error()
		}
		if err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_interaction_read_projection WHERE project_id = $1 AND agent_id = $2 AND state = 'open'`, projectUUID, agentUUID).
			Scan(&openInteractions); err != nil {
			return false, err.Error()
		}
		if err := env.db.QueryRow(ctx, `SELECT count(*) FROM model_call_contexts WHERE project_id = $1 AND agent_id = $2`, projectUUID, agentUUID).
			Scan(&modelContexts); err != nil {
			return false, err.Error()
		}
		if err := env.db.QueryRow(ctx, scopedAgentRuntimeLockCountSQL, projectUUID, agentUUID).
			Scan(&locks); err != nil {
			return false, err.Error()
		}
		if err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_wakeups wake JOIN agents agent ON agent.id = wake.agent_id WHERE agent.project_id = $1 AND wake.agent_id = $2`, projectUUID, agentUUID).
			Scan(&wakeups); err != nil {
			return false, err.Error()
		}
		if anthropicRequestCount.Load() > 0 {
			return true, ""
		}
		ready := configEvents == 2 && openInteractions == 1 && modelContexts == 1 &&
			locks == 0 && wakeups == 0
		return ready, fmt.Sprintf(
			"config/tool boundary not settled configs=%d interactions=%d contexts=%d locks=%d wakeups=%d anthropic_requests=%d logs=%s",
			configEvents,
			openInteractions,
			modelContexts,
			locks,
			wakeups,
			anthropicRequestCount.Load(),
			worker.logExcerpt(),
		)
	})
	if anthropicRequestBeforeResolution.Load() || anthropicRequestCount.Load() != 0 {
		t.Fatalf(
			"Anthropic was called before the open tool interaction resolved: requests=%d logs=%s",
			anthropicRequestCount.Load(),
			worker.logExcerpt(),
		)
	}

	resolved := env.requestJSON(
		t,
		ctx,
		http.MethodPost,
		project.projectPath+"/agents/"+agentID+"/interactions/"+interactionID+"/resolve",
		map[string]any{
			"answers": []map[string]any{{"option_indices": []int{0}}},
		},
		"",
		project.adminToken,
		http.StatusOK,
	)
	if resolved["state"] != "resolved" {
		t.Fatalf("interaction resolution state = %v, want resolved", resolved["state"])
	}

	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var count int
		err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.event_kind = 'model_output' AND block.block_kind = 'text' AND block.text_content = $3`, projectUUID, agentUUID, finalText).
			Scan(&count)
		if err != nil {
			return false, err.Error()
		}
		return count == 1, "final assistant output not recorded yet"
	})
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var toolCalls int
		if err := env.db.QueryRow(ctx, `
SELECT count(*)
FROM tool_call_read_projection call
JOIN agent_interaction_read_projection interaction ON interaction.agent_id = call.agent_id
  AND interaction.tool_call_id = call.id
WHERE call.project_id = $1
  AND call.agent_id = $2
  AND call.name = 'ask_question'
  AND call.state = 'completed'
  AND interaction.id = $3
`, projectUUID, agentUUID, interactionUUID).
			Scan(&toolCalls); err != nil {
			return false, err.Error()
		}
		return toolCalls == 1, "ask_question tool call is not completed yet"
	})
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var locks, wakeups int
		if err := env.db.QueryRow(ctx, scopedAgentRuntimeLockCountSQL, projectUUID, agentUUID).
			Scan(&locks); err != nil {
			return false, err.Error()
		}
		if err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_wakeups wake JOIN agents agent ON agent.id = wake.agent_id WHERE agent.project_id = $1 AND wake.agent_id = $2`, projectUUID, agentUUID).
			Scan(&wakeups); err != nil {
			return false, err.Error()
		}
		return locks == 0 && wakeups == 0, "runtime lock or wakeup still present"
	})
	var contextStates string
	var contextCount, distinctTurns int
	if err := env.db.QueryRow(ctx, `
SELECT count(*),
       count(DISTINCT context_turn.turn_id),
       coalesce(string_agg(
         context.state || ':' || provider_config.api_format,
         ',' ORDER BY context.input_event_sequence, context.attempt_number, context.created_at, context.id
       ), '')
FROM model_call_contexts context
JOIN model_call_context_turns context_turn
  ON context_turn.project_id = context.project_id
 AND context_turn.agent_id = context.agent_id
 AND context_turn.model_call_context_id = context.id
JOIN configured_model_revisions revision
  ON revision.org_id = context.org_id
 AND revision.id = context.configured_model_revision_id
JOIN model_provider_configs provider_config
  ON provider_config.org_id = revision.org_id
 AND provider_config.id = revision.model_provider_config_id
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.operation_kind = 'normal'`, projectUUID, agentUUID).Scan(
		&contextCount,
		&distinctTurns,
		&contextStates,
	); err != nil {
		t.Fatalf("query changed-format tool contexts: %v", err)
	}
	if anthropicRequestBeforeResolution.Load() || openAIRequestCount.Load() != 1 ||
		anthropicRequestCount.Load() != 1 || contextCount != 2 || distinctTurns != 1 ||
		contextStates != "succeeded:openai-responses,succeeded:anthropic-messages" {
		t.Fatalf(
			"changed-format tool continuation early_anthropic=%v requests=%d/%d contexts=%d turns=%d states=%q",
			anthropicRequestBeforeResolution.Load(),
			openAIRequestCount.Load(),
			anthropicRequestCount.Load(),
			contextCount,
			distinctTurns,
			contextStates,
		)
	}
}

func TestServiceE2EDeterministicBacklogSteeringCancelAndQueuedContinuation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "deterministic-conversation-controls")

	var requestCount atomic.Int64
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode OpenAI request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		requestText := mustJSONString(body["input"])
		w.Header().Set("Content-Type", "application/json")
		switch requestCount.Add(1) {
		case 1:
			if !strings.Contains(requestText, "steering priority message") {
				t.Errorf("first request did not admit steering input: %+v", body)
			}
			if strings.Contains(requestText, "queued second message") || strings.Contains(requestText, "queued third message") ||
				strings.Contains(requestText, "queued canceled message") {
				t.Errorf("first request should not admit queued backlog while steering exists: %+v", body)
			}
			writeOpenAIMessage(w, nil, "resp_steering_controls", "steering turn complete")
		case 2:
			if strings.Contains(requestText, "queued canceled message") {
				t.Errorf("second request included canceled backlog input: %+v", body)
			}
			if !strings.Contains(requestText, "queued third message") || strings.Contains(requestText, "queued second message") {
				t.Errorf("second request should admit only the reordered front queued input: %+v", body)
			}
			writeOpenAIMessage(w, nil, "resp_queued_controls_first", "queued third turn complete")
		case 3:
			if strings.Contains(requestText, "queued canceled message") {
				t.Errorf("third request included canceled backlog input: %+v", body)
			}
			if !strings.Contains(requestText, "queued second message") {
				t.Errorf("third request should admit the remaining queued input: %+v", body)
			}
			writeOpenAIMessage(w, nil, "resp_queued_controls_second", "queued second turn complete")
		default:
			t.Errorf("unexpected extra OpenAI request: %+v", body)
			http.Error(w, "unexpected request", http.StatusTeapot)
		}
	}))
	defer openai.Close()

	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPI(
		t,
		ctx,
		"deterministic-conversation-controls",
		"openai-prod",
		"service-e2e-local",
	)
	agentID := project.createAgent(t, ctx)
	canceledInputID := project.createInputWithDeliveryMode(t, ctx, agentID, "queued canceled message", "")
	secondInputID := project.createInputWithDeliveryMode(t, ctx, agentID, "queued second message", "")
	thirdInputID := project.createInputWithDeliveryMode(t, ctx, agentID, "queued third message", "")
	project.createInputWithDeliveryMode(t, ctx, agentID, "steering priority message", executionstore.DeliveryModeSteering)
	project.env.requestJSON(
		t,
		ctx,
		http.MethodPost,
		project.projectPath+"/agents/"+agentID+"/inputs/"+thirdInputID+"/move",
		map[string]any{"position": "front"},
		"",
		project.adminToken,
		http.StatusOK,
	)
	project.env.requestJSON(
		t,
		ctx,
		http.MethodPost,
		project.projectPath+"/agents/"+agentID+"/inputs/"+canceledInputID+"/cancel",
		nil,
		"",
		project.adminToken,
		http.StatusOK,
	)

	backlog := project.env.requestJSON(
		t,
		ctx,
		http.MethodGet,
		project.projectPath+"/agents/"+agentID+"/inputs/backlog",
		nil,
		"",
		project.adminToken,
		http.StatusOK,
	)
	backlogData := backlog["data"].([]any)
	if len(backlogData) != 2 || backlogData[0].(map[string]any)["id"] != thirdInputID ||
		backlogData[1].(map[string]any)["id"] != secondInputID {
		t.Fatalf("backlog before worker = %+v, want reordered third then second without canceled input", backlogData)
	}

	env.startWorker(
		t,
		ctx,
		project.projectID,
		serviceWorkerOptions{ProviderConfig: "openai-prod", BaseURL: openai.URL},
	)
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var outputs, canceledInputs int
		if err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.event_kind = 'model_output' AND block.block_kind = 'text' AND block.text_content IN ('steering turn complete', 'queued third turn complete', 'queued second turn complete')`, projectUUID, agentUUID).
			Scan(&outputs); err != nil {
			return false, err.Error()
		}
		canceledUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgentInput, canceledInputID)
		if err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_inputs WHERE project_id = $1 AND agent_id = $2 AND id = $3 AND state = 'canceled'`, projectUUID, agentUUID, canceledUUID).
			Scan(&canceledInputs); err != nil {
			return false, err.Error()
		}
		return outputs == 3 && canceledInputs == 1, "conversation-control outputs or canceled input not ready"
	})
	if got := requestCount.Load(); got != 3 {
		t.Fatalf("fake OpenAI server saw %d requests, want 3", got)
	}
}

type deterministicProject struct {
	env          *serviceE2EEnvironment
	orgID        string
	projectID    string
	adminToken   string
	adminSession string
	adminCSRF    string
	adminUserID  string
	projectPath  string
	agentID      string
	configID     string
}

func (e *serviceE2EEnvironment) bootstrapProjectViaAPI(
	t *testing.T,
	ctx context.Context,
	seed string,
	providerConfig string,
	configuredModelName string,
) deterministicProject {
	return e.bootstrapProjectViaAPIWithTools(t, ctx, seed, providerConfig, configuredModelName)
}

func (e *serviceE2EEnvironment) bootstrapProjectViaAPIWithTools(
	t *testing.T,
	ctx context.Context,
	seed string,
	providerConfig string,
	configuredModelName string,
	tools ...string,
) deterministicProject {
	t.Helper()
	return e.bootstrapProjectViaAPIWithToolsAndModelOptions(
		t,
		ctx,
		seed,
		providerConfig,
		configuredModelName,
		nil,
		tools...)
}

func (e *serviceE2EEnvironment) bootstrapProjectViaAPIWithToolsAndModelOptions(
	t *testing.T,
	ctx context.Context,
	seed string,
	providerConfig string,
	configuredModelName string,
	modelOptions map[string]serviceE2EConfiguredModelOptions,
	tools ...string,
) deterministicProject {
	t.Helper()
	sourceYAML := strings.Join([]string{
		"instruction: Help the user make progress.",
		"model:",
		"  provider_config: " + providerConfig,
		"  name: " + configuredModelName,
	}, "\n")
	if len(tools) > 0 {
		sourceYAML += "\ntools:\n"
		for _, name := range tools {
			sourceYAML += "  " + name + ": {}\n"
		}
	}
	sourceYAML += "\n"
	return e.bootstrapProjectViaAPIWithSourceAndModelOptions(t, ctx, seed, sourceYAML, modelOptions)
}

func (e *serviceE2EEnvironment) bootstrapProjectViaAPIWithSource(
	t *testing.T,
	ctx context.Context,
	seed string,
	sourceYAML string,
) deterministicProject {
	return e.bootstrapProjectViaAPIWithSourceAndModelOptions(t, ctx, seed, sourceYAML, nil)
}

type serviceE2EConfiguredModelOptions struct {
	ContextWindowTokens    int
	MaxOutputTokens        int
	DefaultMaxOutputTokens int
	APIVariantOptions      map[string]any
}

func (e *serviceE2EEnvironment) bootstrapProjectViaAPIWithSourceAndModelOptions(
	t *testing.T,
	ctx context.Context,
	seed string,
	sourceYAML string,
	modelOptions map[string]serviceE2EConfiguredModelOptions,
) deterministicProject {
	t.Helper()
	db, err := storage.Open(ctx, e.databaseURL)
	if err != nil {
		t.Fatalf("open e2e db: %v", err)
	}
	defer db.Close()
	store := storage.NewStore(db)
	adminUser, err := storagetest.CreateVerifiedUser(
		ctx,
		db,
		storagetest.CreateVerifiedUserInput{Email: seed + "-owner@example.com", DisplayName: "Owner"},
	)
	if err != nil {
		t.Fatalf("create e2e owner: %v", err)
	}
	adminPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{UserID: adminUser.ID, Name: "e2e owner", TokenID: seed + "-admin"},
	)
	if err != nil {
		t.Fatalf("create e2e owner pat: %v", err)
	}
	adminToken := adminPAT.Token
	adminSession := seed + "-admin-session"
	adminCSRF := seed + "-admin-csrf"
	if _, err := store.Identity().CreateBrowserSession(
		ctx,
		identitystore.CreateBrowserSessionInput{
			UserID:    adminUser.ID,
			Token:     adminSession,
			CSRFToken: adminCSRF,
			TTL:       time.Hour,
		},
	); err != nil {
		t.Fatalf("create e2e owner browser session: %v", err)
	}
	created := e.requestJSON(
		t,
		ctx,
		http.MethodPost,
		"/api/v1/orgs",
		map[string]any{"name": seed + " Org"},
		"idem-"+seed+"-org",
		adminToken,
		http.StatusCreated,
	)
	org := created["org"].(map[string]any)
	project := created["project"].(map[string]any)
	orgID := org["id"].(string)
	projectID := project["id"].(string)
	projectPath := "/api/v1/orgs/" + orgID + "/projects/" + projectID
	e.bootstrapServiceE2EModelProviders(t, ctx, orgID, projectPath, adminToken, modelOptions)
	config := e.requestJSON(
		t,
		ctx,
		http.MethodPost,
		projectPath+"/agent-configs",
		map[string]any{"source_format": "yaml", "source": sourceYAML},
		"",
		adminToken,
		http.StatusCreated,
	)
	profile := e.requestJSON(
		t,
		ctx,
		http.MethodPost,
		projectPath+"/agent-profiles",
		map[string]any{"name": "Deterministic Service E2E", "config": config["id"].(string)},
		"idem-"+seed+"-agent-profile",
		adminToken,
		http.StatusCreated,
	)
	adminUserID, err := publicid.Encode(publicid.KindUser, adminUser.ID)
	if err != nil {
		t.Fatalf("encode admin user id: %v", err)
	}
	return deterministicProject{
		env:          e,
		orgID:        orgID,
		projectID:    projectID,
		adminToken:   adminToken,
		adminSession: adminSession,
		adminCSRF:    adminCSRF,
		adminUserID:  adminUserID,
		projectPath:  projectPath,
		agentID:      profile["id"].(string),
		configID:     profile["current_config"].(map[string]any)["id"].(string),
	}
}

func (e *serviceE2EEnvironment) bootstrapServiceE2EModelProviders(
	t *testing.T,
	ctx context.Context,
	orgID, projectPath, adminToken string,
	modelOptions map[string]serviceE2EConfiguredModelOptions,
) {
	t.Helper()
	openAIKey := os.Getenv("OPENAI_API_KEY")
	if openAIKey == "" {
		openAIKey = "service-e2e-test-key"
	}
	anthropicKey := os.Getenv("ANTHROPIC_API_KEY")
	if anthropicKey == "" {
		anthropicKey = "service-e2e-test-key"
	}
	openRouterKey := os.Getenv("OPENROUTER_API_KEY")
	if openRouterKey == "" {
		openRouterKey = "service-e2e-test-key"
	}
	openAISecret := e.requestJSON(
		t,
		ctx,
		http.MethodPost,
		"/api/v1/orgs/"+orgID+"/secrets",
		map[string]any{
			"owner": map[string]any{"kind": "org"}, "name": "service-e2e-openai-key",
			"material": map[string]any{"kind": "generic", "value": openAIKey},
		},
		"",
		adminToken,
		http.StatusCreated,
	)
	openAIConfig := e.requestJSON(
		t,
		ctx,
		http.MethodPost,
		"/api/v1/orgs/"+orgID+"/model-provider-configs",
		map[string]any{
			"name":                 "openai-prod",
			"preset":               "openai",
			"credential_secret_id": openAISecret["id"].(string),
		},
		"",
		adminToken,
		http.StatusCreated,
	)
	e.createServiceE2EConfiguredModels(
		t,
		ctx,
		projectPath,
		adminToken,
		serviceE2EProviderConfigID(t, openAIConfig),
		modelOptions,
		"service-e2e-local",
		"service-e2e-alt",
		"gpt-5.5",
		os.Getenv("OMNARA_E2E_OPENAI_PROVIDER_MODEL_SLUG"),
	)
	openAIChatConfig := e.requestJSON(
		t,
		ctx,
		http.MethodPost,
		"/api/v1/orgs/"+orgID+"/model-provider-configs",
		map[string]any{
			"name":                 "openai-chat-prod",
			"api_format":           "openai-chat-completions",
			"base_url":             "https://api.openai.com/v1",
			"credential_secret_id": openAISecret["id"].(string),
		},
		"",
		adminToken,
		http.StatusCreated,
	)
	e.createServiceE2EConfiguredModels(
		t,
		ctx,
		projectPath,
		adminToken,
		serviceE2EProviderConfigID(t, openAIChatConfig),
		modelOptions,
		liveOpenAIChatConfiguredModelName(),
	)
	openRouterSecret := e.requestJSON(
		t,
		ctx,
		http.MethodPost,
		"/api/v1/orgs/"+orgID+"/secrets",
		map[string]any{
			"owner": map[string]any{"kind": "org"},
			"name":  "service-e2e-openrouter-key",
			"material": map[string]any{
				"kind": "generic", "value": openRouterKey,
			},
		},
		"",
		adminToken,
		http.StatusCreated,
	)
	openRouterConfig := e.requestJSON(
		t,
		ctx,
		http.MethodPost,
		"/api/v1/orgs/"+orgID+"/model-provider-configs",
		map[string]any{
			"name":                 "openrouter-prod",
			"preset":               "openrouter",
			"credential_secret_id": openRouterSecret["id"].(string),
		},
		"",
		adminToken,
		http.StatusCreated,
	)
	e.createServiceE2EConfiguredModels(
		t,
		ctx,
		projectPath,
		adminToken,
		serviceE2EProviderConfigID(t, openRouterConfig),
		modelOptions,
		"service-e2e-openrouter",
		liveOpenRouterConfiguredModelName(),
	)
	anthropicSecret := e.requestJSON(
		t,
		ctx,
		http.MethodPost,
		"/api/v1/orgs/"+orgID+"/secrets",
		map[string]any{
			"owner": map[string]any{"kind": "org"},
			"name":  "service-e2e-anthropic-key",
			"material": map[string]any{
				"kind": "generic", "value": anthropicKey,
			},
		},
		"",
		adminToken,
		http.StatusCreated,
	)
	anthropicConfig := e.requestJSON(
		t,
		ctx,
		http.MethodPost,
		"/api/v1/orgs/"+orgID+"/model-provider-configs",
		map[string]any{
			"name":                 "anthropic-prod",
			"preset":               "anthropic",
			"credential_secret_id": anthropicSecret["id"].(string),
		},
		"",
		adminToken,
		http.StatusCreated,
	)
	e.createServiceE2EConfiguredModels(
		t,
		ctx,
		projectPath,
		adminToken,
		serviceE2EProviderConfigID(t, anthropicConfig),
		modelOptions,
		"service-e2e-claude",
		"claude-sonnet-4-6",
		os.Getenv("OMNARA_E2E_ANTHROPIC_PROVIDER_MODEL_SLUG"),
	)
}

func serviceE2EProviderConfigID(t *testing.T, response map[string]any) string {
	t.Helper()
	config, ok := response["config"].(map[string]any)
	if !ok {
		t.Fatalf("create model provider response has no config: %+v", response)
	}
	return config["id"].(string)
}

func liveOpenAIChatConfiguredModelName() string {
	if providerModelSlug := os.Getenv("OMNARA_E2E_OPENAI_CHAT_PROVIDER_MODEL_SLUG"); providerModelSlug != "" {
		return providerModelSlug
	}
	if providerModelSlug := os.Getenv("OMNARA_E2E_OPENAI_PROVIDER_MODEL_SLUG"); providerModelSlug != "" {
		return providerModelSlug
	}
	return "gpt-4.1-mini"
}

func liveOpenRouterConfiguredModelName() string {
	if providerModelSlug := os.Getenv("OMNARA_E2E_OPENROUTER_PROVIDER_MODEL_SLUG"); providerModelSlug != "" {
		return providerModelSlug
	}
	return "z-ai/glm-5.2"
}

func (e *serviceE2EEnvironment) createServiceE2EConfiguredModels(
	t *testing.T,
	ctx context.Context,
	projectPath, adminToken, providerConfigID string,
	modelOptions map[string]serviceE2EConfiguredModelOptions,
	configuredModelNames ...string,
) {
	t.Helper()
	seen := map[string]bool{}
	for _, configuredModelName := range configuredModelNames {
		if configuredModelName == "" || seen[configuredModelName] {
			continue
		}
		seen[configuredModelName] = true
		e.createServiceE2EConfiguredModel(
			t,
			ctx,
			projectPath,
			adminToken,
			providerConfigID,
			configuredModelName,
			modelOptions[configuredModelName],
		)
	}
}

func (e *serviceE2EEnvironment) createServiceE2EConfiguredModel(
	t *testing.T,
	ctx context.Context,
	projectPath, adminToken, providerConfigID, configuredModelName string,
	options serviceE2EConfiguredModelOptions,
) {
	t.Helper()
	if options.ContextWindowTokens == 0 {
		options.ContextWindowTokens = 128000
	}
	if options.MaxOutputTokens == 0 {
		options.MaxOutputTokens = 8192
	}
	if options.DefaultMaxOutputTokens == 0 {
		options.DefaultMaxOutputTokens = 4096
	}
	body := map[string]any{
		"name":                      configuredModelName,
		"provider_model_slug":       configuredModelName,
		"context_window_tokens":     options.ContextWindowTokens,
		"max_output_tokens":         options.MaxOutputTokens,
		"default_max_output_tokens": options.DefaultMaxOutputTokens,
	}
	if options.APIVariantOptions != nil {
		body["api_variant_options"] = options.APIVariantOptions
	}
	configuredModel := e.requestJSON(
		t,
		ctx,
		http.MethodPost,
		strings.TrimSuffix(
			strings.Split(projectPath, "/projects/")[0],
			"/",
		)+"/model-provider-configs/"+providerConfigID+"/models",
		body,
		"",
		adminToken,
		http.StatusCreated,
	)
	e.requestJSON(
		t,
		ctx,
		http.MethodPost,
		projectPath+"/model-grants",
		map[string]any{"configured_model_id": configuredModel["id"].(string)},
		"",
		adminToken,
		http.StatusCreated,
	)
}

func (p deterministicProject) createAgent(t *testing.T, ctx context.Context) string {
	t.Helper()
	launched := p.env.requestJSON(
		t,
		ctx,
		http.MethodPost,
		p.projectPath+"/agents",
		map[string]any{"profile": p.agentID, "config": p.configID},
		"idem-"+p.projectID+"-agent",
		p.adminToken,
		http.StatusCreated,
	)
	return launched["agent"].(map[string]any)["id"].(string)
}

func (p deterministicProject) createInput(t *testing.T, ctx context.Context, agentID, text string) {
	t.Helper()
	p.createInputWithDeliveryMode(t, ctx, agentID, text, "")
}

func (p deterministicProject) updateConfig(t *testing.T, ctx context.Context, agentID, sourceYAML string) {
	t.Helper()
	sum := sha256.Sum256([]byte(sourceYAML))
	p.env.requestJSON(
		t,
		ctx,
		http.MethodPost,
		p.projectPath+"/agents/"+agentID+"/config",
		map[string]any{"source_format": "yaml", "source": sourceYAML},
		"idem-"+agentID+"-config-"+hex.EncodeToString(sum[:8]),
		p.adminToken,
		http.StatusOK,
	)
}

func (p deterministicProject) createInputWithDeliveryMode(
	t *testing.T,
	ctx context.Context,
	agentID, text string,
	deliveryMode executionstore.AgentInputDeliveryMode,
) string {
	t.Helper()
	sum := sha256.Sum256([]byte(text))
	body := map[string]any{"content_blocks": []map[string]any{{"type": "text", "text": text}}}
	if deliveryMode != "" {
		body["delivery_mode"] = string(deliveryMode)
	}
	created := p.env.requestJSON(
		t,
		ctx,
		http.MethodPost,
		p.projectPath+"/agents/"+agentID+"/inputs",
		body,
		"idem-"+agentID+"-input-"+hex.EncodeToString(sum[:8]),
		p.adminToken,
		http.StatusCreated,
	)
	return created["agent_input"].(map[string]any)["id"].(string)
}

func waitForServiceE2ECondition(t *testing.T, ctx context.Context, ready func() (bool, string)) {
	t.Helper()
	waitForServiceE2EConditionUntil(t, ctx, time.Now().Add(90*time.Second), ready)
}

func waitForServiceE2EConditionUntil(
	t *testing.T,
	ctx context.Context,
	deadline time.Time,
	ready func() (bool, string),
) {
	t.Helper()
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	last := ""
	for time.Now().Before(deadline) {
		ok, detail := ready()
		if ok {
			return
		}
		last = detail
		select {
		case <-ctx.Done():
			t.Fatalf("context canceled waiting for service E2E condition: %v last=%s", ctx.Err(), last)
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for service E2E condition: %s", last)
}

func requestContainsTool(body map[string]any, name string) bool {
	tools, ok := body["tools"].([]any)
	if !ok {
		return false
	}
	for _, raw := range tools {
		item, ok := raw.(map[string]any)
		if ok && item["name"] == name {
			return true
		}
	}
	return false
}

func requestContainsToolResult(body map[string]any, callID string, contains string) bool {
	input, ok := body["input"].([]any)
	if !ok {
		return false
	}
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if ok && item["type"] == "function_call_output" && item["call_id"] == callID {
			if contains != "" && !strings.Contains(mustJSONString(item["output"]), contains) {
				return false
			}
			return true
		}
	}
	return false
}

func mustJSONString(value any) string {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(body)
}
