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
	"github.com/stretchr/testify/require"
)

func TestServiceE2EOpenRouterOutputLimitContinuesThroughToolsAndText(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OPENROUTER_API_KEY"} {
		t.Setenv(key, "service-e2e-test-key")
	}
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "output-limit-recovery")
	const modelName = "service-e2e-openrouter"
	const finalText = "Recovered with a smaller tool call."
	var requests atomic.Int64
	failures := make(chan string, 1)
	fail := func(w http.ResponseWriter, message string) {
		select {
		case failures <- message:
		default:
		}
		http.Error(w, message, http.StatusBadRequest)
	}
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.URL.Path != "/chat/completions" || json.NewDecoder(r.Body).Decode(&body) != nil {
			fail(w, "invalid chat request")
			return
		}
		if body["stream"] != true || body["max_completion_tokens"] != float64(65536) {
			fail(w, "expected streaming with the configured output allowance")
			return
		}
		messages, ok := body["messages"].([]any)
		if !ok || len(messages) == 0 {
			fail(w, "missing messages")
			return
		}
		switch requests.Add(1) {
		case 1:
			writeOutputLimitChatChunk(w, map[string]any{
				"role": "assistant", "content": "Checking machines.",
				"tool_calls": []any{
					map[string]any{
						"index": 0,
						"id":    "complete-call",
						"type":  "function",
						"function": map[string]any{
							"name":      "list_machines",
							"arguments": "{}",
						},
					},
					map[string]any{
						"index": 1,
						"id":    "partial-call",
						"type":  "function",
						"function": map[string]any{
							"name":      "list_machines",
							"arguments": "{\"unused\":",
						},
					},
				},
			}, "tool_calls", "")
			// The native reason can arrive after the normalized terminal chunk.
			writeOutputLimitChatChunk(w, map[string]any{}, "", "length")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		case 2:
			if len(messages) < 3 {
				fail(w, "missing mixed tool history")
				return
			}
			accepted, _ := messages[len(messages)-2].(map[string]any)
			rejected, _ := messages[len(messages)-1].(map[string]any)
			if accepted["role"] != "tool" || accepted["tool_call_id"] != "complete-call" ||
				!strings.Contains(fmt.Sprint(accepted["content"]), `"machines":[]`) ||
				rejected["role"] != "tool" || rejected["tool_call_id"] != "partial-call" ||
				!strings.Contains(fmt.Sprint(rejected["content"]), "complete JSON object") ||
				strings.Contains(mustJSONString(messages), "Automatic Omnara harness notice") {
				fail(w, "mixed calls must receive the real result and a paired argument error")
				return
			}

			writeOutputLimitChatChunk(w, map[string]any{"role": "assistant", "tool_calls": []any{
				map[string]any{
					"index": 0,
					"id":    "accepted-call",
					"type":  "function",
					"function": map[string]any{
						"name":      "list_machines",
						"arguments": "{}",
					},
				},
			}}, "tool_calls", "")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		case 3:
			last, _ := messages[len(messages)-1].(map[string]any)
			previous, _ := messages[len(messages)-2].(map[string]any)
			if last["role"] != "tool" || last["tool_call_id"] != "accepted-call" ||
				!strings.Contains(fmt.Sprint(last["content"]), `"machines":[]`) ||
				previous["role"] != "assistant" || !strings.Contains(mustJSONString(previous), "accepted-call") {
				fail(w, "completion must receive the accepted call and real tool result")
				return
			}
			writeOutputLimitChatChunk(w, map[string]any{
				"role": "assistant", "reasoning": "working through results", "content": "The available machines",
			}, "length", "")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		case 4:
			last, _ := messages[len(messages)-1].(map[string]any)
			previous, _ := messages[len(messages)-2].(map[string]any)
			if last["role"] != "user" || !strings.Contains(fmt.Sprint(last["content"]), "Automatic Omnara harness notice") ||
				previous["role"] != "assistant" || previous["reasoning"] != "working through results" ||
				previous["content"] != "The available machines" {
				fail(w, "text cutoff must preserve reasoning and append a harness notice")
				return
			}
			writeOutputLimitChatChunk(w, map[string]any{"role": "assistant"}, "length", "")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		case 5:
			if strings.Count(mustJSONString(messages), "Automatic Omnara harness notice") != 2 ||
				!strings.Contains(mustJSONString(messages), `"reasoning":"working through results"`) {
				fail(w, "empty cutoff must continue without removing historical reasoning or notices")
				return
			}
			writeServiceE2EOpenRouterChatMessage(w, "completed", modelName, finalText, 100, 12)
		default:
			fail(w, "unexpected extra model request")
		}
	}))
	defer router.Close()
	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithToolsAndModelOptions(
		t,
		ctx,
		"output-limit-recovery",
		"openrouter-prod",
		modelName,
		serviceE2EConfiguredModelOptionsByIdentity{
			{ProviderConfigName: "openrouter-prod", ConfiguredModelName: modelName}: {
				ContextWindowTokens: 128000, MaxOutputTokens: 65536, DefaultMaxOutputTokens: 65536,
			},
		},
		"list_machines",
	)
	agentID := project.createAgent(t, ctx)
	project.createInput(t, ctx, agentID, "Check available machines.")
	worker := env.startWorker(
		t,
		ctx,
		project.projectID,
		serviceWorkerOptions{
			ProviderConfig: "openrouter-prod",
			BaseURL:        router.URL,
		},
	)
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	waitForServiceE2ETextOutput(t, ctx, env, projectUUID, agentUUID, finalText, failures, worker)
	waitForServiceE2EAgentIdle(t, ctx, env, projectUUID, agentUUID)
	var succeeded, failed, truncated, calls int
	require.NoError(t, env.db.QueryRow(
		ctx,
		`SELECT count(*) FILTER(WHERE state='succeeded'),count(*) FILTER(WHERE state='failed') FROM model_call_contexts WHERE agent_id=$1`,
		agentUUID,
	).Scan(
		&succeeded,
		&failed,
	))
	require.NoError(t, env.db.QueryRow(
		ctx,
		`SELECT count(*) FROM model_outputs output JOIN model_call_contexts context ON context.agent_id=output.agent_id AND context.id=output.model_call_context_id WHERE output.agent_id=$1 AND output.stop_reason='max_tokens' AND output.provider_replay IS NOT NULL AND context.provider_metadata->>'request_max_output_tokens'='65536' AND context.provider_metadata->'openrouter'->>'finish_reason'='tool_calls' AND context.provider_metadata->'openrouter'->>'native_finish_reason'='length'`,
		agentUUID,
	).Scan(&truncated))
	require.NoError(t, env.db.QueryRow(
		ctx,
		`SELECT count(*) FROM tool_calls WHERE agent_id=$1`,
		agentUUID,
	).Scan(&calls))
	if requests.Load() != 5 ||
		succeeded != 5 ||
		failed != 0 ||
		truncated != 1 ||
		calls != 3 {
		t.Fatalf(
			"requests=%d succeeded=%d failed=%d truncated=%d calls=%d",
			requests.Load(),
			succeeded,
			failed,
			truncated,
			calls,
		)
	}
}

func writeOutputLimitChatChunk(w http.ResponseWriter, delta map[string]any, finish, native string) {
	chunk := map[string]any{
		"id": "output-limit-response", "model": "service-e2e-openrouter",
		"choices": []any{
			map[string]any{
				"index":                0,
				"delta":                delta,
				"finish_reason":        finish,
				"native_finish_reason": native,
			},
		},
	}
	if native != "" {
		chunk["usage"] = map[string]any{"prompt_tokens": 100, "completion_tokens": 65536}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(w, "data: %s\n\n", mustJSONString(chunk))
}
