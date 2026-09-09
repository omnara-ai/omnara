//go:build integration

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func truncatedKernelResponse() model.Response {
	return model.Response{
		ID: "truncated-response", StopReason: model.StopReasonMaxTokens,
		Content: []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "partial response"}},
		Usage:   model.Usage{InputTokens: 100, OutputTokens: 128, ReasoningTokens: 64},
	}
}

func TestLargeOutputCompactsWithinAvailableContextAndContinues(t *testing.T) {
	for _, tc := range []struct {
		name             string
		sourceStopReason model.StopReason
		partialSummary   bool
	}{
		{name: "completed summary", sourceStopReason: model.StopReasonMaxTokens},
		{name: "partial summary after answer cutoff", sourceStopReason: model.StopReasonMaxTokens, partialSummary: true},
		{name: "partial summary after completed answer", sourceStopReason: model.StopReasonEndTurn, partialSummary: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newKernelFixture(t, ctx)
			agentID, userID := fixture.createAgentWithModelOptions(t, ctx, "openai/large-output", fixture.Now,
				kernelConfiguredModelOptions{ContextWindowTokens: new(32_000), MaxOutputTokens: new(24_000)})
			largeText := strings.Repeat("work ", 19_200)
			const summary = `## Goal
Complete the requested work and confirm completion.
## Instructions
Finish the task before reporting success.
## Progress
A large response was produced. Check what remains before concluding.
## Relevant Artifacts
The previous response contains the work so far. No external artifacts were created.
## Next Steps
Finish any remaining work and provide a concise completion message.`
			summaryResponse := completeProgressiveSummaryResponse(summary)
			responses := []model.Response{
				{ID: "large-source", StopReason: tc.sourceStopReason,
					Content: []model.ResponsePart{{Type: "text", Text: largeText}},
					Usage:   model.Usage{InputTokens: 1_000, OutputTokens: 24_000}},
			}
			if tc.partialSummary {
				summaryResponse.StopReason = model.StopReasonMaxTokens
				responses = append(responses, summaryResponse, completeProgressiveSummaryResponse("Finish the task."))
			}
			responses = append(responses, summaryResponse, model.Response{
				ID: "finished", StopReason: model.StopReasonEndTurn,
				Content: []model.ResponsePart{{Type: "text", Text: "Task finished."}},
			})
			client := &sequenceKernelModel{
				providerModelSlug: "large-output",
				capabilities:      model.Capabilities{ContextWindowTokens: 32_000, MaxOutputTokens: new(24_000)},
				responses:         responses,
			}
			executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
			work := fixture.admitContentInputTurn(t, ctx, agentID, userID,
				"Complete the work and then confirm it is finished.", fixture.Now)
			for step := range 5 {
				require.NoError(t, executor.ExecuteModelWork(ctx, work))
				fixture.releaseModelRuntimeLock(t, ctx, work)
				if step == 0 && tc.sourceStopReason == model.StopReasonEndTurn {
					work = fixture.admitContentInputTurn(t, ctx, agentID, userID, "Continue the task.", fixture.Now.Add(time.Second))
					continue
				}
				if pendingModelWork(t, ctx, fixture, agentID) == 0 {
					break
				}
				claim := claimNextAgentWorkForKernelTest(t, ctx, fixture, agentID, executionstore.AgentWorkModel)
				work = modelWorkExecutionFromClaimForKernelTest(claim, work.Now.Add(time.Second))
			}
			wantRequests := 3
			if tc.partialSummary {
				wantRequests = 5
			}
			require.Equal(t, wantRequests, client.respondedCount())
			require.Equal(t, 24_000, client.responded[0].Policy.MaxOutputTokens)
			largeSummary, continuation := client.responded[wantRequests-2], client.responded[wantRequests-1]
			require.True(t, isCompactionRequestBundle(largeSummary.Bundle))
			require.True(t, strings.Contains(string(largeSummary.ProviderRequest), strings.TrimSpace(largeText)),
				"summary request must retain the complete large output")
			require.Positive(t, largeSummary.Policy.MaxOutputTokens)
			require.Less(t, largeSummary.Policy.MaxOutputTokens, 16_000)
			summaryInput := modelcontext.EstimatePreparedRequest(largeSummary.ProviderRequest, nil)
			require.Greater(t, summaryInput, 24_000)
			margin := modelcontext.DefaultSafetyMarginTokens(32_000)
			require.LessOrEqual(t, summaryInput+largeSummary.Policy.MaxOutputTokens+margin, 32_000)
			require.NotNil(t, continuation.Bundle.ContextCheckpoint)
			require.Equal(t, summary, continuation.Bundle.ContextCheckpoint.Summary)
			wantCutoffNotice := tc.sourceStopReason == model.StopReasonMaxTokens
			require.Equal(t, wantCutoffNotice, continuation.Bundle.ContextCheckpoint.EndsWithOutputLimit)
			checkpointText := modelcontext.ProjectedCheckpointContent(*continuation.Bundle.ContextCheckpoint)
			require.Equal(t, wantCutoffNotice, strings.Contains(checkpointText, "[Automatic Omnara harness notice]"))
			require.False(t, strings.Contains(string(continuation.ProviderRequest), strings.TrimSpace(largeText)),
				"continuation should use the checkpoint")
			require.Equal(t, 24_000, continuation.Policy.MaxOutputTokens)
			require.Zero(t, pendingModelWork(t, ctx, fixture, agentID))
			var preserved, finished, failed, cutoffs int
			require.NoError(t, fixture.Pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE block.text_content = $2),
		       count(*) FILTER (WHERE block.text_content = 'Task finished.' AND output.stop_reason = 'end_turn'),
		       count(*) FILTER (WHERE output.stop_reason = 'error'),
		       count(*) FILTER (WHERE output.stop_reason = 'max_tokens')
		FROM model_outputs output
		LEFT JOIN content_blocks block ON block.agent_id = output.agent_id AND block.owner_model_output_id = output.id
		WHERE output.agent_id = $1`, agentID, largeText).Scan(&preserved, &finished, &failed, &cutoffs))
			require.Equal(t, 1, preserved)
			require.Equal(t, 1, finished)
			require.Zero(t, failed)
			wantCutoffs := 0
			if wantCutoffNotice {
				wantCutoffs = 1
			}
			require.Equal(t, wantCutoffs, cutoffs)
		})
	}
}

func TestOutputLimitContinuesAcrossClaimsUntilEndTurn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content []model.ResponsePart
	}{
		{name: "empty"},
		{name: "reasoning", content: []model.ResponsePart{
			{Type: model.ResponsePartTypeReasoning, Text: "finished thinking"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newKernelFixture(t, ctx)
			agentID, userID := fixture.createAgent(t, ctx, "openai/output-recovery", fixture.Now)
			work := fixture.admitContentInputTurn(t, ctx, agentID, userID, "complete the task", fixture.Now)
			client := &sequenceKernelModel{providerModelSlug: "output-recovery", responses: []model.Response{
				truncatedKernelResponse(),
				{ID: "thinking", StopReason: model.StopReasonMaxTokens,
					Content: []model.ResponsePart{{Type: model.ResponsePartTypeReasoning, Text: "thinking"}}},
				{ID: "empty", StopReason: model.StopReasonMaxTokens},
				truncatedKernelResponse(),
				{ID: "done", StopReason: model.StopReasonEndTurn, Content: tc.content},
			}}
			executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
			var openingInputs int
			require.NoError(t, fixture.Pool.QueryRow(ctx,
				`SELECT count(*) FROM agent_inputs WHERE agent_id=$1`, agentID).Scan(&openingInputs))
			responseCount := len(client.responses)
			for attempt := range responseCount {
				require.NoError(t, executor.ExecuteModelWork(ctx, work))
				// Replaying completed work must not issue a second provider request.
				err := executor.ExecuteModelWork(ctx, work)
				require.True(t, err == nil || errors.Is(err, storeerr.ErrAgentNotAdvanceable), "%v", err)
				require.Equal(t, attempt+1, client.respondedCount())
				history, err := modelcontext.CanonicalHistory(client.responded[attempt].Bundle)
				require.NoError(t, err)
				notices := 0
				for _, entry := range history {
					if entry.Message.Role == modelprotocol.RoleUser &&
						strings.Contains(string(entry.Message.Content), "Automatic Omnara harness notice") {
						notices++
					}
				}
				require.Equal(t, attempt, notices)
				fixture.releaseModelRuntimeLock(t, ctx, work)
				if attempt < responseCount-1 {
					require.Equal(t, 1, pendingModelWork(t, ctx, fixture, agentID))
					claim := claimNextAgentWorkForKernelTest(t, ctx, fixture, agentID, executionstore.AgentWorkModel)
					next := modelWorkExecutionFromClaimForKernelTest(claim, fixture.Now.Add(time.Second))
					require.Equal(t, executionstore.ModelWorkContinue, next.Kind)
					require.Equal(t, work.TurnID, next.TurnID)
					work = next
				}
			}
			require.Zero(t, pendingModelWork(t, ctx, fixture, agentID))
			var cutoffs, failed, inputs int
			require.NoError(t, fixture.Pool.QueryRow(ctx, `
   SELECT count(*) FILTER (WHERE stop_reason='max_tokens'), count(*) FILTER (WHERE stop_reason='error')
   FROM model_outputs WHERE agent_id=$1`, agentID).Scan(&cutoffs, &failed))
			require.NoError(t, fixture.Pool.QueryRow(ctx,
				`SELECT count(*) FROM agent_inputs WHERE agent_id=$1`, agentID).Scan(&inputs))
			require.Equal(t, 4, cutoffs)
			require.Zero(t, failed)
			require.Equal(t, openingInputs, inputs, "notices must not create durable user inputs")
		})
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
			next := pendingModelWork(t, ctx, fixture, agentID)
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
	var failures int
	next := pendingModelWork(t, ctx, fixture, agentID)
	require.NoError(t, fixture.Pool.QueryRow(
		ctx,
		`SELECT count(*) FROM model_outputs WHERE agent_id=$1 AND stop_reason='error'`,
		agentID,
	).Scan(&failures))
	if next != 0 || failures != 1 || client.respondedCount() != 0 {
		t.Fatalf("work=%d failures=%d sends=%d", next, failures, client.respondedCount())
	}
}

func TestOutputContinuationSurvivesRetryAndCompaction(t *testing.T) {
	for _, compact := range []bool{false, true} {
		name := "retry"
		if compact {
			name = "compaction"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newKernelFixture(t, ctx)
			agentID, userID := fixture.createAgent(t, ctx, "openai/recovery-retry", fixture.Now)
			client := &sequenceKernelModel{providerModelSlug: "recovery-retry", responses: []model.Response{
				{
					ID:         "seed",
					StopReason: model.StopReasonMaxTokens,
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
			work = fixture.admitSteeringInputsTurn(
				t,
				ctx,
				agentID,
				userID,
				[]string{"complete the task"},
				fixture.Now.Add(time.Second),
			)
			require.NoError(t, executor.ExecuteModelWork(ctx, work))
			fixture.releaseModelRuntimeLock(t, ctx, work)
			client = &sequenceKernelModel{
				providerModelSlug: "recovery-retry",
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
			claim := claimNextAgentWorkForKernelTest(t, ctx, fixture, agentID, executionstore.AgentWorkModel)
			work = modelWorkExecutionFromClaimForKernelTest(claim, fixture.Now.Add(2*time.Second))
			if !compact && work.Kind != executionstore.ModelWorkResume {
				t.Fatalf("successor kind=%s", work.Kind)
			}
			require.NoError(t, executor.ExecuteModelWork(ctx, work))
			fixture.releaseModelRuntimeLock(t, ctx, work)
			work = executeNextModelWork(t, ctx, fixture, executor, work)
			fixture.releaseModelRuntimeLock(t, ctx, work)
			var outputs, checkpoints int
			require.NoError(t, fixture.Pool.QueryRow(
				ctx,
				`SELECT count(*) FROM model_outputs WHERE agent_id=$1 AND stop_reason='max_tokens'`,
				agentID,
			).Scan(
				&outputs,
			))
			require.NoError(t, fixture.Pool.QueryRow(
				ctx,
				`SELECT count(*) FROM context_checkpoints WHERE agent_id=$1`,
				agentID,
			).Scan(&checkpoints))
			next := pendingModelWork(t, ctx, fixture, agentID)
			wantCheckpoints, wantSends := 0, 3
			if compact {
				wantCheckpoints, wantSends = 1, 4
				bundle := client.responded[len(client.responded)-1].Bundle
				require.NotNil(t, bundle.ContextCheckpoint)
				require.True(t, bundle.ContextCheckpoint.EndsWithOutputLimit)
				prepared, err := (openairesponses.Client{
					ProviderModelSlug: "recovery-retry", EndpointPath: "/responses",
				}).Prepare(ctx, model.PrepareInput{Context: bundle})
				require.NoError(t, err)
				var request struct {
					Input []struct {
						Content json.RawMessage `json:"content"`
					} `json:"input"`
				}
				require.NoError(t, json.Unmarshal(prepared.Body, &request))
				require.NotEmpty(t, request.Input)
				var checkpointText string
				require.NoError(t, json.Unmarshal(request.Input[0].Content, &checkpointText))
				require.Contains(t, checkpointText, "</context_checkpoint>\n\n[Automatic Omnara harness notice]")
				require.Greater(t, len(request.Input), 1)
				require.Contains(t, string(request.Input[1].Content), "complete the task")

			}
			if outputs != 4 ||
				checkpoints != wantCheckpoints ||
				next != 1 ||
				client.respondedCount() != wantSends {
				t.Fatalf(
					"outputs=%d checkpoints=%d next=%d sends=%d",
					outputs,
					checkpoints,
					next,
					client.respondedCount(),
				)
			}
		})
	}
}

func TestNewInputSupersedesOutputContinuation(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgent(t, ctx, "openai/recovery-steering", fixture.Now)
	client := &sequenceKernelModel{providerModelSlug: "recovery-steering", responses: []model.Response{
		truncatedKernelResponse(), {ID: "done", StopReason: model.StopReasonEndTurn},
	}}
	executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client)}
	work := fixture.admitContentInputTurn(t, ctx, agentID, userID, "complete the task", fixture.Now)
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	fixture.releaseModelRuntimeLock(t, ctx, work)
	work = fixture.admitSteeringInputsTurn(
		t, ctx, agentID, userID, []string{"change direction"}, fixture.Now.Add(time.Second))
	require.NoError(t, executor.ExecuteModelWork(ctx, work))
	fixture.releaseModelRuntimeLock(t, ctx, work)
	history, err := modelcontext.CanonicalHistory(client.responded[1].Bundle)
	require.NoError(t, err)
	require.Contains(t, string(history[len(history)-2].Message.Content), "Automatic Omnara harness notice")
	require.Contains(t, string(history[len(history)-1].Message.Content), "change direction")
	require.Zero(t, pendingModelWork(t, ctx, fixture, agentID))
}

func pendingModelWork(t *testing.T, ctx context.Context, fixture kernelFixture, agentID storage.ID) int {
	t.Helper()
	var count int
	require.NoError(t, fixture.Pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_next_model_work($1,$2)`, kernelTestProjectID, agentID).Scan(&count))
	return count
}
