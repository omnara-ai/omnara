//go:build integration

package kernel

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/stretchr/testify/require"
)

type progressiveSeedTurn struct {
	input  string
	output string
}

func seedProgressiveHistory(
	t *testing.T, ctx context.Context, turns []progressiveSeedTurn,
) (kernelFixture, uuid.UUID, uuid.UUID) {
	t.Helper()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgent(t, ctx, "openai/kernel-test", fixture.Now)
	for index, turn := range turns {
		response := completeProgressiveSummaryResponse(turn.output)
		response.ID = "progressive-seed-" + strconv.Itoa(index)
		client := &sequenceKernelModel{providerModelSlug: "kernel-test", responses: []model.Response{response}}
		work := fixture.admitContentInputTurn(t, ctx, agentID, userID, turn.input,
			fixture.Now.Add(time.Duration(index*2+1)*time.Second))
		executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
		require.NoError(t, executor.ExecuteModelWork(ctx, work))
		fixture.releaseModelRuntimeLock(t, ctx, work)
	}
	return fixture, agentID, userID
}

func TestAgentExecutorProgressiveCompactionCompletesWithoutReexpandingSource(t *testing.T) {
	ctx := context.Background()
	seedText := "PROGRESSIVE_SEED_HISTORY " + strings.Repeat("durable seed detail ", 120)
	secondSeedText := "SECOND_PROGRESSIVE_SEED " + strings.Repeat("second durable seed detail ", 120)
	fixture, agentID, userID := seedProgressiveHistory(t, ctx, []progressiveSeedTurn{
		{input: seedText, output: "first seed history accepted"},
		{input: secondSeedText, output: "second seed history accepted"},
	})

	const (
		intermediateSummary = "INTERMEDIATE_PROGRESSIVE_CHECKPOINT Preserve the earlier seed request."
		finalSummary        = "FINAL_PROGRESSIVE_CHECKPOINT Continue with the current request using the established seed."
	)
	progressiveModel := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		preparedInputTokenEstimator: func(bundle modelcontext.Bundle) int {
			if isCompactionRequestBundle(bundle) {
				return 500
			}
			if bundle.ContextCheckpoint != nil &&
				(strings.Contains(bundle.ContextCheckpoint.Summary, "FINAL_PROGRESSIVE_CHECKPOINT") ||
					strings.Contains(bundle.ContextCheckpoint.Summary, "[Earlier conversation compacted.]") ||
					strings.Contains(bundle.ContextCheckpoint.Summary, "[Additional closed history compacted.]")) {
				return 500
			}
			return 200_000
		},
		capabilities: model.Capabilities{
			ContextWindowTokens: 128000,
			MaxOutputTokens:     new(256),
		},
		responses: []model.Response{
			{
				ID:         "resp_progressive_truncated",
				Content:    []model.ResponsePart{{Type: "text", Text: "truncated summary"}},
				StopReason: model.StopReasonMaxTokens,
			},
			{
				ID:         "resp_progressive_intermediate",
				Content:    []model.ResponsePart{{Type: "text", Text: intermediateSummary}},
				StopReason: model.StopReasonEndTurn,
			},
			{
				ID:         "resp_progressive_final_summary",
				Content:    []model.ResponsePart{{Type: "text", Text: finalSummary}},
				StopReason: model.StopReasonEndTurn,
			},
			{
				ID: "resp_after_progressive_compaction",
				Content: []model.ResponsePart{
					{Type: "text", Text: "continued after progressive compaction"},
				},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	turn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"CURRENT_PROGRESSIVE_REQUEST "+strings.Repeat("current request detail ", 80),
		fixture.Now.Add(5*time.Second),
	)
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, progressiveModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(6 * time.Second) },
	}
	work := turn
	for lease, expectedCalls := range []int{2, 3, 4} {
		if lease > 0 {
			work = continueTurnOnNewLeaseForKernelTest(
				t, ctx, fixture, work, fixture.Now.Add(time.Duration(6+lease)*time.Second),
			)
		}
		require.NoError(t, executor.ExecuteModelWork(ctx, work))
		require.Equal(t, expectedCalls, progressiveModel.respondedCount(), "lease %d", lease)
	}
	for index, request := range progressiveModel.responded {
		require.Equal(t, index < 3, isCompactionRequestBundle(request.Bundle), "request %d", index)
	}

	rows, err := fixture.Pool.Query(ctx, `
		SELECT checkpoint.id
		FROM context_checkpoints checkpoint
		JOIN agent_events event
		  ON event.agent_id = checkpoint.agent_id
		 AND event.context_checkpoint_id = checkpoint.id
		JOIN agents agent ON agent.id = checkpoint.agent_id
		WHERE agent.project_id = $1 AND checkpoint.agent_id = $2
		ORDER BY event.sequence`, kernelTestProjectID, agentID)
	if err != nil {
		t.Fatalf("list progressive checkpoints: %v", err)
	}
	defer rows.Close()
	var checkpointIDs []uuid.UUID
	for rows.Next() {
		var checkpointID uuid.UUID
		if err := rows.Scan(&checkpointID); err != nil {
			t.Fatalf("scan progressive checkpoint id: %v", err)
		}
		checkpointIDs = append(checkpointIDs, checkpointID)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate progressive checkpoints: %v", err)
	}
	if len(checkpointIDs) != 2 {
		t.Fatalf("progressive checkpoints = %v, want two", checkpointIDs)
	}
	firstCheckpoint, found, err := fixture.Store.Execution().GetContextCheckpoint(
		ctx,
		kernelTestProjectID,
		agentID,
		checkpointIDs[0],
	)
	if err != nil || !found {
		t.Fatalf("load first progressive checkpoint: found=%v err=%v", found, err)
	}
	finalCheckpoint, found, err := fixture.Store.Execution().GetContextCheckpoint(
		ctx,
		kernelTestProjectID,
		agentID,
		checkpointIDs[1],
	)
	if err != nil || !found {
		t.Fatalf("load final progressive checkpoint: found=%v err=%v", found, err)
	}
	if firstCheckpoint.SummarizedThroughEventSequence != 2 ||
		firstCheckpoint.Summary != intermediateSummary {
		t.Fatalf("first progressive checkpoint = %+v", firstCheckpoint)
	}
	if finalCheckpoint.SummarizedThroughEventSequence != 3 ||
		finalCheckpoint.Summary != finalSummary {
		t.Fatalf("final progressive checkpoint = %+v", finalCheckpoint)
	}

	var reexpandedContexts, compactedParents int
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM model_call_contexts
		WHERE project_id = $1
		  AND agent_id = $2
		  AND operation_kind = 'compaction'
		  AND input_event_sequence >= $3
		  AND source_event_sequence_end <= $4`,
		kernelTestProjectID,
		agentID,
		firstCheckpoint.CheckpointEventSequence,
		firstCheckpoint.SummarizedThroughEventSequence,
	).Scan(&reexpandedContexts); err != nil {
		t.Fatalf("count reexpanded progressive source contexts: %v", err)
	}
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM model_call_contexts
		WHERE project_id = $1
		  AND agent_id = $2
		  AND operation_kind = 'normal'
		  AND state = 'failed'
		  AND recovery_kind = 'compact'`, kernelTestProjectID, agentID).Scan(&compactedParents); err != nil {
		t.Fatalf("count progressive parent contexts: %v", err)
	}
	if reexpandedContexts != 0 || compactedParents != 2 {
		t.Fatalf(
			"reexpanded source contexts / compacted parents = %d/%d, want 0/2",
			reexpandedContexts,
			compactedParents,
		)
	}
	finalRequest := string(progressiveModel.responded[3].ProviderRequest)
	if !strings.Contains(finalRequest, finalSummary) ||
		!strings.Contains(finalRequest, "CURRENT_PROGRESSIVE_REQUEST") ||
		!strings.Contains(finalRequest, secondSeedText) ||
		strings.Contains(finalRequest, seedText) {
		t.Fatalf("final progressive request did not preserve the recent raw turn: %s", finalRequest)
	}
	var finalOutputs int
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM agent_events event
		JOIN agents agent ON agent.id = event.agent_id
		JOIN content_blocks block
		  ON block.agent_id = event.agent_id
		 AND block.owner_model_output_id = event.model_output_id
		WHERE agent.project_id = $1
		  AND event.agent_id = $2
		  AND event.turn_id = $3
		  AND block.block_kind = 'text'
		  AND block.text_content = 'continued after progressive compaction'`,
		kernelTestProjectID,
		agentID,
		turn.TurnID,
	).Scan(&finalOutputs); err != nil {
		t.Fatalf("count progressive final outputs: %v", err)
	}
	if finalOutputs != 1 {
		t.Fatalf("progressive final output count = %d, want 1", finalOutputs)
	}
}

func TestProgressiveCompactionAdvancesBeyondThreeCheckpointsBeforeNormalWork(t *testing.T) {
	ctx := context.Background()
	var seeds []progressiveSeedTurn
	for index := range 4 {
		seeds = append(seeds, progressiveSeedTurn{
			input:  "seed input " + strings.Repeat("progressive exhaustion history ", 80) + strconv.Itoa(index+1),
			output: "seed output " + strings.Repeat("completed detail ", 80),
		})
	}
	fixture, agentID, userID := seedProgressiveHistory(t, ctx, seeds)

	turn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"current request after progressive exhaustion seeds",
		fixture.Now.Add(20*time.Second),
	)
	watermark, err := fixture.Store.Execution().MaxEventSequence(ctx, kernelTestProjectID, agentID)
	if err != nil {
		t.Fatalf("load progressive exhaustion watermark: %v", err)
	}
	if watermark != turn.OpeningEventSequence || watermark != 10 {
		t.Fatalf(
			"progressive exhaustion watermark/opening = %d/%d, want 10",
			watermark,
			turn.OpeningEventSequence,
		)
	}

	client := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		preparedInputTokenEstimator: func(bundle modelcontext.Bundle) int {
			if isCompactionRequestBundle(bundle) && strings.Count(string(bundle.Messages[0].Content), "Event ") <= 2 {
				return 500
			}
			if bundle.ContextCheckpoint != nil && bundle.ContextCheckpoint.Summary == "step four summary" {
				return 500
			}
			return 200_000
		},
		capabilities: model.Capabilities{ContextWindowTokens: 128_000, MaxOutputTokens: new(256)},
		responses: []model.Response{
			completeProgressiveSummaryResponse("step one summary"),
			completeProgressiveSummaryResponse("step two summary"),
			completeProgressiveSummaryResponse("step three summary"),
			completeProgressiveSummaryResponse("step four summary"),
			completeProgressiveSummaryResponse("Continued after four advancing checkpoints."),
		},
	}
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, client),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
	}
	work := turn
	for lease := range 5 {
		if lease > 0 {
			work = continueTurnOnNewLeaseForKernelTest(
				t, ctx, fixture, work, fixture.Now.Add(time.Duration(21+lease)*time.Second),
			)
		}
		require.NoError(t, executor.ExecuteModelWork(ctx, work))
		require.Equal(t, lease+1, client.respondedCount(), "lease %d", lease)
	}
	for index, request := range client.responded {
		require.Equal(t, index < 4, isCompactionRequestBundle(request.Bundle), "request %d", index)
	}
	rows, err := fixture.Pool.Query(ctx, `SELECT summarized_through_event_sequence
        FROM context_checkpoints WHERE agent_id=$1 ORDER BY summarized_through_event_sequence`, agentID)
	require.NoError(t, err)
	defer rows.Close()
	var ends []int64
	for rows.Next() {
		var end int64
		require.NoError(t, rows.Scan(&end))
		ends = append(ends, end)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []int64{3, 5, 7, 9}, ends)
	require.Contains(t, string(client.responded[4].ProviderRequest), "current request after progressive exhaustion seeds")
	require.Contains(t, string(client.responded[4].ProviderRequest), "step four summary")
	assertNoTerminalContextErrors(t, ctx, fixture, agentID)
	require.Zero(t, pendingModelWork(t, ctx, fixture, agentID))
}
