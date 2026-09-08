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
	"github.com/stretchr/testify/require"
)

func truncatedKernelResponse() model.Response {
	return model.Response{
		ID: "truncated-response", StopReason: model.StopReasonMaxTokens,
		Content: []model.ResponsePart{
			{Type: model.ResponsePartTypeText, Text: "partial response"},
			{
				Type:           model.ResponsePartTypeToolCall,
				ProviderCallID: "complete-call",
				ToolName:       "run_command",
				ToolInput:      json.RawMessage(`{"command":"echo complete"}`),
			},
			{
				Type:           model.ResponsePartTypeToolCall,
				ProviderCallID: "partial-call",
				ToolName:       "run_command",
				ToolInput:      json.RawMessage(`{"command":`),
			},
		},
		ProviderReplay: json.RawMessage(`[{"type":"message","content":"partial"}]`),
		Usage:          model.Usage{InputTokens: 100, OutputTokens: 8192, ReasoningTokens: 1000},
	}
}

func TestOutputTruncationContinuesAcrossClaimsAndStopsAtDurableBound(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgent(t, ctx, "openai/output-recovery", fixture.Now)
	work := fixture.admitContentInputTurn(t, ctx, agentID, userID, "complete the task", fixture.Now)
	turnID := work.TurnID
	for attempt := range 3 {
		client := &sequenceKernelModel{
			providerModelSlug: "output-recovery",
			responses: []model.Response{
				truncatedKernelResponse(),
			},
		}

		publisher := &capturingStreamPublisher{}
		for index, callID := range []string{"complete-call", "partial-call"} {
			client.streamEvents = append(client.streamEvents, model.StreamEvent{
				Kind: model.StreamEventBlockStart, BlockIndex: index,
				Block: &model.StreamBlock{Kind: model.StreamBlockToolUse, ToolCallID: callID, ToolName: "run_command"},
			})
		}
		executor := AgentExecutor{
			Store: fixture.Store,
			ModelResolver: liveTestModelResolver(
				fixture.Store,
				client,
			),
			StreamPublisher: publisher,
		}

		require.NoError(t, executor.ExecuteModelWork(ctx, work))

		previews := 0
		for _, frame := range publisher.envelopes(t) {
			if frame.Event.Block != nil && frame.Event.Block.Kind == model.StreamBlockToolUse {
				previews++
				if frame.Event.Block.ToolCallID == "complete-call" || frame.Event.Block.ToolCallID == "partial-call" {
					t.Fatal("preview did not use a public tool identity")
				}
			}
		}
		if previews != 2 {
			t.Fatalf("provisional tool previews=%d", previews)
		}
		// Stale completed work must not send again.
		if err := executor.ExecuteModelWork(ctx, work); err != nil && !errors.Is(err, storeerr.ErrAgentNotAdvanceable) {
			t.Fatal(err)
		}
		if client.respondedCount() != 1 {
			t.Fatalf("provider sends=%d", client.respondedCount())
		}
		if attempt > 0 {
			bundle := client.responded[0].Bundle
			if _, err := modelcontext.CanonicalHistory(bundle); err != nil {
				t.Fatal(err)
			}
			last := bundle.Messages[len(bundle.Messages)-1]
			if last.Role != modelprotocol.RoleUser || !strings.Contains(string(last.Content), "smaller tool calls") {
				t.Fatalf("feedback=%+v", last)
			}
			if len(client.responded[0].ProviderReplays) != 0 {
				t.Fatal("truncated provider replay reached successor")
			}
		}
		fixture.releaseModelRuntimeLock(t, ctx, work)
		if attempt < 2 {
			claim := claimNextAgentWorkForKernelTest(t, ctx, fixture, agentID, executionstore.AgentWorkModel)
			work = modelWorkExecutionFromClaimForKernelTest(claim, fixture.Now.Add(time.Second))
			if work.Kind != executionstore.ModelWorkContinue || work.TurnID != turnID {
				t.Fatalf("successor=%+v", work)
			}
		}
	}
	var outputs, continuations, feedback, exhausted, toolCalls, inputTokens, outputTokens, reasoningTokens int
	require.NoError(t, fixture.Pool.QueryRow(ctx, `
 SELECT count(*),count(*) FILTER(WHERE output.continue_after_truncation),
        sum(context.input_tokens_total),sum(context.output_tokens_total),sum(context.reasoning_output_tokens)
 FROM model_outputs output JOIN model_call_contexts context
 ON context.agent_id=output.agent_id AND context.id=output.model_call_context_id
 WHERE output.agent_id=$1 AND output.stop_reason='max_tokens'
 AND context.state='succeeded' AND output.provider_replay IS NULL`, agentID).
		Scan(&outputs, &continuations, &inputTokens, &outputTokens, &reasoningTokens))
	require.NoError(t, fixture.Pool.QueryRow(
		ctx,
		`SELECT count(*), count(*) FILTER(WHERE text_content LIKE '%Automatic continuation stopped%') FROM content_blocks WHERE agent_id=$1 AND block_kind='error'`,
		agentID,
	).Scan(
		&feedback,
		&exhausted,
	))
	require.NoError(t, fixture.Pool.QueryRow(
		ctx,
		`SELECT count(*) FROM tool_calls WHERE agent_id=$1`,
		agentID,
	).Scan(&toolCalls))
	if outputs != 3 ||
		continuations != 2 ||
		feedback != 3 ||
		exhausted != 1 ||
		toolCalls != 0 ||
		inputTokens != 300 ||
		outputTokens != 24576 ||
		reasoningTokens != 3000 {
		t.Fatalf(
			"outputs=%d continued=%d feedback=%d exhausted=%d tools=%d usage=%d/%d/%d",
			outputs,
			continuations,
			feedback,
			exhausted,
			toolCalls,
			inputTokens,
			outputTokens,
			reasoningTokens,
		)
	}
	_, found, err := fixture.Store.Execution().ClaimNextAgentWork(ctx, kernelTestClaimInput(time.Time{}))
	if found || (err != nil && !errors.Is(err, storeerr.ErrNoClaimableAgentWakeup)) {
		t.Fatalf("exhausted work found=%v err=%v", found, err)
	}
	// A new user input resets the bound, and exhaustion feedback retains its
	// harness role when that input starts another attempt.
	work = fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"continue with smaller writes",
		fixture.Now.Add(time.Minute),
	)
	client := &sequenceKernelModel{providerModelSlug: "output-recovery", responses: []model.Response{
		truncatedKernelResponse(), {
			ID:         "completed",
			StopReason: model.StopReasonEndTurn,
			Content: []model.ResponsePart{
				{
					Type: model.ResponsePartTypeText,
					Text: "task complete",
				},
			},
		},
	}}
	executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	sawExhaustion := false
	for _, message := range client.responded[0].Bundle.Messages {
		if strings.Contains(string(message.Content), "Automatic continuation stopped") {
			sawExhaustion = true
			if message.Role != modelprotocol.RoleUser {
				t.Fatalf("exhaustion feedback role=%s", message.Role)
			}
		}
	}
	if !sawExhaustion {
		t.Fatal("missing prior exhaustion feedback")
	}
	fixture.releaseModelRuntimeLock(t, ctx, work)
	work = executeNextModelWork(t, ctx, fixture, executor, work)
	fixture.releaseModelRuntimeLock(t, ctx, work)
	if client.respondedCount() != 2 {
		t.Fatalf("reset journey sends=%d", client.respondedCount())
	}
	_, found, err = fixture.Store.Execution().ClaimNextAgentWork(ctx, kernelTestClaimInput(time.Time{}))
	if found || (err != nil && !errors.Is(err, storeerr.ErrNoClaimableAgentWakeup)) {
		t.Fatalf("completed work found=%v err=%v", found, err)
	}

}

func TestCancelOutputContinuationBeforeAndAfterRuntimeClaim(t *testing.T) {
	for _, afterClaim := range []bool{false, true} {
		name := "before claim"
		if afterClaim {
			name = "after claim"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newKernelFixture(t, ctx)
			agentID, userID := fixture.createAgent(t, ctx, "openai/cancel-output-recovery", fixture.Now)
			work := fixture.admitContentInputTurn(t, ctx, agentID, userID, "complete the task", fixture.Now)
			client := &sequenceKernelModel{
				providerModelSlug: "cancel-output-recovery",
				responses: []model.Response{
					truncatedKernelResponse(),
				},
			}
			executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
			require.NoError(t, executor.ExecuteModelWork(ctx, work))
			fixture.releaseModelRuntimeLock(t, ctx, work)
			if afterClaim {
				work = modelWorkExecutionFromClaimForKernelTest(
					claimNextAgentWorkForKernelTest(
						t,
						ctx,
						fixture,
						agentID,
						executionstore.AgentWorkModel,
					),
					fixture.Now,
				)
			}
			canceled, err := fixture.Store.Execution().CancelAgent(
				ctx,
				executionstore.CancelAgentInput{
					ProjectID: kernelTestProjectID,
					AgentID:   agentID,
					Actor: kernelTestOmnaraActorParams(
						t,
						userID,
					),
				},
			)
			if err != nil || !canceled.Affected {
				t.Fatalf("cancel=%+v err=%v", canceled, err)
			}
			if afterClaim {
				err := executor.ExecuteModelWork(ctx, work)
				if err == nil {
					t.Fatal("canceled claimed work was executable")
				}
			}
			var next int
			require.NoError(t, fixture.Pool.QueryRow(
				ctx,
				`SELECT count(*) FROM agent_next_model_work($1,$2)`,
				kernelTestProjectID,
				agentID,
			).Scan(&next))
			if next != 0 || client.respondedCount() != 1 {
				t.Fatalf("work=%d sends=%d", next, client.respondedCount())
			}
		})
	}
}

func TestOutputContinuationIsConsumedBySuccessorTerminalFailure(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgent(t, ctx, "openai/failed-output-recovery", fixture.Now)
	work := fixture.admitContentInputTurn(t, ctx, agentID, userID, "complete the task", fixture.Now)
	client := &sequenceKernelModel{
		providerModelSlug: "failed-output-recovery",
		responses: []model.Response{
			truncatedKernelResponse(),
		},
	}
	executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	fixture.releaseModelRuntimeLock(t, ctx, work)
	client = &sequenceKernelModel{
		providerModelSlug: "failed-output-recovery",
		prepareErr: model.ProviderError{
			Kind:    model.ErrorKindInvalidRequest,
			Code:    "invalid-test-request",
			Message: "invalid request",
		},
	}
	executor.ModelResolver = liveTestModelResolver(fixture.Store, client)
	work = executeNextModelWork(t, ctx, fixture, executor, work)
	fixture.releaseModelRuntimeLock(t, ctx, work)
	var next, failures int
	require.NoError(t, fixture.Pool.QueryRow(
		ctx,
		`SELECT count(*) FROM agent_next_model_work($1,$2)`,
		kernelTestProjectID,
		agentID,
	).Scan(&next))
	require.NoError(t, fixture.Pool.QueryRow(
		ctx,
		`SELECT count(*) FROM model_outputs WHERE agent_id=$1 AND stop_reason='error'`,
		agentID,
	).Scan(&failures))
	if next != 0 || failures != 1 || client.respondedCount() != 0 {
		t.Fatalf("work=%d failures=%d sends=%d", next, failures, client.respondedCount())
	}
}

func TestOutputContinuationBoundSurvivesRetryAndCompaction(t *testing.T) {
	for _, compact := range []bool{false, true} {
		name := "retry"
		if compact {
			name = "compaction"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newKernelFixture(t, ctx)
			agentID, userID := fixture.createAgent(t, ctx, "openai/recovery-bound", fixture.Now)
			client := &sequenceKernelModel{providerModelSlug: "recovery-bound", responses: []model.Response{
				{
					ID:         "seed",
					StopReason: model.StopReasonEndTurn,
					Content: []model.ResponsePart{
						{
							Type: "text",
							Text: "seed history",
						},
					},
				},
				truncatedKernelResponse(),
			}}
			executor := AgentExecutor{
				Store: fixture.Store,
				ModelResolver: liveTestModelResolver(
					fixture.Store,
					client,
				),
				ModelRetryDelay: immediateKernelModelRetryDelay,
			}
			work := fixture.admitContentInputTurn(t, ctx, agentID, userID, "establish earlier history", fixture.Now)
			require.NoError(t, executor.ExecuteModelWork(ctx, work))
			fixture.releaseModelRuntimeLock(t, ctx, work)
			work = fixture.admitContentInputTurn(
				t,
				ctx,
				agentID,
				userID,
				"complete the task",
				fixture.Now.Add(time.Second),
			)
			require.NoError(t, executor.ExecuteModelWork(ctx, work))
			fixture.releaseModelRuntimeLock(t, ctx, work)
			client = &sequenceKernelModel{
				providerModelSlug: "recovery-bound",
				responses: []model.Response{
					truncatedKernelResponse(),
					truncatedKernelResponse(),
				},
			}
			if compact {
				client.responses = append([]model.Response{
					{ID: "overflow", StopReason: model.StopReasonContextWindow},
					{
						ID:         "summary",
						StopReason: model.StopReasonEndTurn,
						Content: []model.ResponsePart{
							{
								Type: "text",
								Text: "Earlier history established the task.",
							},
						},
					},
				}, client.responses...)
			} else {
				client.errs = []error{
					model.ProviderError{
						Kind:    model.ErrorKindTransient,
						Message: "temporary provider failure",
					},
				}
			}
			executor.ModelResolver = liveTestModelResolver(fixture.Store, client)
			work = executeNextModelWork(t, ctx, fixture, executor, work)
			fixture.releaseModelRuntimeLock(t, ctx, work)
			// A retry and a checkpoint are not semantic progress: both resume the
			// same output frontier, preserving the consecutive truncation count.
			claim := claimNextAgentWorkForKernelTest(t, ctx, fixture, agentID, executionstore.AgentWorkModel)
			work = modelWorkExecutionFromClaimForKernelTest(claim, fixture.Now.Add(2*time.Second))
			if !compact && work.Kind != executionstore.ModelWorkResume {
				t.Fatalf("successor kind=%s", work.Kind)
			}
			require.NoError(t, executor.ExecuteModelWork(ctx, work))
			fixture.releaseModelRuntimeLock(t, ctx, work)
			work = executeNextModelWork(t, ctx, fixture, executor, work)
			fixture.releaseModelRuntimeLock(t, ctx, work)
			var outputs, continued, checkpoints, next int
			require.NoError(t, fixture.Pool.QueryRow(
				ctx,
				`SELECT count(*),count(*) FILTER(WHERE continue_after_truncation) FROM model_outputs WHERE agent_id=$1 AND stop_reason='max_tokens'`,
				agentID,
			).Scan(
				&outputs,
				&continued,
			))
			require.NoError(t, fixture.Pool.QueryRow(
				ctx,
				`SELECT count(*) FROM context_checkpoints WHERE agent_id=$1`,
				agentID,
			).Scan(&checkpoints))
			require.NoError(t, fixture.Pool.QueryRow(
				ctx,
				`SELECT count(*) FROM agent_next_model_work($1,$2)`,
				kernelTestProjectID,
				agentID,
			).Scan(&next))
			wantCheckpoints, wantSends := 0, 3
			if compact {
				wantCheckpoints, wantSends = 1, 4
			}
			if outputs != 3 ||
				continued != 2 ||
				checkpoints != wantCheckpoints ||
				next != 0 ||
				client.respondedCount() != wantSends {
				t.Fatalf(
					"outputs=%d continued=%d checkpoints=%d next=%d sends=%d",
					outputs,
					continued,
					checkpoints,
					next,
					client.respondedCount(),
				)
			}
		})
	}
}

func TestOutputContinuationBoundResetsOnSteeringAndToolProgress(t *testing.T) {
	for _, useTool := range []bool{false, true} {
		name := "steering"
		if useTool {
			name = "tool result"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newKernelFixture(t, ctx)
			agentID, userID := fixture.createAgent(t, ctx, "openai/recovery-progress", fixture.Now, "list_machines")
			client := &sequenceKernelModel{
				providerModelSlug: "recovery-progress",
				responses: []model.Response{
					truncatedKernelResponse(),
					truncatedKernelResponse(),
				},
			}
			if useTool {
				client.responses = append(
					client.responses,
					model.Response{
						ID:         "progress",
						StopReason: model.StopReasonToolUse,
						Content: []model.ResponsePart{
							{
								Type:           model.ResponsePartTypeToolCall,
								ProviderCallID: "accepted-call",
								ToolName:       "list_machines",
								ToolInput:      json.RawMessage(`{}`),
							},
						},
					},
				)
			}
			client.responses = append(
				client.responses,
				truncatedKernelResponse(),
				truncatedKernelResponse(),
				truncatedKernelResponse(),
			)
			executor := AgentExecutor{
				Store: fixture.Store,
				ModelResolver: liveTestModelResolver(
					fixture.Store,
					client,
				),
				ToolExecutor: tools.Executor{
					Store: fixture.Store,
				},
			}
			work := fixture.admitContentInputTurn(t, ctx, agentID, userID, "complete the task", fixture.Now)
			require.NoError(t, executor.ExecuteModelWork(ctx, work))
			fixture.releaseModelRuntimeLock(t, ctx, work)
			work = executeNextModelWork(t, ctx, fixture, executor, work)
			fixture.releaseModelRuntimeLock(t, ctx, work)
			if useTool {
				work = executeNextModelWork(t, ctx, fixture, executor, work)
				toolWork := nextToolWorkExecution(t, ctx, fixture, work)
				require.NoError(t, executor.ExecuteToolWork(ctx, toolWork))
				require.NoError(t, fixture.Store.Execution().ReleaseAgentRuntimeLock(
					ctx,
					toolWork.ProjectID,
					toolWork.AgentID,
					toolWork.RuntimeLockID,
				))
				work = modelWorkExecutionFromClaimForKernelTest(
					claimNextAgentWorkForKernelTest(
						t,
						ctx,
						fixture,
						agentID,
						executionstore.AgentWorkModel,
					),
					fixture.Now,
				)
			} else {
				work = fixture.admitSteeringInputsTurn(
					t,
					ctx,
					agentID,
					userID,
					[]string{
						"use smaller calls",
					},
					fixture.Now.Add(time.Second),
				)
			}
			for attempt := range 3 {
				if attempt > 0 {
					work = modelWorkExecutionFromClaimForKernelTest(
						claimNextAgentWorkForKernelTest(
							t,
							ctx,
							fixture,
							agentID,
							executionstore.AgentWorkModel,
						),
						fixture.Now,
					)
				}
				require.NoError(t, executor.ExecuteModelWork(ctx, work))
				fixture.releaseModelRuntimeLock(t, ctx, work)
			}
			var outputs, continued, next int
			require.NoError(t, fixture.Pool.QueryRow(
				ctx,
				`SELECT count(*),count(*) FILTER(WHERE continue_after_truncation) FROM model_outputs WHERE agent_id=$1 AND stop_reason='max_tokens'`,
				agentID,
			).Scan(
				&outputs,
				&continued,
			))
			require.NoError(t, fixture.Pool.QueryRow(
				ctx,
				`SELECT count(*) FROM agent_next_model_work($1,$2)`,
				kernelTestProjectID,
				agentID,
			).Scan(&next))
			if outputs != 5 || continued != 4 || next != 0 {
				t.Fatalf("outputs=%d continued=%d next=%d", outputs, continued, next)
			}
		})
	}
}
