//go:build integration

package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/harness/kernel"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/storage"
)

func TestWorkerReplacementHonorsDurableProviderRetryDeadline(t *testing.T) {
	ctx := context.Background()
	pool := openWorkerIntegrationDB(t, ctx)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(workerTestKeyWrapper(t)))
	testStartedAt := time.Now().UTC()
	seededAt := testStartedAt.Add(-time.Minute)
	agentID, userID := createWorkerAgentWithUser(t, ctx, store, seededAt)
	createWorkerInput(
		t,
		ctx,
		store,
		agentID,
		userID,
		"retry across worker replacement",
		seededAt.Add(time.Second),
	)
	retryAfterSeconds := int64(1)
	modelClient := &retryThenSuccessModelClient{
		sequenceModelClient: &sequenceModelClient{
			providerModelSlug: "worker-kernel-test",
			responses: []model.Response{{
				ID:         "resp_worker_retry_recovered",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "replacement worker completed"}},
				StopReason: model.StopReasonEndTurn,
			}},
		},
		retryError: model.ProviderError{
			Kind:    model.ErrorKindRateLimit,
			Source:  "test-provider",
			Code:    "rate_limited",
			Message: "retry later",
			RetryAfter: &model.RetryAfter{
				DeltaSeconds: &retryAfterSeconds,
			},
		},
	}
	executor := kernel.AgentExecutor{
		Store:          store,
		ContextBuilder: modelcontext.Builder{Store: modelcontext.NewStore(store.Execution(), store.Artifacts(), store.Integrations())},
		ModelResolver:  liveWorkerTestModelResolver(store, modelClient),
	}
	firstWorker := NewWorker(store.Execution(), executor, Options{
		RuntimeLockLeaseDuration: 15 * time.Second,
		Capacity:                 1,
	})
	worked, err := firstWorker.RunOnce(ctx)
	if err != nil || !worked {
		t.Fatalf("first worker retryable attempt worked=%v err=%v", worked, err)
	}
	if modelClient.calls != 1 {
		t.Fatalf("provider calls after first worker = %d, want 1", modelClient.calls)
	}
	waitNoRuntimeLock(t, ctx, pool, agentID)

	var retryAt time.Time
	var storedRetryAfter int64
	if err := pool.QueryRow(ctx, `
SELECT context.retry_at,
       (context.error_details #>> '{retry_after,delta_seconds}')::bigint
FROM model_call_contexts context
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.attempt_number = 1
  AND context.state = 'failed'
  AND context.recovery_kind = 'retry'
`, workerTestProjectID, agentID).Scan(&retryAt, &storedRetryAfter); err != nil {
		t.Fatalf("load durable provider retry: %v", err)
	}
	if storedRetryAfter != retryAfterSeconds || !retryAt.After(testStartedAt) {
		t.Fatalf(
			"durable retry_at=%s retry_after=%d, want a future deadline with provider hint %d",
			retryAt,
			storedRetryAfter,
			retryAfterSeconds,
		)
	}

	replacementWorker := NewWorker(store.Execution(), executor, Options{
		RuntimeLockLeaseDuration: 15 * time.Second,
		Capacity:                 1,
	})
	worked, err = replacementWorker.RunOnce(ctx)
	if err != nil || worked {
		t.Fatalf("replacement worker before retry deadline worked=%v err=%v", worked, err)
	}
	if modelClient.calls != 1 {
		t.Fatalf("provider calls before retry deadline = %d, want 1", modelClient.calls)
	}

	requireWorkerClaim(t, ctx, replacementWorker, "replacement worker after retry deadline")
	if modelClient.calls != 2 {
		t.Fatalf("provider calls after replacement = %d, want 2", modelClient.calls)
	}
	waitNoRuntimeLock(t, ctx, pool, agentID)
	assertAssistantEventRecorded(t, ctx, pool, agentID, "replacement worker completed")

	var contexts, operationFrontiers, firstAttempt, lastAttempt int
	if err := pool.QueryRow(ctx, `
SELECT
	count(*),
	count(DISTINCT context.input_event_sequence),
	min(context.attempt_number),
	max(context.attempt_number)
FROM model_call_contexts context
WHERE context.project_id = $1
  AND context.agent_id = $2
`, workerTestProjectID, agentID).Scan(
		&contexts,
		&operationFrontiers,
		&firstAttempt,
		&lastAttempt,
	); err != nil {
		t.Fatalf("load replacement-worker model lineage: %v", err)
	}
	if contexts != 2 || operationFrontiers != 1 || firstAttempt != 1 || lastAttempt != 2 {
		t.Fatalf(
			"model lineage contexts=%d frontiers=%d attempts=%d..%d, want two context rows at one frontier numbered 1..2",
			contexts,
			operationFrontiers,
			firstAttempt,
			lastAttempt,
		)
	}
}

type retryThenSuccessModelClient struct {
	*sequenceModelClient
	retryError error
}

func (c *retryThenSuccessModelClient) Respond(
	_ context.Context,
	_ model.Request,
) (model.Response, error) {
	c.calls++
	switch c.calls {
	case 1:
		return model.Response{}, c.retryError
	case 2:
		return c.responses[0], nil
	default:
		return model.Response{}, errors.New("unexpected extra model call")
	}
}
