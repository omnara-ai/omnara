//go:build integration

package kernel

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestAgentExecutorRetriesDatabaseUnsafeSuccessfulResponse(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/malformed-success-model", now)
	turn := fixture.admitContentInputTurn(t, ctx, agentID, userID, "hello", now.Add(time.Millisecond))
	modelClient := &sequenceKernelModel{
		providerModelSlug: "malformed-success-model",
		responses: []model.Response{{
			ID:         "resp-malformed",
			Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "unsafe\x00text"}},
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
		t.Fatalf("execute malformed response: %v", err)
	}

	var state executionstore.ModelCallState
	var recoveryKind executionstore.ModelCallRecoveryKind
	var errorCode, errorMessage string
	var errorDetails []byte
	if err := fixture.Pool.QueryRow(ctx, `
SELECT context.state, context.recovery_kind, context.error_code, context.error_message, context.error_details
FROM model_call_contexts context
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.input_event_sequence = $3
`, kernelTestProjectID, agentID, turn.OpeningEventSequence).Scan(
		&state,
		&recoveryKind,
		&errorCode,
		&errorMessage,
		&errorDetails,
	); err != nil {
		t.Fatalf("load malformed response attempt: %v", err)
	}
	if state != executionstore.ModelCallContextFailed || recoveryKind != executionstore.ModelCallRecoveryRetry ||
		errorCode != "malformed_success_response" || strings.Contains(errorMessage, "\x00") ||
		!kernelModelCallOutcomeAmbiguous(t, errorDetails) {
		t.Fatalf(
			"malformed response attempt = %q/%q/%q message=%q",
			state,
			recoveryKind,
			errorCode,
			errorMessage,
		)
	}
	var outputs int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM agent_events event
JOIN agents agent ON agent.id = event.agent_id
WHERE agent.project_id = $1
  AND event.agent_id = $2
  AND event.event_kind = 'model_output'
`, kernelTestProjectID, agentID).Scan(&outputs); err != nil {
		t.Fatalf("count malformed response outputs: %v", err)
	}
	if outputs != 0 {
		t.Fatalf("malformed response created %d model outputs", outputs)
	}
}

func TestAgentExecutorRetriesInterruptedModelContext(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgent(t, ctx, "openai/kernel-test", fixture.Now)
	turn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"recover interrupted model call",
		fixture.Now.Add(time.Second),
	)
	watermark, err := fixture.Store.Execution().MaxEventSequence(ctx, kernelTestProjectID, agentID)
	if err != nil {
		t.Fatalf("max event sequence: %v", err)
	}
	snapshot, err := fixture.Store.Execution().CaptureAgentConfigForEventWatermark(ctx, kernelTestProjectID, agentID, watermark)
	if err != nil {
		t.Fatalf("capture config snapshot: %v", err)
	}
	modelClaim, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID:          kernelTestProjectID,
		AgentID:            agentID,
		RuntimeLockID:      turn.RuntimeLockID,
		OpeningInputIDs:    turn.InputIDs,
		AgentConfigID:      snapshot.AgentConfig.ID,
		InputEventSequence: watermark,
	})
	if err != nil {
		t.Fatalf("claim initial model context: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		turn.RuntimeLockID,
	); err != nil {
		t.Fatalf("interrupt first model attempt: %v", err)
	}
	interrupted, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		kernelTestProjectID,
		agentID,
		modelClaim.Context.ID,
	)
	if err != nil || !found || interrupted.State != executionstore.ModelCallContextFailed ||
		interrupted.RecoveryKind != executionstore.ModelCallRecoveryRetry || interrupted.RetryAt == nil ||
		!kernelModelCallOutcomeAmbiguous(t, interrupted.ErrorDetails) {
		t.Fatalf("load interrupted model retry: attempt=%+v found=%v err=%v", interrupted, found, err)
	}
	retryAt := interrupted.RetryAt.Add(time.Second)
	retryWork, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		kernelTestClaimInput(retryAt),
	)
	if err != nil {
		t.Fatalf("claim interrupted model retry: %v", err)
	}
	if !found || retryWork.Kind != executionstore.AgentWorkModel ||
		retryWork.Model.ModelCallContextID != modelClaim.Context.ID {
		t.Fatalf(
			"interrupted retry work = %+v found=%v, want context %s",
			retryWork,
			found,
			modelClaim.Context.ID,
		)
	}
	retryTurn := modelWorkExecutionFromClaimForKernelTest(retryWork, retryAt)
	modelClient := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		responses: []model.Response{{
			ID:         "resp_superseded_retry",
			Content:    []model.ResponsePart{{Type: "text", Text: "rebuilt request response"}},
			StopReason: model.StopReasonEndTurn,
		}},
	}
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return retryAt },
	}
	if err := executor.ExecuteModelWork(ctx, retryTurn); err != nil {
		t.Fatalf("execute interrupted model retry: %v", err)
	}
	if modelClient.preparedCount() != 1 {
		t.Fatalf("model prepared %d requests, want one request on the new lease", modelClient.preparedCount())
	}
	var recoveredContextID storage.ID
	if err := fixture.Pool.QueryRow(ctx, `
SELECT id
FROM model_call_contexts
WHERE project_id = $1
  AND agent_id = $2
  AND input_event_sequence = $3
  AND operation_kind = 'normal'
ORDER BY attempt_number DESC
LIMIT 1
`, kernelTestProjectID, agentID, modelClaim.Context.InputEventSequence).Scan(&recoveredContextID); err != nil {
		t.Fatalf("load recovered context id: %v", err)
	}
	recovered, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		kernelTestProjectID,
		agentID,
		recoveredContextID,
	)
	if err != nil || !found {
		t.Fatalf("load recovered context: err=%v found=%v", err, found)
	}
	if recovered.State != executionstore.ModelCallContextSucceeded {
		t.Fatalf("recovered context state = %q, want succeeded", recovered.State)
	}
	var interruptedAttempts, succeededAttempts int
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT count(*) FILTER (
		         WHERE state = 'failed'
		           AND recovery_kind = 'retry'
		           AND error_code = 'runtime_released_before_model_result_acceptance'
		       ),
		       count(*) FILTER (WHERE state = 'succeeded')
		FROM model_call_contexts
		WHERE project_id = $1
		  AND agent_id = $2
		  AND input_event_sequence = $3
		  AND operation_kind = 'normal'
	`, kernelTestProjectID, agentID, modelClaim.Context.InputEventSequence).Scan(&interruptedAttempts, &succeededAttempts); err != nil {
		t.Fatalf("count interrupted context attempts: %v", err)
	}
	if interruptedAttempts != 1 || succeededAttempts != 1 {
		t.Fatalf(
			"interrupted/succeeded attempts = %d/%d, want 1/1",
			interruptedAttempts,
			succeededAttempts,
		)
	}
	var finalOutputs int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM agent_events event
JOIN content_blocks block
  ON block.agent_id = event.agent_id
 AND block.owner_model_output_id = event.model_output_id
JOIN agents agent ON agent.id = event.agent_id
WHERE agent.project_id = $1
  AND event.agent_id = $2
  AND event.turn_id = $3
  AND event.event_kind = 'model_output'
  AND block.block_kind = 'text'
  AND block.text_content = 'rebuilt request response'
`, kernelTestProjectID, agentID, retryTurn.TurnID).Scan(&finalOutputs); err != nil {
		t.Fatalf("count rebuilt final output: %v", err)
	}
	if finalOutputs != 1 {
		t.Fatalf("rebuilt final outputs = %d, want 1", finalOutputs)
	}
}
