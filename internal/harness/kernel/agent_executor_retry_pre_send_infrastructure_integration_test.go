//go:build integration

package kernel

import (
	"context"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestAgentExecutorStopsNinthConsecutivePreSendFailure(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/pre-send-exhaustion-model", now)
	turn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"exercise pre-send exhaustion",
		now.Add(time.Millisecond),
	)
	openingEventSequence := turn.OpeningEventSequence
	currentNow := now.Add(2 * time.Millisecond)
	executor := AgentExecutor{
		Store: fixture.Store,
		ModelResolver: providerErrorTestResolver{err: model.ProviderError{
			Kind:    model.ErrorKindTransient,
			Source:  "test-resolver",
			Code:    "resolver_temporarily_unavailable",
			Message: "the model resolver is temporarily unavailable",
		}},
		ToolExecutor:    tools.Executor{Store: fixture.Store},
		Now:             func() time.Time { return currentNow },
		ModelRetryDelay: immediateKernelModelRetryDelay,
	}
	var operationFrontier int64
	maxAttempts := executionstore.MaxModelCallRetriesPerOperation + 1
	for attemptNumber := 1; attemptNumber <= maxAttempts; attemptNumber++ {
		if err := executor.ExecuteModelWork(ctx, turn); err != nil {
			t.Fatalf("execute pre-send attempt %d: %v", attemptNumber, err)
		}
		if attemptNumber == maxAttempts {
			break
		}
		if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
			ctx,
			kernelTestProjectID,
			agentID,
			turn.RuntimeLockID,
		); err != nil {
			t.Fatalf("release runtime after pre-send attempt %d: %v", attemptNumber, err)
		}
		currentNow = currentNow.Add(time.Minute)
		claim, found, err := fixture.Store.Execution().ClaimNextAgentWork(
			ctx,
			kernelTestClaimInput(currentNow),
		)
		if err != nil {
			t.Fatalf("claim pre-send attempt %d: %v", attemptNumber+1, err)
		}
		if !found || claim.Kind != executionstore.AgentWorkModel || claim.Model.ModelCallContextID == storage.NilID {
			t.Fatalf("pre-send attempt %d claim = %+v found=%v", attemptNumber+1, claim, found)
		}
		predecessor, predecessorFound, loadErr := fixture.Store.Execution().GetModelCallContext(
			ctx,
			kernelTestProjectID,
			agentID,
			claim.Model.ModelCallContextID,
		)
		if loadErr != nil || !predecessorFound {
			t.Fatalf(
				"load pre-send attempt %d predecessor: found=%v err=%v",
				attemptNumber+1,
				predecessorFound,
				loadErr,
			)
		}
		if operationFrontier == 0 {
			operationFrontier = predecessor.InputEventSequence
		} else if predecessor.InputEventSequence != operationFrontier {
			t.Fatalf(
				"pre-send attempt %d frontier = %d, want %d",
				attemptNumber+1,
				predecessor.InputEventSequence,
				operationFrontier,
			)
		}
		turn = modelWorkExecutionFromClaimForKernelTest(claim, currentNow)
	}

	var (
		terminalContextState executionstore.ModelCallState
		operationFrontiers   int
		attempts             int
		retryingAttempts     int
		stoppedAttempts      int
		outputs              int
		stopReason           string
		terminalCode         string
		terminalRetries      int
	)
	if err := fixture.Pool.QueryRow(ctx, `
SELECT coalesce(max(context.state) FILTER (
         WHERE context.state = 'failed' AND context.recovery_kind IS NULL
       ), ''),
       count(DISTINCT context.input_event_sequence)::integer,
       count(*)::integer,
       count(*) FILTER (WHERE context.recovery_kind = 'retry')::integer,
	       count(*) FILTER (
	         WHERE context.state = 'failed' AND context.recovery_kind IS NULL
	       )::integer,
	       count(DISTINCT output.id)::integer,
       coalesce(max(output.stop_reason), ''),
	       coalesce(max(context.error_code) FILTER (
	         WHERE context.state = 'failed' AND context.recovery_kind IS NULL
	       ), ''),
	       count(*) FILTER (
	         WHERE context.state = 'failed'
           AND context.recovery_kind IS NULL
           AND context.retry_at IS NOT NULL
       )::integer
FROM model_call_contexts context
LEFT JOIN model_outputs output
	  ON output.agent_id = context.agent_id
 AND output.model_call_context_id = context.id
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.operation_kind = 'normal'
  AND context.input_event_sequence = $3
`, kernelTestProjectID, agentID, openingEventSequence).Scan(
		&terminalContextState,
		&operationFrontiers,
		&attempts,
		&retryingAttempts,
		&stoppedAttempts,
		&outputs,
		&stopReason,
		&terminalCode,
		&terminalRetries,
	); err != nil {
		t.Fatalf("load exhausted pre-send outcome: %v", err)
	}
	if terminalContextState != executionstore.ModelCallContextFailed || operationFrontiers != 1 || attempts != maxAttempts ||
		retryingAttempts != executionstore.MaxModelCallRetriesPerOperation ||
		stoppedAttempts != 1 || outputs != 1 ||
		stopReason != "error" ||
		terminalCode != "resolver_temporarily_unavailable" || terminalRetries != 0 {
		t.Fatalf(
			"pre-send terminal outcome = state %s frontiers=%d attempts=%d retry=%d stop=%d outputs=%d stop_reason=%q code=%q terminal_retries=%d",
			terminalContextState,
			operationFrontiers,
			attempts,
			retryingAttempts,
			stoppedAttempts,
			outputs,
			stopReason,
			terminalCode,
			terminalRetries,
		)
	}
	var wakeups int
	if err := fixture.Pool.QueryRow(
		ctx,
		`SELECT count(*) FROM agent_wakeups wake JOIN agents agent ON agent.id = wake.agent_id WHERE agent.project_id = $1 AND wake.agent_id = $2`,
		kernelTestProjectID,
		agentID,
	).Scan(&wakeups); err != nil {
		t.Fatalf("count wakeups after pre-send exhaustion: %v", err)
	}
	if wakeups != 0 {
		t.Fatalf("wakeups after pre-send exhaustion = %d, want 0", wakeups)
	}
}
