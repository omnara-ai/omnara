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

func TestServiceE2EDeterministicCompactionRetryContinuesTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "deterministic-compaction-retry")

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
		w.Header().Set("Content-Type", "application/json")
		switch requestCount.Add(1) {
		case 1:
			_, _ = w.Write(
				[]byte(
					`{"id":"resp_before_compaction","status":"completed","output":[` +
						`{"id":"msg_before_compaction","type":"message",` +
						`"content":[{"type":"output_text","text":"history before compaction"}]}],` +
						`"usage":{"input_tokens":7,"output_tokens":4}}`,
				),
			)
		case 2:
			http.Error(
				w,
				`{"error":{"message":"context length exceeded in deterministic test","code":"context_length_exceeded"}}`,
				http.StatusBadRequest,
			)
		case 3:
			instructions, _ := body["instructions"].(string)
			if !strings.Contains(instructions, "compact a closed Omnara event prefix") {
				t.Errorf("third request was not the compaction prompt: %+v", body)
			}
			inputText := mustJSONString(body["input"])
			if !strings.Contains(inputText, "create a small accepted history item") {
				t.Errorf("compaction prompt did not include semantic history: %+v", body)
			}
			if !strings.Contains(inputText, "history before compaction") {
				t.Errorf("compaction prompt did not include the complete closed turn: %+v", body)
			}
			_, _ = w.Write(
				[]byte(
					`{"id":"resp_compaction_summary","status":"completed","output":[` +
						`{"id":"msg_compaction_summary","type":"message","content":[` +
						`{"type":"output_text","text":"Earlier request asked for a small accepted history item, and the model replied with history before compaction."}]}],` +
						`"usage":{"input_tokens":11,"output_tokens":8}}`,
				),
			)
		case 4:
			instructions, _ := body["instructions"].(string)
			if strings.Contains(instructions, "Earlier request asked") ||
				!strings.Contains(instructions, "not a new user request") {
				t.Errorf("retry request elevated checkpoint content into instructions: %+v", body)
			}
			assertServiceE2ECheckpointUserHistory(
				t,
				body["input"],
				"Earlier request asked for a small accepted history item, and the model replied with history before compaction.",
			)
			if count := strings.Count(mustJSONString(body["input"]), "history before compaction"); count != 1 {
				t.Errorf("compacted history appears %d times, want checkpoint only: %+v", count, body)
			}
			_, _ = w.Write(
				[]byte(
					`{"id":"resp_after_compaction","status":"completed","output":[` +
						`{"id":"msg_after_compaction","type":"message",` +
						`"content":[{"type":"output_text","text":"final answer after compact retry"}]}],` +
						`"usage":{"input_tokens":9,"output_tokens":5}}`,
				),
			)
		default:
			t.Errorf("unexpected extra OpenAI request: %+v", body)
			http.Error(w, "unexpected request", http.StatusTeapot)
		}
	}))
	defer openai.Close()

	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithToolsAndModelOptions(
		t,
		ctx,
		"deterministic-compaction",
		"openai-prod",
		"service-e2e-local",
		map[string]serviceE2EConfiguredModelOptions{
			"service-e2e-local": {
				ContextWindowTokens:    128000,
				MaxOutputTokens:        8192,
				DefaultMaxOutputTokens: 64,
			},
		},
	)
	agentID := project.createAgent(t, ctx)
	project.createInput(t, ctx, agentID, "create a small accepted history item")
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
		err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.event_kind = 'model_output' AND block.text_content = 'history before compaction'`, projectUUID, agentUUID).
			Scan(&count)
		if err != nil {
			return false, err.Error()
		}
		return count == 1, "first model output not recorded yet"
	})
	firstWorker.stop()

	project.createInput(t, ctx, agentID, "force budget overflow "+strings.Repeat("large context ", 120))
	secondWorker := env.startWorker(t, ctx, project.projectID, serviceWorkerOptions{
		ProviderConfig: "openai-prod", BaseURL: openai.URL,
	})
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var outputs, checkpoints, contexts int
		if err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.event_kind = 'model_output' AND block.text_content = 'final answer after compact retry'`, projectUUID, agentUUID).
			Scan(&outputs); err != nil {
			return false, err.Error()
		}
		if err := env.db.QueryRow(ctx, `SELECT count(*) FROM context_checkpoints checkpoint JOIN agents agent ON agent.id = checkpoint.agent_id WHERE agent.project_id = $1 AND checkpoint.agent_id = $2`, projectUUID, agentUUID).
			Scan(&checkpoints); err != nil {
			return false, err.Error()
		}
		if err := env.db.QueryRow(ctx, `SELECT count(*) FROM model_call_contexts WHERE project_id = $1 AND agent_id = $2`, projectUUID, agentUUID).
			Scan(&contexts); err != nil {
			return false, err.Error()
		}
		return outputs == 1 &&
				checkpoints == 1, fmt.Sprintf(
				"compaction retry output/checkpoint not recorded yet requests=%d contexts=%d worker_logs=%s",
				requestCount.Load(),
				contexts,
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
		var err error
		retryContexts, err = successfulContextsUsingSummaryCheckpoint(
			ctx,
			env,
			projectUUID,
			agentUUID,
			"Earlier request asked",
			"large context",
		)
		if err != nil {
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
				"durable compaction retry state not complete failed=%d producer=%d retry_contexts=%d locks=%d wakeups=%d",
				failedContextWindow,
				checkpointProducer,
				retryContexts,
				locks,
				wakeups,
			)
	})
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var retryLineageRows int
		if err := env.db.QueryRow(ctx, `
WITH failed_context AS (
  SELECT context.input_event_sequence
  FROM model_call_contexts context
  WHERE context.project_id = $1
    AND context.agent_id = $2
    AND context.operation_kind = 'normal'
    AND context.state = 'failed'
    AND context.recovery_kind = 'compact'
    AND context.error_kind = 'context_window'
  LIMIT 1
), produced_checkpoint AS (
  SELECT checkpoint.id, checkpoint_event.sequence AS event_sequence
	  FROM context_checkpoints checkpoint
	  JOIN model_call_contexts compaction
	    ON compaction.agent_id = checkpoint.agent_id
   AND compaction.id = checkpoint.producer_model_call_context_id
  JOIN failed_context
    ON failed_context.input_event_sequence = compaction.input_event_sequence
	  JOIN agent_events checkpoint_event
	    ON checkpoint_event.agent_id = checkpoint.agent_id
   AND checkpoint_event.context_checkpoint_id = checkpoint.id
)
SELECT count(*)
FROM model_call_contexts retry_context
JOIN produced_checkpoint
  ON retry_context.input_event_sequence >= produced_checkpoint.event_sequence
WHERE retry_context.project_id = $1
  AND retry_context.agent_id = $2
  AND retry_context.operation_kind = 'normal'
  AND retry_context.state = 'succeeded'
`, projectUUID, agentUUID).Scan(&retryLineageRows); err != nil {
			return false, err.Error()
		}
		return retryLineageRows == 1, fmt.Sprintf("retry context lineage rows=%d", retryLineageRows)
	})
	if got := requestCount.Load(); got != 4 {
		t.Fatalf("fake OpenAI server saw %d requests, want 4", got)
	}
}

func assertServiceE2ECheckpointUserHistory(
	t *testing.T,
	rawItems any,
	summaryText string,
) {
	t.Helper()
	items, ok := rawItems.([]any)
	if !ok || len(items) == 0 {
		t.Errorf("provider history = %T(%v), want checkpoint user item", rawItems, rawItems)
		return
	}
	checkpoint, ok := items[0].(map[string]any)
	if !ok || checkpoint["role"] != "user" {
		t.Errorf("first provider history item = %T(%v), want user checkpoint", items[0], items[0])
		return
	}
	checkpointText := fmt.Sprint(checkpoint["content"])
	if !strings.Contains(checkpointText, "<context_checkpoint>") ||
		!strings.Contains(checkpointText, "</context_checkpoint>") ||
		!strings.Contains(checkpointText, summaryText) {
		t.Errorf("checkpoint user history is not clearly delimited: %v", checkpoint)
	}
}

func TestServiceE2EDeterministicCompactionKeepsToolGroupRaw(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "deterministic-compaction-tool-group")

	const commandNeedle = "SERVICE_TOOL_GROUP_MUST_STAY_RAW"
	const finalText = "service continued after raw tool group compaction"
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
			t.Errorf("decode OpenAI request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch requestCount.Add(1) {
		case 1:
			instructions, _ := body["instructions"].(string)
			if strings.Contains(instructions, "compact a closed Omnara event prefix") {
				failModelRequest(w, http.StatusBadRequest, "first request unexpectedly used the compaction prompt: %+v", body)
				return
			}
			writeOpenAIFunctionCall(
				w,
				failModelRequest,
				"resp_tool_before_split_compaction",
				"call_split_tool_group",
				"run_command",
				map[string]any{
					"command": "printf '" + commandNeedle + "\\n' " + strings.Repeat("&& true ", 80),
				},
			)
		case 2:
			instructions, _ := body["instructions"].(string)
			if !strings.Contains(instructions, "compact a closed Omnara event prefix") {
				failModelRequest(w, http.StatusBadRequest, "second request was not the compaction prompt: %+v", body)
				return
			}
			inputText := mustJSONString(body["input"])
			if !strings.Contains(inputText, "compactable service user history") {
				t.Errorf("compaction prompt did not include compactable user history: %+v", body)
			}
			if strings.Contains(inputText, commandNeedle) {
				t.Errorf("compaction prompt split tool group into checkpoint source: %+v", body)
			}
			writeOpenAIMessage(
				w,
				failModelRequest,
				"resp_split_tool_summary",
				"The compacted service user instruction should continue after the tool attempt.",
			)
		case 3:
			instructions, _ := body["instructions"].(string)
			if strings.Contains(instructions, "compact a closed Omnara event prefix") {
				failModelRequest(w, http.StatusBadRequest, "third request unexpectedly reused the compaction prompt: %+v", body)
				return
			}
			inputText := mustJSONString(body["input"])
			if !strings.Contains(inputText, "The compacted service user instruction") ||
				!strings.Contains(inputText, commandNeedle) ||
				!strings.Contains(inputText, "no_active_agent_machine_binding") {
				t.Errorf("retry request did not include summary plus raw tool group/result: %+v", body)
			}
			writeOpenAIMessage(w, failModelRequest, "resp_after_split_tool_compaction", finalText)
		default:
			t.Errorf("unexpected extra OpenAI request: %+v", body)
			http.Error(w, "unexpected request", http.StatusTeapot)
		}
	}))
	defer openai.Close()

	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithToolsAndModelOptions(
		t,
		ctx,
		"deterministic-compaction-tool-group",
		"openai-prod",
		"service-e2e-local",
		map[string]serviceE2EConfiguredModelOptions{
			"service-e2e-local": {
				ContextWindowTokens:    1500,
				MaxOutputTokens:        512,
				DefaultMaxOutputTokens: 64,
			},
		},
		"run_command",
	)
	agentID := project.createAgent(t, ctx)
	project.createInput(t, ctx, agentID, "compactable service user history "+strings.Repeat("old user detail ", 120))
	worker := env.startWorker(t, ctx, project.projectID, serviceWorkerOptions{
		ProviderConfig: "openai-prod", BaseURL: openai.URL,
	})
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		select {
		case failure := <-modelFailures:
			t.Fatalf("fake model request failed: %s", failure)
		default:
		}
		var outputs, checkpoints int
		if err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.event_kind = 'model_output' AND block.text_content = $3`, projectUUID, agentUUID, finalText).
			Scan(&outputs); err != nil {
			return false, err.Error()
		}
		if err := env.db.QueryRow(ctx, `SELECT count(*) FROM context_checkpoints checkpoint JOIN agents agent ON agent.id = checkpoint.agent_id WHERE agent.project_id = $1 AND checkpoint.agent_id = $2 AND checkpoint.summarized_through_event_sequence = 2`, projectUUID, agentUUID).
			Scan(&checkpoints); err != nil {
			return false, err.Error()
		}
		return outputs == 1 &&
				checkpoints == 1, fmt.Sprintf(
				"tool-group compaction output/checkpoint not ready outputs=%d checkpoints=%d requests=%d worker_logs=%s",
				outputs,
				checkpoints,
				requestCount.Load(),
				worker.logExcerpt(),
			)
	})
	if got := requestCount.Load(); got != 3 {
		t.Fatalf("fake OpenAI server saw %d requests, want 3", got)
	}
}

func successfulContextsUsingSummaryCheckpoint(
	ctx context.Context,
	env *serviceE2EEnvironment,
	projectUUID, agentUUID string,
	summaryNeedle, omittedNeedle string,
) (int, error) {
	var contexts int
	err := env.db.QueryRow(ctx, `
SELECT count(DISTINCT mcc.id)
FROM context_checkpoints checkpoint
JOIN agent_events checkpoint_event ON checkpoint_event.agent_id = checkpoint.agent_id
  AND checkpoint_event.context_checkpoint_id = checkpoint.id
JOIN model_call_contexts mcc ON mcc.agent_id = checkpoint.agent_id
  AND mcc.operation_kind = 'normal'
  AND mcc.input_event_sequence >= checkpoint_event.sequence
WHERE mcc.project_id = $1
  AND checkpoint.agent_id = $2
  AND mcc.state = 'succeeded'
  AND checkpoint.summary LIKE '%' || $3 || '%'
  AND checkpoint.summary NOT LIKE '%' || $4 || '%'
`, projectUUID, agentUUID, summaryNeedle, omittedNeedle).Scan(&contexts)
	return contexts, err
}
