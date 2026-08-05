//go:build integration

package kernel

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/compaction"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestAgentExecutorProgressiveCompactionCompletesWithoutReexpandingSource(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgent(t, ctx, "openai/kernel-test", fixture.Now)

	seedText := "PROGRESSIVE_SEED_HISTORY " + strings.Repeat("durable seed detail ", 120)
	seedModel := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		responses: []model.Response{
			{
				ID:         "resp_progressive_seed_one",
				Content:    []model.ResponsePart{{Type: "text", Text: "first seed history accepted"}},
				StopReason: model.StopReasonEndTurn,
			},
			{
				ID:         "resp_progressive_seed_two",
				Content:    []model.ResponsePart{{Type: "text", Text: "second seed history accepted"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	seedTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		seedText,
		fixture.Now.Add(time.Second),
	)
	seedExecutor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, seedModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(2 * time.Second) },
	}
	if err := seedExecutor.ExecuteModelWork(ctx, seedTurn); err != nil {
		t.Fatalf("execute progressive seed turn: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		seedTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release progressive seed runtime: %v", err)
	}
	secondSeedText := "SECOND_PROGRESSIVE_SEED " + strings.Repeat("second durable seed detail ", 120)
	secondSeedTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		secondSeedText,
		fixture.Now.Add(3*time.Second),
	)
	seedExecutor.Now = func() time.Time { return fixture.Now.Add(4 * time.Second) }
	if err := seedExecutor.ExecuteModelWork(ctx, secondSeedTurn); err != nil {
		t.Fatalf("execute second progressive seed turn: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		secondSeedTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release second progressive seed runtime: %v", err)
	}

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
			MaxOutputTokens:     256,
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
				ID:         "resp_after_progressive_compaction",
				Content:    []model.ResponsePart{{Type: "text", Text: "continued after progressive compaction"}},
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
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute first progressive compaction lease: %v", err)
	}
	if progressiveModel.respondedCount() != 2 {
		t.Fatalf(
			"first progressive lease prepared %d requests, want truncated and smaller compaction calls",
			progressiveModel.respondedCount(),
		)
	}

	secondLease := continueTurnOnNewLeaseForKernelTest(
		t,
		ctx,
		fixture,
		turn,
		fixture.Now.Add(7*time.Second),
	)
	if err := executor.ExecuteModelWork(ctx, secondLease); err != nil {
		t.Fatalf("execute second progressive compaction lease: %v", err)
	}
	if progressiveModel.respondedCount() != 3 {
		t.Fatalf(
			"second progressive lease prepared %d requests, want final compaction call",
			progressiveModel.respondedCount(),
		)
	}

	finalLease := continueTurnOnNewLeaseForKernelTest(
		t,
		ctx,
		fixture,
		secondLease,
		fixture.Now.Add(8*time.Second),
	)
	if err := executor.ExecuteModelWork(ctx, finalLease); err != nil {
		t.Fatalf("execute post-progressive model call: %v", err)
	}
	if progressiveModel.respondedCount() != 4 {
		t.Fatalf(
			"progressive journey prepared %d requests, want three compaction calls and one normal call",
			progressiveModel.respondedCount(),
		)
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
	var checkpointIDs []storage.ID
	for rows.Next() {
		var checkpointID storage.ID
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
	for _, test := range []struct {
		name      string
		frontier  int64
		wantDepth int
	}{
		{name: "first", frontier: firstCheckpoint.CheckpointEventSequence, wantDepth: 1},
		{name: "final", frontier: finalCheckpoint.CheckpointEventSequence, wantDepth: 2},
	} {
		depth, err := fixture.Store.Execution().CountConsecutiveContextCheckpointLineage(
			ctx,
			kernelTestProjectID,
			agentID,
			test.frontier,
		)
		if err != nil || depth != test.wantDepth {
			t.Fatalf("%s checkpoint lineage depth = %d, want %d (err=%v)", test.name, depth, test.wantDepth, err)
		}
	}
	semanticFrontier, err := fixture.Store.Execution().MaxEventSequence(ctx, kernelTestProjectID, agentID)
	if err != nil {
		t.Fatalf("load post-compaction semantic frontier: %v", err)
	}
	depth, err := fixture.Store.Execution().CountConsecutiveContextCheckpointLineage(
		ctx,
		kernelTestProjectID,
		agentID,
		semanticFrontier,
	)
	if err != nil || depth != 0 {
		t.Fatalf("post-compaction semantic frontier lineage depth = %d, want 0 (err=%v)", depth, err)
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

func TestProgressiveCompactionExhaustionPublishesOneParentError(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgent(t, ctx, "openai/kernel-test", fixture.Now)

	seedModel := &sequenceKernelModel{providerModelSlug: "kernel-test"}
	for index := 0; index < 4; index++ {
		suffix := strconv.Itoa(index + 1)
		seedModel.responses = append(seedModel.responses, model.Response{
			ID: "resp_progressive_exhaustion_seed_" + suffix,
			Content: []model.ResponsePart{{
				Type: "text",
				Text: "seed output " + strings.Repeat("completed detail ", 80),
			}},
			StopReason: model.StopReasonEndTurn,
		})
		seedTurn := fixture.admitContentInputTurn(
			t,
			ctx,
			agentID,
			userID,
			"seed input "+strings.Repeat("progressive exhaustion history ", 80)+suffix,
			fixture.Now.Add(time.Duration(index*3+1)*time.Second),
		)
		seedNow := fixture.Now.Add(time.Duration(index*3+2) * time.Second)
		seedExecutor := AgentExecutor{
			Store:         fixture.Store,
			ModelResolver: liveTestModelResolver(fixture.Store, seedModel),
			ToolExecutor:  tools.Executor{Store: fixture.Store},
			Now:           func() time.Time { return seedNow },
		}
		if err := seedExecutor.ExecuteModelWork(ctx, seedTurn); err != nil {
			t.Fatalf("execute progressive exhaustion seed %d: %v", index, err)
		}
		if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
			ctx,
			kernelTestProjectID,
			agentID,
			seedTurn.RuntimeLockID,
		); err != nil {
			t.Fatalf("release progressive exhaustion seed %d: %v", index, err)
		}
	}

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
		t.Fatalf("progressive exhaustion watermark/opening = %d/%d, want 10", watermark, turn.OpeningEventSequence)
	}

	compactionModel := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		preparedInputTokenEstimator: func(bundle modelcontext.Bundle) int {
			if isCompactionRequestBundle(bundle) {
				return 500
			}
			return 200_000
		},
		capabilities: model.Capabilities{
			ContextWindowTokens: 128000,
			MaxOutputTokens:     256,
		},
		responses: []model.Response{
			completeProgressiveSummaryResponse("step one summary"),
			completeProgressiveSummaryResponse("step two summary"),
			completeProgressiveSummaryResponse("step three summary"),
			completeProgressiveSummaryResponse("step four summary"),
		},
	}
	resolver := liveTestModelResolver(fixture.Store, compactionModel)
	runNow := fixture.Now.Add(21 * time.Second)
	runner := compaction.Runner{
		Store:    compaction.NewStore(fixture.Store.Execution()),
		Resolver: resolver,
		ContextBuilder: modelcontext.Builder{
			Store: modelcontext.NewStore(fixture.Store.Execution(), fixture.Store.Artifacts(), fixture.Store.Integrations()),
		},
		Now: func() time.Time { return runNow },
	}
	claimOverflowParent := func(
		frontier, sourceEventSequenceEnd int64,
	) (executionstore.ModelCallClaim, executionstore.ModelCallClaim) {
		t.Helper()
		snapshot, err := fixture.Store.Execution().CaptureAgentConfigForEventWatermark(
			ctx,
			kernelTestProjectID,
			agentID,
			frontier,
		)
		if err != nil {
			t.Fatalf("capture progressive exhaustion parent snapshot: %v", err)
		}
		parent, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
			ProjectID:          kernelTestProjectID,
			AgentID:            agentID,
			RuntimeLockID:      turn.RuntimeLockID,
			OpeningInputIDs:    turn.InputIDs,
			AgentConfigID:      snapshot.AgentConfig.ID,
			InputEventSequence: frontier,
		})
		if err != nil {
			t.Fatalf("claim progressive exhaustion parent: %v", err)
		}
		handoff, err := fixture.Store.Execution().RecordModelCallFailureAndClaimCompaction(
			ctx,
			executionstore.RecordModelCallFailureAndClaimCompactionInput{
				ParentContextID: parent.Context.ID,
				Failure: executionstore.RecordRecoverableModelCallFailureInput{
					ProjectID:          kernelTestProjectID,
					AgentID:            agentID,
					ModelCallContextID: parent.Context.ID,
					RuntimeLockID:      turn.RuntimeLockID,
					RecoveryKind:       executionstore.ModelCallRecoveryCompact,
					ErrorKind:          model.ErrorKindContextWindow,
					ErrorCode:          "prepared_request_budget_overflow",
					ErrorMessage:       "The serialized provider request exceeds the configured input budget.",
				},
				SourceEventSequenceEnd: sourceEventSequenceEnd,
			},
		)
		if err != nil {
			t.Fatalf("record progressive exhaustion parent failure: %v", err)
		}
		if handoff.BoundaryPreempted || !handoff.CompactionCall.Created || !handoff.CompactionCall.Claimed {
			t.Fatalf("progressive exhaustion handoff = %+v, want newly claimed child", handoff)
		}
		runNow = runNow.Add(time.Second)
		return parent, handoff.CompactionCall
	}
	run := func(
		start, end, frontier int64,
		blockedContextID storage.ID,
		compactionClaim executionstore.ModelCallClaim,
	) compaction.RunResult {
		t.Helper()
		result, err := runner.RunClaimed(ctx, compaction.RunInput{
			Plan: compaction.Plan{
				ProjectID:          kernelTestProjectID,
				AgentID:            agentID,
				InputEventSequence: frontier,
				EventSequenceStart: start,
				EventSequenceEnd:   end,
			},
			TurnID:                   turn.TurnID,
			OpeningInputIDs:          turn.InputIDs,
			OpeningEventSequence:     turn.OpeningEventSequence,
			RuntimeLockID:            turn.RuntimeLockID,
			ParentModelCallContextID: blockedContextID,
		}, compactionClaim)
		if err != nil {
			t.Fatalf("run progressive exhaustion source %d..%d: %v", start, end, err)
		}
		runNow = runNow.Add(time.Second)
		return result
	}

	frontier := watermark
	ranges := [][2]int64{{1, 3}, {4, 5}, {6, 7}}
	for step, sourceRange := range ranges {
		parent, compactionClaim := claimOverflowParent(frontier, sourceRange[1])
		result := run(
			sourceRange[0],
			sourceRange[1],
			frontier,
			parent.Context.ID,
			compactionClaim,
		)
		if result.State != compaction.RunCompleted || result.Checkpoint == nil {
			t.Fatalf("progressive exhaustion step %d result = %+v", step+1, result)
		}
		depth, err := fixture.Store.Execution().CountConsecutiveContextCheckpointLineage(
			ctx,
			kernelTestProjectID,
			agentID,
			result.Checkpoint.CheckpointEventSequence,
		)
		if err != nil || depth != step+1 {
			t.Fatalf(
				"progressive exhaustion step %d lineage depth = %d (err=%v)",
				step+1,
				depth,
				err,
			)
		}
		frontier = result.Checkpoint.CheckpointEventSequence
	}

	parent, compactionClaim := claimOverflowParent(frontier, 9)
	terminal := run(8, 9, frontier, parent.Context.ID, compactionClaim)
	if terminal.State != compaction.RunTerminal || terminal.Checkpoint != nil {
		t.Fatalf("progressive exhaustion terminal result = %+v", terminal)
	}
	if compactionModel.respondedCount() != 4 {
		t.Fatalf("progressive exhaustion prepared %d requests, want four", compactionModel.respondedCount())
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
	var checkpoints, failedCompactions, parentContexts int
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM context_checkpoints checkpoint
		JOIN agents agent ON agent.id = checkpoint.agent_id
		WHERE agent.project_id = $1 AND checkpoint.agent_id = $2`, kernelTestProjectID, agentID).Scan(&checkpoints); err != nil {
		t.Fatalf("count bounded progressive checkpoints: %v", err)
	}
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM model_call_contexts
		WHERE project_id = $1
		  AND agent_id = $2
		  AND operation_kind = 'compaction'
		  AND state = 'failed'`, kernelTestProjectID, agentID).Scan(&failedCompactions); err != nil {
		t.Fatalf("count failed progressive compactions: %v", err)
	}
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM model_call_contexts
		WHERE project_id = $1
		  AND agent_id = $2
		  AND operation_kind = 'normal'
		  AND input_event_sequence = $3`,
		kernelTestProjectID,
		agentID,
		parent.Context.InputEventSequence,
	).Scan(&parentContexts); err != nil {
		t.Fatalf("count progressive exhaustion parent contexts: %v", err)
	}
	if checkpoints != 3 || failedCompactions != 1 || parentContexts != 1 {
		t.Fatalf(
			"bounded progressive checkpoints / failed compactions / parent contexts = %d/%d/%d, want 3/1/1",
			checkpoints,
			failedCompactions,
			parentContexts,
		)
	}
}
