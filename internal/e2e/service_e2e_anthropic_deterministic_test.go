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

func TestServiceE2EDeterministicAnthropicWorkerRunsModelTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "deterministic-anthropic-model-turn")
	var requestCount atomic.Int64
	const modelText = "deterministic anthropic worker completed the turn"
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			http.NotFound(w, r)
			return
		}
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
		}
		requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(
			[]byte(
				`{"id":"msg_service_e2e_deterministic","type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"` + modelText + `"}],"usage":{"input_tokens":7,"output_tokens":5}}`,
			),
		)
	}))
	defer anthropic.Close()

	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPI(t, ctx, "deterministic-anthropic", "anthropic-prod", "service-e2e-claude")
	agentID := project.createAgent(t, ctx)
	project.createInput(t, ctx, agentID, "run one deterministic Anthropic model turn")
	env.startWorker(
		t,
		ctx,
		project.projectID,
		serviceWorkerOptions{ProviderConfig: "anthropic-prod", BaseURL: anthropic.URL},
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
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("deterministic Anthropic server saw %d requests, want 1", got)
	}
}

func TestServiceE2EDeterministicAnthropicCompactionRetryContinuesTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "deterministic-anthropic-compaction")

	var requestCount atomic.Int64
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			http.NotFound(w, r)
			return
		}
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
		}
		w.Header().Set("Content-Type", "application/json")
		switch requestCount.Add(1) {
		case 1:
			writeAnthropicMessage(w, "msg_anthropic_before_compaction", "anthropic history before compaction")
		case 2:
			http.Error(
				w,
				`{"type":"error","error":{"type":"invalid_request_error","error_type":"context_length_exceeded","message":"prompt exceeds context window"}}`,
				http.StatusBadRequest,
			)
		case 3:
			requestText := mustJSONString(body)
			if !strings.Contains(requestText, "compact a closed Omnara event prefix") {
				t.Errorf("third Anthropic request was not the compaction prompt: %+v", body)
			}
			if !strings.Contains(requestText, "create a small Anthropic accepted history item") {
				t.Errorf("Anthropic compaction prompt did not include semantic history: %+v", body)
			}
			if !strings.Contains(requestText, "anthropic history before compaction") {
				t.Errorf("Anthropic compaction prompt did not include the complete closed turn: %+v", body)
			}
			writeAnthropicMessage(
				w,
				"msg_anthropic_summary",
				"Earlier Anthropic request asked for a small accepted history item, and the model replied with anthropic history before compaction.",
			)
		case 4:
			systemText := fmt.Sprint(body["system"])
			if strings.Contains(systemText, "Earlier Anthropic request asked") ||
				!strings.Contains(systemText, "not a new user request") {
				t.Errorf("Anthropic retry elevated checkpoint content into system text: %+v", body)
			}
			assertServiceE2ECheckpointUserHistory(
				t,
				body["messages"],
				"Earlier Anthropic request asked for a small accepted history item, and the model replied with anthropic history before compaction.",
			)
			if count := strings.Count(mustJSONString(body["messages"]), "anthropic history before compaction"); count != 1 {
				t.Errorf("compacted Anthropic history appears %d times, want checkpoint only: %+v", count, body)
			}
			writeAnthropicMessage(w, "msg_anthropic_after_compaction", "anthropic final answer after compact retry")
		default:
			t.Errorf("unexpected extra Anthropic request: %+v", body)
			http.Error(w, "unexpected request", http.StatusTeapot)
		}
	}))
	defer anthropic.Close()

	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithToolsAndModelOptions(
		t,
		ctx,
		"deterministic-anthropic-compaction",
		"anthropic-prod",
		"service-e2e-claude",
		map[string]serviceE2EConfiguredModelOptions{
			"service-e2e-claude": {
				ContextWindowTokens:    128000,
				MaxOutputTokens:        8192,
				DefaultMaxOutputTokens: 64,
			},
		},
	)
	agentID := project.createAgent(t, ctx)
	project.createInput(t, ctx, agentID, "create a small Anthropic accepted history item")
	firstWorker := env.startWorker(t, ctx, project.projectID, serviceWorkerOptions{
		ProviderConfig: "anthropic-prod", BaseURL: anthropic.URL,
	})

	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var count int
		err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.event_kind = 'model_output' AND block.text_content = 'anthropic history before compaction'`, projectUUID, agentUUID).
			Scan(&count)
		if err != nil {
			return false, err.Error()
		}
		return count == 1, "first Anthropic output not recorded yet"
	})
	firstWorker.stop()

	project.createInput(
		t,
		ctx,
		agentID,
		"force Anthropic budget overflow "+strings.Repeat("large anthropic context ", 120),
	)
	secondWorker := env.startWorker(t, ctx, project.projectID, serviceWorkerOptions{
		ProviderConfig: "anthropic-prod", BaseURL: anthropic.URL,
	})
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var outputs, checkpoints int
		if err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.event_kind = 'model_output' AND block.text_content = 'anthropic final answer after compact retry'`, projectUUID, agentUUID).
			Scan(&outputs); err != nil {
			return false, err.Error()
		}
		if err := env.db.QueryRow(ctx, `SELECT count(*) FROM context_checkpoints checkpoint JOIN agents agent ON agent.id = checkpoint.agent_id WHERE agent.project_id = $1 AND checkpoint.agent_id = $2`, projectUUID, agentUUID).
			Scan(&checkpoints); err != nil {
			return false, err.Error()
		}
		return outputs == 1 &&
				checkpoints == 1, fmt.Sprintf(
				"Anthropic compaction retry not complete outputs=%d checkpoints=%d requests=%d logs=%s",
				outputs,
				checkpoints,
				requestCount.Load(),
				secondWorker.logExcerpt(),
			)
	})
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var failedContextWindow, checkpointProducer, retryContexts, locks, wakeups int
		if err := env.db.QueryRow(ctx, `SELECT count(*) FROM model_call_contexts WHERE project_id = $1 AND agent_id = $2 AND operation_kind = 'normal' AND state = 'failed' AND recovery_kind = 'compact' AND error_kind = 'context_window'`, projectUUID, agentUUID).
			Scan(&failedContextWindow); err != nil {
			return false, err.Error()
		}
		if err := env.db.QueryRow(ctx, `SELECT count(*) FROM context_checkpoints checkpoint JOIN model_call_contexts mcc ON mcc.agent_id = checkpoint.agent_id AND mcc.id = checkpoint.producer_model_call_context_id WHERE mcc.project_id = $1 AND checkpoint.agent_id = $2 AND checkpoint.summary <> '' AND mcc.operation_kind = 'compaction' AND mcc.state = 'succeeded'`, projectUUID, agentUUID).
			Scan(&checkpointProducer); err != nil {
			return false, err.Error()
		}
		if err := env.db.QueryRow(ctx, `SELECT count(*) FROM context_checkpoints checkpoint JOIN agent_events checkpoint_event ON checkpoint_event.agent_id = checkpoint.agent_id AND checkpoint_event.context_checkpoint_id = checkpoint.id JOIN model_call_contexts mcc ON mcc.agent_id = checkpoint.agent_id AND mcc.operation_kind = 'normal' AND mcc.input_event_sequence >= checkpoint_event.sequence WHERE mcc.project_id = $1 AND checkpoint.agent_id = $2 AND mcc.state = 'succeeded'`, projectUUID, agentUUID).
			Scan(&retryContexts); err != nil {
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
		return failedContextWindow == 1 && checkpointProducer == 1 && retryContexts == 1 && locks == 0 &&
				wakeups == 0, fmt.Sprintf(
				"Anthropic durable compaction state failed=%d producer=%d retry_contexts=%d locks=%d wakeups=%d",
				failedContextWindow,
				checkpointProducer,
				retryContexts,
				locks,
				wakeups,
			)
	})
	if got := requestCount.Load(); got != 4 {
		t.Fatalf("fake Anthropic server saw %d requests, want 4", got)
	}
}

func writeAnthropicMessage(w http.ResponseWriter, id, text string) {
	body, err := json.Marshal(map[string]any{
		"id":          id,
		"type":        "message",
		"role":        "assistant",
		"stop_reason": "end_turn",
		"content": []map[string]any{{
			"type": "text",
			"text": text,
		}},
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
	})
	if err != nil {
		http.Error(w, "marshal Anthropic message response: "+err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(body)
}
