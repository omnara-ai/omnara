//go:build integration

package kernel

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
)

func TestAgentExecutorSendsFittingRequestAfterHighUsageTruncation(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgentWithModelOptions(
		t,
		ctx,
		"openai/kernel-test",
		fixture.Now,
		kernelConfiguredModelOptions{
			ContextWindowTokens: intPtrForKernelCompactionTest(10_000),
			MaxOutputTokens:     intPtrForKernelCompactionTest(6_000),
		},
	)

	const replayMarker = "reasoning-replay-that-max-tokens-must-clear"
	firstModel := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		capabilities: model.Capabilities{
			ContextWindowTokens: 10_000,
			MaxOutputTokens:     6_000,
		},
		responses: []model.Response{{
			ID:                      "resp_large_hidden_reasoning",
			ServedProviderModelSlug: "router/fallback-model",
			ProviderReplay: json.RawMessage(
				`[{"type":"reasoning","encrypted_content":"` + replayMarker + `"}]`,
			),
			Content:    []model.ResponsePart{{Type: "text", Text: "short visible truncated response"}},
			StopReason: model.StopReasonMaxTokens,
			Usage: model.Usage{
				InputTokens:     1_450,
				OutputTokens:    5_000,
				ReasoningTokens: 4_990,
			},
		}},
	}
	firstTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"seed a response with provider usage that is much larger than its visible content",
		fixture.Now.Add(time.Second),
	)
	firstExecutor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, firstModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(2 * time.Second) },
	}
	if err := firstExecutor.ExecuteModelWork(ctx, firstTurn); err != nil {
		t.Fatalf("execute first turn: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		firstTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release first runtime lock: %v", err)
	}

	currentModel := &sequenceKernelModel{
		providerModelSlug:           "kernel-test",
		preparedInputTokenEstimator: func(modelcontext.Bundle) int { return 500 },
		capabilities: model.Capabilities{
			ContextWindowTokens: 10_000,
			MaxOutputTokens:     6_000,
		},
		responses: []model.Response{{
			ID:         "resp_current_request",
			Content:    []model.ResponsePart{{Type: "text", Text: "current fitting request was sent"}},
			StopReason: model.StopReasonEndTurn,
		}},
	}
	currentTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"continue with the current fitting request",
		fixture.Now.Add(3*time.Second),
	)
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, currentModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(4 * time.Second) },
	}
	if err := executor.ExecuteModelWork(ctx, currentTurn); err != nil {
		t.Fatalf("execute current fitting turn: %v", err)
	}
	if currentModel.respondedCount() != 1 {
		t.Fatalf("current turn sent %d requests, want one normal provider call", currentModel.respondedCount())
	}
	request := currentModel.responded[0]
	if len(request.ProviderReplays) != 0 {
		t.Fatalf("max-token replay reached current request: %s", request.ProviderReplays[0])
	}
	if body := string(request.ProviderRequest); !strings.Contains(body, "short visible truncated response") ||
		!strings.Contains(body, "continue with the current fitting request") ||
		strings.Contains(body, replayMarker) {
		t.Fatalf("current prepared request contains the wrong prior surface: %s", body)
	}

	var priorInput, priorOutput, priorReasoning int
	var priorReplayCleared bool
	var servedModel string
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT context.input_tokens_total,
		       context.output_tokens_total,
		       context.reasoning_output_tokens,
		       output.provider_replay IS NULL,
		       output.served_provider_model_slug
		FROM model_call_contexts context
		JOIN model_outputs output
		  ON output.agent_id = context.agent_id
		 AND output.model_call_context_id = context.id
		WHERE context.project_id = $1
		  AND context.agent_id = $2
		  AND context.input_event_sequence = $3`,
		kernelTestProjectID,
		agentID,
		firstTurn.OpeningEventSequence,
	).Scan(&priorInput, &priorOutput, &priorReasoning, &priorReplayCleared, &servedModel); err != nil {
		t.Fatalf("load prior provider evidence: %v", err)
	}
	if priorInput != 1_450 || priorOutput != 5_000 || priorReasoning != 4_990 ||
		!priorReplayCleared || servedModel != "router/fallback-model" {
		t.Fatalf(
			"prior evidence = input:%d output:%d reasoning:%d replay_cleared:%v served:%q",
			priorInput,
			priorOutput,
			priorReasoning,
			priorReplayCleared,
			servedModel,
		)
	}

	var compactionContexts, checkpoints, finalOutputs int
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM model_call_contexts context
		WHERE context.project_id = $1
		  AND context.agent_id = $2
		  AND context.operation_kind = 'compaction'`, kernelTestProjectID, agentID).Scan(&compactionContexts); err != nil {
		t.Fatalf("count unexpected compaction contexts: %v", err)
	}
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM context_checkpoints checkpoint
		JOIN agents agent ON agent.id = checkpoint.agent_id
		WHERE agent.project_id = $1 AND checkpoint.agent_id = $2`, kernelTestProjectID, agentID).Scan(&checkpoints); err != nil {
		t.Fatalf("count unexpected checkpoints: %v", err)
	}
	if err := fixture.Pool.QueryRow(ctx, `SELECT count(*) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.turn_id = $3 AND block.block_kind = 'text' AND block.text_content = 'current fitting request was sent'`, kernelTestProjectID, agentID, currentTurn.TurnID).
		Scan(&finalOutputs); err != nil {
		t.Fatalf("count current output: %v", err)
	}
	if compactionContexts != 0 || checkpoints != 0 || finalOutputs != 1 {
		t.Fatalf(
			"current request state compactions=%d checkpoints=%d outputs=%d, want 0/0/1",
			compactionContexts,
			checkpoints,
			finalOutputs,
		)
	}
}

func TestAgentExecutorCompactionKeepsRecentRawTail(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	providerName := "openai-keep-recent-prod"
	configuredModelName := "kernel-keep-recent-test"
	secret, err := fixture.ensureProviderCredential(t, ctx, providerName, fixture.Now)
	if err != nil {
		t.Fatalf("ensure provider credential: %v", err)
	}
	providerConfig, err := fixture.Store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              kernelTestOrgID,
		Name:               providerName,
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		APIVariant:         "default",
		BaseURL:            "https://api.openai.com/v1",
		CredentialSecretID: secret.ID,
	})
	if err != nil {
		t.Fatalf("create keep-recent provider config: %v", err)
	}
	configuredModel, err := fixture.Store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                  kernelTestOrgID,
		ModelProviderConfigID:  providerConfig.ID,
		Name:                   configuredModelName,
		ProviderModelSlug:      configuredModelName,
		ContextWindowTokens:    3500,
		MaxOutputTokens:        64,
		DefaultMaxOutputTokens: intPtrForKernelCompactionTest(64),
	})
	if err != nil {
		t.Fatalf("create keep-recent configured model: %v", err)
	}
	if _, err := fixture.Store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID:             kernelTestOrgID,
		ProjectID:         kernelTestProjectID,
		ConfiguredModelID: configuredModel.ID,
	}); err != nil {
		t.Fatalf("grant keep-recent configured model: %v", err)
	}
	sourceYAML := "instruction: Help the user make progress.\nmodel:\n  provider_config: " + providerName + "\n  name: " + configuredModelName + "\n"
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(sourceYAML), agentconfig.CompileOptions{
		ResolveModelSelection: func(providerConfigName string, selectedModelName string) (agentconfig.ResolvedModelSelection, error) {
			return resolvedKernelAgentConfigModel(configuredModel), nil
		},
	})
	if err != nil {
		t.Fatalf("compile keep-recent config: %v", err)
	}
	config, err := fixture.Store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               kernelTestProjectID,
		Definition:              json.RawMessage(compiled.CanonicalJSON),
		Source:                  sourceYAML,
		SourceFormat:            "yaml",
		ConfiguredModelID:       configuredModel.ID,
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiled.Hash,
	})
	if err != nil {
		t.Fatalf("create keep-recent agent config: %v", err)
	}
	profile, err := fixture.Store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID:       kernelTestProjectID,
		Name:            "Kernel Keep Recent",
		CurrentConfigID: config.ID,
		IdempotencyKey:  "kernel-keep-recent-profile",
	})
	if err != nil {
		t.Fatalf("create keep-recent profile: %v", err)
	}
	launch, err := fixture.Store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      kernelTestProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  config.ID,
			LaunchedBy:     kernelTestUserPrincipal(kernelTestUserID),
			IdempotencyKey: "kernel-keep-recent-launch",
		},
	)
	if err != nil {
		t.Fatalf("launch keep-recent agent: %v", err)
	}
	agentID, userID := launch.Agent.ID, kernelTestUserID

	oldNeedle := "OLD_HISTORY_TO_SUMMARIZE " + strings.Repeat("compactable old payload ", 400)
	oldModel := &sequenceKernelModel{
		providerModelSlug: configuredModelName,
		capabilities: model.Capabilities{
			ContextWindowTokens: 10000,
			MaxOutputTokens:     64,
		},
		responses: []model.Response{{
			ID:         "resp_old_history",
			Content:    []model.ResponsePart{{Type: "text", Text: "old history accepted"}},
			StopReason: model.StopReasonEndTurn,
		}},
	}
	oldTurn := fixture.admitContentInputTurn(t, ctx, agentID, userID, oldNeedle, fixture.Now.Add(time.Second))
	oldExecutor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, oldModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(2 * time.Second) },
	}
	if err := oldExecutor.ExecuteModelWork(ctx, oldTurn); err != nil {
		t.Fatalf("execute old history turn: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		oldTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release old history lock: %v", err)
	}

	recentNeedle := "RECENT_RAW_TAIL_MUST_REMAIN_VISIBLE " + strings.Repeat("recent detail ", 80)
	recentModel := &sequenceKernelModel{
		providerModelSlug: configuredModelName,
		capabilities: model.Capabilities{
			ContextWindowTokens: 10000,
			MaxOutputTokens:     64,
		},
		responses: []model.Response{{
			ID:         "resp_recent_tail",
			Content:    []model.ResponsePart{{Type: "text", Text: "recent tail accepted"}},
			StopReason: model.StopReasonEndTurn,
		}},
	}
	recentTurn := fixture.admitContentInputTurn(t, ctx, agentID, userID, recentNeedle, fixture.Now.Add(3*time.Second))
	recentExecutor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, recentModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(4 * time.Second) },
	}
	if err := recentExecutor.ExecuteModelWork(ctx, recentTurn); err != nil {
		t.Fatalf("execute recent tail turn: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		recentTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release recent tail lock: %v", err)
	}

	retryModel := &sequenceKernelModel{
		providerModelSlug: configuredModelName,
		preparedInputTokenEstimator: func(bundle modelcontext.Bundle) int {
			if bundle.ContextCheckpoint != nil || isCompactionRequestBundle(bundle) {
				return 500
			}
			return 4_000
		},
		capabilities: model.Capabilities{
			ContextWindowTokens: 3500,
			MaxOutputTokens:     64,
		},
		responses: []model.Response{
			{
				ID:         "resp_recent_tail_summary",
				Content:    []model.ResponsePart{{Type: "text", Text: "The old compactable payload was summarized."}},
				StopReason: model.StopReasonEndTurn,
			},
			{
				ID:         "resp_after_recent_tail_compaction",
				Content:    []model.ResponsePart{{Type: "text", Text: "continued with recent raw tail"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	retryTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"continue with the recent detail "+strings.Repeat("final pressure ", 100),
		fixture.Now.Add(5*time.Second),
	)
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, retryModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(6 * time.Second) },
	}
	if err := executor.ExecuteModelWork(ctx, retryTurn); err != nil {
		t.Fatalf("execute keep-recent retry turn: %v", err)
	}
	if retryModel.respondedCount() != 1 {
		t.Fatalf("keep-recent retry prepared %d requests on first lease, want compaction summary", retryModel.respondedCount())
	}
	finalTurn := continueTurnOnNewLeaseForKernelTest(
		t,
		ctx,
		fixture,
		retryTurn,
		fixture.Now.Add(7*time.Second),
	)
	if err := executor.ExecuteModelWork(ctx, finalTurn); err != nil {
		t.Fatalf("execute keep-recent post-compaction turn: %v", err)
	}
	if retryModel.respondedCount() != 2 {
		t.Fatalf(
			"keep-recent retry prepared %d requests across leases, want compaction summary and final retry",
			retryModel.respondedCount(),
		)
	}
	summaryRequest := string(retryModel.responded[0].ProviderRequest)
	if !strings.Contains(summaryRequest, "OLD_HISTORY_TO_SUMMARIZE") {
		t.Fatalf("summary request did not include old compactable history: %s", summaryRequest)
	}
	if strings.Contains(summaryRequest, recentNeedle) || strings.Contains(summaryRequest, "recent tail accepted") {
		t.Fatalf("summary request compacted recent raw tail: %s", summaryRequest)
	}
	retryRequest := string(retryModel.responded[1].ProviderRequest)
	if !strings.Contains(retryRequest, "The old compactable payload was summarized.") ||
		!strings.Contains(retryRequest, recentNeedle) ||
		!strings.Contains(retryRequest, "recent tail accepted") {
		t.Fatalf("retry request did not include summary plus complete recent raw turn: %s", retryRequest)
	}
	if strings.Contains(retryRequest, "OLD_HISTORY_TO_SUMMARIZE") {
		t.Fatalf("retry request retained compacted old history raw: %s", retryRequest)
	}

	var summarizedThrough, recentInputSequence int64
	if err := fixture.Pool.QueryRow(ctx, `SELECT checkpoint.summarized_through_event_sequence FROM context_checkpoints checkpoint JOIN agents agent ON agent.id = checkpoint.agent_id WHERE agent.project_id = $1 AND checkpoint.agent_id = $2`, kernelTestProjectID, agentID).
		Scan(&summarizedThrough); err != nil {
		t.Fatalf("load keep-recent checkpoint: %v", err)
	}
	if err := fixture.Pool.QueryRow(ctx, `SELECT event.sequence FROM agent_events event JOIN agents agent ON agent.id = event.agent_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.agent_input_id = $3`, kernelTestProjectID, agentID, recentTurn.InputIDs[0]).
		Scan(&recentInputSequence); err != nil {
		t.Fatalf("load recent input sequence: %v", err)
	}
	if summarizedThrough >= recentInputSequence {
		t.Fatalf(
			"checkpoint summarized through %d, should keep recent input sequence %d raw",
			summarizedThrough,
			recentInputSequence,
		)
	}
}

func TestAgentExecutorCompactsMultiInputTurnAfterLocalBudgetOverflow(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgent(t, ctx, "openai/kernel-test", fixture.Now)

	firstModel := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		responses: []model.Response{{
			ID:         "resp_multi_large_history",
			Content:    []model.ResponsePart{{Type: "text", Text: "large multi-input history accepted"}},
			StopReason: model.StopReasonEndTurn,
		}},
	}
	largeText := "large multi-input history " + strings.Repeat("context payload ", 180)
	firstTurn := fixture.admitContentInputTurn(t, ctx, agentID, userID, largeText, fixture.Now.Add(time.Second))
	firstExecutor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, firstModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(2 * time.Second) },
	}
	if err := firstExecutor.ExecuteModelWork(ctx, firstTurn); err != nil {
		t.Fatalf("execute first turn: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		firstTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release first runtime lock: %v", err)
	}

	retryModel := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		capabilities: model.Capabilities{
			ContextWindowTokens: 1300,
			MaxOutputTokens:     64,
		},
		responses: []model.Response{
			{
				ID:         "resp_multi_budget_summary",
				Content:    []model.ResponsePart{{Type: "text", Text: "The earlier oversized history has been compacted for a multi-input turn."}},
				StopReason: model.StopReasonEndTurn,
			},
			{
				ID:         "resp_after_multi_budget_compaction",
				Content:    []model.ResponsePart{{Type: "text", Text: "continued after multi-input budget compaction"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	retryTurn := fixture.admitSteeringInputsTurn(
		t,
		ctx,
		agentID,
		userID,
		[]string{
			"first steering continuation " + strings.Repeat("retained detail ", 50),
			"second steering continuation " + strings.Repeat("retained detail ", 50),
		},
		fixture.Now.Add(3*time.Second),
	)
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, retryModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(4 * time.Second) },
	}
	if err := executor.ExecuteModelWork(ctx, retryTurn); err != nil {
		t.Fatalf("execute multi-input budget retry turn: %v", err)
	}
	if retryModel.respondedCount() != 1 {
		t.Fatalf("multi-input budget retry prepared %d requests on first lease, want compaction summary", retryModel.respondedCount())
	}
	finalTurn := continueTurnOnNewLeaseForKernelTest(
		t,
		ctx,
		fixture,
		retryTurn,
		fixture.Now.Add(5*time.Second),
	)
	if err := executor.ExecuteModelWork(ctx, finalTurn); err != nil {
		t.Fatalf("execute multi-input post-compaction turn: %v", err)
	}
	if retryModel.respondedCount() != 2 {
		t.Fatalf(
			"multi-input budget retry prepared %d requests across leases, want compaction summary and final retry",
			retryModel.respondedCount(),
		)
	}

	var compactionWatermark int64
	if err := fixture.Pool.QueryRow(ctx, `
			SELECT mcc.input_event_sequence
			FROM context_checkpoints checkpoint
			JOIN model_call_contexts mcc ON mcc.agent_id = checkpoint.agent_id
		  AND mcc.id = checkpoint.producer_model_call_context_id
		JOIN LATERAL (
			  SELECT opening.turn_id
			  FROM agent_events opening
			  WHERE opening.agent_id = mcc.agent_id
		    AND opening.is_opening_event
		    AND opening.sequence <= mcc.input_event_sequence
		  ORDER BY opening.sequence DESC, opening.id DESC
		  LIMIT 1
		) context_turn ON true
			WHERE mcc.project_id = $1
			  AND checkpoint.agent_id = $2
		  AND context_turn.turn_id = $3`, kernelTestProjectID, agentID, retryTurn.TurnID).Scan(&compactionWatermark); err != nil {
		t.Fatalf("load compaction model context watermark: %v", err)
	}
	if compactionWatermark <= 0 {
		t.Fatalf("compaction model context watermark = %d, want positive", compactionWatermark)
	}
	var finalOutputs int
	if err := fixture.Pool.QueryRow(ctx, `SELECT count(*) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.turn_id = $3 AND block.block_kind = 'text' AND block.text_content = 'continued after multi-input budget compaction'`, kernelTestProjectID, agentID, retryTurn.TurnID).
		Scan(&finalOutputs); err != nil {
		t.Fatalf("count final multi-input budget output: %v", err)
	}
	if finalOutputs != 1 {
		t.Fatalf("final multi-input budget output count = %d, want 1", finalOutputs)
	}
}

func TestAgentExecutorCompactionKeepsToolCallResultGroupRaw(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgentWithModelOptions(
		t,
		ctx,
		"openai/kernel-test",
		fixture.Now,
		kernelConfiguredModelOptions{
			ContextWindowTokens: intPtrForKernelCompactionTest(1600),
			MaxOutputTokens:     intPtrForKernelCompactionTest(64),
		},
		"run_command",
	)
	userNeedle := "pre-tool user instruction " + strings.Repeat("compactable user history ", 120)
	commandNeedle := "TOOL_GROUP_MUST_STAY_RAW"
	toolInput := json.RawMessage(`{"command":"printf '` + commandNeedle + `\n' ` + strings.Repeat("&& true ", 80) + `"}`)
	modelClient := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		preparedInputTokenEstimator: func(bundle modelcontext.Bundle) int {
			if bundle.ContextCheckpoint != nil || isCompactionRequestBundle(bundle) {
				return 500
			}
			if len(bundle.ToolResults) > 0 {
				return 2_000
			}
			return 500
		},
		capabilities: model.Capabilities{
			ContextWindowTokens: 1600,
			MaxOutputTokens:     64,
		},
		responses: []model.Response{
			{
				ID:         "resp_tool_call_before_compaction",
				StopReason: model.StopReasonToolUse,
				Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{{
					ID:    "call_split_turn_tool",
					Name:  "run_command",
					Input: toolInput,
				}}),
			},
			{
				ID:         "resp_split_turn_summary",
				Content:    []model.ResponsePart{{Type: "text", Text: "The compacted user instruction asked the agent to continue after tool execution."}},
				StopReason: model.StopReasonEndTurn,
			},
			{
				ID:         "resp_after_split_turn_compaction",
				Content:    []model.ResponsePart{{Type: "text", Text: "continued after preserving the raw tool call and result group"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	turn := fixture.admitContentInputTurn(t, ctx, agentID, userID, userNeedle, fixture.Now.Add(time.Second))
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(2 * time.Second) },
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute split-turn compaction: %v", err)
	}
	if modelClient.respondedCount() != 1 {
		t.Fatalf("prepared %d requests on first lease, want the initial tool call", modelClient.respondedCount())
	}
	scope := executeNextToolWork(t, ctx, fixture, executor, turn)
	select {
	case <-scope.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("split-turn tool work did not finish")
	}
	if err := scope.Err(); err != nil {
		t.Fatalf("execute split-turn tool work: %v", err)
	}
	compactionTurn := executeNextModelWork(t, ctx, fixture, executor, turn)
	if modelClient.respondedCount() != 2 {
		t.Fatalf("prepared %d requests across two leases, want tool call and compaction summary", modelClient.respondedCount())
	}
	finalTurn := continueTurnOnNewLeaseForKernelTest(
		t,
		ctx,
		fixture,
		compactionTurn,
		fixture.Now.Add(4*time.Second),
	)
	if err := executor.ExecuteModelWork(ctx, finalTurn); err != nil {
		t.Fatalf("execute split-turn post-compaction call: %v", err)
	}
	if modelClient.respondedCount() != 3 {
		t.Fatalf("prepared %d requests across leases, want initial tool call, compaction summary, retry", modelClient.respondedCount())
	}
	summaryRequest := string(modelClient.responded[1].ProviderRequest)
	if !strings.Contains(summaryRequest, "compactable user history") {
		t.Fatalf("summary request did not include compactable user history: %s", summaryRequest)
	}
	if strings.Contains(summaryRequest, commandNeedle) {
		t.Fatalf("summary request split tool group into checkpoint source: %s", summaryRequest)
	}
	retryRequest := string(modelClient.responded[2].ProviderRequest)
	if !strings.Contains(retryRequest, "The compacted user instruction") ||
		!strings.Contains(retryRequest, commandNeedle) ||
		!strings.Contains(retryRequest, "no_active_agent_machine_binding") {
		t.Fatalf("retry request did not contain summary plus raw tool group/result: %s", retryRequest)
	}
	var summarizedThrough int64
	if err := fixture.Pool.QueryRow(ctx, `
SELECT summarized_through_event_sequence
	FROM context_checkpoints checkpoint
	JOIN agents agent ON agent.id = checkpoint.agent_id
	WHERE agent.project_id = $1 AND checkpoint.agent_id = $2
`, kernelTestProjectID, agentID).
		Scan(&summarizedThrough); err != nil {
		t.Fatalf("load split-turn checkpoint: %v", err)
	}
	var toolCallEventSequence, toolResultEventSequence int64
	if err := fixture.Pool.QueryRow(ctx, `
SELECT min(CASE WHEN event.event_kind = 'model_output' THEN event.sequence END),
       min(CASE WHEN event.event_kind = 'tool_result' THEN event.sequence END)
	FROM agent_events event
	JOIN agents agent ON agent.id = event.agent_id
	WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.turn_id = $3
`, kernelTestProjectID, agentID, turn.TurnID).Scan(&toolCallEventSequence, &toolResultEventSequence); err != nil {
		t.Fatalf("load tool group event sequences: %v", err)
	}
	if summarizedThrough != toolCallEventSequence-1 {
		t.Fatalf(
			"checkpoint summarized through %d, want closed range before raw tool group %d..%d",
			summarizedThrough,
			toolCallEventSequence,
			toolResultEventSequence,
		)
	}
	var finalOutputs int
	if err := fixture.Pool.QueryRow(ctx, `SELECT count(*) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.turn_id = $3 AND block.block_kind = 'text' AND block.text_content = 'continued after preserving the raw tool call and result group'`, kernelTestProjectID, agentID, turn.TurnID).
		Scan(&finalOutputs); err != nil {
		t.Fatalf("count split-turn final output: %v", err)
	}
	if finalOutputs != 1 {
		t.Fatalf("final output count = %d, want 1", finalOutputs)
	}
}

func TestAgentExecutorStopsWhenOnlyUnansweredOpeningExceedsSerializedBudget(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgent(t, ctx, "openai/kernel-test", fixture.Now)

	seedModel := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		responses: []model.Response{{
			ID: "resp_serialized_candidate_seed",
			Content: []model.ResponsePart{{
				Type: "text",
				Text: "seed output " + strings.Repeat("durable detail ", 50),
			}},
			StopReason: model.StopReasonEndTurn,
		}},
	}
	seedTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"seed input "+strings.Repeat("serialized candidate history ", 50),
		fixture.Now.Add(time.Second),
	)
	seedExecutor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, seedModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(2 * time.Second) },
	}
	if err := seedExecutor.ExecuteModelWork(ctx, seedTurn); err != nil {
		t.Fatalf("execute serialized-candidate seed: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		seedTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release serialized-candidate seed runtime: %v", err)
	}

	turn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"current input "+strings.Repeat("must remain semantically represented ", 50),
		fixture.Now.Add(3*time.Second),
	)
	watermark, err := fixture.Store.Execution().MaxEventSequence(ctx, kernelTestProjectID, agentID)
	if err != nil {
		t.Fatalf("load serialized-candidate watermark: %v", err)
	}
	if watermark != 4 || turn.OpeningEventSequence != 4 {
		t.Fatalf("serialized-candidate watermark/opening = %d/%d, want 4", watermark, turn.OpeningEventSequence)
	}

	compactionModel := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		capabilities: model.Capabilities{
			ContextWindowTokens:    10_000,
			MaxOutputTokens:        1_024,
			DefaultMaxOutputTokens: 1_024,
		},
		preparedInputTokenEstimator: func(bundle modelcontext.Bundle) int {
			if isCompactionRequestBundle(bundle) {
				return 500
			}
			if bundle.ContextCheckpoint != nil &&
				(strings.Contains(bundle.ContextCheckpoint.Summary, "[Earlier conversation compacted.]") ||
					strings.Contains(bundle.ContextCheckpoint.Summary, "[Additional closed history compacted.]")) {
				return 500
			}
			return 9_000
		},
		responses: []model.Response{
			completeProgressiveSummaryResponse("Earlier seed history remains relevant."),
		},
	}
	runNow := fixture.Now.Add(4 * time.Second)
	executor := AgentExecutor{
		Store: fixture.Store,
		ContextBuilder: modelcontext.Builder{
			Store: modelcontext.NewStore(fixture.Store.Execution(), fixture.Store.Artifacts(), fixture.Store.Integrations()),
		},
		ModelResolver: liveTestModelResolver(fixture.Store, compactionModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return runNow },
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute oversized unanswered opening: %v", err)
	}
	if compactionModel.respondedCount() != 1 {
		t.Fatalf("serialized-candidate provider calls = %d, want 1", compactionModel.respondedCount())
	}
	assertDurableModelErrorForKernelTest(
		t,
		ctx,
		fixture,
		agentID,
		turn.TurnID,
		string(model.ErrorKindContextWindow),
		"compaction_source_irreducible",
	)
	var checkpoints int
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT count(*)
			FROM context_checkpoints checkpoint
			JOIN agents agent ON agent.id = checkpoint.agent_id
			WHERE agent.project_id = $1 AND checkpoint.agent_id = $2`, kernelTestProjectID, agentID).Scan(&checkpoints); err != nil {
		t.Fatalf("count serialized-candidate checkpoints: %v", err)
	}
	if checkpoints != 0 {
		t.Fatalf("serialized-candidate checkpoint count = %d, want 0", checkpoints)
	}
}
