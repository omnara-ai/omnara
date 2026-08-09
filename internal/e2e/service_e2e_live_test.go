//go:build integration && servicee2e && live

package e2e

import (
	"context"
	"crypto/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/testutil/mcptest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestServiceE2ELiveOpenAIModelTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Fatal("OPENAI_API_KEY is required for live OpenAI service E2E")
	}
	runLiveServiceModelTurn(t, ctx, liveServiceModelTurnOptions{
		Seed:                "live-openai-model-turn",
		ProviderConfig:      "openai-prod",
		ConfiguredModelName: liveOpenAIConfiguredModelName(),
		BaseURL:             os.Getenv("OPENAI_BASE_URL"),
	})
}

func TestServiceE2ELiveOpenAIChatCompletionsModelTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Fatal("OPENAI_API_KEY is required for live OpenAI Chat Completions service E2E")
	}
	runLiveServiceModelTurn(t, ctx, liveServiceModelTurnOptions{
		Seed:                "live-openai-chat-completions-model-turn",
		ProviderConfig:      "openai-chat-prod",
		ConfiguredModelName: liveOpenAIChatConfiguredModelName(),
		BaseURL:             os.Getenv("OPENAI_BASE_URL"),
	})
}

func TestServiceE2ELiveOpenRouterModelTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		t.Fatal("OPENROUTER_API_KEY is required for live OpenRouter service E2E")
	}
	runLiveServiceModelTurn(t, ctx, liveServiceModelTurnOptions{
		Seed:                "live-openrouter-model-turn",
		ProviderConfig:      "openrouter-prod",
		ConfiguredModelName: liveOpenRouterConfiguredModelName(),
		BaseURL:             os.Getenv("OPENROUTER_BASE_URL"),
	})
}

func TestServiceE2ELiveAnthropicModelTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Fatal("ANTHROPIC_API_KEY is required for live Anthropic service E2E")
	}
	runLiveServiceModelTurn(t, ctx, liveServiceModelTurnOptions{
		Seed:                "live-anthropic-model-turn",
		ProviderConfig:      "anthropic-prod",
		ConfiguredModelName: liveAnthropicConfiguredModelName(),
		BaseURL:             os.Getenv("ANTHROPIC_BASE_URL"),
	})
}

func TestServiceE2ELiveAPIFormatSwitchingPreservesHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Fatal("OPENAI_API_KEY is required for live API-format switching E2E")
	}
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Fatal("ANTHROPIC_API_KEY is required for live API-format switching E2E")
	}

	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "live-api-format-switching")
	toolResult := "OPAQUE_TOOL_RESULT_" + rand.Text()
	mcpServer := mcptest.NewJSONServerWithGreetResult(t, toolResult)
	env.startAPI(t, ctx)
	nonce := strings.ToUpper(strings.ReplaceAll(env.seed, "-", "_"))
	toolName := toolcatalog.MCPRuntimeToolName("docs", "greet")
	toolArgument := "TOOL_FACT_" + nonce
	secondFact := "BETA_" + nonce
	thirdFact := "GAMMA_" + nonce
	stages := []liveAPIFormatSwitchStage{
		{
			ProviderConfig:      "openai-prod",
			ConfiguredModelName: liveOpenAIConfiguredModelName(),
			BaseURL:             os.Getenv("OPENAI_BASE_URL"),
			Prompt: strings.Join([]string{
				"Call the " + toolName + " tool exactly once with name set to " + toolArgument + ".",
				"The exact result is generated inside the tool server and cannot be reconstructed from the tool name or arguments.",
				"Treat that exact result as a durable fact for the rest of the conversation.",
				"After the tool returns, do not call another tool. Reply with exactly TOOL_CAPTURED_" + nonce + ".",
			}, "\n"),
			ExpectedOutput:     []string{"TOOL_CAPTURED_" + nonce},
			ForbiddenOutput:    []string{toolResult},
			ExpectedFormat:     "openai-responses",
			ExpectedModelCalls: 2,
		},
		{
			ProviderConfig:      "anthropic-prod",
			ConfiguredModelName: liveAnthropicConfiguredModelName(),
			BaseURL:             os.Getenv("ANTHROPIC_BASE_URL"),
			Prompt: strings.Join([]string{
				"Do not call any tool. Recall the exact greeting returned by the only earlier tool call.",
				"Also remember this second opaque durable fact for later: " + secondFact + ". Do not repeat the second fact yet.",
				"Reply on one line beginning with ANTHROPIC_RECALL_" + nonce + " followed by the exact tool greeting.",
			}, "\n"),
			ExpectedOutput:     []string{"ANTHROPIC_RECALL_" + nonce, toolResult},
			ExpectedFormat:     "anthropic-messages",
			ExpectedModelCalls: 1,
		},
		{
			ProviderConfig:      "openai-chat-prod",
			ConfiguredModelName: liveOpenAIChatConfiguredModelName(),
			BaseURL:             os.Getenv("OPENAI_BASE_URL"),
			Prompt: strings.Join([]string{
				"Do not call any tool. Recall the exact earlier tool greeting and the second durable fact.",
				"Also remember this third opaque durable fact for later: " + thirdFact + ". Do not repeat the third fact yet.",
				"Reply on one line beginning with CHAT_RECALL_" + nonce + " followed by the tool greeting and second fact.",
			}, "\n"),
			ExpectedOutput:     []string{"CHAT_RECALL_" + nonce, toolResult, secondFact},
			ExpectedFormat:     "openai-chat-completions",
			ExpectedModelCalls: 1,
		},
		{
			ProviderConfig:      "openai-prod",
			ConfiguredModelName: liveOpenAIConfiguredModelName(),
			BaseURL:             os.Getenv("OPENAI_BASE_URL"),
			Prompt: strings.Join([]string{
				"Do not call any tool. Recall the exact earlier tool greeting and both later durable facts.",
				"Reply on one line beginning with FINAL_RECALL_" + nonce + " followed by the greeting and both facts.",
			}, "\n"),
			ExpectedOutput:     []string{"FINAL_RECALL_" + nonce, toolResult, secondFact, thirdFact},
			ExpectedFormat:     "openai-responses",
			ExpectedModelCalls: 1,
		},
	}

	project := env.bootstrapProjectViaAPIWithSource(
		t,
		ctx,
		"live-api-format-switching",
		liveAPIFormatSwitchConfig(stages[0], mcpServer.URL),
	)
	agentID := project.createAgent(t, ctx)
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)

	for index, stage := range stages {
		var beforeSequence int64
		if err := env.db.QueryRow(ctx, `SELECT coalesce(max(event.sequence), 0) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id WHERE agent.project_id = $1 AND event.agent_id = $2`, projectUUID, agentUUID).
			Scan(&beforeSequence); err != nil {
			t.Fatalf("query event sequence before stage %d: %v", index+1, err)
		}
		if index > 0 {
			project.updateConfig(t, ctx, agentID, liveAPIFormatSwitchConfig(stage, mcpServer.URL))
		}
		project.createInput(t, ctx, agentID, stage.Prompt)
		worker := env.startWorker(t, ctx, project.projectID, serviceWorkerOptions{
			ProviderConfig: stage.ProviderConfig,
			BaseURL:        stage.BaseURL,
		})
		output := waitForLiveModelOutputContaining(
			t,
			ctx,
			env,
			project.projectID,
			agentID,
			beforeSequence,
			stage.ExpectedOutput,
			worker,
		)
		for _, forbidden := range stage.ForbiddenOutput {
			if strings.Contains(output, forbidden) {
				t.Fatalf("stage %d output %q unexpectedly repeated %q", index+1, output, forbidden)
			}
		}
		waitForLiveAgentIdle(t, ctx, env, project.projectID, agentID, worker)
		worker.stop()
	}

	rows, err := env.db.Query(ctx, `
SELECT provider_config.api_format, revision.provider_model_slug
FROM model_call_contexts context
JOIN configured_model_revisions revision
  ON revision.org_id = context.org_id
 AND revision.id = context.configured_model_revision_id
JOIN model_provider_configs provider_config
  ON provider_config.org_id = revision.org_id
 AND provider_config.id = revision.model_provider_config_id
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.operation_kind = 'normal'
  AND context.state = 'succeeded'
ORDER BY context.input_event_sequence, context.attempt_number, context.created_at, context.id`, projectUUID, agentUUID)
	if err != nil {
		t.Fatalf("query API-format switching model contexts: %v", err)
	}
	defer rows.Close()
	var gotFormats, gotModels []string
	for rows.Next() {
		var apiFormat, providerModelSlug string
		if err := rows.Scan(&apiFormat, &providerModelSlug); err != nil {
			t.Fatalf("scan API-format switching model context: %v", err)
		}
		gotFormats = append(gotFormats, apiFormat)
		gotModels = append(gotModels, providerModelSlug)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate API-format switching model contexts: %v", err)
	}
	wantFormats := make([]string, 0, len(stages))
	wantModels := make([]string, 0, len(stages))
	for _, stage := range stages {
		for range stage.ExpectedModelCalls {
			wantFormats = append(wantFormats, stage.ExpectedFormat)
			wantModels = append(wantModels, stage.ConfiguredModelName)
		}
	}
	if strings.Join(gotFormats, ",") != strings.Join(wantFormats, ",") ||
		strings.Join(gotModels, ",") != strings.Join(wantModels, ",") {
		t.Fatalf(
			"model context formats/models = %v/%v, want %v/%v",
			gotFormats,
			gotModels,
			wantFormats,
			wantModels,
		)
	}
	var toolCalls, completedGreetingCalls, greetingResultBlocks int
	if err := env.db.QueryRow(ctx, `
SELECT count(*),
       count(*) FILTER (WHERE call.name = $3 AND call.state = 'completed')
FROM tool_call_read_projection call
WHERE call.project_id = $1 AND call.agent_id = $2`, projectUUID, agentUUID, toolName).Scan(
		&toolCalls,
		&completedGreetingCalls,
	); err != nil {
		t.Fatalf("query API-format switching tool calls: %v", err)
	}
	if err := env.db.QueryRow(ctx, `
SELECT count(*)
FROM tool_call_read_projection call
JOIN tool_call_results result
  ON result.agent_id = call.agent_id
 AND result.tool_call_id = call.id
JOIN content_blocks block
  ON block.agent_id = result.agent_id
 AND block.owner_tool_call_result_id = result.id
WHERE call.project_id = $1
  AND call.agent_id = $2
  AND call.name = $3
  AND block.block_kind = 'text'
  AND block.text_content = $4`, projectUUID, agentUUID, toolName, toolResult).Scan(&greetingResultBlocks); err != nil {
		t.Fatalf("query API-format switching tool result: %v", err)
	}
	if toolCalls != 1 || completedGreetingCalls != 1 || greetingResultBlocks != 1 {
		t.Fatalf(
			"tool evidence calls=%d completed_greetings=%d greeting_results=%d, want 1/1/1",
			toolCalls,
			completedGreetingCalls,
			greetingResultBlocks,
		)
	}
}

func TestServiceE2ELiveOpenAICompactionRecall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Fatal("OPENAI_API_KEY is required for live OpenAI compaction recall E2E")
	}
	runLiveServiceCompactionRecall(t, ctx, liveServiceModelTurnOptions{
		Seed:                "live-openai-compaction-recall",
		ProviderConfig:      "openai-prod",
		ConfiguredModelName: liveOpenAIConfiguredModelName(),
		BaseURL:             os.Getenv("OPENAI_BASE_URL"),
	})
}

func TestServiceE2ELiveAnthropicCompactionRecall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Fatal("ANTHROPIC_API_KEY is required for live Anthropic compaction recall E2E")
	}
	runLiveServiceCompactionRecall(t, ctx, liveServiceModelTurnOptions{
		Seed:                "live-anthropic-compaction-recall",
		ProviderConfig:      "anthropic-prod",
		ConfiguredModelName: liveAnthropicConfiguredModelName(),
		BaseURL:             os.Getenv("ANTHROPIC_BASE_URL"),
	})
}

type liveServiceModelTurnOptions struct {
	Seed                string
	ProviderConfig      string
	ConfiguredModelName string
	BaseURL             string
}

type liveAPIFormatSwitchStage struct {
	ProviderConfig      string
	ConfiguredModelName string
	BaseURL             string
	Prompt              string
	ExpectedOutput      []string
	ForbiddenOutput     []string
	ExpectedFormat      string
	ExpectedModelCalls  int
}

func liveAPIFormatSwitchConfig(stage liveAPIFormatSwitchStage, mcpURL string) string {
	return strings.Join([]string{
		"name: Live API Format Switching",
		"instruction: Use tools only when explicitly requested. Preserve exact tool results and opaque facts across the conversation.",
		"model:",
		"  provider_config: " + stage.ProviderConfig,
		"  name: " + stage.ConfiguredModelName,
		"mcp:",
		"  docs:",
		"    url: " + mcpURL,
		"    permission:",
		"      mode: always_allow",
		"",
	}, "\n")
}

func runLiveServiceModelTurn(t *testing.T, ctx context.Context, opts liveServiceModelTurnOptions) {
	t.Helper()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, opts.Seed)
	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPI(t, ctx, opts.Seed, opts.ProviderConfig, opts.ConfiguredModelName)
	agentID := project.createAgent(t, ctx)
	nonce := strings.ToUpper(strings.ReplaceAll(opts.Seed+"-"+env.seed, "-", "_"))
	project.createInput(t, ctx, agentID, "Reply with exactly this token and no extra words: "+nonce)
	worker := env.startWorker(t, ctx, project.projectID, serviceWorkerOptions{
		ProviderConfig: opts.ProviderConfig,
		BaseURL:        opts.BaseURL,
	})
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)

	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var count int
		err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.event_kind = 'model_output' AND block.block_kind = 'text' AND block.text_content LIKE '%' || $3 || '%'`, projectUUID, agentUUID, nonce).
			Scan(&count)
		if err != nil {
			return false, err.Error()
		}
		return count == 1, "live provider assistant output not recorded yet"
	})
	waitForLiveAgentIdle(t, ctx, env, project.projectID, agentID, worker)
	assertLiveModelUsage(t, ctx, env, projectUUID, agentUUID, nonce)
}

func assertLiveModelUsage(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	projectID, agentID string,
	outputContains string,
) {
	t.Helper()
	var usage modelenvelope.Usage
	if err := env.db.QueryRow(ctx, `
SELECT coalesce(context.input_tokens_total, 0),
       coalesce(context.uncached_input_tokens, 0),
       coalesce(context.cache_read_input_tokens, 0),
       coalesce(context.cache_write_input_tokens, 0),
       coalesce(context.output_tokens_total, 0),
       coalesce(context.reasoning_output_tokens, 0)
FROM agent_events event
JOIN agents agent ON agent.id = event.agent_id
JOIN content_blocks block
  ON block.agent_id = event.agent_id
 AND block.owner_model_output_id = event.model_output_id
JOIN model_outputs output
  ON output.agent_id = event.agent_id
 AND output.id = event.model_output_id
JOIN model_call_contexts context
  ON context.agent_id = output.agent_id
 AND context.id = output.model_call_context_id
WHERE agent.project_id = $1
  AND event.agent_id = $2
  AND event.event_kind = 'model_output'
  AND block.block_kind = 'text'
  AND block.text_content LIKE '%' || $3 || '%'
  AND context.operation_kind = 'normal'
  AND context.state = 'succeeded'
ORDER BY event.sequence DESC
LIMIT 1`, projectID, agentID, outputContains).Scan(
		&usage.InputTokens,
		&usage.UncachedInputTokens,
		&usage.CacheReadTokens,
		&usage.CacheWriteTokens,
		&usage.OutputTokens,
		&usage.ReasoningTokens,
	); err != nil {
		t.Fatalf("query live model usage: %v", err)
	}
	if usage.InputTokens == 0 || usage.OutputTokens == 0 {
		t.Fatalf("live model usage totals not persisted: %+v", usage)
	}
	if normalized := modelenvelope.NormalizeUsage(usage); normalized != usage {
		t.Fatalf("live model usage is internally inconsistent: %+v", usage)
	}
}

func runLiveServiceCompactionRecall(t *testing.T, ctx context.Context, opts liveServiceModelTurnOptions) {
	t.Helper()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, opts.Seed)
	env.startAPI(t, ctx)
	sourceYAML := strings.Join([]string{
		"name: Live Compaction Recall",
		"instruction: Help the user make progress.",
		"model:",
		"  provider_config: " + opts.ProviderConfig,
		"  name: " + opts.ConfiguredModelName,
		"",
	}, "\n")
	project := env.bootstrapProjectViaAPIWithSourceAndModelOptions(
		t,
		ctx,
		opts.Seed,
		sourceYAML,
		map[string]serviceE2EConfiguredModelOptions{
			opts.ConfiguredModelName: {
				ContextWindowTokens:    16000,
				MaxOutputTokens:        8192,
				DefaultMaxOutputTokens: 4096,
			},
		},
	)
	config := env.requestJSON(
		t,
		ctx,
		http.MethodGet,
		project.projectPath+"/agent-configs/"+project.configID,
		nil,
		"",
		project.adminToken,
		http.StatusOK,
	)
	modelConfig := config["model"].(map[string]any)
	if modelConfig["context_window_tokens"] != float64(16000) || modelConfig["max_output_tokens"] != float64(8192) ||
		modelConfig["default_max_output_tokens"] != float64(4096) {
		t.Fatalf(
			"live compaction agent config model budget = %+v, want context_window_tokens=16000 max_output_tokens=8192 default_max_output_tokens=4096",
			modelConfig,
		)
	}
	agentID := project.createAgent(t, ctx)
	projectLabel := "PROJECT_LABEL_" + strings.ToUpper(strings.ReplaceAll(opts.Seed+"-"+env.seed, "-", "_"))
	bridgeToken := "BRIDGE_" + strings.ToUpper(strings.ReplaceAll(opts.Seed+"-"+env.seed, "-", "_"))
	rawPaddingToken := "RAW_PADDING_" + strings.ToUpper(strings.ReplaceAll(opts.Seed+"-"+env.seed, "-", "_"))
	firstPrompt := strings.Join([]string{
		"The fictional project label for this conversation is " + projectLabel + ".",
		"Remember this exact label if the conversation is summarized or compacted.",
		"Reply with exactly ACK_" + projectLabel + " and no other words.",
		"Disposable context padding for the first turn: " + strings.Repeat(rawPaddingToken+" ", 250),
	}, "\n")
	project.createInput(t, ctx, agentID, firstPrompt)
	worker := env.startWorker(t, ctx, project.projectID, serviceWorkerOptions{
		ProviderConfig: opts.ProviderConfig,
		BaseURL:        opts.BaseURL,
	})
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	waitForLiveModelOutputText(t, ctx, env, project.projectID, agentID, projectLabel, worker)

	secondPrompt := strings.Join([]string{
		"Create enough ordinary context pressure that the system may compact older history before this turn completes.",
		"Do not mention the earlier project label in this response.",
		"Reply with exactly " + bridgeToken + " and no other words.",
		"Additional current-turn padding: " + strings.Repeat("fresh continuation detail ", 1100),
	}, "\n")
	var beforeBridgeSequence int64
	if err := env.db.QueryRow(ctx, `SELECT coalesce(max(event.sequence), 0) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id WHERE agent.project_id = $1 AND event.agent_id = $2`, projectUUID, agentUUID).
		Scan(&beforeBridgeSequence); err != nil {
		t.Fatalf("query pre-bridge event sequence: %v", err)
	}
	project.createInput(t, ctx, agentID, secondPrompt)
	waitForServiceE2EConditionUntil(t, ctx, time.Now().Add(5*time.Minute), func() (bool, string) {
		var checkpoints, compactedContexts, failedBudgetContexts int
		if err := env.db.QueryRow(ctx, `SELECT count(*) FROM context_checkpoints checkpoint JOIN agents agent ON agent.id = checkpoint.agent_id WHERE agent.project_id = $1 AND checkpoint.agent_id = $2 AND checkpoint.summary LIKE '%' || $3 || '%' AND checkpoint.summary NOT LIKE '%' || $4 || '%'`, projectUUID, agentUUID, projectLabel, rawPaddingToken).
			Scan(&checkpoints); err != nil {
			return false, err.Error()
		}
		var err error
		compactedContexts, err = successfulContextsUsingSummaryCheckpoint(
			ctx,
			env,
			projectUUID,
			agentUUID,
			projectLabel,
			rawPaddingToken,
		)
		if err != nil {
			return false, err.Error()
		}
		if err := env.db.QueryRow(ctx, `SELECT count(*) FROM model_call_contexts WHERE project_id = $1 AND agent_id = $2 AND operation_kind = 'normal' AND state = 'failed' AND recovery_kind = 'compact' AND error_kind = 'context_window' AND error_code = 'prepared_request_budget_overflow'`, projectUUID, agentUUID).
			Scan(&failedBudgetContexts); err != nil {
			return false, err.Error()
		}
		latestOutput, err := latestModelOutputTextAfterSequence(
			ctx,
			env,
			projectUUID,
			agentUUID,
			beforeBridgeSequence,
		)
		if err != nil {
			return false, err.Error()
		}
		outputMatches := strings.Contains(latestOutput, bridgeToken) && !strings.Contains(latestOutput, projectLabel)
		if outputMatches && checkpoints == 1 && compactedContexts >= 1 && failedBudgetContexts == 1 {
			return true, ""
		}
		var locks, wakeups int
		if err := env.db.QueryRow(ctx, scopedAgentRuntimeLockCountSQL, projectUUID, agentUUID).
			Scan(&locks); err != nil {
			return false, err.Error()
		}
		if err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_wakeups wake JOIN agents agent ON agent.id = wake.agent_id WHERE agent.project_id = $1 AND wake.agent_id = $2`, projectUUID, agentUUID).
			Scan(&wakeups); err != nil {
			return false, err.Error()
		}
		if checkpoints == 1 && compactedContexts >= 1 && failedBudgetContexts == 1 && locks == 0 && wakeups == 0 {
			t.Fatalf(
				"post-compaction model output violated bridge contract: output=%q want_contains=%q want_excludes=%q",
				latestOutput,
				bridgeToken,
				projectLabel,
			)
		}
		return false, "live compaction bridge not complete checkpoints=" + itoa(checkpoints) +
			" compacted_contexts=" + itoa(compactedContexts) +
			" failed_budget_contexts=" + itoa(failedBudgetContexts) +
			" locks=" + itoa(locks) +
			" wakeups=" + itoa(wakeups) +
			" latest_output=" + strconv.Quote(latestOutput) +
			" worker_logs=" + worker.logExcerpt()
	})

	var beforeRecallSequence int64
	if err := env.db.QueryRow(ctx, `SELECT coalesce(max(event.sequence), 0) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id WHERE agent.project_id = $1 AND event.agent_id = $2`, projectUUID, agentUUID).
		Scan(&beforeRecallSequence); err != nil {
		t.Fatalf("query pre-recall event sequence: %v", err)
	}
	project.createInput(
		t,
		ctx,
		agentID,
		"What is the fictional project label from earlier? Reply with the exact label and no explanation.",
	)
	waitForServiceE2EConditionUntil(t, ctx, time.Now().Add(5*time.Minute), func() (bool, string) {
		var compactedContexts, locks, wakeups int
		var err error
		compactedContexts, err = successfulContextsUsingSummaryCheckpoint(
			ctx,
			env,
			projectUUID,
			agentUUID,
			projectLabel,
			rawPaddingToken,
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
		latestOutput, err := latestModelOutputTextAfterSequence(
			ctx,
			env,
			projectUUID,
			agentUUID,
			beforeRecallSequence,
		)
		if err != nil {
			return false, err.Error()
		}
		if strings.Contains(latestOutput, projectLabel) && compactedContexts >= 2 && locks == 0 && wakeups == 0 {
			return true, ""
		}
		if compactedContexts >= 2 && locks == 0 && wakeups == 0 {
			t.Fatalf(
				"post-compaction recall output omitted durable fact: output=%q want_contains=%q",
				latestOutput,
				projectLabel,
			)
		}
		return false, "live compaction recall not complete compacted_contexts=" + itoa(compactedContexts) +
			" locks=" + itoa(locks) +
			" wakeups=" + itoa(wakeups) +
			" latest_output=" + strconv.Quote(latestOutput) +
			" worker_logs=" + worker.logExcerpt()
	})
}

func latestModelOutputTextAfterSequence(
	ctx context.Context,
	env *serviceE2EEnvironment,
	projectUUID, agentUUID string,
	afterSequence int64,
) (string, error) {
	var latestOutput string
	err := env.db.QueryRow(ctx, `
SELECT coalesce((
  SELECT string_agg(block.text_content, '' ORDER BY block.ordinal)
  FROM agent_events event
  JOIN agents agent ON agent.id = event.agent_id
  JOIN content_blocks block
    ON block.agent_id = event.agent_id
   AND block.owner_model_output_id = event.model_output_id
  WHERE agent.project_id = $1
    AND event.agent_id = $2
    AND event.sequence > $3
    AND event.event_kind = 'model_output'
    AND block.block_kind = 'text'
  GROUP BY event.sequence
  ORDER BY event.sequence DESC
  LIMIT 1
), '')`, projectUUID, agentUUID, afterSequence).Scan(&latestOutput)
	return latestOutput, err
}

func waitForLiveModelOutputText(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	projectID, agentID, contains string,
	worker serviceProcess,
) {
	t.Helper()
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	waitForServiceE2EConditionUntil(t, ctx, time.Now().Add(3*time.Minute), func() (bool, string) {
		latestOutput, err := latestModelOutputTextAfterSequence(ctx, env, projectUUID, agentUUID, 0)
		if err != nil {
			return false, err.Error()
		}
		if strings.Contains(latestOutput, contains) {
			return true, ""
		}
		return false, "latest live model output=" + strconv.Quote(latestOutput) +
			" missing=" + contains + " worker_logs=" + worker.logExcerpt()
	})
}

func waitForLiveModelOutputContaining(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	projectID, agentID string,
	afterSequence int64,
	expected []string,
	worker serviceProcess,
) string {
	t.Helper()
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	var matchedOutput string
	waitForServiceE2EConditionUntil(t, ctx, time.Now().Add(3*time.Minute), func() (bool, string) {
		latestOutput, err := latestModelOutputTextAfterSequence(
			ctx,
			env,
			projectUUID,
			agentUUID,
			afterSequence,
		)
		if err != nil {
			return false, err.Error()
		}
		for _, value := range expected {
			if !strings.Contains(latestOutput, value) {
				var latestFailure string
				_ = env.db.QueryRow(ctx, `
SELECT concat_ws(' ', error_code, error_message, error_details::text)
FROM model_call_contexts
WHERE project_id = $1
  AND agent_id = $2
  AND input_event_sequence > $3
  AND state = 'failed'
  AND coalesce(recovery_kind, '') = ''
ORDER BY input_event_sequence DESC, attempt_number DESC
LIMIT 1`, projectUUID, agentUUID, afterSequence).Scan(&latestFailure)
				if latestFailure != "" {
					t.Fatalf("terminal live model failure: %s worker_logs=%s", latestFailure, worker.logExcerpt())
				}
				return false, "latest live model output=" + latestOutput +
					" missing=" + value + " worker_logs=" + worker.logExcerpt()
			}
		}
		matchedOutput = latestOutput
		return true, ""
	})
	return matchedOutput
}

func waitForLiveAgentIdle(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	projectID, agentID string,
	worker serviceProcess,
) {
	t.Helper()
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
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
		return locks == 0 && wakeups == 0,
			"live provider runtime lock or wakeup still present worker_logs=" + worker.logExcerpt()
	})
}

func liveOpenAIConfiguredModelName() string {
	if providerModelSlug := os.Getenv("OMNARA_E2E_OPENAI_PROVIDER_MODEL_SLUG"); providerModelSlug != "" {
		return providerModelSlug
	}
	return "gpt-5.5"
}

func liveAnthropicConfiguredModelName() string {
	if providerModelSlug := os.Getenv("OMNARA_E2E_ANTHROPIC_PROVIDER_MODEL_SLUG"); providerModelSlug != "" {
		return providerModelSlug
	}
	return "claude-sonnet-4-6"
}
