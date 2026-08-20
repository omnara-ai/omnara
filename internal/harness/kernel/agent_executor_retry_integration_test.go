//go:build integration

package kernel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestAgentExecutorRetriesTransientProviderResponse(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/transient-retry-model", now)
	turn := fixture.admitContentInputTurn(t, ctx, agentID, userID, "hello", now.Add(time.Millisecond))
	modelClient := &sequenceKernelModel{
		providerModelSlug: "transient-retry-model",
		errs: []error{
			model.ProviderError{
				Kind:    model.ErrorKindTransient,
				Source:  "test-provider",
				Message: "stream ended without terminal event",
				Metadata: json.RawMessage(
					`{"event":"response.error","raw_event_bytes":9}`,
				),
			},
			model.ProviderError{
				Kind:    model.ErrorKindTransient,
				Source:  "test-provider",
				Message: "stream ended without terminal event again",
			},
		},
		responses: []model.Response{{
			ID:                      "resp-transient-retry",
			ProviderReportedCostUSD: "0.000003",
			Content:                 []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "done after retry"}},
			StopReason:              model.StopReasonEndTurn,
		}},
		errorResponses: []model.Response{
			{ID: "resp-transient-error-1", ProviderReportedCostUSD: "0.000001"},
			{ID: "resp-transient-error-2", ProviderReportedCostUSD: "0.000001"},
		},
	}
	currentNow := now.Add(2 * time.Millisecond)
	executor := AgentExecutor{
		Store:           fixture.Store,
		ModelResolver:   liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:    tools.Executor{Store: fixture.Store},
		StreamPublisher: &capturingStreamPublisher{},
		Now:             func() time.Time { return currentNow },
		ModelRetryDelay: immediateKernelModelRetryDelay,
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute first attempt: %v", err)
	}
	if modelClient.preparedCount() != 1 {
		t.Fatalf("prepared requests after first lease = %d, want 1", modelClient.preparedCount())
	}
	var operationFrontier int64
	for attemptNumber := 2; attemptNumber <= 3; attemptNumber++ {
		if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
			ctx,
			kernelTestProjectID,
			agentID,
			turn.RuntimeLockID,
		); err != nil {
			t.Fatalf("release runtime after attempt %d: %v", attemptNumber-1, err)
		}
		currentNow = now.Add(time.Duration(attemptNumber) * time.Minute)
		claim, found, err := fixture.Store.Execution().ClaimNextAgentWork(
			ctx,
			kernelTestClaimInput(currentNow),
		)
		if err != nil {
			t.Fatalf("claim work for attempt %d: %v", attemptNumber, err)
		}
		if !found || claim.Kind != executionstore.AgentWorkModel || claim.Model.ModelCallContextID == storage.NilID {
			t.Fatalf("attempt %d claim = %+v found=%v, want a retry continuation", attemptNumber, claim, found)
		}
		predecessor, found, err := fixture.Store.Execution().GetModelCallContext(
			ctx,
			kernelTestProjectID,
			agentID,
			claim.Model.ModelCallContextID,
		)
		if err != nil || !found {
			t.Fatalf("load predecessor for attempt %d: found=%v err=%v", attemptNumber, found, err)
		}
		if predecessor.AttemptNumber != attemptNumber-1 {
			t.Fatalf(
				"attempt %d predecessor number = %d, want %d",
				attemptNumber,
				predecessor.AttemptNumber,
				attemptNumber-1,
			)
		}
		if operationFrontier == 0 {
			operationFrontier = predecessor.InputEventSequence
		} else if predecessor.InputEventSequence != operationFrontier {
			t.Fatalf(
				"attempt %d frontier = %d, want %d",
				attemptNumber,
				predecessor.InputEventSequence,
				operationFrontier,
			)
		}
		turn = modelWorkExecutionFromClaimForKernelTest(claim, currentNow)
		if err := executor.ExecuteModelWork(ctx, turn); err != nil {
			t.Fatalf("execute attempt %d: %v", attemptNumber, err)
		}
		if modelClient.preparedCount() != attemptNumber {
			t.Fatalf(
				"prepared requests after attempt %d = %d, want %d",
				attemptNumber,
				modelClient.preparedCount(),
				attemptNumber,
			)
		}
	}
	if got, want := modelClient.respondHadSink, []bool{true, false, false}; len(got) != len(want) ||
		got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("respond stream sink flags = %v, want %v", got, want)
	}
	firstRequest := string(modelClient.prepared[0].ProviderRequest)
	for index := 1; index < len(modelClient.prepared); index++ {
		if got := string(modelClient.prepared[index].ProviderRequest); got != firstRequest {
			t.Fatalf("retry request %d changed bytes:\nfirst=%s\nretry=%s", index+1, firstRequest, got)
		}
	}
	var failedContexts, contextsWithProviderMetadata, succeededContexts, contextsWithCost, contexts, operationFrontiers int
	if err := fixture.Pool.QueryRow(ctx, `
			SELECT count(*) FILTER (
			         WHERE context.state = 'failed'
			           AND context.recovery_kind = 'retry'
			           AND context.error_kind = 'transient'
			       ),
			       count(*) FILTER (
			         WHERE context.error_details->'provider_metadata'->>'event' = 'response.error'
			           AND (context.error_details->'provider_metadata'->>'raw_event_bytes')::integer = 9
			       ),
			       count(*) FILTER (
			         WHERE context.state = 'succeeded'
			           AND context.provider_response_id = 'resp-transient-retry'
			       ),
			       count(*) FILTER (
			         WHERE (context.state = 'failed' AND context.provider_reported_cost_usd = 0.000001)
			            OR (context.state = 'succeeded' AND context.provider_reported_cost_usd = 0.000003)
			       ),
			       count(*),
			       count(DISTINCT context.input_event_sequence)
		FROM model_call_contexts context
		WHERE context.project_id = $1
		  AND context.agent_id = $2
		  AND context.input_event_sequence = $3
		  AND context.operation_kind = 'normal'
	`, kernelTestProjectID, agentID, operationFrontier).Scan(
		&failedContexts,
		&contextsWithProviderMetadata,
		&succeededContexts,
		&contextsWithCost,
		&contexts,
		&operationFrontiers,
	); err != nil {
		t.Fatalf("count durable model call retry contexts: %v", err)
	}
	if failedContexts != 2 || contextsWithProviderMetadata != 1 || succeededContexts != 1 ||
		contextsWithCost != 3 || contexts != 3 || operationFrontiers != 1 {
		t.Fatalf(
			"model contexts failed/with-metadata/succeeded/with-cost/total/frontiers = %d/%d/%d/%d/%d/%d, want 2/1/1/3/3/1",
			failedContexts,
			contextsWithProviderMetadata,
			succeededContexts,
			contextsWithCost,
			contexts,
			operationFrontiers,
		)
	}
}

func TestManagedModelRetryStopsAfterAdmissionCloses(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	fixture.provisionClusterModel(t, ctx, "managed-retry-prod", "managed-retry-model")
	agentID, userID := fixture.createNamedAgentWithModelOptions(
		t,
		ctx,
		"Managed Retry Admission",
		"managed-retry/managed-retry-model",
		now,
		kernelConfiguredModelOptions{},
	)
	agent, err := fixture.Store.Execution().GetAgentInProject(ctx, kernelTestProjectID, agentID)
	if err != nil {
		t.Fatalf("load managed-retry agent: %v", err)
	}
	attachKernelSlackTarget(
		t,
		ctx,
		fixture,
		agentID,
		agent.AgentProfileID,
		"managed-retry",
		"C_MANAGED_RETRY:1.0",
	)
	work := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"continue an admitted model call",
		now.Add(time.Millisecond),
	)
	modelClient := &sequenceKernelModel{
		providerModelSlug: "managed-retry-model",
		errs: []error{model.ProviderError{
			Kind:    model.ErrorKindTransient,
			Source:  "test-provider",
			Message: "retry this request",
		}},
	}
	currentNow := now.Add(2 * time.Millisecond)
	postCount := 0
	integrationHTTPClient := &http.Client{Transport: kernelSlackRoundTripFunc(
		func(req *http.Request) (*http.Response, error) {
			postCount++
			if req.URL.Path != "/api/chat.postMessage" {
				t.Fatalf("Slack runtime message path = %q", req.URL.Path)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					`{"ok":true,"channel":"C_MANAGED_RETRY","ts":"2.0"}`,
				)),
				Request: req,
			}, nil
		},
	)}
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor: tools.Executor{
			Store:                 fixture.Store,
			IntegrationHTTPClient: integrationHTTPClient,
		},
		StreamPublisher: &capturingStreamPublisher{},
		Now:             func() time.Time { return currentNow },
		ModelRetryDelay: immediateKernelModelRetryDelay,
	}
	if err := executor.ExecuteModelWork(ctx, work); err != nil {
		t.Fatalf("execute admitted model attempt: %v", err)
	}
	if modelClient.preparedCount() != 1 {
		t.Fatalf("prepared requests after admitted attempt = %d, want 1", modelClient.preparedCount())
	}
	if postCount != 0 {
		t.Fatalf("Slack runtime message post count after retryable failure = %d, want 0", postCount)
	}
	fixture.setManagedWorkAdmission(t, ctx, false)
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		work.RuntimeLockID,
	); err != nil {
		t.Fatalf("release admitted model attempt: %v", err)
	}
	currentNow = now.Add(time.Minute)
	retry, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		kernelTestClaimInput(currentNow),
	)
	if err != nil {
		t.Fatalf("claim admitted model retry: %v", err)
	}
	if !found || retry.Kind != executionstore.AgentWorkModel ||
		retry.Model.ModelCallContextID == storage.NilID {
		t.Fatalf("retry claim = %+v found=%v, want model retry continuation", retry, found)
	}
	retryInput := modelWorkExecutionFromClaimForKernelTest(retry, currentNow)
	if err := executor.ExecuteModelWork(ctx, retryInput); err != nil {
		t.Fatalf("deny managed model retry after admission closes: %v", err)
	}
	if postCount != 1 {
		t.Fatalf("Slack runtime message post count after terminal denial = %d, want 1", postCount)
	}
	if err := executor.ExecuteModelWork(ctx, retryInput); err != nil {
		t.Fatalf("replay denied managed model retry: %v", err)
	}
	if postCount != 1 {
		t.Fatalf("Slack runtime message post count after replay = %d, want 1", postCount)
	}
	if modelClient.preparedCount() != 1 || modelClient.respondedCount() != 1 {
		t.Fatalf(
			"prepared/responded requests after denied retry = %d/%d, want 1/1",
			modelClient.preparedCount(),
			modelClient.respondedCount(),
		)
	}
	assertDurableModelErrorForKernelTest(
		t,
		ctx,
		fixture,
		agentID,
		work.TurnID,
		string(modelprotocol.ErrorKindRuntime),
		storeerr.ManagedWorkAdmissionDeniedCode,
	)
	var contexts, retryableContexts, admissionDeniedContexts int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*),
       count(*) FILTER (WHERE state = 'failed' AND recovery_kind = 'retry'),
       count(*) FILTER (
           WHERE state = 'failed'
             AND recovery_kind IS NULL
             AND error_code = $4
       )
FROM model_call_contexts
WHERE project_id = $1
  AND agent_id = $2
  AND input_event_sequence = $3
  AND operation_kind = 'normal'
`, kernelTestProjectID, agentID, work.OpeningEventSequence, storeerr.ManagedWorkAdmissionDeniedCode).Scan(
		&contexts,
		&retryableContexts,
		&admissionDeniedContexts,
	); err != nil {
		t.Fatalf("count denied model retry contexts: %v", err)
	}
	if contexts != 2 || retryableContexts != 1 || admissionDeniedContexts != 1 {
		t.Fatalf(
			"model retry contexts total/retryable/admission-denied = %d/%d/%d, want 2/1/1",
			contexts,
			retryableContexts,
			admissionDeniedContexts,
		)
	}
}

func TestManagedModelRetryReplayReturnsExistingAttemptAfterAdmissionCloses(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	fixture.provisionClusterModel(t, ctx, "managed-replay-prod", "managed-replay-model")
	agentID, userID := fixture.createNamedAgentWithModelOptions(
		t,
		ctx,
		"Managed Retry Replay",
		"managed-replay/managed-replay-model",
		fixture.Now,
		kernelConfiguredModelOptions{},
	)
	turn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"create one retry attempt",
		fixture.Now.Add(time.Millisecond),
	)
	agent, err := fixture.Store.Execution().GetAgentInProject(ctx, kernelTestProjectID, agentID)
	if err != nil {
		t.Fatalf("load managed retry replay agent: %v", err)
	}
	initial, err := fixture.Store.Execution().ClaimNormalModelCall(
		ctx,
		executionstore.ClaimNormalModelCallInput{
			ProjectID:          kernelTestProjectID,
			AgentID:            agentID,
			RuntimeLockID:      turn.RuntimeLockID,
			OpeningInputIDs:    turn.InputIDs,
			AgentConfigID:      agent.CurrentConfigID,
			InputEventSequence: turn.OpeningEventSequence,
		},
	)
	if err != nil {
		t.Fatalf("claim initial managed replay attempt: %v", err)
	}
	if _, err := fixture.Store.Execution().RecordRetryableModelCallFailure(
		ctx,
		executionstore.RecordRecoverableModelCallFailureInput{
			ProjectID:          kernelTestProjectID,
			AgentID:            agentID,
			ModelCallContextID: initial.Context.ID,
			RuntimeLockID:      turn.RuntimeLockID,
			ErrorKind:          model.ErrorKindTransient,
			ErrorCode:          "provider_unavailable",
			ErrorMessage:       "retry this request",
			RetryDelay:         0,
		},
	); err != nil {
		t.Fatalf("record managed replay predecessor: %v", err)
	}
	retryInput := executionstore.ClaimNextModelCallContextInput{
		ProjectID:                     kernelTestProjectID,
		AgentID:                       agentID,
		PredecessorModelCallContextID: initial.Context.ID,
		RuntimeLockID:                 turn.RuntimeLockID,
	}
	retry, err := fixture.Store.Execution().ClaimNextModelCallContext(ctx, retryInput)
	if err != nil {
		t.Fatalf("create managed retry attempt: %v", err)
	}
	if !retry.Created || !retry.Claimed || retry.Context.State != executionstore.ModelCallContextStarted {
		t.Fatalf("managed retry = %+v, want newly started attempt", retry)
	}

	fixture.setManagedWorkAdmission(t, ctx, false)
	replayed, err := fixture.Store.Execution().ClaimNextModelCallContext(ctx, retryInput)
	if err != nil {
		t.Fatalf("replay existing managed retry after admission closes: %v", err)
	}
	if replayed.Context.ID != retry.Context.ID || replayed.Created || replayed.Claimed ||
		replayed.Context.State != executionstore.ModelCallContextStarted ||
		replayed.Context.ErrorCode != "" {
		t.Fatalf("replayed managed retry = %+v, want unchanged existing attempt", replayed)
	}
}

func TestAgentExecutorRetriesWithoutProviderReplayAfterReplayRejection(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/replay-rejection-model", now)
	work := fixture.admitContentInputTurn(t, ctx, agentID, userID, "continue", now.Add(time.Millisecond))
	modelClient := &sequenceKernelModel{
		providerModelSlug: "replay-rejection-model",
		errs: []error{model.ProviderError{
			Kind:    model.ErrorKindReplayRejected,
			Source:  "test-provider",
			Code:    "invalid_encrypted_content",
			Message: "provider replay could not be decrypted",
		}},
		responses: []model.Response{{
			ID:         "resp-after-replay-rejection",
			Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "continued canonically"}},
			StopReason: model.StopReasonEndTurn,
		}},
	}
	currentNow := now.Add(2 * time.Millisecond)
	executor := AgentExecutor{
		Store:           fixture.Store,
		ModelResolver:   liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:    tools.Executor{Store: fixture.Store},
		Now:             func() time.Time { return currentNow },
		ModelRetryDelay: immediateKernelModelRetryDelay,
	}
	if err := executor.ExecuteModelWork(ctx, work); err != nil {
		t.Fatalf("execute replay-rejected attempt: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		work.RuntimeLockID,
	); err != nil {
		t.Fatalf("release replay-rejected attempt: %v", err)
	}
	currentNow = now.Add(time.Minute)
	claim, found, err := fixture.Store.Execution().ClaimNextAgentWork(ctx, kernelTestClaimInput(currentNow))
	if err != nil {
		t.Fatalf("claim retry after replay rejection: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkModel || claim.Model.ModelCallContextID == storage.NilID {
		t.Fatalf("retry claim = %+v found=%v, want model continuation", claim, found)
	}
	work = modelWorkExecutionFromClaimForKernelTest(claim, currentNow)
	if err := executor.ExecuteModelWork(ctx, work); err != nil {
		t.Fatalf("execute canonical retry: %v", err)
	}
	if len(modelClient.prepared) != 2 ||
		modelClient.prepared[0].Policy.ProviderReplayCutoffEventSequence != 0 ||
		modelClient.prepared[1].Policy.ProviderReplayCutoffEventSequence != work.OpeningEventSequence {
		t.Fatalf(
			"provider replay cutoffs = %+v, want 0 then %d",
			modelClient.prepared,
			work.OpeningEventSequence,
		)
	}

	var replayFailures, successes int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*) FILTER (
         WHERE state = 'failed'
           AND recovery_kind = 'retry'
           AND error_kind = 'replay_rejected'
       ),
       count(*) FILTER (
         WHERE state = 'succeeded'
           AND provider_response_id = 'resp-after-replay-rejection'
       )
FROM model_call_contexts
WHERE project_id = $1
  AND agent_id = $2
  AND operation_kind = 'normal'
  AND input_event_sequence = $3
`, kernelTestProjectID, agentID, work.OpeningEventSequence).Scan(&replayFailures, &successes); err != nil {
		t.Fatalf("load replay rejection retry chain: %v", err)
	}
	if replayFailures != 1 || successes != 1 {
		t.Fatalf("replay rejection failures/successes = %d/%d, want 1/1", replayFailures, successes)
	}
}

func TestAgentExecutorStopsRepeatedReplayRejectionAtSameFrontier(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/repeated-replay-rejection-model", now)
	work := fixture.admitContentInputTurn(t, ctx, agentID, userID, "continue", now.Add(time.Millisecond))
	retry := true
	doNotRetry := false
	modelClient := &sequenceKernelModel{
		providerModelSlug: "repeated-replay-rejection-model",
		errs: []error{
			model.ProviderError{
				Kind:      model.ErrorKindReplayRejected,
				Source:    "test-provider",
				Code:      "invalid_replay_first",
				Message:   "provider replay could not be decrypted",
				Retryable: &doNotRetry,
			},
			model.ProviderError{
				Kind:      model.ErrorKindReplayRejected,
				Source:    "test-provider",
				Code:      "invalid_replay_canonical",
				Message:   "canonical request was also labeled as rejected replay",
				Retryable: &retry,
			},
		},
	}
	currentNow := now.Add(2 * time.Millisecond)
	executor := AgentExecutor{
		Store:           fixture.Store,
		ModelResolver:   liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:    tools.Executor{Store: fixture.Store},
		Now:             func() time.Time { return currentNow },
		ModelRetryDelay: immediateKernelModelRetryDelay,
	}
	if err := executor.ExecuteModelWork(ctx, work); err != nil {
		t.Fatalf("execute initial replay-rejected attempt: %v", err)
	}
	currentNow = now.Add(time.Minute)
	canonical := continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, work, currentNow)
	if err := executor.ExecuteModelWork(ctx, canonical); err != nil {
		t.Fatalf("execute canonical retry: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		canonical.RuntimeLockID,
	); err != nil {
		t.Fatalf("release canonical retry: %v", err)
	}
	if modelClient.respondedCount() != 2 || modelClient.preparedCount() != 2 {
		t.Fatalf(
			"prepared/responded requests = %d/%d, want exactly 2/2",
			modelClient.preparedCount(),
			modelClient.respondedCount(),
		)
	}
	if modelClient.prepared[0].Policy.ProviderReplayCutoffEventSequence != 0 ||
		modelClient.prepared[1].Policy.ProviderReplayCutoffEventSequence != work.OpeningEventSequence {
		t.Fatalf(
			"provider replay cutoffs = %d/%d, want 0/%d",
			modelClient.prepared[0].Policy.ProviderReplayCutoffEventSequence,
			modelClient.prepared[1].Policy.ProviderReplayCutoffEventSequence,
			work.OpeningEventSequence,
		)
	}
	assertDurableModelErrorForKernelTest(
		t,
		ctx,
		fixture,
		agentID,
		work.TurnID,
		string(modelprotocol.ErrorKindReplayRejected),
		"invalid_replay_canonical",
	)

	var contexts, retrying, stopped, frontiers, wakeups, locks int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)::integer,
       count(*) FILTER (WHERE state = 'failed' AND recovery_kind = 'retry')::integer,
       count(*) FILTER (WHERE state = 'failed' AND recovery_kind IS NULL)::integer,
       count(DISTINCT input_event_sequence)::integer,
       (SELECT count(*)::integer FROM agent_wakeups wake
        JOIN agents agent ON agent.id = wake.agent_id
        WHERE agent.project_id = $1 AND wake.agent_id = $2),
       (SELECT count(*)::integer FROM agent_runtime_locks runtime_lock
        JOIN agents agent ON agent.id = runtime_lock.agent_id
        WHERE agent.project_id = $1 AND runtime_lock.agent_id = $2)
FROM model_call_contexts
WHERE project_id = $1
  AND agent_id = $2
  AND operation_kind = 'normal'
  AND input_event_sequence = $3
`, kernelTestProjectID, agentID, work.OpeningEventSequence).Scan(
		&contexts,
		&retrying,
		&stopped,
		&frontiers,
		&wakeups,
		&locks,
	); err != nil {
		t.Fatalf("load bounded replay recovery: %v", err)
	}
	if contexts != 2 || retrying != 1 || stopped != 1 || frontiers != 1 || wakeups != 0 || locks != 0 {
		t.Fatalf(
			"bounded recovery contexts/retry/stop/frontiers/wakeups/locks = %d/%d/%d/%d/%d/%d, want 2/1/1/1/0/0",
			contexts,
			retrying,
			stopped,
			frontiers,
			wakeups,
			locks,
		)
	}
}

func TestAgentExecutorBoundsProviderEvidenceAttachedToError(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/error-evidence-model", now)
	turn := fixture.admitContentInputTurn(t, ctx, agentID, userID, "hello", now.Add(time.Millisecond))
	modelClient := &sequenceKernelModel{
		providerModelSlug: "error-evidence-model",
		errs: []error{model.ProviderError{
			Kind: model.ErrorKindTransient, Source: "test-provider", Code: "provider_unavailable", Message: "try later",
		}},
		errorResponses: []model.Response{{
			ID:                      strings.Repeat("r", model.MaxProviderIdentityBytes+1),
			ServedProviderModelSlug: "served-error-model",
			Usage:                   model.Usage{InputTokens: 19, OutputTokens: 3},
		}},
	}
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return now.Add(2 * time.Millisecond) },
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute provider error with unsafe evidence: %v", err)
	}

	var state executionstore.ModelCallState
	var recoveryKind executionstore.ModelCallRecoveryKind
	var responseID string
	var inputTokens, outputTokens int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT context.state,
	       context.recovery_kind,
	       context.provider_response_id,
	       coalesce(context.input_tokens_total, 0),
	       coalesce(context.output_tokens_total, 0)
FROM model_call_contexts context
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.input_event_sequence = $3
`, kernelTestProjectID, agentID, turn.OpeningEventSequence).Scan(
		&state,
		&recoveryKind,
		&responseID,
		&inputTokens,
		&outputTokens,
	); err != nil {
		t.Fatalf("load bounded provider error evidence: %v", err)
	}
	if state != executionstore.ModelCallContextFailed || recoveryKind != executionstore.ModelCallRecoveryRetry ||
		responseID != "" {
		t.Fatalf(
			"provider error evidence = %q/%q response=%q",
			state,
			recoveryKind,
			responseID,
		)
	}
	if inputTokens != 0 || outputTokens != 0 {
		t.Fatalf("bounded provider error usage = input %d output %d", inputTokens, outputTokens)
	}
}

func TestAgentExecutorPreservesRetryAfterEvidenceWhenAttemptsAreExhausted(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/exhausted-rate-limit-model", now)
	turn := fixture.admitContentInputTurn(t, ctx, agentID, userID, "hello", now.Add(time.Millisecond))
	openingEventSequence := turn.OpeningEventSequence
	retryAfterSeconds := int64(3_000_000_000)
	maxAttempts := executionstore.MaxModelCallRetriesPerOperation + 1
	errs := make([]error, maxAttempts)
	for i := range errs {
		errs[i] = model.ProviderError{
			Kind:    model.ErrorKindRateLimit,
			Source:  "test-provider",
			Code:    "rate_limited",
			Message: "retry later",
			RetryAfter: &model.RetryAfter{
				DeltaSeconds: &retryAfterSeconds,
			},
		}
	}
	modelClient := &sequenceKernelModel{
		providerModelSlug: "exhausted-rate-limit-model",
		errs:              errs,
	}
	currentNow := now.Add(2 * time.Millisecond)
	executor := AgentExecutor{
		Store:           fixture.Store,
		ModelResolver:   liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:    tools.Executor{Store: fixture.Store},
		Now:             func() time.Time { return currentNow },
		ModelRetryDelay: immediateKernelModelRetryDelay,
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute first rate-limited attempt: %v", err)
	}

	for attemptNumber := 2; attemptNumber <= maxAttempts; attemptNumber++ {
		if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
			ctx,
			kernelTestProjectID,
			agentID,
			turn.RuntimeLockID,
		); err != nil {
			t.Fatalf("release runtime after attempt %d: %v", attemptNumber-1, err)
		}
		currentNow = now.Add(time.Duration(attemptNumber) * 2 * time.Hour)
		claim, found, err := fixture.Store.Execution().ClaimNextAgentWork(
			ctx,
			kernelTestClaimInput(currentNow),
		)
		if err != nil {
			t.Fatalf("claim work for attempt %d: %v", attemptNumber, err)
		}
		if !found || claim.Kind != executionstore.AgentWorkModel || claim.Model.ModelCallContextID == storage.NilID {
			t.Fatalf("attempt %d claim = %+v found=%v, want retry continuation", attemptNumber, claim, found)
		}
		turn = modelWorkExecutionFromClaimForKernelTest(claim, currentNow)
		if err := executor.ExecuteModelWork(ctx, turn); err != nil {
			t.Fatalf("execute attempt %d: %v", attemptNumber, err)
		}
	}

	var (
		contextState     executionstore.ModelCallState
		recoveryKind     executionstore.ModelCallRecoveryKind
		retryAfterStored int64
		retryAt          *time.Time
		stopReason       string
	)
	if err := fixture.Pool.QueryRow(ctx, `
SELECT context.state, coalesce(context.recovery_kind, ''),
       (context.error_details #>> '{retry_after,delta_seconds}')::bigint,
       context.retry_at, output.stop_reason
FROM model_call_contexts context
JOIN model_outputs output
	  ON output.agent_id = context.agent_id
 AND output.model_call_context_id = context.id
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.input_event_sequence = $3
  AND context.attempt_number = $4
`, kernelTestProjectID, agentID, openingEventSequence, maxAttempts).Scan(
		&contextState,
		&recoveryKind,
		&retryAfterStored,
		&retryAt,
		&stopReason,
	); err != nil {
		t.Fatalf("load exhausted rate-limit outcome: %v", err)
	}
	if contextState != executionstore.ModelCallContextFailed || recoveryKind != "" ||
		retryAfterStored != retryAfterSeconds || retryAt != nil ||
		stopReason != "error" {
		t.Fatalf(
			"exhausted outcome = context %q/%q retry_after=%d retry_at=%v stop=%q",
			contextState,
			recoveryKind,
			retryAfterStored,
			retryAt,
			stopReason,
		)
	}
}

func TestAgentExecutorSteeringStartsFreshFrontierWithFreshRetryBudget(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/steering-retry-model", now)
	turn := fixture.admitContentInputTurn(t, ctx, agentID, userID, "original request", now.Add(time.Millisecond))
	retryAfterSeconds := int64(3600)
	modelClient := &sequenceKernelModel{
		providerModelSlug: "steering-retry-model",
		errs: []error{model.ProviderError{
			Kind:    model.ErrorKindRateLimit,
			Source:  "test-provider",
			Code:    "rate_limited",
			Message: "retry later",
			RetryAfter: &model.RetryAfter{
				DeltaSeconds: &retryAfterSeconds,
			},
		}},
		responses: []model.Response{{
			ID:         "resp_after_steering",
			Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "handled steering"}},
			StopReason: model.StopReasonEndTurn,
		}},
	}
	currentNow := now.Add(2 * time.Millisecond)
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return currentNow },
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute rate-limited attempt: %v", err)
	}
	var oldContextID storage.ID
	var oldRetryAt time.Time
	if err := fixture.Pool.QueryRow(ctx, `
SELECT context.id, context.retry_at
FROM model_call_contexts context
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.input_event_sequence = $3
  AND context.attempt_number = 1
`, kernelTestProjectID, agentID, turn.OpeningEventSequence).Scan(&oldContextID, &oldRetryAt); err != nil {
		t.Fatalf("load retrying context: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		turn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release retrying turn runtime: %v", err)
	}

	steeringAt := now.Add(3 * time.Millisecond)
	steering, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      kernelTestProjectID,
		AgentID:        agentID,
		Actor:          kernelTestOmnaraActorParams(t, userID),
		ContentBlocks:  mustKernelJSON([]map[string]string{{"type": "text", "text": "steer to the new request"}}),
		DeliveryMode:   executionstore.DeliveryModeSteering,
		IdempotencyKey: "steering-supersedes-retry",
	})
	if err != nil {
		t.Fatalf("create steering input during retry: %v", err)
	}
	var readyAt time.Time
	if err := fixture.Pool.QueryRow(ctx, `
SELECT wake.ready_at
FROM agent_wakeups wake
JOIN agents agent ON agent.id = wake.agent_id
WHERE agent.project_id = $1 AND wake.agent_id = $2
`, kernelTestProjectID, agentID).Scan(&readyAt); err != nil {
		t.Fatalf("load steering wakeup: %v", err)
	}
	if !readyAt.Before(oldRetryAt) {
		t.Fatalf("steering wakeup ready_at = %s, want before retry deadline %s", readyAt, oldRetryAt)
	}

	currentNow = steeringAt.Add(time.Millisecond)
	steeredWork, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		kernelTestClaimInput(currentNow),
	)
	if err != nil {
		t.Fatalf("claim steering work: %v", err)
	}
	if !found || steeredWork.Kind != executionstore.AgentWorkModel || steeredWork.Model.ModelCallContextID != storage.NilID ||
		len(steeredWork.Model.InputIDs) != 2 || steeredWork.Model.InputIDs[0] != turn.InputIDs[0] ||
		steeredWork.Model.InputIDs[1] != steering.ID ||
		steeredWork.Model.TurnID == turn.TurnID {
		t.Fatalf(
			"steering work = %+v found=%v, want a fresh turn with both unanswered inputs",
			steeredWork,
			found,
		)
	}
	steeredTurn := modelWorkExecutionFromClaimForKernelTest(steeredWork, currentNow)
	if err := executor.ExecuteModelWork(ctx, steeredTurn); err != nil {
		t.Fatalf("execute steering turn: %v", err)
	}
	var oldState executionstore.ModelCallState
	var oldRecoveryKind executionstore.ModelCallRecoveryKind
	if err := fixture.Pool.QueryRow(ctx, `
SELECT state, recovery_kind FROM model_call_contexts
WHERE project_id = $1 AND agent_id = $2 AND id = $3
`, kernelTestProjectID, agentID, oldContextID).Scan(&oldState, &oldRecoveryKind); err != nil {
		t.Fatalf("load original retrying context: %v", err)
	}
	if oldState != executionstore.ModelCallContextFailed || oldRecoveryKind != executionstore.ModelCallRecoveryRetry {
		t.Fatalf(
			"original retrying context = %q/%q, want immutable failed/retry history",
			oldState,
			oldRecoveryKind,
		)
	}
	var freshState executionstore.ModelCallState
	var freshAttemptNumber int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT context.state, context.attempt_number
FROM model_call_contexts context
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.input_event_sequence = (
    SELECT event.sequence
    FROM agent_events event
    JOIN agents agent ON agent.id = event.agent_id
    WHERE agent.project_id = $1
      AND event.agent_id = $2
      AND event.agent_input_id = $3
  )
`, kernelTestProjectID, agentID, steering.ID).Scan(&freshState, &freshAttemptNumber); err != nil {
		t.Fatalf("load steering model attempt: %v", err)
	}
	if freshState != executionstore.ModelCallContextSucceeded || freshAttemptNumber != 1 {
		t.Fatalf(
			"steering context = %s attempt=%d, want succeeded first attempt",
			freshState,
			freshAttemptNumber,
		)
	}
}

func TestAgentExecutorConfigChangeRebuildsRetryingContextAtNewFrontier(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/config-change-during-retry", now)
	turn := fixture.admitContentInputTurn(t, ctx, agentID, userID, "use the active configuration", now.Add(time.Millisecond))
	retryAfterSeconds := int64(3600)
	modelClient := &sequenceKernelModel{
		providerModelSlug: "config-change-during-retry",
		errs: []error{model.ProviderError{
			Kind:    model.ErrorKindRateLimit,
			Source:  "test-provider",
			Code:    "rate_limited",
			Message: "retry later",
			RetryAfter: &model.RetryAfter{
				DeltaSeconds: &retryAfterSeconds,
			},
		}},
		responses: []model.Response{{
			ID:         "resp_after_config_change",
			Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "continued with the new configuration"}},
			StopReason: model.StopReasonEndTurn,
		}},
	}
	currentNow := now.Add(2 * time.Millisecond)
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return currentNow },
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute rate-limited attempt: %v", err)
	}

	var oldContextID storage.ID
	var oldRetryAt time.Time
	if err := fixture.Pool.QueryRow(ctx, `
SELECT context.id, context.retry_at
FROM model_call_contexts context
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.input_event_sequence = $3
  AND context.attempt_number = 1
`, kernelTestProjectID, agentID, turn.OpeningEventSequence).Scan(&oldContextID, &oldRetryAt); err != nil {
		t.Fatalf("load retrying context: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		turn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release retrying turn runtime: %v", err)
	}

	queuedInput, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      kernelTestProjectID,
		AgentID:        agentID,
		Actor:          kernelTestOmnaraActorParams(t, userID),
		ContentBlocks:  mustKernelJSON([]map[string]string{{"type": "text", "text": "wait for the active turn"}}),
		DeliveryMode:   executionstore.DeliveryModeQueued,
		IdempotencyKey: "queued-behind-config-change-retry",
	})
	if err != nil {
		t.Fatalf("create queued input during retry backoff: %v", err)
	}

	var configChangeStartedAt time.Time
	if err := fixture.Pool.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&configChangeStartedAt); err != nil {
		t.Fatalf("read database time before config change: %v", err)
	}
	nextConfig := fixture.kernelAgentConfigInput(
		t,
		ctx,
		"Kernel Config Changed During Retry",
		"config-change-during-retry",
	)
	changed, err := fixture.Store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: nextConfig,
		AgentID:                agentID,
		ActorType:              identitystore.PrincipalTypeSystem,
		Reason:                 "test_config_change_during_retry",
		IdempotencyKey:         "config-change-during-retry",
	})
	if err != nil {
		t.Fatalf("change config during retry backoff: %v", err)
	}
	var configChangeCompletedAt time.Time
	if err := fixture.Pool.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&configChangeCompletedAt); err != nil {
		t.Fatalf("read database time after config change: %v", err)
	}
	var readyAt time.Time
	if err := fixture.Pool.QueryRow(ctx, `
SELECT wake.ready_at
FROM agent_wakeups wake
JOIN agents agent ON agent.id = wake.agent_id
WHERE agent.project_id = $1 AND wake.agent_id = $2
`, kernelTestProjectID, agentID).Scan(&readyAt); err != nil {
		t.Fatalf("load wakeup after config change: %v", err)
	}
	if readyAt.Before(configChangeStartedAt) || readyAt.After(configChangeCompletedAt) || !readyAt.Before(oldRetryAt) {
		t.Fatalf(
			"config-change wakeup ready_at = %s, want within [%s, %s] before old retry %s",
			readyAt,
			configChangeStartedAt,
			configChangeCompletedAt,
			oldRetryAt,
		)
	}

	currentNow = configChangeCompletedAt
	freshWork, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		kernelTestClaimInput(currentNow),
	)
	if err != nil {
		t.Fatalf("claim config-change continuation: %v", err)
	}
	if !found || freshWork.Kind != executionstore.AgentWorkModel || freshWork.Model.ModelCallContextID != storage.NilID ||
		freshWork.Model.TurnID != turn.TurnID || len(freshWork.Model.InputIDs) != 1 || freshWork.Model.InputIDs[0] != turn.InputIDs[0] {
		t.Fatalf("config-change work = %+v found=%v, want a fresh context for the active turn", freshWork, found)
	}

	oldRetry, err := fixture.Store.Execution().ClaimNextModelCallContext(ctx, executionstore.ClaimNextModelCallContextInput{
		ProjectID:                     kernelTestProjectID,
		AgentID:                       agentID,
		PredecessorModelCallContextID: oldContextID,
		RuntimeLockID:                 freshWork.RuntimeLock.ID,
	})
	if err != nil {
		t.Fatalf("probe stale context retry: %v", err)
	}
	if oldRetry.Claimed || oldRetry.Context.AttemptNumber != 1 {
		t.Fatalf("stale context retry = %+v, want the original unclaimed context", oldRetry)
	}

	freshTurn := modelWorkExecutionFromClaimForKernelTest(freshWork, currentNow)
	if err := executor.ExecuteModelWork(ctx, freshTurn); err != nil {
		t.Fatalf("execute config-change continuation: %v", err)
	}
	if modelClient.preparedCount() != 2 {
		t.Fatalf("prepared requests = %d, want failed old context and successful fresh context", modelClient.preparedCount())
	}

	var oldState, newState executionstore.ModelCallState
	var queuedState string
	var oldContextCount, newAttemptNumber int
	var newContextID, newConfigID storage.ID
	if err := fixture.Pool.QueryRow(ctx, `
SELECT predecessor.state, count(context.id)
FROM model_call_contexts predecessor
JOIN model_call_contexts context
  ON context.project_id = predecessor.project_id
 AND context.agent_id = predecessor.agent_id
 AND context.input_event_sequence = predecessor.input_event_sequence
 AND context.operation_kind = predecessor.operation_kind
WHERE predecessor.project_id = $1
  AND predecessor.agent_id = $2
  AND predecessor.id = $3
GROUP BY predecessor.state
`, kernelTestProjectID, agentID, oldContextID).Scan(&oldState, &oldContextCount); err != nil {
		t.Fatalf("load old context after config change: %v", err)
	}
	if err := fixture.Pool.QueryRow(ctx, `
SELECT context.id, context.state, context.agent_config_id, context.attempt_number
FROM model_call_contexts context
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.input_event_sequence = $3
`, kernelTestProjectID, agentID, changed.ConfigChange.Event.Sequence).Scan(
		&newContextID,
		&newState,
		&newConfigID,
		&newAttemptNumber,
	); err != nil {
		t.Fatalf("load fresh context after config change: %v", err)
	}
	if err := fixture.Pool.QueryRow(ctx, `
SELECT state FROM agent_inputs
WHERE project_id = $1 AND agent_id = $2 AND id = $3
`, kernelTestProjectID, agentID, queuedInput.ID).Scan(&queuedState); err != nil {
		t.Fatalf("load queued input after config-change continuation: %v", err)
	}
	if oldState != executionstore.ModelCallContextFailed || oldContextCount != 1 ||
		newContextID == oldContextID || newState != executionstore.ModelCallContextSucceeded ||
		newConfigID != changed.AgentConfig.ID || newAttemptNumber != 1 || queuedState != "received" {
		t.Fatalf(
			"contexts old=%s/%s lineage_rows=%d new=%s/%s config=%s attempt=%d queued=%s",
			oldContextID,
			oldState,
			oldContextCount,
			newContextID,
			newState,
			newConfigID,
			newAttemptNumber,
			queuedState,
		)
	}

	var oldContinuableContexts int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM agent_continuable_model_contexts($1, $2)
WHERE model_call_context_id = $3
`, kernelTestProjectID, agentID, oldContextID).Scan(&oldContinuableContexts); err != nil {
		t.Fatalf("check stale context continuation eligibility: %v", err)
	}
	if oldContinuableContexts != 0 {
		t.Fatalf("stale context remains continuable after a later frontier succeeded")
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		freshTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release config-change continuation runtime: %v", err)
	}
	var queuedReadyAt time.Time
	if err := fixture.Pool.QueryRow(ctx, `
SELECT wake.ready_at
FROM agent_wakeups wake
JOIN agents agent ON agent.id = wake.agent_id
WHERE agent.project_id = $1 AND wake.agent_id = $2
`, kernelTestProjectID, agentID).Scan(&queuedReadyAt); err != nil {
		t.Fatalf("load queued wakeup after config-change continuation: %v", err)
	}
	currentNow = queuedReadyAt.Add(time.Millisecond)
	queuedWork, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		kernelTestClaimInput(currentNow),
	)
	if err != nil {
		t.Fatalf("claim queued input after config-change continuation: %v", err)
	}
	if !found || queuedWork.Kind != executionstore.AgentWorkModel ||
		queuedWork.Model.ModelCallContextID != storage.NilID ||
		queuedWork.Model.TurnID == turn.TurnID ||
		len(queuedWork.Model.InputIDs) != 1 ||
		queuedWork.Model.InputIDs[0] != queuedInput.ID {
		t.Fatalf("queued work = %+v found=%v, want the queued input in a fresh turn", queuedWork, found)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		queuedWork.RuntimeLock.ID,
	); err != nil {
		t.Fatalf("release queued input runtime: %v", err)
	}
}

func TestAgentExecutorQueuedInputWaitsForRetryingTurn(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/queued-behind-retry-model", now)
	turn := fixture.admitContentInputTurn(t, ctx, agentID, userID, "first request", now.Add(time.Millisecond))
	retryAfterSeconds := int64(3600)
	modelClient := &sequenceKernelModel{
		providerModelSlug: "queued-behind-retry-model",
		errs: []error{model.ProviderError{
			Kind:    model.ErrorKindRateLimit,
			Source:  "test-provider",
			Code:    "rate_limited",
			Message: "retry later",
			RetryAfter: &model.RetryAfter{
				DeltaSeconds: &retryAfterSeconds,
			},
		}},
		responses: []model.Response{
			{
				ID:         "resp_after_retry",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "finished the original request"}},
				StopReason: model.StopReasonEndTurn,
			},
			{
				ID:         "resp_after_queued_input",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "handled the queued request"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	currentNow := now.Add(2 * time.Millisecond)
	executor := AgentExecutor{
		Store:           fixture.Store,
		ModelResolver:   liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:    tools.Executor{Store: fixture.Store},
		Now:             func() time.Time { return currentNow },
		ModelRetryDelay: immediateKernelModelRetryDelay,
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute rate-limited attempt: %v", err)
	}
	var oldContextID storage.ID
	var oldRetryAt time.Time
	if err := fixture.Pool.QueryRow(ctx, `
SELECT context.id, context.retry_at
FROM model_call_contexts context
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.input_event_sequence = $3
  AND context.attempt_number = 1
`, kernelTestProjectID, agentID, turn.OpeningEventSequence).Scan(&oldContextID, &oldRetryAt); err != nil {
		t.Fatalf("load retrying context: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		turn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release retrying turn runtime: %v", err)
	}
	newInput, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      kernelTestProjectID,
		AgentID:        agentID,
		Actor:          kernelTestOmnaraActorParams(t, userID),
		ContentBlocks:  mustKernelJSON([]map[string]string{{"type": "text", "text": "newer request"}}),
		DeliveryMode:   executionstore.DeliveryModeQueued,
		IdempotencyKey: "queued-input-waits-for-retry",
	})
	if err != nil {
		t.Fatalf("create queued input during retry backoff: %v", err)
	}
	var readyAt time.Time
	if err := fixture.Pool.QueryRow(ctx, `
SELECT wake.ready_at
FROM agent_wakeups wake
JOIN agents agent ON agent.id = wake.agent_id
WHERE agent.project_id = $1 AND wake.agent_id = $2
`, kernelTestProjectID, agentID).Scan(&readyAt); err != nil {
		t.Fatalf("load wakeup after queued input: %v", err)
	}
	if !readyAt.Equal(oldRetryAt) {
		t.Fatalf("queued input wakeup ready_at = %s, want retry deadline %s", readyAt, oldRetryAt)
	}
	var queuedState string
	if err := fixture.Pool.QueryRow(ctx, `
SELECT state FROM agent_inputs
WHERE project_id = $1 AND agent_id = $2 AND id = $3
`, kernelTestProjectID, agentID, newInput.ID).Scan(&queuedState); err != nil {
		t.Fatalf("load queued input before retry: %v", err)
	}
	if queuedState != "received" {
		t.Fatalf("queued input state = %q, want received", queuedState)
	}

	currentNow = oldRetryAt.Add(time.Millisecond)
	retryWork, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		kernelTestClaimInput(currentNow),
	)
	if err != nil {
		t.Fatalf("claim retry at deadline: %v", err)
	}
	if !found || retryWork.Kind != executionstore.AgentWorkModel || retryWork.Model.ModelCallContextID != oldContextID ||
		retryWork.Model.TurnID != turn.TurnID {
		t.Fatalf("retry work = %+v found=%v, want original turn/context", retryWork, found)
	}
	for _, inputID := range retryWork.Model.InputIDs {
		if inputID == newInput.ID {
			t.Fatalf("retry work admitted queued input %s", newInput.ID)
		}
	}
	retryTurn := modelWorkExecutionFromClaimForKernelTest(retryWork, currentNow)
	if err := executor.ExecuteModelWork(ctx, retryTurn); err != nil {
		t.Fatalf("execute original retry: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		retryTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release completed retry turn runtime: %v", err)
	}

	var queuedReadyAt time.Time
	if err := fixture.Pool.QueryRow(ctx, `
SELECT wake.ready_at
FROM agent_wakeups wake
JOIN agents agent ON agent.id = wake.agent_id
WHERE agent.project_id = $1 AND wake.agent_id = $2
`, kernelTestProjectID, agentID).Scan(&queuedReadyAt); err != nil {
		t.Fatalf("load queued wakeup after retry turn: %v", err)
	}
	currentNow = queuedReadyAt.Add(time.Millisecond)
	queuedWork, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		kernelTestClaimInput(currentNow),
	)
	if err != nil {
		t.Fatalf("claim queued input after retry turn: %v", err)
	}
	if !found || queuedWork.Kind != executionstore.AgentWorkModel || queuedWork.Model.ModelCallContextID != storage.NilID ||
		len(queuedWork.Model.InputIDs) != 1 || queuedWork.Model.InputIDs[0] != newInput.ID || queuedWork.Model.TurnID == turn.TurnID {
		t.Fatalf("queued work = %+v found=%v, want a fresh input turn", queuedWork, found)
	}
	queuedTurn := modelWorkExecutionFromClaimForKernelTest(queuedWork, currentNow)
	if err := executor.ExecuteModelWork(ctx, queuedTurn); err != nil {
		t.Fatalf("execute queued input: %v", err)
	}
	if modelClient.preparedCount() != 3 {
		t.Fatalf("prepared requests = %d, want initial attempt, retry, and queued turn", modelClient.preparedCount())
	}
	var oldState, queuedContextState executionstore.ModelCallState
	var oldContextCount, succeededRetryContexts int
	var newContextID storage.ID
	var newAttemptNumber int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT predecessor.state,
       count(context.id),
       count(context.id) FILTER (WHERE context.state = 'succeeded')
FROM model_call_contexts predecessor
JOIN model_call_contexts context
  ON context.project_id = predecessor.project_id
 AND context.agent_id = predecessor.agent_id
 AND context.input_event_sequence = predecessor.input_event_sequence
 AND context.operation_kind = predecessor.operation_kind
WHERE predecessor.project_id = $1
  AND predecessor.agent_id = $2
  AND predecessor.id = $3
GROUP BY predecessor.state
`, kernelTestProjectID, agentID, oldContextID).Scan(
		&oldState,
		&oldContextCount,
		&succeededRetryContexts,
	); err != nil {
		t.Fatalf("load retried context: %v", err)
	}
	if err := fixture.Pool.QueryRow(ctx, `
SELECT context.id, context.state, context.attempt_number
FROM model_call_contexts context
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.input_event_sequence = $3
`, kernelTestProjectID, agentID, queuedTurn.OpeningEventSequence).Scan(
		&newContextID,
		&queuedContextState,
		&newAttemptNumber,
	); err != nil {
		t.Fatalf("load queued turn model context: %v", err)
	}
	if oldState != executionstore.ModelCallContextFailed || oldContextCount != 2 || succeededRetryContexts != 1 ||
		newContextID == oldContextID ||
		queuedContextState != executionstore.ModelCallContextSucceeded || newAttemptNumber != 1 {
		t.Fatalf(
			"contexts old=%s/%s lineage_rows=%d succeeded_retries=%d queued=%s/%s attempt=%d, want immutable failed predecessor plus succeeded retry, then a fresh succeeded context",
			oldContextID,
			oldState,
			oldContextCount,
			succeededRetryContexts,
			newContextID,
			queuedContextState,
			newAttemptNumber,
		)
	}
}
