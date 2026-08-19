//go:build integration

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/compaction"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

func TestAgentExecutorCarriesReplayRejectionThroughCompaction(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	agentID, userID := fixture.createAgent(t, ctx, "openai/replay-compaction-model", now)

	seedModel := &sequenceKernelModel{
		providerModelSlug: "replay-compaction-model",
		responses: []model.Response{
			{
				ID:         "resp-replay-compaction-padding",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "disposable old history accepted"}},
				StopReason: model.StopReasonEndTurn,
			},
			{
				ID:         "resp-replay-compaction-seed",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "recent history that must remain exact"}},
				StopReason: model.StopReasonEndTurn,
				ProviderReplay: json.RawMessage(
					`[{"type":"reasoning","id":"rs_replay_compaction","encrypted_content":"large opaque replay"},` +
						`{"type":"message","id":"msg_replay_compaction","role":"assistant","content":` +
						`[{"type":"output_text","text":"recent history that must remain exact"}]}]`,
				),
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
	paddingTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		strings.Repeat("disposable padding ", 6_000),
		now.Add(time.Second),
	)
	if err := seedExecutor.ExecuteModelWork(ctx, paddingTurn); err != nil {
		t.Fatalf("execute disposable padding turn: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		paddingTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release disposable padding turn: %v", err)
	}

	seedTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"seed replay-backed history",
		now.Add(3*time.Second),
	)
	seedNow = now.Add(4 * time.Second)
	if err := seedExecutor.ExecuteModelWork(ctx, seedTurn); err != nil {
		t.Fatalf("execute replay-backed seed turn: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		seedTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release replay-backed seed turn: %v", err)
	}

	capabilities := model.Capabilities{
		ContextWindowTokens: 128_000,
		MaxOutputTokens:     128,
	}
	modelClient := &sequenceKernelModel{
		providerModelSlug: "replay-compaction-model",
		capabilities:      capabilities,
		preparedInputTokenEstimatorForPolicy: func(
			bundle modelcontext.Bundle,
			policy model.RequestPolicy,
		) int {
			if bundle.ContextCheckpoint != nil {
				for _, message := range bundle.Messages {
					if strings.Contains(string(message.ProviderReplay), "rs_replay_compaction") &&
						policy.AllowsProviderReplay(message.Sequence) {
						return capabilities.ContextWindowTokens * 2
					}
				}
			}
			return 500
		},
		errs: []error{model.ProviderError{
			Kind:    model.ErrorKindReplayRejected,
			Source:  "test-provider",
			Code:    "invalid_encrypted_content",
			Message: "provider replay could not be decrypted",
		}},
		responses: []model.Response{
			{
				ID:         "resp-replay-compaction-overflow",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "context window exceeded"}},
				StopReason: model.StopReasonContextWindow,
			},
			{
				ID:         "resp-replay-compaction-summary",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "The earlier replay-backed history was preserved."}},
				StopReason: model.StopReasonEndTurn,
			},
			{
				ID:         "resp-replay-compaction-final",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "continued after replay-aware compaction"}},
				StopReason: model.StopReasonEndTurn,
				ProviderReplay: json.RawMessage(
					`[{"type":"reasoning","id":"rs_after_compaction","encrypted_content":"new opaque replay"},` +
						`{"type":"message","id":"msg_after_compaction","role":"assistant","content":` +
						`[{"type":"output_text","text":"continued after replay-aware compaction"}]}]`,
				),
			},
			{
				ID:         "resp-replay-compaction-later",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "new replay remained eligible"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	currentNow := now.Add(6 * time.Second)
	executor := AgentExecutor{
		Store:           fixture.Store,
		ModelResolver:   liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:    tools.Executor{Store: fixture.Store},
		Now:             func() time.Time { return currentNow },
		ModelRetryDelay: immediateKernelModelRetryDelay,
	}
	turn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"trigger replay rejection before compaction",
		now.Add(5*time.Second),
	)
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute replay-rejected attempt: %v", err)
	}

	currentNow = now.Add(7 * time.Second)
	retry := continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, turn, currentNow)
	if err := executor.ExecuteModelWork(ctx, retry); err != nil {
		t.Fatalf("execute context overflow and compaction: %v", err)
	}

	currentNow = now.Add(8 * time.Second)
	continuation := continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, retry, currentNow)
	if err := executor.ExecuteModelWork(ctx, continuation); err != nil {
		t.Fatalf("execute post-compaction continuation: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		continuation.RuntimeLockID,
	); err != nil {
		t.Fatalf("release post-compaction continuation: %v", err)
	}

	laterTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"continue with replay created after the rejection",
		now.Add(9*time.Second),
	)
	currentNow = now.Add(10 * time.Second)
	if err := executor.ExecuteModelWork(ctx, laterTurn); err != nil {
		t.Fatalf("execute later turn: %v", err)
	}

	if modelClient.respondedCount() != 5 {
		t.Fatalf(
			"provider requests = %d, want rejection, overflow, summary, continuation, and later turn",
			modelClient.respondedCount(),
		)
	}
	rejectedFrontier := turn.OpeningEventSequence
	if modelClient.responded[0].Policy.ProviderReplayCutoffEventSequence != 0 {
		t.Fatal("initial normal request did not begin with provider replay enabled")
	}
	if len(modelClient.responded[0].ProviderReplays) == 0 {
		t.Fatal("initial normal request did not contain the replay needed by the test")
	}
	if modelClient.responded[1].Policy.ProviderReplayCutoffEventSequence != rejectedFrontier {
		t.Fatalf(
			"normal retry replay cutoff = %d, want rejected frontier %d",
			modelClient.responded[1].Policy.ProviderReplayCutoffEventSequence,
			rejectedFrontier,
		)
	}
	if modelClient.responded[3].Policy.ProviderReplayCutoffEventSequence != rejectedFrontier {
		t.Fatalf(
			"post-compaction replay cutoff = %d, want rejected frontier %d",
			modelClient.responded[3].Policy.ProviderReplayCutoffEventSequence,
			rejectedFrontier,
		)
	}
	postCompactionRequest := string(modelClient.responded[3].ProviderRequest)
	if strings.Contains(postCompactionRequest, "disposable padding") ||
		!strings.Contains(postCompactionRequest, "recent history that must remain exact") ||
		!strings.Contains(postCompactionRequest, "earlier replay-backed history was preserved") {
		t.Fatalf("post-compaction request did not preserve the intended context: %s", postCompactionRequest)
	}

	checkpointProjections := 0
	for _, prepared := range modelClient.prepared {
		if prepared.ContextCheckpoints == 0 {
			continue
		}
		checkpointProjections++
		for index, replay := range prepared.ProviderReplays {
			if strings.Contains(string(replay), "rs_replay_compaction") &&
				prepared.Policy.AllowsProviderReplay(prepared.ProviderReplaySequences[index]) {
				t.Fatalf("checkpoint projection allowed replay from the rejected history")
			}
		}
	}
	if checkpointProjections < 3 {
		t.Fatalf("checkpoint projections = %d, want planner, validator, and continuation", checkpointProjections)
	}

	var checkpoints int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM context_checkpoints checkpoint
JOIN agents agent ON agent.id = checkpoint.agent_id
WHERE agent.project_id = $1 AND checkpoint.agent_id = $2
`, kernelTestProjectID, agentID).Scan(&checkpoints); err != nil {
		t.Fatalf("count replay-aware checkpoints: %v", err)
	}
	if checkpoints != 1 {
		t.Fatalf("replay-aware checkpoints = %d, want 1", checkpoints)
	}

	laterRequest := modelClient.responded[4]
	var oldReplaySequence, newReplaySequence int64
	for index, replay := range laterRequest.ProviderReplays {
		switch {
		case strings.Contains(string(replay), "rs_replay_compaction"):
			oldReplaySequence = laterRequest.ProviderReplaySequences[index]
		case strings.Contains(string(replay), "rs_after_compaction"):
			newReplaySequence = laterRequest.ProviderReplaySequences[index]
		}
	}
	if oldReplaySequence == 0 || newReplaySequence == 0 ||
		laterRequest.Policy.AllowsProviderReplay(oldReplaySequence) ||
		!laterRequest.Policy.AllowsProviderReplay(newReplaySequence) {
		t.Fatalf(
			"later request cutoff=%d old replay=%d new replay=%d",
			laterRequest.Policy.ProviderReplayCutoffEventSequence,
			oldReplaySequence,
			newReplaySequence,
		)
	}
}

func TestAgentExecutorCompactsAndRetriesAfterProviderContextWindow(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgent(t, ctx, "openai/kernel-test", fixture.Now, "run_command")

	firstModel := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		capabilities: model.Capabilities{
			ContextWindowTokens: 128000,
			MaxOutputTokens:     128,
		},
		responses: []model.Response{{
			ID:         "resp_first",
			Content:    []model.ResponsePart{{Type: "text", Text: "first turn accepted"}},
			StopReason: model.StopReasonEndTurn,
			Usage:      model.Usage{InputTokens: 10, OutputTokens: 4},
		}},
	}
	firstTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"seed history before overflow",
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

	retryModel := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		capabilities: model.Capabilities{
			ContextWindowTokens: 128000,
			MaxOutputTokens:     128,
		},
		responses: []model.Response{
			{
				ID:         "resp_context_window",
				Content:    []model.ResponsePart{{Type: "text", Text: "context window exceeded"}},
				StopReason: model.StopReasonContextWindow,
				Usage:      model.Usage{InputTokens: 128000},
			},
			{
				ID:         "resp_summary",
				Content:    []model.ResponsePart{{Type: "text", Text: "The earlier turn established seed history."}},
				StopReason: model.StopReasonEndTurn,
				Usage:      model.Usage{InputTokens: 20, OutputTokens: 8},
			},
			{
				ID:         "resp_after_compaction",
				Content:    []model.ResponsePart{{Type: "text", Text: "continued after compaction"}},
				StopReason: model.StopReasonEndTurn,
				Usage:      model.Usage{InputTokens: 12, OutputTokens: 5},
			},
		},
	}
	retryTurn := fixture.admitSteeringInputsTurn(
		t,
		ctx,
		agentID,
		userID,
		[]string{"trigger context window retry", "second opening steering input"},
		fixture.Now.Add(3*time.Second),
	)
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, retryModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(4 * time.Second) },
	}
	if err := executor.ExecuteModelWork(ctx, retryTurn); err != nil {
		t.Fatalf("execute retry turn: %v", err)
	}
	if len(retryTurn.InputIDs) != 2 {
		t.Fatalf("retry turn input ids = %v, want two opening inputs", retryTurn.InputIDs)
	}
	if retryModel.respondedCount() != 2 {
		t.Fatalf("retry model prepared %d requests on first lease, want provider failure and compaction", retryModel.respondedCount())
	}
	if len(retryModel.responded[0].ToolSpecs) == 0 {
		t.Fatal("normal request omitted the configured tool needed to make the compaction assertion meaningful")
	}
	if len(retryModel.responded[1].ToolSpecs) != 0 {
		t.Fatalf("compaction request exposed tools: %+v", retryModel.responded[1].ToolSpecs)
	}
	var completedCompactionContextID storage.ID
	if err := fixture.Pool.QueryRow(ctx, `
SELECT id
FROM model_call_contexts
WHERE project_id = $1
  AND agent_id = $2
  AND operation_kind = 'compaction'
  AND state = 'succeeded'
ORDER BY input_event_sequence DESC, source_event_sequence_end DESC, attempt_number DESC
LIMIT 1
`, kernelTestProjectID, agentID).Scan(&completedCompactionContextID); err != nil {
		t.Fatalf("load completed compaction context id: %v", err)
	}
	completedCompactionContext, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		kernelTestProjectID,
		agentID,
		completedCompactionContextID,
	)
	if err != nil || !found {
		t.Fatalf("load completed compaction context: found=%v err=%v", found, err)
	}
	if completedCompactionContext.SourceEventSequenceEnd == nil {
		t.Fatalf("completed compaction context has no source range: %+v", completedCompactionContext)
	}
	parentContext, found, err := fixture.Store.Execution().GetNormalModelCallContextForFrontier(
		ctx,
		kernelTestProjectID,
		agentID,
		completedCompactionContext.InputEventSequence,
	)
	if err != nil || !found {
		t.Fatalf("load completed compaction parent: found=%v err=%v", found, err)
	}
	replayedCompaction, err := (compaction.Runner{
		Store:          compaction.NewStore(fixture.Store.Execution()),
		Resolver:       executor.ModelResolver,
		ContextBuilder: executor.contextBuilder(),
		Now:            executor.Now,
	}).Run(ctx, compaction.RunInput{
		Plan: compaction.Plan{
			ProjectID:          kernelTestProjectID,
			AgentID:            agentID,
			InputEventSequence: completedCompactionContext.InputEventSequence,
			EventSequenceStart: compactionSourceStartForKernelTest(t, ctx, fixture.Store, completedCompactionContext),
			EventSequenceEnd:   *completedCompactionContext.SourceEventSequenceEnd,
		},
		TurnID:                   retryTurn.TurnID,
		OpeningInputIDs:          retryTurn.InputIDs,
		OpeningEventSequence:     retryTurn.OpeningEventSequence,
		RuntimeLockID:            retryTurn.RuntimeLockID,
		ParentModelCallContextID: parentContext.ID,
	})
	if err != nil {
		t.Fatalf("replay completed compaction: %v", err)
	}
	if replayedCompaction.State != compaction.RunCompleted || replayedCompaction.Checkpoint == nil {
		t.Fatalf("replayed compaction = %+v, want adopted checkpoint", replayedCompaction)
	}
	if retryModel.respondedCount() != 2 {
		t.Fatalf("completed compaction replay made a provider request; prepared=%d", retryModel.respondedCount())
	}
	finalTurn := continueTurnOnNewLeaseForKernelTest(
		t,
		ctx,
		fixture,
		retryTurn,
		fixture.Now.Add(5*time.Second),
	)
	if err := executor.ExecuteModelWork(ctx, finalTurn); err != nil {
		t.Fatalf("execute post-compaction turn: %v", err)
	}
	if retryModel.respondedCount() != 3 {
		t.Fatalf("retry model prepared %d requests across leases, want provider failure, compaction, retry", retryModel.respondedCount())
	}
	summaryRequest := string(retryModel.responded[1].ProviderRequest)
	if !strings.Contains(summaryRequest, "first turn accepted") ||
		!strings.Contains(summaryRequest, "seed history before overflow") {
		t.Fatalf("compaction summary request did not include semantic history: %s", summaryRequest)
	}
	retryRequest := string(retryModel.responded[2].ProviderRequest)
	if !strings.Contains(retryRequest, "The earlier turn established seed history.") {
		t.Fatalf("retry request did not include checkpoint summary: %s", retryRequest)
	}

	var checkpoints int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM context_checkpoints checkpoint
JOIN agents agent ON agent.id = checkpoint.agent_id
WHERE agent.project_id = $1 AND checkpoint.agent_id = $2
`, kernelTestProjectID, agentID).
		Scan(&checkpoints); err != nil {
		t.Fatalf("count checkpoints: %v", err)
	}
	if checkpoints != 1 {
		t.Fatalf("checkpoint count = %d, want 1", checkpoints)
	}
	var retryContexts int
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM model_call_contexts mcc
		JOIN context_checkpoints checkpoint
		  ON checkpoint.agent_id = mcc.agent_id
		JOIN agent_events checkpoint_event
		  ON checkpoint_event.agent_id = checkpoint.agent_id
		 AND checkpoint_event.context_checkpoint_id = checkpoint.id
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
		  AND mcc.agent_id = $2
		  AND mcc.operation_kind = 'normal'
		  AND mcc.state = 'succeeded'
		  AND mcc.input_event_sequence >= checkpoint_event.sequence
		  AND context_turn.turn_id = $3`, kernelTestProjectID, agentID, retryTurn.TurnID).Scan(&retryContexts); err != nil {
		t.Fatalf("count retry context checkpoint refs: %v", err)
	}
	if retryContexts != 1 {
		t.Fatalf("retry context checkpoint refs = %d, want 1", retryContexts)
	}
	var outputBlocks int
	if err := fixture.Pool.QueryRow(ctx, `SELECT count(*) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.turn_id = $3 AND block.block_kind = 'text' AND block.text_content = 'continued after compaction'`, kernelTestProjectID, agentID, retryTurn.TurnID).
		Scan(&outputBlocks); err != nil {
		t.Fatalf("count final output blocks: %v", err)
	}
	if outputBlocks != 1 {
		t.Fatalf("final output block count = %d, want 1", outputBlocks)
	}
}

func TestAgentExecutorReplaysOverflowWhenPlanningIsInterruptedBeforeHandoff(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgent(t, ctx, "openai/kernel-test", fixture.Now)

	seedModel := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		responses: []model.Response{{
			ID:         "resp_interrupted_compaction_seed",
			Content:    []model.ResponsePart{{Type: "text", Text: "closed history before interrupted compaction"}},
			StopReason: model.StopReasonEndTurn,
		}},
	}
	seedTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"seed compactable history before the interrupted overflow",
		fixture.Now.Add(time.Second),
	)
	if err := (AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, seedModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(2 * time.Second) },
	}).ExecuteModelWork(ctx, seedTurn); err != nil {
		t.Fatalf("execute interrupted-compaction seed: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		seedTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release interrupted-compaction seed runtime: %v", err)
	}

	attemptCtx, cancelAttempt := context.WithCancel(ctx)
	defer cancelAttempt()
	modelClient := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		capabilities: model.Capabilities{
			ContextWindowTokens: 128000,
			MaxOutputTokens:     128,
		},
		responses: []model.Response{
			{
				ID:         "resp_overflow_before_interrupted_handoff",
				Content:    []model.ResponsePart{{Type: "text", Text: "context window exceeded before handoff"}},
				StopReason: model.StopReasonContextWindow,
				Usage:      model.Usage{InputTokens: 128000},
			},
			{
				ID:         "resp_overflow_after_ambiguous_replay",
				Content:    []model.ResponsePart{{Type: "text", Text: "context window exceeded on replay"}},
				StopReason: model.StopReasonContextWindow,
				Usage:      model.Usage{InputTokens: 128000},
			},
			{
				ID:         "resp_summary_after_ambiguous_replay",
				Content:    []model.ResponsePart{{Type: "text", Text: "The earlier compactable history was preserved after recovery."}},
				StopReason: model.StopReasonEndTurn,
			},
			{
				ID:         "resp_final_after_ambiguous_replay",
				Content:    []model.ResponsePart{{Type: "text", Text: "continued after interrupted compaction recovery"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
		afterRespond: func(response model.Response) {
			if response.ID == "resp_overflow_before_interrupted_handoff" {
				cancelAttempt()
			}
		},
	}
	turn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"trigger an overflow whose first maintenance plan is interrupted",
		fixture.Now.Add(3*time.Second),
	)
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(4 * time.Second) },
	}
	if err := executor.ExecuteModelWork(attemptCtx, turn); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted maintenance planning error = %v, want context canceled", err)
	}
	if modelClient.respondedCount() != 1 {
		t.Fatalf("requests before interrupted handoff = %d, want one overflow", modelClient.respondedCount())
	}

	var interruptedContextID storage.ID
	if err := fixture.Pool.QueryRow(ctx, `
SELECT id
FROM model_call_contexts
WHERE project_id = $1
  AND agent_id = $2
  AND input_event_sequence = $3
  AND operation_kind = 'normal'
  AND state = 'started'
`, kernelTestProjectID, agentID, turn.OpeningEventSequence).Scan(&interruptedContextID); err != nil {
		t.Fatalf("load normal context left active before handoff: %v", err)
	}
	assertNoInterruptedCompactionArtifacts := func(stage string) {
		t.Helper()
		var compactions, checkpoints int
		if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM model_call_contexts
WHERE project_id = $1 AND agent_id = $2 AND operation_kind = 'compaction'
`, kernelTestProjectID, agentID).Scan(&compactions); err != nil {
			t.Fatalf("count compactions %s: %v", stage, err)
		}
		if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM context_checkpoints checkpoint
JOIN agents agent ON agent.id = checkpoint.agent_id
WHERE agent.project_id = $1 AND checkpoint.agent_id = $2
`, kernelTestProjectID, agentID).Scan(&checkpoints); err != nil {
			t.Fatalf("count checkpoints %s: %v", stage, err)
		}
		if compactions != 0 || checkpoints != 0 {
			t.Fatalf("compactions/checkpoints %s = %d/%d, want 0/0", stage, compactions, checkpoints)
		}
	}
	assertNoInterruptedCompactionArtifacts("before runtime release")

	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		turn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release runtime after interrupted maintenance planning: %v", err)
	}
	interrupted, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		kernelTestProjectID,
		agentID,
		interruptedContextID,
	)
	if err != nil || !found {
		t.Fatalf("load interrupted overflow context: found=%v err=%v", found, err)
	}
	if interrupted.State != executionstore.ModelCallContextFailed ||
		interrupted.RecoveryKind != executionstore.ModelCallRecoveryRetry ||
		interrupted.ErrorCode != "runtime_released_before_model_result_acceptance" ||
		interrupted.RetryAt == nil ||
		!kernelModelCallOutcomeAmbiguous(t, interrupted.ErrorDetails) {
		t.Fatalf("interrupted overflow context = %+v", interrupted)
	}
	assertNoInterruptedCompactionArtifacts("after runtime release")

	claimAt := interrupted.RetryAt.Add(time.Second)
	if wallNow := time.Now().UTC(); claimAt.Before(wallNow) {
		claimAt = wallNow.Add(time.Second)
	}
	retryClaim, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		kernelTestClaimInput(claimAt),
	)
	if err != nil {
		t.Fatalf("claim ambiguous overflow replay: %v", err)
	}
	if !found || retryClaim.Kind != executionstore.AgentWorkModel ||
		retryClaim.Model.ModelCallContextID != interruptedContextID {
		t.Fatalf("ambiguous overflow replay claim = %+v found=%v", retryClaim, found)
	}
	retryWork := modelWorkExecutionFromClaimForKernelTest(retryClaim, claimAt)
	executor.Now = func() time.Time { return claimAt }
	if err := executor.ExecuteModelWork(ctx, retryWork); err != nil {
		t.Fatalf("execute ambiguous overflow replay and compaction: %v", err)
	}
	if modelClient.respondedCount() != 3 {
		t.Fatalf("requests through replay compaction = %d, want overflow/overflow/summary", modelClient.respondedCount())
	}

	finalNow := claimAt.Add(time.Second)
	finalWork := continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, retryWork, finalNow)
	executor.Now = func() time.Time { return finalNow }
	if err := executor.ExecuteModelWork(ctx, finalWork); err != nil {
		t.Fatalf("execute continuation after interrupted compaction recovery: %v", err)
	}
	if modelClient.respondedCount() != 4 {
		t.Fatalf("total requests after interrupted compaction recovery = %d, want four", modelClient.respondedCount())
	}
	if summaryRequest := string(modelClient.responded[2].ProviderRequest); !strings.Contains(
		summaryRequest,
		"closed history before interrupted compaction",
	) {
		t.Fatalf("replayed compaction summary omitted closed history: %s", summaryRequest)
	}
	if finalRequest := string(modelClient.responded[3].ProviderRequest); !strings.Contains(
		finalRequest,
		"The earlier compactable history was preserved after recovery.",
	) {
		t.Fatalf("continued request omitted recovered checkpoint: %s", finalRequest)
	}

	var ambiguousRetries, compactFailures, successfulCompactions, successfulContinuations int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*) FILTER (
         WHERE context.operation_kind = 'normal'
           AND context.state = 'failed'
           AND context.recovery_kind = 'retry'
           AND context.error_code = 'runtime_released_before_model_result_acceptance'
       ),
       count(*) FILTER (
         WHERE context.operation_kind = 'normal'
           AND context.state = 'failed'
           AND context.recovery_kind = 'compact'
           AND context.error_kind = 'context_window'
       ),
       count(*) FILTER (
         WHERE context.operation_kind = 'compaction'
           AND context.state = 'succeeded'
       ),
       count(*) FILTER (
         WHERE context.operation_kind = 'normal'
           AND context.state = 'succeeded'
       )
FROM model_call_contexts context
JOIN LATERAL (
  SELECT opening.turn_id
  FROM agent_events opening
  WHERE opening.agent_id = context.agent_id
    AND opening.is_opening_event
    AND opening.sequence <= context.input_event_sequence
  ORDER BY opening.sequence DESC, opening.id DESC
  LIMIT 1
) context_turn ON true
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context_turn.turn_id = $3
`, kernelTestProjectID, agentID, turn.TurnID).Scan(
		&ambiguousRetries,
		&compactFailures,
		&successfulCompactions,
		&successfulContinuations,
	); err != nil {
		t.Fatalf("load interrupted compaction recovery chain: %v", err)
	}
	if ambiguousRetries != 1 || compactFailures != 1 ||
		successfulCompactions != 1 || successfulContinuations != 1 {
		t.Fatalf(
			"recovery chain ambiguous/compact/summary/continuation = %d/%d/%d/%d, want 1/1/1/1",
			ambiguousRetries,
			compactFailures,
			successfulCompactions,
			successfulContinuations,
		)
	}
	var checkpoints, finalOutputs int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM context_checkpoints checkpoint
JOIN agents agent ON agent.id = checkpoint.agent_id
WHERE agent.project_id = $1 AND checkpoint.agent_id = $2
`, kernelTestProjectID, agentID).Scan(&checkpoints); err != nil {
		t.Fatalf("count recovered checkpoints: %v", err)
	}
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
  AND block.text_content = 'continued after interrupted compaction recovery'
`, kernelTestProjectID, agentID, turn.TurnID).Scan(&finalOutputs); err != nil {
		t.Fatalf("count interrupted-compaction final outputs: %v", err)
	}
	if checkpoints != 1 || finalOutputs != 1 {
		t.Fatalf("recovered checkpoints/final outputs = %d/%d, want 1/1", checkpoints, finalOutputs)
	}
}

func TestAgentExecutorStopsManagedCompactionAfterAdmissionCloses(t *testing.T) {
	ctx := context.Background()
	journey := newManagedCompactionAdmissionJourney(
		t,
		ctx,
		"resp_managed_context_window",
	)
	if err := journey.executor.ExecuteModelWork(ctx, journey.turn); err != nil {
		t.Fatalf("execute managed context-window attempt: %v", err)
	}
	if journey.model.respondedCount() != 1 {
		t.Fatalf(
			"provider requests after denied compaction = %d, want 1",
			journey.model.respondedCount(),
		)
	}
	assertDurableModelErrorForKernelTest(
		t,
		ctx,
		journey.fixture,
		journey.agentID,
		journey.turn.TurnID,
		string(modelprotocol.ErrorKindRuntime),
		storeerr.ManagedWorkAdmissionDeniedCode,
	)
	var compactableParents, deniedCompactions, checkpoints int
	if err := journey.fixture.Pool.QueryRow(ctx, `
SELECT count(*) FILTER (
           WHERE operation_kind = 'normal'
             AND state = 'failed'
             AND recovery_kind = 'compact'
       ),
       count(*) FILTER (
           WHERE operation_kind = 'compaction'
             AND state = 'failed'
             AND recovery_kind IS NULL
             AND error_code = $3
       )
FROM model_call_contexts
WHERE project_id = $1 AND agent_id = $2
`, kernelTestProjectID, journey.agentID, storeerr.ManagedWorkAdmissionDeniedCode).Scan(
		&compactableParents,
		&deniedCompactions,
	); err != nil {
		t.Fatalf("load denied managed compaction contexts: %v", err)
	}
	if err := journey.fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM context_checkpoints checkpoint
JOIN agents agent ON agent.id = checkpoint.agent_id
WHERE agent.project_id = $1 AND checkpoint.agent_id = $2
`, kernelTestProjectID, journey.agentID).Scan(&checkpoints); err != nil {
		t.Fatalf("count checkpoints after denied managed compaction: %v", err)
	}
	if compactableParents != 1 || deniedCompactions != 1 || checkpoints != 0 {
		t.Fatalf(
			"managed compaction parents/denials/checkpoints = %d/%d/%d, want 1/1/0",
			compactableParents,
			deniedCompactions,
			checkpoints,
		)
	}
}

func TestAgentExecutorStopsManagedCompactionRetryAfterAdmissionCloses(t *testing.T) {
	ctx := context.Background()
	journey := newManagedCompactionAdmissionJourney(t, ctx, "")
	journey.model.errs = []error{nil, model.ProviderError{
		Kind:    model.ErrorKindTransient,
		Source:  "test-provider",
		Message: "retry compaction",
	}}
	journey.executor.ModelRetryDelay = immediateKernelModelRetryDelay
	if err := journey.executor.ExecuteModelWork(ctx, journey.turn); err != nil {
		t.Fatalf("execute retryable managed compaction: %v", err)
	}
	if journey.model.respondedCount() != 2 {
		t.Fatalf("provider requests through retryable compaction = %d, want 2", journey.model.respondedCount())
	}

	journey.fixture.setManagedWorkAdmission(t, ctx, false)
	retry := continueTurnOnNewLeaseForKernelTest(
		t,
		ctx,
		journey.fixture,
		journey.turn,
		journey.fixture.Now.Add(time.Minute),
	)
	journey.executor.Now = func() time.Time { return journey.fixture.Now.Add(time.Minute) }
	if err := journey.executor.ExecuteModelWork(ctx, retry); err != nil {
		t.Fatalf("deny managed compaction retry: %v", err)
	}
	if journey.model.respondedCount() != 2 {
		t.Fatalf("provider requests after denied compaction retry = %d, want 2", journey.model.respondedCount())
	}
	assertDurableModelErrorForKernelTest(
		t,
		ctx,
		journey.fixture,
		journey.agentID,
		journey.turn.TurnID,
		string(modelprotocol.ErrorKindRuntime),
		storeerr.ManagedWorkAdmissionDeniedCode,
	)

	var retryable, denied int
	if err := journey.fixture.Pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE recovery_kind = 'retry'),
       count(*) FILTER (
           WHERE recovery_kind IS NULL
             AND error_code = $3
       )
FROM model_call_contexts
WHERE project_id = $1
  AND agent_id = $2
  AND operation_kind = 'compaction'
`, kernelTestProjectID, journey.agentID, storeerr.ManagedWorkAdmissionDeniedCode).Scan(
		&retryable,
		&denied,
	); err != nil {
		t.Fatalf("load managed compaction retry contexts: %v", err)
	}
	if retryable != 1 || denied != 1 {
		t.Fatalf("managed compaction retryable/denied contexts = %d/%d, want 1/1", retryable, denied)
	}
}

func TestManagedCompactionSourceReplacementStopsAfterAdmissionCloses(t *testing.T) {
	ctx := context.Background()
	journey := newManagedCompactionAdmissionJourney(t, ctx, "")
	frontier, err := journey.fixture.Store.Execution().MaxEventSequence(
		ctx,
		kernelTestProjectID,
		journey.agentID,
	)
	if err != nil {
		t.Fatalf("load managed replacement frontier: %v", err)
	}
	snapshot, err := journey.fixture.Store.Execution().CaptureAgentConfigForEventWatermark(
		ctx,
		kernelTestProjectID,
		journey.agentID,
		frontier,
	)
	if err != nil {
		t.Fatalf("capture managed replacement config: %v", err)
	}
	parent, err := journey.fixture.Store.Execution().ClaimNormalModelCall(
		ctx,
		executionstore.ClaimNormalModelCallInput{
			ProjectID:          kernelTestProjectID,
			AgentID:            journey.agentID,
			RuntimeLockID:      journey.turn.RuntimeLockID,
			OpeningInputIDs:    journey.turn.InputIDs,
			AgentConfigID:      snapshot.AgentConfig.ID,
			InputEventSequence: frontier,
		},
	)
	if err != nil {
		t.Fatalf("claim managed replacement parent: %v", err)
	}
	sourceEnd := journey.turn.OpeningEventSequence - 1
	handoff, err := journey.fixture.Store.Execution().RecordModelCallFailureAndClaimCompaction(
		ctx,
		executionstore.RecordModelCallFailureAndClaimCompactionInput{
			ParentContextID: parent.Context.ID,
			Failure: executionstore.RecordRecoverableModelCallFailureInput{
				ProjectID:          kernelTestProjectID,
				AgentID:            journey.agentID,
				ModelCallContextID: parent.Context.ID,
				RuntimeLockID:      journey.turn.RuntimeLockID,
				RecoveryKind:       executionstore.ModelCallRecoveryCompact,
				ErrorKind:          model.ErrorKindContextWindow,
				ErrorCode:          "context_window",
				ErrorMessage:       "model context exceeded",
			},
			SourceEventSequenceEnd: sourceEnd,
		},
	)
	if err != nil {
		t.Fatalf("claim managed compaction before replacement: %v", err)
	}
	if handoff.BoundaryPreempted || !handoff.CompactionCall.Created || !handoff.CompactionCall.Claimed {
		t.Fatalf("managed compaction handoff = %+v, want newly claimed context", handoff)
	}

	journey.fixture.setManagedWorkAdmission(t, ctx, false)
	replacementEnd := sourceEnd - 1
	replacement, err := journey.fixture.Store.Execution().ReplaceCompactionSource(
		ctx,
		executionstore.ReplaceCompactionSourceInput{
			ProjectID:                  kernelTestProjectID,
			AgentID:                    journey.agentID,
			RuntimeLockID:              journey.turn.RuntimeLockID,
			ModelCallContextID:         handoff.CompactionCall.Context.ID,
			ErrorKind:                  model.ErrorKindPayloadTooLarge,
			ErrorCode:                  "request_too_large",
			ErrorMessage:               "compaction source is too large",
			NextSourceEventSequenceEnd: replacementEnd,
		},
	)
	if err != nil {
		t.Fatalf("deny managed compaction source replacement: %v", err)
	}
	claim := replacement.CompactionCall
	if replacement.BoundaryPreempted || !claim.Created || claim.Claimed ||
		claim.Context.State != executionstore.ModelCallContextFailed ||
		claim.Context.RecoveryKind != "" ||
		claim.Context.ErrorCode != storeerr.ManagedWorkAdmissionDeniedCode ||
		claim.Context.SourceEventSequenceEnd == nil ||
		*claim.Context.SourceEventSequenceEnd != replacementEnd {
		t.Fatalf("managed replacement claim = %+v, want terminal admission denial", replacement)
	}
	assertDurableModelErrorForKernelTest(
		t,
		ctx,
		journey.fixture,
		journey.agentID,
		journey.turn.TurnID,
		string(modelprotocol.ErrorKindRuntime),
		storeerr.ManagedWorkAdmissionDeniedCode,
	)
}

func TestAgentExecutorStopsManagedPostCompactionAttemptAfterAdmissionCloses(t *testing.T) {
	ctx := context.Background()
	journey := newManagedCompactionAdmissionJourney(
		t,
		ctx,
		"resp_managed_summary",
	)
	if err := journey.executor.ExecuteModelWork(ctx, journey.turn); err != nil {
		t.Fatalf("execute managed compaction: %v", err)
	}
	if journey.model.respondedCount() != 2 {
		t.Fatalf(
			"provider requests through compaction = %d, want 2",
			journey.model.respondedCount(),
		)
	}
	continuation := continueTurnOnNewLeaseForKernelTest(
		t,
		ctx,
		journey.fixture,
		journey.turn,
		journey.fixture.Now.Add(5*time.Second),
	)
	if err := journey.executor.ExecuteModelWork(ctx, continuation); err != nil {
		t.Fatalf("deny managed post-compaction attempt: %v", err)
	}
	if journey.model.respondedCount() != 2 {
		t.Fatalf(
			"provider requests after denied post-compaction attempt = %d, want 2",
			journey.model.respondedCount(),
		)
	}
	assertDurableModelErrorForKernelTest(
		t,
		ctx,
		journey.fixture,
		journey.agentID,
		journey.turn.TurnID,
		string(modelprotocol.ErrorKindRuntime),
		storeerr.ManagedWorkAdmissionDeniedCode,
	)
	var deniedCheckpointContinuations int
	if err := journey.fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM model_call_contexts context
JOIN agent_events checkpoint_event ON checkpoint_event.agent_id = context.agent_id
  AND checkpoint_event.sequence = context.input_event_sequence
  AND checkpoint_event.event_kind = 'context_checkpoint'
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.operation_kind = 'normal'
  AND context.state = 'failed'
  AND context.recovery_kind IS NULL
  AND context.error_code = $3
`, kernelTestProjectID, journey.agentID, storeerr.ManagedWorkAdmissionDeniedCode).Scan(
		&deniedCheckpointContinuations,
	); err != nil {
		t.Fatalf("load denied checkpoint continuation: %v", err)
	}
	if deniedCheckpointContinuations != 1 {
		t.Fatalf(
			"denied checkpoint continuations = %d, want 1",
			deniedCheckpointContinuations,
		)
	}
}

type managedCompactionAdmissionJourney struct {
	fixture  kernelFixture
	agentID  storage.ID
	turn     ModelWorkExecution
	executor AgentExecutor
	model    *sequenceKernelModel
}

func newManagedCompactionAdmissionJourney(
	t *testing.T,
	ctx context.Context,
	closeAfterResponseID string,
) managedCompactionAdmissionJourney {
	t.Helper()
	fixture := newKernelFixture(t, ctx)
	fixture.provisionClusterModel(t, ctx, "managed-compaction-prod", "managed-compaction-model")
	agentID, userID := fixture.createAgent(
		t,
		ctx,
		"managed-compaction/managed-compaction-model",
		fixture.Now,
		"run_command",
	)
	seedTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"seed managed history before overflow",
		fixture.Now.Add(time.Second),
	)
	seedModel := &sequenceKernelModel{
		providerModelSlug: "managed-compaction-model",
		responses: []model.Response{{
			ID:         "resp_managed_seed",
			Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "managed seed accepted"}},
			StopReason: model.StopReasonEndTurn,
		}},
	}
	if err := (AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, seedModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(2 * time.Second) },
	}).ExecuteModelWork(ctx, seedTurn); err != nil {
		t.Fatalf("execute managed compaction seed: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		seedTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release managed compaction seed runtime: %v", err)
	}
	modelClient := &sequenceKernelModel{
		providerModelSlug: "managed-compaction-model",
		capabilities: model.Capabilities{
			ContextWindowTokens: 128000,
			MaxOutputTokens:     128,
		},
		responses: []model.Response{
			{
				ID:         "resp_managed_context_window",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "context window exceeded"}},
				StopReason: model.StopReasonContextWindow,
			},
			{
				ID:         "resp_managed_summary",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "Managed history summary."}},
				StopReason: model.StopReasonEndTurn,
			},
			{
				ID:         "resp_managed_continuation",
				Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "continued"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	modelClient.afterRespond = func(response model.Response) {
		if response.ID == closeAfterResponseID {
			fixture.setManagedWorkAdmission(t, ctx, false)
		}
	}
	turn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"trigger managed context compaction",
		fixture.Now.Add(3*time.Second),
	)
	return managedCompactionAdmissionJourney{
		fixture: fixture,
		agentID: agentID,
		turn:    turn,
		executor: AgentExecutor{
			Store:         fixture.Store,
			ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
			ToolExecutor:  tools.Executor{Store: fixture.Store},
			Now:           func() time.Time { return fixture.Now.Add(4 * time.Second) },
		},
		model: modelClient,
	}
}

func TestCompactionExhaustsMalformedResponsesWithoutPersistingUnsafeEvidence(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgent(t, ctx, "openai/kernel-test", fixture.Now)

	seedTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"seed history for malformed compaction response",
		fixture.Now.Add(time.Second),
	)
	seedModel := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		responses: []model.Response{{
			ID:         "resp_malformed_compaction_seed",
			Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "closed seed output"}},
			StopReason: model.StopReasonEndTurn,
		}},
	}
	seedExecutor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, seedModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(2 * time.Second) },
	}
	if err := seedExecutor.ExecuteModelWork(ctx, seedTurn); err != nil {
		t.Fatalf("execute malformed compaction seed turn: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		agentID,
		seedTurn.RuntimeLockID,
	); err != nil {
		t.Fatalf("release malformed compaction seed runtime: %v", err)
	}

	turn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"continue after the closed seed",
		fixture.Now.Add(3*time.Second),
	)
	frontier, err := fixture.Store.Execution().MaxEventSequence(ctx, kernelTestProjectID, agentID)
	if err != nil {
		t.Fatalf("load malformed compaction frontier: %v", err)
	}
	snapshot, err := fixture.Store.Execution().CaptureAgentConfigForEventWatermark(
		ctx,
		kernelTestProjectID,
		agentID,
		frontier,
	)
	if err != nil {
		t.Fatalf("capture malformed compaction config: %v", err)
	}
	runNow := fixture.Now.Add(4 * time.Second)
	parent, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID:          kernelTestProjectID,
		AgentID:            agentID,
		RuntimeLockID:      turn.RuntimeLockID,
		OpeningInputIDs:    turn.InputIDs,
		AgentConfigID:      snapshot.AgentConfig.ID,
		InputEventSequence: frontier,
	})
	if err != nil {
		t.Fatalf("claim malformed compaction parent: %v", err)
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
				ErrorCode:          "configured_input_budget_exceeded",
				ErrorMessage:       "The prepared model request exceeds the configured input budget.",
			},
			SourceEventSequenceEnd: turn.OpeningEventSequence - 1,
		},
	)
	if err != nil {
		t.Fatalf("record malformed compaction parent overflow: %v", err)
	}
	if handoff.BoundaryPreempted || !handoff.CompactionCall.Created || !handoff.CompactionCall.Claimed {
		t.Fatalf("malformed compaction handoff = %+v, want newly claimed child", handoff)
	}
	if err := storagetest.DeleteAgentWakeup(ctx, fixture.Pool, kernelTestProjectID, agentID); err != nil {
		t.Fatalf("clear malformed compaction parent wakeup: %v", err)
	}

	maxAttempts := executionstore.MaxModelCallRetriesPerOperation + 1
	malformedResponses := make([]model.Response, maxAttempts)
	for index := range malformedResponses {
		malformedResponses[index] = model.Response{
			ID:                      "resp\x00malformed_compaction_" + strconv.Itoa(index+1),
			ServedProviderModelSlug: "served\x00malformed_compaction",
			Content:                 []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "complete summary"}},
			StopReason:              model.StopReasonEndTurn,
		}
	}
	compactionModel := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		capabilities: model.Capabilities{
			ContextWindowTokens: 128000,
			MaxOutputTokens:     256,
		},
		responses: malformedResponses,
	}
	executor := AgentExecutor{
		Store:           fixture.Store,
		ModelResolver:   liveTestModelResolver(fixture.Store, compactionModel),
		ModelRetryDelay: immediateKernelModelRetryDelay,
	}
	currentNow := runNow
	runner := compaction.Runner{
		Store:           compaction.NewStore(fixture.Store.Execution()),
		Resolver:        executor.ModelResolver,
		ContextBuilder:  executor.contextBuilder(),
		Now:             func() time.Time { return currentNow },
		ModelRetryDelay: immediateKernelModelRetryDelay,
	}
	runInput := compaction.RunInput{
		Plan: compaction.Plan{
			ProjectID:          kernelTestProjectID,
			AgentID:            agentID,
			InputEventSequence: frontier,
			EventSequenceStart: 1,
			EventSequenceEnd:   turn.OpeningEventSequence - 1,
		},
		TurnID:                   turn.TurnID,
		OpeningInputIDs:          turn.InputIDs,
		OpeningEventSequence:     turn.OpeningEventSequence,
		RuntimeLockID:            turn.RuntimeLockID,
		ParentModelCallContextID: parent.Context.ID,
	}
	var result compaction.RunResult
	for attemptNumber := 1; attemptNumber <= maxAttempts; attemptNumber++ {
		if attemptNumber == 1 {
			result, err = runner.RunClaimed(ctx, runInput, handoff.CompactionCall)
		} else {
			result, err = runner.Run(ctx, runInput)
		}
		if err != nil {
			t.Fatalf("run malformed compaction response attempt %d: %v", attemptNumber, err)
		}
		contextRecord, found, err := fixture.Store.Execution().GetModelCallContext(
			ctx,
			kernelTestProjectID,
			agentID,
			result.ModelCallContextID,
		)
		if err != nil || !found {
			t.Fatalf("load malformed compaction attempt %d: found=%v err=%v", attemptNumber, found, err)
		}
		wantRecoveryKind := executionstore.ModelCallRecoveryRetry
		wantState := compaction.RunRetryScheduled
		if attemptNumber == maxAttempts {
			wantRecoveryKind = ""
			wantState = compaction.RunTerminal
		}
		if result.State != wantState || contextRecord.AttemptNumber != attemptNumber ||
			contextRecord.State != executionstore.ModelCallContextFailed ||
			contextRecord.RecoveryKind != wantRecoveryKind || contextRecord.ErrorCode != "malformed_success_response" ||
			contextRecord.ProviderResponseID != "" ||
			!kernelModelCallOutcomeAmbiguous(t, contextRecord.ErrorDetails) {
			t.Fatalf("malformed compaction attempt %d result=%+v context=%+v", attemptNumber, result, contextRecord)
		}
		if attemptNumber < maxAttempts {
			if result.RetryAt == nil || contextRecord.RetryAt == nil || !result.RetryAt.Equal(*contextRecord.RetryAt) {
				t.Fatalf("malformed compaction retry %d result=%+v context=%+v", attemptNumber, result, contextRecord)
			}
			currentNow = *result.RetryAt
		} else if result.RetryAt != nil || contextRecord.RetryAt != nil {
			t.Fatalf("terminal malformed compaction retained retry time: result=%+v context=%+v", result, contextRecord)
		}
	}
	if compactionModel.respondedCount() != maxAttempts {
		t.Fatalf("malformed compaction provider calls = %d, want %d", compactionModel.respondedCount(), maxAttempts)
	}
	var checkpoints int
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM context_checkpoints checkpoint
		JOIN agents agent ON agent.id = checkpoint.agent_id
		WHERE agent.project_id = $1 AND checkpoint.agent_id = $2`, kernelTestProjectID, agentID).Scan(&checkpoints); err != nil {
		t.Fatalf("count malformed compaction checkpoints: %v", err)
	}
	if checkpoints != 0 {
		t.Fatalf("malformed compaction published %d checkpoints, want none", checkpoints)
	}
	var compactionState, producingState executionstore.ModelCallState
	var outputStopReason, blockKind, blockText string
	var contexts, outputCount, blockCount, wakeups, resumable int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT state
FROM model_call_contexts
WHERE project_id = $1 AND agent_id = $2 AND id = $3`,
		kernelTestProjectID, agentID, result.ModelCallContextID).Scan(&compactionState); err != nil {
		t.Fatalf("load terminal compaction context: %v", err)
	}
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)::integer
FROM model_call_contexts
WHERE project_id = $1
  AND agent_id = $2
  AND operation_kind = 'compaction'
	AND input_event_sequence = $3
	AND source_event_sequence_end = $4`,
		kernelTestProjectID, agentID, frontier, turn.OpeningEventSequence-1).Scan(&contexts); err != nil {
		t.Fatalf("count malformed compaction contexts: %v", err)
	}
	if err := fixture.Pool.QueryRow(ctx, `
SELECT context.state, output.stop_reason, count(DISTINCT output.id)::integer,
       count(block.id)::integer, min(block.block_kind), min(block.text_content)
FROM model_call_contexts context
JOIN model_outputs output ON output.agent_id = context.agent_id
  AND output.model_call_context_id = context.id
JOIN content_blocks block ON block.agent_id = output.agent_id
  AND block.owner_model_output_id = output.id
WHERE context.project_id = $1 AND context.agent_id = $2 AND context.id = $3
GROUP BY context.state, output.stop_reason`,
		kernelTestProjectID, agentID, result.ModelCallContextID).
		Scan(&producingState, &outputStopReason, &outputCount, &blockCount, &blockKind, &blockText); err != nil {
		t.Fatalf("load terminal compaction error output: %v", err)
	}
	if err := fixture.Pool.QueryRow(ctx, `
SELECT (SELECT count(*)::integer FROM agent_wakeups wake JOIN agents agent ON agent.id = wake.agent_id WHERE agent.project_id = $1 AND wake.agent_id = $2),
       (SELECT count(*)::integer FROM agent_continuable_model_contexts($1, $2)
        WHERE model_call_context_id = $3 AND NOT has_later_semantic_event)`,
		kernelTestProjectID, agentID, result.ModelCallContextID).Scan(&wakeups, &resumable); err != nil {
		t.Fatalf("load terminal compaction continuation state: %v", err)
	}
	if compactionState != executionstore.ModelCallContextFailed || producingState != executionstore.ModelCallContextFailed ||
		contexts != maxAttempts ||
		outputStopReason != "error" || outputCount != 1 || blockCount != 1 ||
		blockKind != "error" || blockText != "The model provider returned a malformed successful response." ||
		wakeups != 0 || resumable != 0 {
		t.Fatalf(
			"terminal malformed compaction state context=%s producer=%s contexts=%d output=%s/%d block=%s/%q/%d wakeups=%d resumable=%d",
			compactionState, producingState, contexts, outputStopReason, outputCount,
			blockKind, blockText, blockCount, wakeups, resumable,
		)
	}
}

func TestAgentExecutorRecordsErrorWhenModelGrantDisappearsBeforeCompaction(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgent(t, ctx, "openai/kernel-test", fixture.Now)

	firstModel := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		capabilities: model.Capabilities{
			ContextWindowTokens: 128000,
			MaxOutputTokens:     128,
		},
		responses: []model.Response{{
			ID:         "resp_seed_before_revoke_compaction",
			Content:    []model.ResponsePart{{Type: "text", Text: "seed history before revoke compaction"}},
			StopReason: model.StopReasonEndTurn,
		}},
	}
	firstTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"seed before revoke compaction",
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

	var currentConfigID storage.ID
	if err := fixture.Pool.QueryRow(ctx, `SELECT current_config_id FROM agents WHERE project_id = $1 AND id = $2`, kernelTestProjectID, agentID).
		Scan(&currentConfigID); err != nil {
		t.Fatalf("load agent current config id: %v", err)
	}
	config, found, err := fixture.Store.Execution().GetAgentConfig(ctx, kernelTestProjectID, currentConfigID)
	if err != nil {
		t.Fatalf("load agent config: %v", err)
	}
	if !found {
		t.Fatalf("agent config not found")
	}
	configuredModelID := configuredModelIDForKernelConfig(t, ctx, fixture.Store, config)
	grant, err := fixture.Store.Models().GetActiveProjectModelGrantForConfiguredModel(
		ctx,
		kernelTestOrgID,
		kernelTestProjectID,
		configuredModelID,
	)
	if err != nil {
		t.Fatalf("load active model grant: %v", err)
	}

	var revokeErr error
	retryModel := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		capabilities: model.Capabilities{
			ContextWindowTokens: 128000,
			MaxOutputTokens:     128,
		},
		responses: []model.Response{
			{
				ID:         "resp_context_window_then_revoke",
				Content:    []model.ResponsePart{{Type: "text", Text: "context window exceeded"}},
				StopReason: model.StopReasonContextWindow,
				Usage:      model.Usage{InputTokens: 128000},
			},
			{
				ID:         "resp_compaction_should_not_send",
				Content:    []model.ResponsePart{{Type: "text", Text: "should not send after grant revoke"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
		afterRespond: func(response model.Response) {
			if response.ID == "resp_context_window_then_revoke" {
				_, revokeErr = fixture.Store.Models().DeleteProjectModelGrant(
					ctx,
					kernelTestOrgID,
					kernelTestProjectID,
					grant.ID,
				)
			}
		},
	}
	retryTurn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"trigger context window after revoke",
		fixture.Now.Add(3*time.Second),
	)
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, retryModel),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(4 * time.Second) },
	}
	err = executor.ExecuteModelWork(ctx, retryTurn)
	if revokeErr != nil {
		t.Fatalf("revoke model grant from response hook: %v", revokeErr)
	}
	if err != nil {
		t.Fatalf("execute turn after grant unavailable before compaction: %v", err)
	}
	if retryModel.respondedCount() != 1 {
		t.Fatalf(
			"prepared %d requests, want only the initial request before live compaction resolution fails",
			retryModel.respondedCount(),
		)
	}
	retryModel.mu.Lock()
	remainingResponses := append([]model.Response(nil), retryModel.responses...)
	retryModel.mu.Unlock()
	if len(remainingResponses) != 1 || remainingResponses[0].ID != "resp_compaction_should_not_send" {
		t.Fatalf("compaction provider response was consumed after grant revoke; remaining=%+v", remainingResponses)
	}
	var errorOutputs int
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM model_outputs output
		JOIN agent_events event
		  ON event.agent_id = output.agent_id
		 AND event.model_output_id = output.id
		JOIN model_call_contexts context
		  ON context.agent_id = output.agent_id
		 AND context.id = output.model_call_context_id
		WHERE context.project_id = $1
		  AND output.agent_id = $2
		  AND event.turn_id = $3
		  AND output.stop_reason = 'error'
		  AND context.state = 'failed'
		  AND context.recovery_kind IS NULL
		  AND context.error_kind = 'auth'
		  AND context.error_code = 'model_grant_unavailable'`,
		kernelTestProjectID,
		agentID,
		retryTurn.TurnID,
	).Scan(&errorOutputs); err != nil {
		t.Fatalf("count grant-unavailable error outputs: %v", err)
	}
	if errorOutputs != 1 {
		t.Fatalf("grant-unavailable error outputs = %d, want 1", errorOutputs)
	}
	var providerRequestID, providerResponseID string
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT context.provider_request_id,
		       context.provider_response_id
		FROM model_call_contexts context
		WHERE context.project_id = $1
		  AND context.agent_id = $2
		  AND context.operation_kind = 'compaction'
		ORDER BY context.attempt_number DESC
		LIMIT 1`, kernelTestProjectID, agentID).Scan(
		&providerRequestID,
		&providerResponseID,
	); err != nil {
		t.Fatalf("load pre-send compaction evidence: %v", err)
	}
	if providerRequestID != "" || providerResponseID != "" {
		t.Fatalf(
			"pre-send compaction response evidence = request %q response %q, want empty",
			providerRequestID,
			providerResponseID,
		)
	}
	var failedNormalContexts, failedCompactionContexts, normalContexts, compactionContexts int
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT count(DISTINCT context.id) FILTER (
		         WHERE context.operation_kind = 'normal'
		           AND context.state = 'failed'
		       ),
		       count(DISTINCT context.id) FILTER (
		         WHERE context.operation_kind = 'compaction'
		           AND context.state = 'failed'
		       ),
		       count(context.id) FILTER (
		         WHERE context.operation_kind = 'normal'
		       ),
		       count(context.id) FILTER (
		         WHERE context.operation_kind = 'compaction'
		       )
		FROM model_call_contexts context
		WHERE context.project_id = $1
		  AND context.agent_id = $2
		  AND context.input_event_sequence = $3`,
		kernelTestProjectID,
		agentID,
		retryTurn.OpeningEventSequence,
	).Scan(
		&failedNormalContexts,
		&failedCompactionContexts,
		&normalContexts,
		&compactionContexts,
	); err != nil {
		t.Fatalf("load terminal compaction lineage: %v", err)
	}
	if failedNormalContexts != 1 || failedCompactionContexts != 1 || normalContexts != 1 || compactionContexts != 1 {
		t.Fatalf(
			"terminal compaction lineage failed normal/compaction=%d/%d context rows=%d/%d, want 1/1 and 1/1",
			failedNormalContexts,
			failedCompactionContexts,
			normalContexts,
			compactionContexts,
		)
	}
}

func TestAgentExecutorResolvesPinnedAgentConfigModel(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgent(t, ctx, "openai/pinned-kernel-model", fixture.Now)
	currentConfig := fixture.currentAgentConfig(t, ctx, agentID)
	currentRevisionID := currentRevisionIDForKernelConfig(t, ctx, fixture.Store, currentConfig)
	pinnedModel := &sequenceKernelModel{
		providerModelSlug: "pinned-kernel-model",
		responses: []model.Response{{
			ID:         "resp_pinned_model",
			Content:    []model.ResponsePart{{Type: "text", Text: "pinned model accepted"}},
			StopReason: model.StopReasonEndTurn,
		}},
	}
	wrongModel := &sequenceKernelModel{
		providerModelSlug: "worker-default-model",
		responses: []model.Response{{
			ID:         "resp_wrong_model",
			Content:    []model.ResponsePart{{Type: "text", Text: "wrong model should not be called"}},
			StopReason: model.StopReasonEndTurn,
		}},
	}
	turn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"use the pinned config model",
		fixture.Now.Add(time.Second),
	)
	executor := AgentExecutor{
		Store: fixture.Store,
		ModelResolver: staticTestModelResolver{Clients: []model.ResolvedClient{
			{
				Client:                    wrongModel,
				ConfiguredModelRevisionID: kernelTestID("wrong-pinned-model-revision").String(),
			},
			{
				Client:                    pinnedModel,
				ConfiguredModelRevisionID: currentRevisionID.String(),
			},
		}},
		ToolExecutor: tools.Executor{Store: fixture.Store},
		Now:          func() time.Time { return fixture.Now.Add(2 * time.Second) },
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute pinned model turn: %v", err)
	}
	if pinnedModel.respondedCount() != 1 {
		t.Fatalf("pinned model prepared %d requests, want 1", pinnedModel.respondedCount())
	}
	if wrongModel.respondedCount() != 0 {
		t.Fatalf("worker default model prepared %d requests, want 0", wrongModel.respondedCount())
	}
	var apiFormat, providerModelSlug string
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT provider_config.api_format, revision.provider_model_slug
		FROM model_call_contexts context
		JOIN configured_model_revisions revision
		  ON revision.org_id = context.org_id
		 AND revision.id = context.configured_model_revision_id
		JOIN model_provider_configs provider_config
		  ON provider_config.org_id = revision.org_id
		 AND provider_config.id = revision.model_provider_config_id
		JOIN LATERAL (
		  SELECT opening.turn_id
		  FROM agent_events opening
		  WHERE opening.agent_id = context.agent_id
		    AND opening.is_opening_event
		    AND opening.sequence <= context.input_event_sequence
		  ORDER BY opening.sequence DESC, opening.id DESC
		  LIMIT 1
		) context_turn ON true
		WHERE context.project_id = $1
		  AND context.agent_id = $2
		  AND context_turn.turn_id = $3
		  AND context.state = 'succeeded'
	`, kernelTestProjectID, agentID, turn.TurnID).Scan(&apiFormat, &providerModelSlug); err != nil {
		t.Fatalf("load pinned model context: %v", err)
	}
	if apiFormat != "openai-responses" || providerModelSlug != "pinned-kernel-model" {
		t.Fatalf(
			"model context API format/provider model slug = %s/%s, want openai-responses/pinned-kernel-model",
			apiFormat,
			providerModelSlug,
		)
	}
}
