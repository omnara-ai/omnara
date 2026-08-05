//go:build integration

package kernel

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/compaction"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
)

func TestAgentExecutorClampsPreferredRawTailToSerializedModelBudget(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/retention-clamp-model", now)

	seedModel := &sequenceKernelModel{
		providerModelSlug: "retention-clamp-model",
		responses: []model.Response{
			{
				ID:         "resp-retention-old",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "older turn completed"}},
				StopReason: model.StopReasonEndTurn,
			},
			{
				ID: "resp-retention-recent",
				Content: []model.ResponsePart{{
					Type: model.ResponsePartTypeText,
					Text: "RECENT_COMPLETED_OUTPUT " + strings.Repeat("recent result ", 80),
				}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	seedNow := now.Add(2 * time.Second)
	seedExecutor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, seedModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return seedNow },
	}
	oldTurn := fixture.admitContentInputTurn(t, ctx, agentID, userID, "older completed turn", now.Add(time.Second))
	if err := seedExecutor.ExecuteModelWork(ctx, oldTurn); err != nil {
		t.Fatalf("execute older turn: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx, kernelTestProjectID, agentID, oldTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release older turn runtime: %v", err)
	}

	recentInput := "RECENT_COMPLETED_INPUT " + strings.Repeat("recent detail ", 120)
	recentTurn := fixture.admitContentInputTurn(t, ctx, agentID, userID, recentInput, now.Add(3*time.Second))
	seedNow = now.Add(3500 * time.Millisecond)
	if err := seedExecutor.ExecuteModelWork(ctx, recentTurn); err != nil {
		t.Fatalf("execute recent completed turn: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx, kernelTestProjectID, agentID, recentTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release recent turn runtime: %v", err)
	}

	currentInput := "CURRENT_UNANSWERED_INPUT " + strings.Repeat("current detail ", 30)
	turn := fixture.admitContentInputTurn(t, ctx, agentID, userID, currentInput, now.Add(4*time.Second))
	capabilities := model.Capabilities{
		ContextWindowTokens:    10_000,
		MaxOutputTokens:        1_024,
		DefaultMaxOutputTokens: 1_024,
	}
	preferredTailTokens := compaction.RecentTailTargetTokens(model.UsableInputTokensForRequest(
		capabilities,
		model.RequestPolicyFromCapabilities(capabilities),
	))
	events, err := loadContextEventsForCompaction(
		ctx,
		fixture.Store.Execution(),
		kernelTestProjectID,
		agentID,
		0,
		turn.OpeningEventSequence,
	)
	if err != nil {
		t.Fatalf("load retention-clamp events: %v", err)
	}
	preferred := retainFromForRecentEvents(events, preferredTailTokens)
	preferred, ok, err := compaction.SelectRetainFromEventSequence(compaction.RetainBoundaryInput{
		SourceEventSequenceStart:  1,
		DesiredRetainFromSequence: preferred,
		DesiredRetainTokens:       preferredTailTokens,
		MaximumRetainFromSequence: turn.OpeningEventSequence,
		Events:                    events,
	})
	if err != nil || !ok || preferred >= turn.OpeningEventSequence {
		t.Fatalf(
			"test setup preferred retain boundary = %d ok=%t err=%v, want history retained before opening %d",
			preferred,
			ok,
			err,
			turn.OpeningEventSequence,
		)
	}

	modelClient := &sequenceKernelModel{
		providerModelSlug: "retention-clamp-model",
		capabilities:      capabilities,
		preparedInputTokenEstimator: func(bundle modelcontext.Bundle) int {
			if isCompactionRequestBundle(bundle) ||
				(bundle.ContextCheckpoint != nil &&
					bundle.ContextCheckpoint.SummarizedThroughEventSequence >= turn.OpeningEventSequence-1) {
				return 500
			}
			return 9_000
		},
		responses: []model.Response{
			{
				ID:         "resp-retention-summary",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "The completed turns were compacted."}},
				StopReason: model.StopReasonEndTurn,
			},
			{
				ID:         "resp-retention-final",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "continued after retention clamp"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	currentNow := now.Add(5 * time.Second)
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return currentNow },
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute retention-clamp compaction: %v", err)
	}
	retry := continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, turn, now.Add(6*time.Second))
	currentNow = retry.Now
	if err := executor.ExecuteModelWork(ctx, retry); err != nil {
		t.Fatalf("execute retention-clamp retry: %v", err)
	}
	if modelClient.respondedCount() != 2 {
		t.Fatalf("retention-clamp provider calls = %d, want 2", modelClient.respondedCount())
	}
	summaryRequest := string(modelClient.responded[0].ProviderRequest)
	if !strings.Contains(summaryRequest, "RECENT_COMPLETED_INPUT") ||
		!strings.Contains(summaryRequest, "RECENT_COMPLETED_OUTPUT") {
		t.Fatalf("clamped summary request omitted completed recent turn: %s", summaryRequest)
	}
	finalRequest := string(modelClient.responded[1].ProviderRequest)
	if !strings.Contains(finalRequest, "The completed turns were compacted.") ||
		!strings.Contains(finalRequest, "CURRENT_UNANSWERED_INPUT") ||
		strings.Contains(finalRequest, "RECENT_COMPLETED_INPUT") {
		t.Fatalf("post-compaction request did not contain summary plus unanswered input: %s", finalRequest)
	}

	var summarizedThrough int64
	if err := fixture.Pool.QueryRow(ctx, `
SELECT checkpoint.summarized_through_event_sequence
FROM context_checkpoints checkpoint
JOIN agents agent ON agent.id = checkpoint.agent_id
WHERE agent.project_id = $1 AND checkpoint.agent_id = $2
ORDER BY checkpoint.summarized_through_event_sequence DESC
LIMIT 1
`, kernelTestProjectID, agentID).Scan(&summarizedThrough); err != nil {
		t.Fatalf("load retention-clamp checkpoint: %v", err)
	}
	if summarizedThrough != turn.OpeningEventSequence-1 {
		t.Fatalf(
			"checkpoint summarized through %d, want all completed history through %d",
			summarizedThrough,
			turn.OpeningEventSequence-1,
		)
	}
}
