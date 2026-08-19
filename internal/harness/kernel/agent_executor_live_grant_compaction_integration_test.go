//go:build integration

package kernel

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/modelprovider"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
)

func TestAgentExecutorCompactsRetryWhenReplacementGrantShrinksWindow(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	const originalWindow = 10_000
	agentID, userID := fixture.createAgentWithModelOptions(
		t,
		ctx,
		"openai/live-grant-shrink",
		fixture.Now,
		kernelConfiguredModelOptions{
			ContextWindowTokens: intPtrForKernelCompactionTest(originalWindow),
			MaxOutputTokens:     intPtrForKernelCompactionTest(64),
		},
	)

	var configID storage.ID
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT current_config_id
		FROM agents
		WHERE project_id = $1 AND id = $2`, kernelTestProjectID, agentID).Scan(&configID); err != nil {
		t.Fatalf("load agent config id: %v", err)
	}
	config, found, err := fixture.Store.Execution().GetAgentConfig(ctx, kernelTestProjectID, configID)
	if err != nil || !found {
		t.Fatalf("load agent config: found=%v err=%v", found, err)
	}
	configuredModelID := configuredModelIDForKernelConfig(t, ctx, fixture.Store, config)
	originalGrantID := currentProjectModelGrantIDForKernelConfig(t, ctx, fixture.Store, config)

	productionResolver := modelprovider.Resolver{
		Models:  fixture.Store.Models(),
		Secrets: fixture.Store.Secrets(),
	}
	const historyMarker = "HISTORY_BEFORE_LIVE_GRANT_SHRINK"
	historyNeedle := historyMarker + " " + strings.Repeat("durable context detail ", 120)
	seedModel := &sequenceKernelModel{
		providerModelSlug: "live-grant-shrink",
		responses: []model.Response{{
			ID:         "resp_before_live_grant_shrink",
			Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "history accepted before grant shrink"}},
			StopReason: model.StopReasonEndTurn,
		}},
	}
	seedTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		historyNeedle,
		fixture.Now.Add(time.Second),
	)
	seedResolver := &productionPolicyTestResolver{
		Resolver: productionResolver,
		Client:   seedModel,
	}
	seedExecutor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: seedResolver,
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(2 * time.Second) },
	}
	if err := seedExecutor.ExecuteModelWork(ctx, seedTurn); err != nil {
		t.Fatalf("execute history turn: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		seedTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release history turn lock: %v", err)
	}

	currentNow := fixture.Now.Add(4 * time.Second)
	retryModel := &sequenceKernelModel{
		providerModelSlug: "live-grant-shrink",
		preparedInputTokenEstimator: func(bundle modelcontext.Bundle) int {
			if bundle.ContextCheckpoint != nil || isCompactionRequestBundle(bundle) {
				return 500
			}
			return 3_000
		},
		errs: []error{model.ProviderError{
			Kind:    model.ErrorKindRateLimit,
			Source:  "test-provider",
			Code:    "rate_limited_before_grant_shrink",
			Message: "retry after the live grant changes",
		}},
		responses: []model.Response{
			{
				ID:         "resp_live_grant_shrink_summary",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "The earlier durable context was summarized after the model window shrank."}},
				StopReason: model.StopReasonEndTurn,
			},
			{
				ID:         "resp_after_live_grant_shrink",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "continued under the replacement grant"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	retryTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"continue after the provider retry",
		fixture.Now.Add(3*time.Second),
	)
	retryResolver := &productionPolicyTestResolver{
		Resolver: productionResolver,
		Client:   retryModel,
	}
	executor := AgentExecutor{
		Store:           fixture.Store,
		ModelResolver:   retryResolver,
		ToolExecutor:    tools.Executor{Store: fixture.Store},
		Now:             func() time.Time { return currentNow },
		ModelRetryDelay: immediateKernelModelRetryDelay,
	}
	if err := executor.ExecuteModelWork(ctx, retryTurn); err != nil {
		t.Fatalf("execute sent attempt before grant shrink: %v", err)
	}
	if retryModel.respondedCount() != 1 {
		t.Fatalf("provider sends before grant shrink = %d, want 1", retryModel.respondedCount())
	}
	if len(retryResolver.resolutions) != 1 ||
		retryResolver.resolutions[0].ContextWindowTokens != originalWindow {
		t.Fatalf("initial live policy resolution = %+v", retryResolver.resolutions)
	}

	var contextID storage.ID
	var retryAt time.Time
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT context.id,
		       context.retry_at
		FROM model_call_contexts context
		WHERE context.project_id = $1
		  AND context.agent_id = $2
		  AND context.operation_kind = 'normal'
		  AND context.input_event_sequence = $3
		  AND context.attempt_number = 1`,
		kernelTestProjectID,
		agentID,
		retryTurn.OpeningEventSequence,
	).Scan(&contextID, &retryAt); err != nil {
		t.Fatalf("load retryable attempt before grant shrink: %v", err)
	}

	if _, err := fixture.Store.Models().DeleteProjectModelGrant(
		ctx,
		kernelTestOrgID,
		kernelTestProjectID,
		originalGrantID,
	); err != nil {
		t.Fatalf("revoke original model grant: %v", err)
	}
	shrunkWindow := 2_500
	_, err = fixture.Store.Models().CreateProjectModelGrant(
		ctx,
		modelstore.CreateProjectModelGrantInput{
			OrgID:               kernelTestOrgID,
			ProjectID:           kernelTestProjectID,
			ConfiguredModelID:   configuredModelID,
			ContextWindowTokens: &shrunkWindow,
		},
	)
	if err != nil {
		t.Fatalf("create narrower replacement grant: %v", err)
	}

	currentNow = retryAt.Add(time.Millisecond)
	shrunkRetry := continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, retryTurn, currentNow)
	if shrunkRetry.ModelCallContextID != contextID {
		t.Fatalf(
			"retry context after grant shrink = %s, want %s",
			shrunkRetry.ModelCallContextID,
			contextID,
		)
	}
	if err := executor.ExecuteModelWork(ctx, shrunkRetry); err != nil {
		t.Fatalf("execute retry under narrower replacement grant: %v", err)
	}
	if retryModel.respondedCount() != 2 {
		t.Fatalf(
			"provider sends after narrower retry = %d, want original send plus compaction only",
			retryModel.respondedCount(),
		)
	}
	if summaryRequest := string(retryModel.responded[1].ProviderRequest); !strings.Contains(summaryRequest, historyMarker) {
		t.Fatalf("compaction request omitted prior durable history: %s", summaryRequest)
	}
	if len(retryResolver.resolutions) != 3 {
		t.Fatalf("live policy resolutions after compaction = %+v, want normal, retry, and compaction", retryResolver.resolutions)
	}
	for index, resolution := range retryResolver.resolutions[1:] {
		if resolution.ContextWindowTokens != shrunkWindow {
			t.Fatalf("replacement live policy resolution %d = %+v", index+2, resolution)
		}
	}

	var secondContextID storage.ID
	var secondState executionstore.ModelCallState
	var secondRecoveryKind executionstore.ModelCallRecoveryKind
	var secondErrorCode string
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT id,
		       state,
		       recovery_kind,
		       error_code
		FROM model_call_contexts
		WHERE project_id = $1
		  AND agent_id = $2
		  AND input_event_sequence = (
		    SELECT input_event_sequence
		    FROM model_call_contexts
		    WHERE project_id = $1 AND agent_id = $2 AND id = $3
		  )
		  AND operation_kind = 'normal'
		  AND attempt_number = 2`,
		kernelTestProjectID,
		agentID,
		contextID,
	).Scan(
		&secondContextID,
		&secondState,
		&secondRecoveryKind,
		&secondErrorCode,
	); err != nil {
		t.Fatalf("load compacting retry under replacement grant: %v", err)
	}
	if secondState != executionstore.ModelCallContextFailed ||
		secondRecoveryKind != executionstore.ModelCallRecoveryCompact ||
		secondErrorCode != "configured_input_budget_exceeded" {
		t.Fatalf(
			"replacement retry = state=%s recovery=%s code=%s",
			secondState,
			secondRecoveryKind,
			secondErrorCode,
		)
	}
	var replacementCompactions int
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM model_call_contexts context
		WHERE context.project_id = $1
		  AND context.agent_id = $2
		  AND context.operation_kind = 'compaction'
		  AND context.input_event_sequence = (
		    SELECT input_event_sequence
		    FROM model_call_contexts
		    WHERE project_id = $1 AND agent_id = $2 AND id = $3
		  )
		  AND context.state = 'succeeded'`,
		kernelTestProjectID,
		agentID,
		secondContextID,
	).Scan(&replacementCompactions); err != nil {
		t.Fatalf("count replacement-grant compactions: %v", err)
	}
	if replacementCompactions != 1 {
		t.Fatalf("replacement-grant compactions = %d, want 1", replacementCompactions)
	}

	currentNow = currentNow.Add(time.Second)
	finalTurn := continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, shrunkRetry, currentNow)
	if err := executor.ExecuteModelWork(ctx, finalTurn); err != nil {
		t.Fatalf("execute normal retry after replacement-grant compaction: %v", err)
	}
	if retryModel.respondedCount() != 3 {
		t.Fatalf("provider sends after compaction = %d, want 3", retryModel.respondedCount())
	}
	if len(retryResolver.resolutions) != 4 ||
		retryResolver.resolutions[3].ContextWindowTokens != shrunkWindow {
		t.Fatalf("final live policy resolution = %+v", retryResolver.resolutions)
	}
	finalRequest := string(retryModel.responded[2].ProviderRequest)
	if !strings.Contains(finalRequest, "The earlier durable context was summarized after the model window shrank.") ||
		strings.Contains(finalRequest, historyNeedle) {
		t.Fatalf("final request did not replace raw history with checkpoint summary: %s", finalRequest)
	}

	var finalContextID storage.ID
	var finalState executionstore.ModelCallState
	var finalAttemptNumber int32
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT final_context.id,
		       final_context.state,
		       final_context.attempt_number
		FROM model_call_contexts compaction
		JOIN context_checkpoints checkpoint
		  ON checkpoint.agent_id = compaction.agent_id
		 AND checkpoint.producer_model_call_context_id = compaction.id
		JOIN agent_events checkpoint_event
		  ON checkpoint_event.agent_id = checkpoint.agent_id
		 AND checkpoint_event.context_checkpoint_id = checkpoint.id
		JOIN model_call_contexts final_context
		  ON final_context.project_id = compaction.project_id
		 AND final_context.agent_id = checkpoint.agent_id
		 AND final_context.operation_kind = 'normal'
		 AND final_context.input_event_sequence >= checkpoint_event.sequence
		WHERE compaction.project_id = $1
		  AND compaction.agent_id = $2
		  AND compaction.input_event_sequence = (
		    SELECT input_event_sequence
		    FROM model_call_contexts
		    WHERE project_id = $1 AND agent_id = $2 AND id = $3
		  )
		  AND final_context.state = 'succeeded'`,
		kernelTestProjectID,
		agentID,
		secondContextID,
	).Scan(&finalContextID, &finalState, &finalAttemptNumber); err != nil {
		t.Fatalf("load final replacement-grant attempt: %v", err)
	}
	if finalContextID == contextID ||
		finalState != executionstore.ModelCallContextSucceeded || finalAttemptNumber != 1 {
		t.Fatalf(
			"final context/state/attempt = %s/%s/%d, want a new succeeded context with attempt 1",
			finalContextID,
			finalState,
			finalAttemptNumber,
		)
	}
}

type productionPolicyTestResolver struct {
	Resolver model.Resolver
	Client   model.Client

	resolutions []productionPolicyResolution
}

type productionPolicyResolution struct {
	ContextWindowTokens int
}

func (r *productionPolicyTestResolver) Resolve(
	ctx context.Context,
	selection model.Selection,
) (model.ResolvedClient, error) {
	resolved, err := r.Resolver.Resolve(ctx, selection)
	if err != nil {
		return model.ResolvedClient{}, err
	}
	capabilities := model.CapabilitiesForClient(resolved.Client)
	r.resolutions = append(r.resolutions, productionPolicyResolution{
		ContextWindowTokens: capabilities.ContextWindowTokens,
	})
	resolved.Client = resolvedPolicyTestClient{
		Client:       r.Client,
		capabilities: capabilities,
		apiFormat:    model.APIFormatForClient(resolved.Client),
		apiVariant:   model.APIVariantForClient(resolved.Client),
	}
	return resolved, nil
}

type resolvedPolicyTestClient struct {
	model.Client
	capabilities model.Capabilities
	apiFormat    modelprotocol.APIFormat
	apiVariant   modelprotocol.APIVariant
}

func (c resolvedPolicyTestClient) Capabilities() model.Capabilities {
	return c.capabilities
}

func (c resolvedPolicyTestClient) APIFormat() modelprotocol.APIFormat {
	return c.apiFormat
}

func (c resolvedPolicyTestClient) ModelAPIVariant() modelprotocol.APIVariant {
	return c.apiVariant
}
