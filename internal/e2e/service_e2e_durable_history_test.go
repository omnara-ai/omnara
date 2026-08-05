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

func TestServiceE2EDeterministicHistoricalToolExchangeSurvivesNewTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "historical-tool-exchange")

	const (
		callID          = "call_historical_question"
		opaqueAnswer    = "OPAQUE_TOOL_RESULT_7F3A9D"
		firstTurnOutput = "historical tool turn completed"
		secondInput     = "continue in a new turn without calling the tool again"
		secondOutput    = "new turn retained the historical tool exchange"
	)
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
		switch requestCount.Add(1) {
		case 1:
			if !requestContainsTool(body, "ask_question") {
				failModelRequest(
					w,
					http.StatusBadRequest,
					"first request did not expose ask_question: %+v",
					body,
				)
				return
			}
			writeOpenAIFunctionCall(
				w,
				failModelRequest,
				"resp_historical_question",
				callID,
				"ask_question",
				map[string]any{
					"questions": []map[string]any{{
						"prompt": "Provide an opaque value.",
						"options": []map[string]any{{
							"label": "Use the default value",
						}},
					}},
				},
			)
		case 2:
			if !requestContainsToolResult(body, callID, opaqueAnswer) {
				failModelRequest(
					w,
					http.StatusBadRequest,
					"same-turn continuation omitted opaque tool result: %+v",
					body["input"],
				)
				return
			}
			writeOpenAIMessage(
				w,
				failModelRequest,
				"resp_historical_question_complete",
				firstTurnOutput,
			)
		case 3:
			if err := validateHistoricalOpenAIToolExchange(
				body["input"],
				callID,
				opaqueAnswer,
				secondInput,
			); err != nil {
				failModelRequest(w, http.StatusBadRequest, "%v", err)
				return
			}
			writeOpenAIMessage(
				w,
				failModelRequest,
				"resp_historical_question_new_turn",
				secondOutput,
			)
		default:
			failModelRequest(
				w,
				http.StatusTeapot,
				"unexpected extra OpenAI request: %+v",
				body,
			)
		}
	}))
	defer openai.Close()

	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithTools(
		t,
		ctx,
		"historical-tool-exchange",
		"openai-prod",
		"service-e2e-local",
		"ask_question",
	)
	agentID := project.createAgent(t, ctx)
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	project.createInput(t, ctx, agentID, "ask one question and then finish the turn")
	worker := env.startWorker(
		t,
		ctx,
		project.projectID,
		serviceWorkerOptions{ProviderConfig: "openai-prod", BaseURL: openai.URL},
	)

	interactionID := waitForOpenServiceE2EInteraction(
		t,
		ctx,
		project,
		agentID,
		modelFailures,
		worker,
	)
	resolved := env.requestJSON(
		t,
		ctx,
		http.MethodPost,
		project.projectPath+"/agents/"+agentID+"/interactions/"+interactionID+"/resolve",
		map[string]any{
			"answers": []map[string]any{{
				"option_indices": []int{1},
				"text":           opaqueAnswer,
			}},
		},
		"",
		project.adminToken,
		http.StatusOK,
	)
	if resolved["state"] != "resolved" {
		t.Fatalf("interaction resolution state = %v, want resolved", resolved["state"])
	}
	waitForServiceE2ETextOutput(
		t,
		ctx,
		env,
		projectUUID,
		agentUUID,
		firstTurnOutput,
		modelFailures,
		worker,
	)

	project.createInput(t, ctx, agentID, secondInput)
	waitForServiceE2ETextOutput(
		t,
		ctx,
		env,
		projectUUID,
		agentUUID,
		secondOutput,
		modelFailures,
		worker,
	)

	var completedToolCalls, contexts, distinctTurns int
	if err := env.db.QueryRow(ctx, `
SELECT count(*)
FROM tool_call_read_projection call
WHERE call.project_id = $1
  AND call.agent_id = $2
  AND call.provider_call_id = $3
  AND call.state = 'completed'
`, projectUUID, agentUUID, callID).Scan(&completedToolCalls); err != nil {
		t.Fatalf("query historical tool call: %v", err)
	}
	if err := env.db.QueryRow(ctx, `
SELECT count(*), count(DISTINCT context_turn.turn_id)
FROM model_call_contexts context
JOIN model_call_context_turns context_turn
  ON context_turn.project_id = context.project_id
 AND context_turn.agent_id = context.agent_id
 AND context_turn.model_call_context_id = context.id
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.operation_kind = 'normal'
`, projectUUID, agentUUID).Scan(&contexts, &distinctTurns); err != nil {
		t.Fatalf("query historical tool model contexts: %v", err)
	}
	if requestCount.Load() != 3 || completedToolCalls != 1 || contexts != 3 || distinctTurns != 2 {
		t.Fatalf(
			"historical tool journey requests=%d tools=%d contexts=%d turns=%d, want 3/1/3/2",
			requestCount.Load(),
			completedToolCalls,
			contexts,
			distinctTurns,
		)
	}
}

func TestServiceE2ENewInputAfterTerminalModelErrorSeesDurableError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "terminal-error-history")

	const (
		errorMessage = "DURABLE_AUTH_FAILURE_C81E2B"
		secondInput  = "the provider is fixed now; continue in a new turn"
		finalOutput  = "continued after durable provider error"
	)
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
		switch requestCount.Add(1) {
		case 1:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprintf(
				w,
				`{"error":{"message":%q,"type":"authentication_error","code":"invalid_api_key"}}`,
				errorMessage,
			)
		case 2:
			input := mustJSONString(body["input"])
			errorIndex := strings.Index(input, errorMessage)
			newInputIndex := strings.Index(input, secondInput)
			if strings.Count(input, errorMessage) != 1 ||
				strings.Count(input, secondInput) != 1 ||
				errorIndex < 0 ||
				newInputIndex < 0 ||
				errorIndex >= newInputIndex {
				failModelRequest(
					w,
					http.StatusBadRequest,
					"recovery request did not place exactly one durable error before exactly one new input: %s",
					input,
				)
				return
			}
			writeOpenAIMessage(
				w,
				failModelRequest,
				"resp_after_terminal_error",
				finalOutput,
			)
		default:
			failModelRequest(
				w,
				http.StatusTeapot,
				"unexpected extra OpenAI request after terminal error: %+v",
				body,
			)
		}
	}))
	defer openai.Close()

	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPI(
		t,
		ctx,
		"terminal-error-history",
		"openai-prod",
		"service-e2e-local",
	)
	agentID := project.createAgent(t, ctx)
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	project.createInput(t, ctx, agentID, "make a request that encounters an authentication failure")
	worker := env.startWorker(
		t,
		ctx,
		project.projectID,
		serviceWorkerOptions{ProviderConfig: "openai-prod", BaseURL: openai.URL},
	)

	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		select {
		case failure := <-modelFailures:
			t.Fatalf("fake model request failed: %s", failure)
		default:
		}
		var errorBlocks, stoppedAuthContexts, locks, wakeups int
		if err := env.db.QueryRow(ctx, `
SELECT count(*)
FROM content_blocks block
JOIN agent_events event
  ON event.agent_id = block.agent_id
 AND event.model_output_id = block.owner_model_output_id
JOIN agents agent ON agent.id = block.agent_id
WHERE agent.project_id = $1
  AND block.agent_id = $2
  AND event.event_kind = 'model_output'
  AND block.block_kind = 'error'
  AND block.text_content = $3
`, projectUUID, agentUUID, errorMessage).Scan(&errorBlocks); err != nil {
			return false, err.Error()
		}
		if err := env.db.QueryRow(ctx, `
SELECT count(*)
FROM model_call_contexts context
JOIN model_outputs output
  ON output.agent_id = context.agent_id
 AND output.model_call_context_id = context.id
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.operation_kind = 'normal'
  AND context.state = 'failed'
  AND context.recovery_kind IS NULL
  AND context.error_kind = 'auth'
  AND context.error_code = 'invalid_api_key'
  AND output.stop_reason = 'error'
`, projectUUID, agentUUID).Scan(&stoppedAuthContexts); err != nil {
			return false, err.Error()
		}
		if err := env.db.QueryRow(
			ctx,
			scopedAgentRuntimeLockCountSQL,
			projectUUID,
			agentUUID,
		).Scan(&locks); err != nil {
			return false, err.Error()
		}
		if err := env.db.QueryRow(
			ctx,
			`SELECT count(*) FROM agent_wakeups wake JOIN agents agent ON agent.id = wake.agent_id WHERE agent.project_id = $1 AND wake.agent_id = $2`,
			projectUUID,
			agentUUID,
		).Scan(&wakeups); err != nil {
			return false, err.Error()
		}
		return errorBlocks == 1 && stoppedAuthContexts == 1 && locks == 0 && wakeups == 0,
			fmt.Sprintf(
				"terminal error not settled blocks=%d contexts=%d locks=%d wakeups=%d requests=%d logs=%s",
				errorBlocks,
				stoppedAuthContexts,
				locks,
				wakeups,
				requestCount.Load(),
				worker.logExcerpt(),
			)
	})

	project.createInput(t, ctx, agentID, secondInput)
	waitForServiceE2ETextOutput(
		t,
		ctx,
		env,
		projectUUID,
		agentUUID,
		finalOutput,
		modelFailures,
		worker,
	)

	var contexts, distinctTurns, durableErrors int
	if err := env.db.QueryRow(ctx, `
SELECT count(*), count(DISTINCT context_turn.turn_id)
FROM model_call_contexts context
JOIN model_call_context_turns context_turn
  ON context_turn.project_id = context.project_id
 AND context_turn.agent_id = context.agent_id
 AND context_turn.model_call_context_id = context.id
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.operation_kind = 'normal'
`, projectUUID, agentUUID).Scan(&contexts, &distinctTurns); err != nil {
		t.Fatalf("query terminal-error model contexts: %v", err)
	}
	if err := env.db.QueryRow(ctx, `
SELECT count(*)
FROM content_blocks block
JOIN agents agent ON agent.id = block.agent_id
WHERE agent.project_id = $1
  AND block.agent_id = $2
  AND block_kind = 'error'
  AND text_content = $3
`, projectUUID, agentUUID, errorMessage).Scan(&durableErrors); err != nil {
		t.Fatalf("query durable provider errors: %v", err)
	}
	if requestCount.Load() != 2 || contexts != 2 || distinctTurns != 2 || durableErrors != 1 {
		t.Fatalf(
			"terminal-error recovery requests=%d contexts=%d turns=%d errors=%d, want 2/2/2/1",
			requestCount.Load(),
			contexts,
			distinctTurns,
			durableErrors,
		)
	}
}

func validateHistoricalOpenAIToolExchange(
	rawInput any,
	callID string,
	resultNeedle string,
	newInputNeedle string,
) error {
	items, ok := rawInput.([]any)
	if !ok {
		return fmt.Errorf("OpenAI input = %T, want an item array", rawInput)
	}
	callIndex, resultIndex, newInputIndex := -1, -1, -1
	callCount, resultCount := 0, 0
	for index, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch item["type"] {
		case "function_call":
			if item["call_id"] == callID {
				callCount++
				callIndex = index
			}
		case "function_call_output":
			if item["call_id"] == callID {
				resultCount++
				resultIndex = index
				if !strings.Contains(mustJSONString(item["output"]), resultNeedle) {
					return fmt.Errorf(
						"historical tool result omitted opaque value %q: %+v",
						resultNeedle,
						item,
					)
				}
			}
		}
		if strings.Contains(mustJSONString(item), newInputNeedle) {
			newInputIndex = index
		}
	}
	if callCount != 1 || resultCount != 1 {
		return fmt.Errorf(
			"historical tool exchange appeared %d/%d times, want exactly one call and one result: %+v",
			callCount,
			resultCount,
			items,
		)
	}
	if callIndex < 0 || resultIndex <= callIndex || newInputIndex <= resultIndex {
		return fmt.Errorf(
			"historical tool exchange order call=%d result=%d new_input=%d: %+v",
			callIndex,
			resultIndex,
			newInputIndex,
			items,
		)
	}
	return nil
}

func waitForOpenServiceE2EInteraction(
	t *testing.T,
	ctx context.Context,
	project deterministicProject,
	agentID string,
	modelFailures <-chan string,
	worker serviceProcess,
) string {
	t.Helper()
	var interactionID string
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		select {
		case failure := <-modelFailures:
			t.Fatalf("fake model request failed: %s", failure)
		default:
		}
		listed := project.env.requestJSON(
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
		if !ok || len(data) == 0 {
			return false, "question interaction not open yet; worker_logs=" + worker.logExcerpt()
		}
		item, ok := data[0].(map[string]any)
		if !ok || item["interaction_kind"] != "question" || item["state"] != "open" {
			return false, "unexpected open interaction: " + mustJSONString(data[0])
		}
		interactionID, _ = item["id"].(string)
		return interactionID != "", "question interaction omitted its id"
	})
	return interactionID
}

func waitForServiceE2ETextOutput(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	projectUUID string,
	agentUUID string,
	text string,
	modelFailures <-chan string,
	worker serviceProcess,
) {
	t.Helper()
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		select {
		case failure := <-modelFailures:
			t.Fatalf("fake model request failed: %s", failure)
		default:
		}
		var count int
		if err := env.db.QueryRow(ctx, `
SELECT count(*)
FROM agent_events event
JOIN agents agent ON agent.id = event.agent_id
JOIN content_blocks block
  ON block.agent_id = event.agent_id
 AND block.owner_model_output_id = event.model_output_id
WHERE agent.project_id = $1
  AND event.agent_id = $2
  AND event.event_kind = 'model_output'
  AND block.block_kind = 'text'
  AND block.text_content = $3
`, projectUUID, agentUUID, text).Scan(&count); err != nil {
			return false, err.Error()
		}
		return count == 1, "model output not recorded yet; worker_logs=" + worker.logExcerpt()
	})
}
