//go:build integration

package kernel

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

type stepCommitTrace struct{ commits atomic.Int64 }
type stepCommitKey struct{}

func (c *stepCommitTrace) TraceQueryStart(
	ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData,
) context.Context {
	return context.WithValue(ctx, stepCommitKey{}, strings.EqualFold(strings.TrimSpace(data.SQL), "commit"))
}
func (c *stepCommitTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if commit, _ := ctx.Value(stepCommitKey{}).(bool); commit && data.Err == nil {
		c.commits.Add(1)
	}
}

func TestModelAndListAgentsCommitCount(t *testing.T) {
	for _, merged := range []bool{false, true} {
		name := "before"
		if merged {
			name = "after"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			f := newKernelFixture(t, ctx)
			profile := f.createConfigAndProfileBookmark(t, ctx, "Commit Count", "commit-count", `instruction: List agents.
model:
  provider_config: openai-prod
  name: commit-count
subagents:
  fork:
    type: self
`)
			launch, err := f.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
				ProjectID: kernelTestProjectID, ProfileID: profile.ID, AgentConfigID: profile.CurrentConfigID,
				LaunchedBy: kernelTestUserPrincipal(kernelTestUserID), IdempotencyKey: "count",
			})
			require.NoError(t, err)
			_, _, _, err = f.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{ProjectID: kernelTestProjectID, AgentID: launch.Agent.ID, Actor: kernelTestOmnaraActorParams(t, kernelTestUserID), ContentBlocks: json.RawMessage(`[{"type":"text","text":"list agents"}]`), IdempotencyKey: "count-input"})
			require.NoError(t, err)
			trace := &stepCommitTrace{}
			config := f.Pool.Config()
			config.ConnConfig.Tracer = trace
			pool, err := pgxpool.NewWithConfig(ctx, config)
			require.NoError(t, err)
			defer pool.Close()
			counted := storage.NewStore(pool)
			client := &sequenceKernelModel{providerModelSlug: "commit-count", responses: []model.Response{{ID: "count-response", StopReason: model.StopReasonToolUse, Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{{ID: "count-call", Name: toolcatalog.ToolNameListAgents, Input: json.RawMessage(`{}`)}})}}}
			executor := AgentExecutor{Store: counted, ModelResolver: liveTestModelResolver(counted, client)}
			var claim executionstore.ClaimedAgentWork
			var found bool
			if merged {
				claim, found, err = counted.Execution().ClaimNextAgentWork(ctx, kernelTestClaimInput(f.Now))
			} else {
				claim, found, err = counted.Execution().IntegrationClaimUnprepared(ctx, kernelTestClaimInput(f.Now))
			}
			require.NoError(t, err)
			require.True(t, found)
			work := modelWorkExecutionFromClaimForKernelTest(claim, f.Now)
			if merged {
				disposition, err := executor.ExecuteModelWorkStep(ctx, work)
				require.NoError(t, err)
				require.Equal(t, executionstore.StepFinished, disposition)
				require.EqualValues(t, 2, trace.commits.Load())
			} else {
				require.NoError(t, executor.ExecuteModelWork(ctx, work))
				require.NoError(t, counted.Execution().ReleaseAgentRuntimeLock(
					ctx, claim.ProjectID, claim.AgentID, claim.RuntimeLock.ID,
				))
				require.EqualValues(t, 4, trace.commits.Load())
			}
			claim, found, err = counted.Execution().ClaimNextAgentWork(ctx, kernelTestClaimInput(f.Now))
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, executionstore.AgentWorkTool, claim.Kind)
			tool := ToolWorkExecution{
				RootAgentID: claim.RootAgentID,
				Prepared:    claim.Tool.Prepared, ProjectID: claim.ProjectID, AgentID: claim.AgentID,
				TurnID: claim.Tool.TurnID, ModelCallContextID: claim.Tool.ModelCallContextID,
				ModelOutputID: claim.Tool.ModelOutputID,
				SourceEventID: claim.Tool.SourceEventID, RuntimeLockID: claim.RuntimeLock.ID,
			}
			if merged {
				disposition, err := executor.ExecuteToolWorkStep(ctx, tool)
				require.NoError(t, err)
				require.Equal(t, executionstore.StepFinished, disposition)
				require.EqualValues(t, 4, trace.commits.Load())
			} else {
				require.NoError(t, executor.ExecuteToolWork(ctx, tool))
				require.NoError(t, counted.Execution().ReleaseAgentRuntimeLock(
					ctx, claim.ProjectID, claim.AgentID, claim.RuntimeLock.ID,
				))
				require.EqualValues(t, 8, trace.commits.Load())
			}
			t.Logf("%s: %d explicit commits for one model step plus list_agents", name, trace.commits.Load())
			var results, wakes int
			require.NoError(t, f.Pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM tool_call_results WHERE agent_id=$1 AND outcome='succeeded'),
 (SELECT count(*) FROM agent_wakeups WHERE agent_id=$1)`, launch.Agent.ID).Scan(&results, &wakes))
			require.Equal(t, 1, results)
			require.Equal(t, 1, wakes)
			if merged {
				newer, found, err := counted.Execution().ClaimNextAgentWork(ctx, kernelTestClaimInput(f.Now))
				require.NoError(t, err)
				require.True(t, found)
				before := trace.commits.Load()
				replayInput := executionstore.ExecuteToolCallInput{
					RootAgentID: claim.RootAgentID, ProjectID: claim.ProjectID, AgentID: claim.AgentID,
					ToolCallID: claim.Tool.Prepared.FirstCall.ID, RuntimeLockID: claim.RuntimeLock.ID,
					BatchOutputID: claim.Tool.ModelOutputID, Authorize: true,
				}
				replay := func(outcome executionstore.ToolResultOutcome) (executionstore.ExecuteToolCallResult, error) {
					return counted.Execution().ExecuteToolCall(ctx, replayInput,
						func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
							return executionstore.CompleteToolCallInExecution(executionstore.ToolCallCompletionInput{
								Outcome: outcome, ResultContentParts: json.RawMessage(`[{"type":"text","text":"replayed"}]`),
							}), nil
						})
				}
				resolved, err := replay(executionstore.ToolResultOutcomeSucceeded)
				require.NoError(t, err)
				require.False(t, resolved.Applied)
				require.Equal(t, executionstore.StepFinished, resolved.StepDisposition)
				require.Equal(t, before, trace.commits.Load())
				_, err = replay(executionstore.ToolResultOutcomeFailed)
				require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
				var exists bool
				require.NoError(t, f.Pool.QueryRow(ctx,
					`SELECT EXISTS(SELECT 1 FROM agent_runtime_locks WHERE id=$1)`, newer.RuntimeLock.ID).Scan(&exists))
				require.True(t, exists)
			}
		})
	}
}
