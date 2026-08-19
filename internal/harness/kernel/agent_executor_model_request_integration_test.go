//go:build integration

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestAgentExecutorAppliesManagedWorkAdmissionAtModelClaim(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	fixture.provisionClusterModel(t, ctx, "managed-prod", "managed-model")
	fixture.setManagedWorkAdmission(t, ctx, false)

	agentID, userID := fixture.createNamedAgentWithModelOptions(
		t,
		ctx,
		"Managed Admission Denied",
		"managed/managed-model",
		now,
		kernelConfiguredModelOptions{},
	)
	turn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"exercise managed model admission",
		now.Add(time.Millisecond),
	)
	modelClient := &sequenceKernelModel{
		providerModelSlug: "managed-model",
		responses: []model.Response{{
			ID:         "resp-managed-model",
			Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "done"}},
			StopReason: model.StopReasonEndTurn,
		}},
	}
	resolver := &selectionRecordingResolver{client: modelClient}
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: resolver,
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return now.Add(2 * time.Millisecond) },
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute denied managed model work: %v", err)
	}
	if len(resolver.selections) != 0 || modelClient.preparedCount() != 0 || modelClient.respondedCount() != 0 {
		t.Fatalf(
			"denied model resolver/prepared/responded = %d/%d/%d, want 0/0/0",
			len(resolver.selections),
			modelClient.preparedCount(),
			modelClient.respondedCount(),
		)
	}
	assertDurableModelErrorForKernelTest(
		t,
		ctx,
		fixture,
		agentID,
		turn.TurnID,
		string(modelprotocol.ErrorKindRuntime),
		storeerr.ManagedWorkAdmissionDeniedCode,
	)
	var state, recoveryKind, errorKind, errorCode, apiFormat, requestID, responseID string
	var retryAbsent bool
	if err := fixture.Pool.QueryRow(ctx, `
SELECT context.state,
       coalesce(context.recovery_kind, ''),
       context.error_kind,
       context.error_code,
       context.api_format,
       context.provider_request_id,
       context.provider_response_id,
       context.retry_at IS NULL
FROM model_call_contexts context
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.input_event_sequence = $3
`, kernelTestProjectID, agentID, turn.OpeningEventSequence).Scan(
		&state,
		&recoveryKind,
		&errorKind,
		&errorCode,
		&apiFormat,
		&requestID,
		&responseID,
		&retryAbsent,
	); err != nil {
		t.Fatalf("load denied managed model context: %v", err)
	}
	if state != string(executionstore.ModelCallContextFailed) || recoveryKind != "" ||
		errorKind != string(modelprotocol.ErrorKindRuntime) ||
		errorCode != storeerr.ManagedWorkAdmissionDeniedCode || apiFormat != "" ||
		requestID != "" || responseID != "" || !retryAbsent {
		t.Fatalf(
			"denied context = %q/%q/%q/%q api=%q request=%q response=%q retry_absent=%v",
			state,
			recoveryKind,
			errorKind,
			errorCode,
			apiFormat,
			requestID,
			responseID,
			retryAbsent,
		)
	}
	var contexts int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM model_call_contexts
WHERE project_id = $1 AND agent_id = $2 AND input_event_sequence = $3
	`, kernelTestProjectID, agentID, turn.OpeningEventSequence).Scan(&contexts); err != nil {
		t.Fatalf("count denied managed model contexts: %v", err)
	}
	if contexts != 1 || len(resolver.selections) != 0 {
		t.Fatalf("denied managed model contexts/resolver calls = %d/%d, want 1/0", contexts, len(resolver.selections))
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		turn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release denied model runtime lock: %v", err)
	}
	claim, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		kernelTestClaimInput(now.Add(time.Minute)),
	)
	if err != nil && !errors.Is(err, storeerr.ErrNoClaimableAgentWakeup) {
		t.Fatalf("claim after denied managed model work: %v", err)
	}
	if found {
		t.Fatalf("claim after denied managed model work = %+v, want no work", claim)
	}
	var wakeups int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)::integer
FROM agent_wakeups wake
JOIN agents agent ON agent.id = wake.agent_id
WHERE agent.project_id = $1 AND wake.agent_id = $2
`, kernelTestProjectID, agentID).Scan(&wakeups); err != nil {
		t.Fatalf("count wakeups after denied managed model work: %v", err)
	}
	if wakeups != 0 {
		t.Fatalf("wakeups after denied managed model work = %d, want 0", wakeups)
	}

	tenantAgentID, tenantUserID := fixture.createNamedAgentWithModelOptions(
		t,
		ctx,
		"Tenant Admission Allowed",
		"openai/tenant-admission-model",
		now.Add(10*time.Millisecond),
		kernelConfiguredModelOptions{},
	)
	tenantTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		tenantAgentID,
		tenantUserID,
		"exercise tenant model admission",
		now.Add(11*time.Millisecond),
	)
	tenantModel := &sequenceKernelModel{
		providerModelSlug: "tenant-admission-model",
		responses: []model.Response{{
			ID:         "resp-tenant-admission-model",
			Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "done"}},
			StopReason: model.StopReasonEndTurn,
		}},
	}
	tenantExecutor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, tenantModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return now.Add(12 * time.Millisecond) },
	}
	if err := tenantExecutor.ExecuteModelWork(ctx, tenantTurn); err != nil {
		t.Fatalf("execute tenant model while managed admission is closed: %v", err)
	}
	if tenantModel.respondedCount() != 1 {
		t.Fatalf("tenant model responses = %d, want 1", tenantModel.respondedCount())
	}

	fixture.setManagedWorkAdmission(t, ctx, true)
	reopenedAgentID, reopenedUserID := fixture.createNamedAgentWithModelOptions(
		t,
		ctx,
		"Managed Admission Reopened",
		"managed/managed-model",
		now.Add(20*time.Millisecond),
		kernelConfiguredModelOptions{},
	)
	reopenedTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		reopenedAgentID,
		reopenedUserID,
		"exercise reopened managed model admission",
		now.Add(21*time.Millisecond),
	)
	if err := executor.ExecuteModelWork(ctx, reopenedTurn); err != nil {
		t.Fatalf("execute reopened managed model work: %v", err)
	}
	if len(resolver.selections) != 1 || modelClient.respondedCount() != 1 {
		t.Fatalf("reopened model resolver/responses = %d/%d, want 1/1", len(resolver.selections), modelClient.respondedCount())
	}
}

func TestAgentExecutorDurablyRetriesUnclassifiedModelResolverFailure(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/missing-kernel-model", now)
	turn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"exercise resolver infrastructure failure",
		now.Add(time.Millisecond),
	)
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: staticTestModelResolver{},
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return now.Add(2 * time.Millisecond) },
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute turn with unclassified resolver failure: %v", err)
	}
	var retryingContexts, providerErrorOutputs int
	if err := fixture.Pool.QueryRow(ctx, `
			SELECT count(*) FILTER (
			         WHERE context.state = 'failed'
			           AND context.recovery_kind = 'retry'
			           AND context.error_kind = 'transient'
			           AND context.error_code = 'resolve_model_failed'
			           AND context.retry_at IS NOT NULL
			       ),
			       count(DISTINCT output.id)
				FROM model_call_contexts context
				LEFT JOIN model_outputs output
				  ON output.agent_id = context.agent_id
			 AND output.model_call_context_id = context.id
			WHERE context.project_id = $1
			  AND context.agent_id = $2
		`, kernelTestProjectID, agentID).Scan(&retryingContexts, &providerErrorOutputs); err != nil {
		t.Fatalf("inspect model context after infrastructure failure: %v", err)
	}
	if retryingContexts != 1 || providerErrorOutputs != 0 {
		t.Fatalf(
			"retrying contexts/provider outputs = %d/%d, want 1/0",
			retryingContexts,
			providerErrorOutputs,
		)
	}
}

func TestAgentExecutorDurablyRetriesUnclassifiedModelPrepareFailure(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/prepare-infrastructure-failure", now)
	turn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"exercise request preparation infrastructure failure",
		now.Add(time.Millisecond),
	)
	wantErr := errors.New("artifact store unavailable")
	executor := AgentExecutor{
		Store: fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, &sequenceKernelModel{
			providerModelSlug: "prepare-infrastructure-failure",
			prepareErr:        wantErr,
		}),
		ToolExecutor: tools.Executor{Store: fixture.Store},
		Now:          func() time.Time { return now.Add(2 * time.Millisecond) },
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute turn prepare failure: %v", err)
	}
	var contextState executionstore.ModelCallState
	var recoveryKind executionstore.ModelCallRecoveryKind
	var errorKind, errorCode string
	var retryAt time.Time
	var outputCount int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT context.state, context.recovery_kind, context.error_kind, context.error_code,
	       context.retry_at,
		       (SELECT count(*) FROM model_outputs output
		        WHERE output.agent_id = context.agent_id
	          AND output.model_call_context_id = context.id)
FROM model_call_contexts context
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.input_event_sequence = $3
ORDER BY context.attempt_number DESC
LIMIT 1
`, kernelTestProjectID, agentID, turn.OpeningEventSequence).Scan(
		&contextState,
		&recoveryKind,
		&errorKind,
		&errorCode,
		&retryAt,
		&outputCount,
	); err != nil {
		t.Fatalf("inspect model context after prepare infrastructure failure: %v", err)
	}
	if contextState != executionstore.ModelCallContextFailed ||
		recoveryKind != executionstore.ModelCallRecoveryRetry ||
		errorKind != string(model.ErrorKindTransient) ||
		errorCode != "prepare_model_request_failed" || retryAt.IsZero() || outputCount != 0 {
		t.Fatalf(
			"prepare infrastructure state=%q recovery=%q error=%s/%s retry=%v outputs=%d",
			contextState,
			recoveryKind,
			errorKind,
			errorCode,
			retryAt,
			outputCount,
		)
	}
}

func TestAgentExecutorRecordsOutputLimitConflictBeforeProviderPreparation(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/output-limit-conflict", now)
	turn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"exercise deterministic output policy validation",
		now.Add(time.Millisecond),
	)
	baseClient := &sequenceKernelModel{providerModelSlug: "output-limit-conflict"}
	modelClient := &outputLimitKernelModel{sequenceKernelModel: baseClient}
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return now.Add(2 * time.Millisecond) },
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute turn with output-limit conflict: %v", err)
	}

	var contextState executionstore.ModelCallState
	var recoveryKind executionstore.ModelCallRecoveryKind
	var errorKind, errorCode, outputStopReason, blockKind, blockText string
	var outputCount, blockCount int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT context.state, COALESCE(context.recovery_kind::text, ''), context.error_kind, context.error_code,
       output.stop_reason, count(DISTINCT output.id)::integer,
       count(block.id)::integer, min(block.block_kind), min(block.text_content)
FROM model_call_contexts context
JOIN model_outputs output ON output.agent_id = context.agent_id
  AND output.model_call_context_id = context.id
JOIN content_blocks block ON block.agent_id = output.agent_id
  AND block.owner_model_output_id = output.id
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.input_event_sequence = $3
GROUP BY context.id, output.stop_reason
ORDER BY context.attempt_number DESC
LIMIT 1
`, kernelTestProjectID, agentID, turn.OpeningEventSequence).Scan(
		&contextState,
		&recoveryKind,
		&errorKind,
		&errorCode,
		&outputStopReason,
		&outputCount,
		&blockCount,
		&blockKind,
		&blockText,
	); err != nil {
		t.Fatalf("inspect output-limit conflict: %v", err)
	}
	if contextState != executionstore.ModelCallContextFailed || recoveryKind != "" ||
		errorKind != string(model.ErrorKindInvalidRequest) ||
		errorCode != model.OutputTokenLimitIncompatibleCode || outputStopReason != "error" ||
		outputCount != 1 || blockCount != 1 || blockKind != "error" ||
		!strings.Contains(blockText, model.ErrOutputTokenLimitIncompatible.Error()) ||
		!strings.Contains(blockText, "must be at least") ||
		modelClient.validationCalls != 1 || baseClient.preparedCount() != 0 ||
		baseClient.respondedCount() != 0 {
		t.Fatalf(
			"output-limit state=%q recovery=%q error=%s/%s output=%s/%d block=%s/%q/%d validation=%d prepare=%d respond=%d",
			contextState,
			recoveryKind,
			errorKind,
			errorCode,
			outputStopReason,
			outputCount,
			blockKind,
			blockText,
			blockCount,
			modelClient.validationCalls,
			baseClient.preparedCount(),
			baseClient.respondedCount(),
		)
	}
}

type outputLimitKernelModel struct {
	*sequenceKernelModel
	validationCalls int
}

func (m *outputLimitKernelModel) OutputTokenLimits(
	policy model.RequestPolicy,
) (model.OutputTokenLimits, error) {
	m.validationCalls++
	return model.OutputTokenLimits{Minimum: policy.MaxOutputTokens + 1}, nil
}

func TestAgentExecutorAppliesCompiledModelOverridesToRequestPolicy(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	sourceYAML := `
instruction: Apply saved model request options.
model:
  provider_config: openai-prod
  name: request-options-model
  context_window_tokens: 64000
  default_max_output_tokens: 1234
  cache_retention: short
`
	profile := fixture.createConfigAndProfileBookmark(
		t,
		ctx,
		"Kernel Model Options",
		"kernel-model-options-agent",
		sourceYAML,
		now,
	)
	launch, err := fixture.Store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      kernelTestProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     kernelTestUserPrincipal(kernelTestUserID),
			IdempotencyKey: "kernel-model-options-agent",
		},
	)
	if err != nil {
		t.Fatalf("launch agent: %v", err)
	}
	turn := fixture.admitContentInputTurn(t, ctx, launch.Agent.ID, kernelTestUserID, "hello", now.Add(2*time.Millisecond))
	modelClient := &sequenceKernelModel{
		providerModelSlug: "request-options-model",
		responses:         []model.Response{{ID: "resp-options", Content: []model.ResponsePart{{Type: "text", Text: "done"}}, StopReason: model.StopReasonEndTurn}},
	}
	resolver := &selectionRecordingResolver{
		client: modelClient,
	}
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: resolver,
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return now.Add(3 * time.Millisecond) },
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute turn: %v", err)
	}
	if len(resolver.selections) != 1 {
		t.Fatalf("resolver selections = %d, want 1", len(resolver.selections))
	}
	selection := resolver.selections[0]
	if selection.Overrides.DefaultMaxOutputTokens == nil || *selection.Overrides.DefaultMaxOutputTokens != 1234 ||
		selection.Overrides.CacheRetention != string(model.CacheRetentionShort) {
		t.Fatalf("selection overrides = %+v", selection.Overrides)
	}
	if selection.Overrides.ContextWindowTokens == nil || *selection.Overrides.ContextWindowTokens != 64000 {
		t.Fatalf("selection token overrides = %+v", selection.Overrides)
	}
	if modelClient.preparedCount() != 1 {
		t.Fatalf("prepared requests = %d, want 1", modelClient.preparedCount())
	}
	policy := modelClient.prepared[0].Policy
	if policy.MaxOutputTokens != 1234 || policy.CacheRetention != model.CacheRetentionShort {
		t.Fatalf("prepared policy = %+v", policy)
	}
}

func TestAgentExecutorPersistsMaxTokensAsSuccessfulModelOutput(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/max-tokens-output", now)
	turn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"return a partial response",
		now.Add(time.Millisecond),
	)
	modelClient := &sequenceKernelModel{
		providerModelSlug: "max-tokens-output",
		responses: []model.Response{{
			ID:         "resp-max-tokens-output",
			Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "partial response"}},
			StopReason: model.StopReasonMaxTokens,
		}},
	}
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return now.Add(2 * time.Millisecond) },
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute max-tokens response: %v", err)
	}

	var state executionstore.ModelCallState
	var stopReason model.StopReason
	var text string
	if err := fixture.Pool.QueryRow(ctx, `
SELECT context.state, output.stop_reason, block.text_content
	FROM model_call_contexts context
	JOIN model_outputs output
	  ON output.agent_id = context.agent_id
 AND output.model_call_context_id = context.id
	JOIN content_blocks block
	  ON block.agent_id = output.agent_id
 AND block.owner_model_output_id = output.id
 AND block.ordinal = 0
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.input_event_sequence = $3
`, kernelTestProjectID, agentID, turn.OpeningEventSequence).Scan(&state, &stopReason, &text); err != nil {
		t.Fatalf("load max-tokens model output: %v", err)
	}
	if state != executionstore.ModelCallContextSucceeded ||
		stopReason != model.StopReasonMaxTokens ||
		text != "partial response" {
		t.Fatalf("max-tokens output = %q/%q/%q", state, stopReason, text)
	}
}

func TestAgentExecutorCarriesDurableProviderReplayIntoNextTurn(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/durable-provider-replay", now)
	config := fixture.currentAgentConfig(t, ctx, agentID)
	providerConfigID := currentConfiguredModelForKernelConfig(t, ctx, fixture.Store, config).
		ModelProviderConfigID.String()
	const replayMarker = "encrypted_durable_replay_marker"
	replay := json.RawMessage(
		`[{"type":"reasoning","id":"rs_durable","encrypted_content":"` + replayMarker + `"},` +
			`{"type":"message","id":"msg_durable","role":"assistant","content":` +
			`[{"type":"output_text","text":"first response"}]}]`,
	)
	modelClient := &sequenceKernelModel{
		providerModelSlug: "durable-provider-replay",
		responses: []model.Response{
			{
				ID:             "resp-durable-provider-replay-first",
				Content:        []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "first response"}},
				StopReason:     model.StopReasonEndTurn,
				ProviderReplay: replay,
			},
			{
				ID:         "resp-durable-provider-replay-second",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "second response"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return now.Add(2 * time.Millisecond) },
	}

	firstTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"persist provider replay",
		now.Add(time.Millisecond),
	)
	if err := executor.ExecuteModelWork(ctx, firstTurn); err != nil {
		t.Fatalf("execute replay-producing turn: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		firstTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release replay-producing turn: %v", err)
	}

	bundle, err := (modelcontext.Builder{
		Store: modelcontext.NewStore(fixture.Store.Execution(), fixture.Store.Artifacts(), fixture.Store.Integrations()),
	}).Build(
		ctx,
		modelcontext.BuildInput{
			ProjectID:       kernelTestProjectID,
			AgentID:         agentID,
			TurnID:          firstTurn.TurnID,
			OpeningInputIDs: firstTurn.InputIDs,
			Now:             now.Add(9 * time.Millisecond),
		},
	)
	if err != nil {
		t.Fatalf("rebuild canonical history with provider replay: %v", err)
	}
	history, err := modelcontext.CanonicalHistory(bundle)
	if err != nil {
		t.Fatalf("assemble canonical history with provider replay: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("canonical history entries = %d, want user and assistant", len(history))
	}
	assistant := history[1]
	if assistant.Message.Role != modelprotocol.RoleAssistant ||
		len(assistant.AssistantContent) != 1 {
		t.Fatalf("canonical assistant history = %+v", assistant)
	}
	textBlock, ok := assistant.AssistantContent[0].(modelcontext.AssistantBlockEntry)
	if !ok || !strings.Contains(string(textBlock.Content), "first response") {
		t.Fatalf("canonical assistant content = %#v, want first response text", assistant.AssistantContent)
	}
	if !strings.Contains(string(assistant.Message.ProviderReplay), replayMarker) {
		t.Fatalf("durable provider replay = %s, want marker %q", assistant.Message.ProviderReplay, replayMarker)
	}
	replaySource := assistant.Message.ProviderReplaySource
	if replaySource.ModelProviderConfigID != providerConfigID ||
		replaySource.RequestedProviderModelSlug != "durable-provider-replay" ||
		replaySource.APIFormat != modelprotocol.APIFormatOpenAIResponses ||
		replaySource.APIVariant != modelprotocol.APIVariantDefault {
		t.Fatalf("provider replay source = %+v", replaySource)
	}

	secondTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"rebuild provider replay",
		now.Add(10*time.Millisecond),
	)
	if err := executor.ExecuteModelWork(ctx, secondTurn); err != nil {
		t.Fatalf("execute replay-consuming turn: %v", err)
	}
	if modelClient.preparedCount() != 2 {
		t.Fatalf("prepared requests = %d, want one request per turn", modelClient.preparedCount())
	}
	if len(modelClient.prepared[0].ProviderReplays) != 0 {
		t.Fatalf("first request provider replay = %s, want none", modelClient.prepared[0].ProviderReplays)
	}
	secondReplays := modelClient.prepared[1].ProviderReplays
	if len(secondReplays) != 1 || !strings.Contains(string(secondReplays[0]), replayMarker) {
		t.Fatalf(
			"second request provider replay = %s, want durable marker %q",
			secondReplays,
			replayMarker,
		)
	}
}

func TestAgentExecutorStopsSerializedProviderRequestOverflowWhenOpeningIsIrreducible(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/serialized-overflow-model", now)
	turn := fixture.admitContentInputTurn(t, ctx, agentID, userID, "hello", now.Add(time.Millisecond))
	modelClient := &sequenceKernelModel{
		providerModelSlug:          "serialized-overflow-model",
		preparedInputTokenEstimate: 200_000,
		responses: []model.Response{{
			ID:         "must-not-be-sent",
			Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "unexpected"}},
			StopReason: model.StopReasonEndTurn,
		}},
	}
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return now.Add(2 * time.Millisecond) },
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute turn: %v", err)
	}
	if len(modelClient.respondHadSink) != 0 {
		t.Fatalf("provider respond calls = %d, want none", len(modelClient.respondHadSink))
	}
	var state executionstore.ModelCallState
	var recoveryKind executionstore.ModelCallRecoveryKind
	var errorKind, errorCode string
	var errorDetails json.RawMessage
	if err := fixture.Pool.QueryRow(ctx, `
SELECT context.state, coalesce(context.recovery_kind, ''), context.error_kind, context.error_code,
       context.error_details
FROM model_call_contexts context
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.input_event_sequence = $3
`, kernelTestProjectID, agentID, turn.OpeningEventSequence).Scan(
		&state,
		&recoveryKind,
		&errorKind,
		&errorCode,
		&errorDetails,
	); err != nil {
		t.Fatalf("load serialized overflow attempt: %v", err)
	}
	if state != executionstore.ModelCallContextFailed || recoveryKind != "" ||
		errorKind != string(model.ErrorKindContextWindow) ||
		errorCode != "context_cannot_be_compacted" {
		t.Fatalf(
			"serialized overflow attempt = %q/%q/%q/%q",
			state,
			recoveryKind,
			errorKind,
			errorCode,
		)
	}
	var details struct {
		CompactionTrigger struct {
			Kind    string          `json:"kind"`
			Code    string          `json:"code"`
			Message string          `json:"message"`
			Details json.RawMessage `json:"details"`
		} `json:"compaction_trigger"`
	}
	if err := json.Unmarshal(errorDetails, &details); err != nil {
		t.Fatalf("decode serialized overflow details: %v", err)
	}
	trigger := details.CompactionTrigger
	var triggerDetails struct {
		Source           string                      `json:"source"`
		RequestAdmission model.InputBudgetAssessment `json:"request_admission"`
	}
	if err := json.Unmarshal(trigger.Details, &triggerDetails); err != nil {
		t.Fatalf("decode serialized overflow trigger details: %v", err)
	}
	if trigger.Kind != string(model.ErrorKindContextWindow) ||
		trigger.Code != "configured_input_budget_exceeded" ||
		!strings.Contains(trigger.Message, "exceeding the configured budget") ||
		triggerDetails.Source != "openai-responses" ||
		triggerDetails.RequestAdmission.EstimatedInputTokens <=
			triggerDetails.RequestAdmission.UsableInputTokens {
		t.Fatalf(
			"serialized overflow compaction trigger = %+v source=%q",
			trigger,
			triggerDetails.Source,
		)
	}
	var compactionDependencies int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM model_call_contexts context
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.input_event_sequence = $3
  AND context.operation_kind = 'compaction'
`, kernelTestProjectID, agentID, turn.OpeningEventSequence).Scan(&compactionDependencies); err != nil {
		t.Fatalf("count serialized overflow compaction dependencies: %v", err)
	}
	if compactionDependencies != 0 {
		t.Fatalf("irreducible serialized overflow created %d compaction dependencies", compactionDependencies)
	}
}

func TestAgentExecutorRetainsSafeEvidenceFromSemanticallyMalformedResponse(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/malformed-semantic-model", now)
	turn := fixture.admitContentInputTurn(t, ctx, agentID, userID, "hello", now.Add(time.Millisecond))
	modelClient := &sequenceKernelModel{
		providerModelSlug: "malformed-semantic-model",
		responses: []model.Response{{
			ID:                      "resp-malformed-semantic",
			ServedProviderModelSlug: "served-malformed-semantic",
			Content: []model.ResponsePart{{
				Type:           model.ResponsePartTypeToolCall,
				ProviderCallID: "call_1",
				ToolInput:      json.RawMessage(`{}`),
			}},
			StopReason: model.StopReasonToolUse,
			Usage:      model.Usage{InputTokens: 17, OutputTokens: 5},
		}},
	}
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return now.Add(2 * time.Millisecond) },
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute semantically malformed response: %v", err)
	}

	var state executionstore.ModelCallState
	var recoveryKind executionstore.ModelCallRecoveryKind
	var errorCode, responseID string
	var errorDetails json.RawMessage
	var inputTokens, outputTokens int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT context.state,
	       context.recovery_kind,
	       context.error_code,
	       context.provider_response_id,
	       context.error_details,
	       coalesce(context.input_tokens_total, 0),
	       coalesce(context.output_tokens_total, 0)
FROM model_call_contexts context
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.input_event_sequence = $3
`, kernelTestProjectID, agentID, turn.OpeningEventSequence).Scan(
		&state,
		&recoveryKind,
		&errorCode,
		&responseID,
		&errorDetails,
		&inputTokens,
		&outputTokens,
	); err != nil {
		t.Fatalf("load semantically malformed response attempt: %v", err)
	}
	if state != executionstore.ModelCallContextFailed || recoveryKind != executionstore.ModelCallRecoveryRetry ||
		errorCode != "malformed_success_response" || responseID != "resp-malformed-semantic" ||
		!kernelModelCallOutcomeAmbiguous(t, errorDetails) {
		t.Fatalf(
			"semantically malformed response attempt = %q/%q/%q response=%q",
			state,
			recoveryKind,
			errorCode,
			responseID,
		)
	}
	if inputTokens != 17 || outputTokens != 5 {
		t.Fatalf("malformed response usage = input %d output %d", inputTokens, outputTokens)
	}
}

func TestAgentExecutorRetriesRefusalWithToolCallsAsMalformedSuccess(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/contradictory-refusal-model", now)
	turn := fixture.admitContentInputTurn(t, ctx, agentID, userID, "hello", now.Add(time.Millisecond))
	modelClient := &sequenceKernelModel{
		providerModelSlug: "contradictory-refusal-model",
		responses: []model.Response{{
			ID: "resp-contradictory-refusal",
			Content: []model.ResponsePart{{
				Type:           model.ResponsePartTypeToolCall,
				ProviderCallID: "call-contradictory-refusal",
				ToolName:       "run_command",
				ToolInput:      json.RawMessage(`{"command":"true"}`),
			}},
			StopReason: model.StopReasonRefusal,
		}},
	}
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return now.Add(2 * time.Millisecond) },
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute contradictory refusal response: %v", err)
	}

	var state executionstore.ModelCallState
	var recoveryKind executionstore.ModelCallRecoveryKind
	var errorKind, errorCode string
	if err := fixture.Pool.QueryRow(ctx, `
SELECT context.state, context.recovery_kind, context.error_kind, context.error_code
FROM model_call_contexts context
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.input_event_sequence = $3
`, kernelTestProjectID, agentID, turn.OpeningEventSequence).Scan(
		&state,
		&recoveryKind,
		&errorKind,
		&errorCode,
	); err != nil {
		t.Fatalf("load contradictory refusal attempt: %v", err)
	}
	if state != executionstore.ModelCallContextFailed || recoveryKind != executionstore.ModelCallRecoveryRetry ||
		errorKind != string(model.ErrorKindUnknown) || errorCode != "contradictory_stop_reason" {
		t.Fatalf(
			"contradictory refusal attempt = %q/%q/%q/%q",
			state,
			recoveryKind,
			errorKind,
			errorCode,
		)
	}
	var toolCalls, modelOutputs int
	if err := fixture.Pool.QueryRow(ctx, `
	SELECT (SELECT count(*) FROM tool_calls call JOIN agents agent ON agent.id = call.agent_id WHERE agent.project_id = $1 AND call.agent_id = $2),
	       (SELECT count(*) FROM model_outputs output JOIN agents agent ON agent.id = output.agent_id WHERE agent.project_id = $1 AND output.agent_id = $2)
`, kernelTestProjectID, agentID).Scan(&toolCalls, &modelOutputs); err != nil {
		t.Fatalf("count durable output from contradictory refusal: %v", err)
	}
	if toolCalls != 0 || modelOutputs != 0 {
		t.Fatalf("contradictory refusal persisted tool calls/outputs = %d/%d, want 0/0", toolCalls, modelOutputs)
	}
}

func TestAgentExecutorRejectsOversizedProviderIdentityBeforePersistence(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/oversized-provider-identity-model", now)
	turn := fixture.admitContentInputTurn(t, ctx, agentID, userID, "hello", now.Add(time.Millisecond))
	modelClient := &sequenceKernelModel{
		providerModelSlug: "oversized-provider-identity-model",
		responses: []model.Response{{
			ID: "resp-oversized-provider-identity",
			Content: []model.ResponsePart{{
				Type:           model.ResponsePartTypeToolCall,
				ProviderCallID: strings.Repeat("x", model.MaxProviderIdentityBytes+1),
				ToolName:       "run_command",
				ToolInput:      json.RawMessage(`{"command":"true"}`),
			}},
			StopReason: model.StopReasonToolUse,
		}},
	}
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return now.Add(2 * time.Millisecond) },
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute oversized provider identity response: %v", err)
	}

	var state executionstore.ModelCallState
	var recoveryKind executionstore.ModelCallRecoveryKind
	var errorCode string
	if err := fixture.Pool.QueryRow(ctx, `
SELECT context.state, context.recovery_kind, context.error_code
FROM model_call_contexts context
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.input_event_sequence = $3
`, kernelTestProjectID, agentID, turn.OpeningEventSequence).Scan(
		&state,
		&recoveryKind,
		&errorCode,
	); err != nil {
		t.Fatalf("load oversized provider identity attempt: %v", err)
	}
	if state != executionstore.ModelCallContextFailed || recoveryKind != executionstore.ModelCallRecoveryRetry ||
		errorCode != "malformed_success_response" {
		t.Fatalf("oversized provider identity attempt = %q/%q/%q", state, recoveryKind, errorCode)
	}
	var toolCalls int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM tool_calls call
JOIN agents agent ON agent.id = call.agent_id
WHERE agent.project_id = $1
  AND call.agent_id = $2
`, kernelTestProjectID, agentID).Scan(&toolCalls); err != nil {
		t.Fatalf("count tool calls after oversized provider identity: %v", err)
	}
	if toolCalls != 0 {
		t.Fatalf("oversized provider identity persisted %d tool calls", toolCalls)
	}
}

func TestAgentExecutorRetriesToolUseWithoutToolCallsAsMalformedSuccess(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/malformed-tool-use-model", now)
	turn := fixture.admitContentInputTurn(t, ctx, agentID, userID, "hello", now.Add(time.Millisecond))
	modelClient := &sequenceKernelModel{
		providerModelSlug: "malformed-tool-use-model",
		responses: []model.Response{{
			ID:         "resp-malformed-tool-use",
			StopReason: model.StopReasonToolUse,
		}},
	}
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return now.Add(2 * time.Millisecond) },
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute malformed tool-use response: %v", err)
	}

	var state executionstore.ModelCallState
	var recoveryKind executionstore.ModelCallRecoveryKind
	var errorKind, errorCode string
	if err := fixture.Pool.QueryRow(ctx, `
SELECT context.state, context.recovery_kind, context.error_kind, context.error_code
FROM model_call_contexts context
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.input_event_sequence = $3
`, kernelTestProjectID, agentID, turn.OpeningEventSequence).Scan(
		&state,
		&recoveryKind,
		&errorKind,
		&errorCode,
	); err != nil {
		t.Fatalf("load malformed tool-use attempt: %v", err)
	}
	if state != executionstore.ModelCallContextFailed || recoveryKind != executionstore.ModelCallRecoveryRetry ||
		errorKind != string(model.ErrorKindUnknown) || errorCode != string(model.StopReasonToolUse) {
		t.Fatalf(
			"malformed tool-use attempt = %q/%q/%q/%q",
			state,
			recoveryKind,
			errorKind,
			errorCode,
		)
	}
}

func TestAgentExecutorRoutesSerializedProviderRequestOverflowThroughCompactionBeforeSend(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/serialized-overflow-history-model", now)
	firstTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"completed historical request",
		now.Add(time.Millisecond),
	)
	modelClient := &sequenceKernelModel{
		providerModelSlug:          "serialized-overflow-history-model",
		preparedInputTokenEstimate: 500,
		preparedInputTokenEstimates: []int{
			100,
			200_000,
		},
		responses: []model.Response{
			{
				ID:         "resp-historical-turn",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "historical work completed"}},
				StopReason: model.StopReasonEndTurn,
			},
			{
				ID:         "resp-compaction-summary",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "Historical work completed."}},
				StopReason: model.StopReasonEndTurn,
			},
			{
				ID:         "resp-after-compaction",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "continued after compaction"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	currentNow := now.Add(2 * time.Millisecond)
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return currentNow },
	}
	if err := executor.ExecuteModelWork(ctx, firstTurn); err != nil {
		t.Fatalf("execute historical turn: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		firstTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release historical turn runtime: %v", err)
	}

	secondTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"continue with compactable history",
		now.Add(10*time.Millisecond),
	)
	currentNow = now.Add(13 * time.Millisecond)
	if err := executor.ExecuteModelWork(ctx, secondTurn); err != nil {
		t.Fatalf("execute serialized overflow turn: %v", err)
	}

	var state executionstore.ModelCallState
	var recoveryKind executionstore.ModelCallRecoveryKind
	var errorCode string
	if err := fixture.Pool.QueryRow(ctx, `
SELECT context.state, context.recovery_kind, context.error_code
FROM model_call_contexts context
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.input_event_sequence = $3
`, kernelTestProjectID, agentID, secondTurn.OpeningEventSequence).Scan(
		&state,
		&recoveryKind,
		&errorCode,
	); err != nil {
		t.Fatalf("load compactable serialized overflow attempt: %v", err)
	}
	if state != executionstore.ModelCallContextFailed || recoveryKind != executionstore.ModelCallRecoveryCompact ||
		errorCode != "configured_input_budget_exceeded" {
		t.Fatalf(
			"compactable serialized overflow attempt = %q/%q/%q",
			state,
			recoveryKind,
			errorCode,
		)
	}
	var compactionDependencies int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM model_call_contexts context
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.input_event_sequence = $3
  AND context.operation_kind = 'compaction'
`, kernelTestProjectID, agentID, secondTurn.OpeningEventSequence).Scan(&compactionDependencies); err != nil {
		t.Fatalf("count compactable serialized overflow dependencies: %v", err)
	}
	if compactionDependencies != 1 {
		t.Fatalf("serialized overflow compaction dependencies = %d, want 1", compactionDependencies)
	}
}
