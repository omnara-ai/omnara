//go:build integration

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestModelAndListAgentsCommitCount(t *testing.T) {
	for _, name := range []string{"separate_transactions", "two_transaction_steps", "database_tool_claim"} {
		merged := name != "separate_transactions"
		inline := name == "database_tool_claim"
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
			trace := &integrationdb.QueryTrace{}
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
			if merged {
				require.EqualValues(t, 0, trace.ExecutionBase.Load(), "claim base loads")
				require.EqualValues(t, 1, trace.ExecutionFacts.Load(), "claim fact loads")
				require.Zero(t, trace.ExecutionContexts.Load(), "claim context loads")
			}
			work := modelWorkExecutionFromClaimForKernelTest(claim, f.Now)
			if merged {
				disposition, err := executor.ExecuteModelWorkStep(ctx, work)
				require.NoError(t, err)
				require.Equal(t, executionstore.StepFinished, disposition)
				require.EqualValues(t, 2, trace.Commits.Load())
			} else {
				require.NoError(t, executor.ExecuteModelWork(ctx, work))
				require.NoError(t, counted.Execution().ReleaseAgentRuntimeLock(
					ctx, claim.ProjectID, claim.AgentID, claim.RuntimeLock.ID,
				))
				require.EqualValues(t, 4, trace.Commits.Load())
			}
			t.Logf("%s: %d explicit commits for one model step", name, trace.Commits.Load())
			modelStatements := trace.Statements.Load()
			modelTrips := trace.RoundTrips.Load()
			if merged {
				require.EqualValues(t, 47, modelTrips)
				require.EqualValues(t, 1, trace.ExecutionBase.Load())
				require.EqualValues(t, 2, trace.ExecutionFacts.Load())
				require.Zero(t, trace.ExecutionContexts.Load())
			}
			t.Logf("model trips=%d base=%d facts=%d contexts=%d",
				trace.RoundTrips.Load(), trace.ExecutionBase.Load(), trace.ExecutionFacts.Load(), trace.ExecutionContexts.Load())
			if merged {
				require.EqualValues(t, 47, modelStatements, "merged model statement count")
			}
			t.Logf("%s: %d statements for one model step", name, modelStatements)
			toolClaimInput := kernelTestClaimInput(f.Now)
			if inline {
				toolClaimInput.DatabaseTools = tools.PlanDatabaseTool
			}
			claim, found, err = counted.Execution().ClaimNextAgentWork(ctx, toolClaimInput)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, executionstore.AgentWorkTool, claim.Kind)
			if merged {
				require.EqualValues(t, 1, trace.ExecutionBase.Load(), "tool claim base loads")
				require.EqualValues(t, 3, trace.ExecutionFacts.Load(), "tool claim fact loads")
				require.EqualValues(t, 1, trace.ExecutionContexts.Load(), "tool claim context loads")
			}
			tool := ToolWorkExecution{
				RootAgentID: claim.RootAgentID,
				Prepared:    claim.Tool.Prepared, ProjectID: claim.ProjectID, AgentID: claim.AgentID,
				TurnID: claim.Tool.TurnID, ModelCallContextID: claim.Tool.ModelCallContextID,
				ModelOutputID: claim.Tool.ModelOutputID,
				SourceEventID: claim.Tool.SourceEventID, RuntimeLockID: claim.RuntimeLock.ID,
			}
			if inline {
				require.Equal(t, executionstore.StepFinished, claim.StepDisposition)
				require.EqualValues(t, 3, trace.Commits.Load())
			} else if merged {
				disposition, err := executor.ExecuteToolWorkStep(ctx, tool)
				require.NoError(t, err)
				require.Equal(t, executionstore.StepFinished, disposition)
				require.EqualValues(t, 4, trace.Commits.Load())
			} else {
				require.NoError(t, executor.ExecuteToolWork(ctx, tool))
				require.NoError(t, counted.Execution().ReleaseAgentRuntimeLock(
					ctx, claim.ProjectID, claim.AgentID, claim.RuntimeLock.ID,
				))
				require.EqualValues(t, 8, trace.Commits.Load())
			}
			t.Logf("%s: %d explicit commits for one model step plus list_agents", name, trace.Commits.Load())
			toolStatements := trace.Statements.Load() - modelStatements
			t.Logf("cycle trips=%d base=%d facts=%d contexts=%d",
				trace.RoundTrips.Load(), trace.ExecutionBase.Load(), trace.ExecutionFacts.Load(), trace.ExecutionContexts.Load())
			if merged && !inline {
				require.EqualValues(t, 32, toolStatements, "merged list_agents statement count")
			}
			if inline {
				require.EqualValues(t, 25, toolStatements)
				require.EqualValues(t, 23, trace.RoundTrips.Load()-modelTrips)
				require.EqualValues(t, 1, trace.ExecutionBase.Load())
				require.EqualValues(t, 3, trace.ExecutionFacts.Load())
				require.EqualValues(t, 1, trace.ExecutionContexts.Load())
			} else if merged {
				require.EqualValues(t, 30, trace.RoundTrips.Load()-modelTrips)
				require.EqualValues(t, 2, trace.ExecutionBase.Load())
				require.EqualValues(t, 4, trace.ExecutionFacts.Load())
				require.EqualValues(t, 1, trace.ExecutionContexts.Load())
			}
			t.Logf("%s: %d statements for list_agents step", name, toolStatements)
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
				before := trace.Commits.Load()
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
				require.Equal(t, before, trace.Commits.Load())
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

func databaseToolClaimFixture(t *testing.T, calls []model.ToolCall, extra string) (kernelFixture, uuid.UUID) {
	t.Helper()
	ctx := t.Context()
	f := newKernelFixture(t, ctx)
	profile := f.createConfigAndProfileBookmark(t, ctx, "Database tools", "database-tools", `instruction: Use tools.
model:
  provider_config: openai-prod
  name: database-tools
subagents:
  fork:
    type: self
event_webhook:
  url: https://example.com/events
  events: [model_output, tool_call_update, tool_result]
`+extra)
	launch, err := f.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: kernelTestProjectID, ProfileID: profile.ID, AgentConfigID: profile.CurrentConfigID,
		LaunchedBy: kernelTestUserPrincipal(kernelTestUserID), IdempotencyKey: "database-tools",
	})
	require.NoError(t, err)
	_, _, _, err = f.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID: kernelTestProjectID, AgentID: launch.Agent.ID, Actor: kernelTestOmnaraActorParams(t, kernelTestUserID),
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"use tools"}]`), IdempotencyKey: "database-tools",
	})
	require.NoError(t, err)
	client := &sequenceKernelModel{providerModelSlug: "database-tools", responses: []model.Response{{
		ID: "database-tools", StopReason: model.StopReasonToolUse, Content: modeltest.ResponsePartsForToolCalls(calls),
	}}}
	executor := AgentExecutor{Store: f.Store, ModelResolver: liveTestModelResolver(f.Store, client)}
	claim, found, err := f.Store.Execution().ClaimNextAgentWork(ctx, kernelTestClaimInput(f.Now))
	require.NoError(t, err)
	require.True(t, found)
	disposition, err := executor.ExecuteModelWorkStep(ctx, modelWorkExecutionFromClaimForKernelTest(claim, f.Now))
	require.NoError(t, err)
	require.Equal(t, executionstore.StepFinished, disposition)
	return f, launch.Agent.ID
}

func TestDatabaseToolClaimBatchAndFallback(t *testing.T) {
	for _, test := range []struct {
		name     string
		second   string
		input    string
		extra    string
		finished bool
	}{
		{name: "multiple database tools", second: "list_agents", input: `{}`, finished: true},
		{name: "process observation", second: "list_processes", input: `{}`,
			extra: `tools:
  list_processes:
    type: built_in
    permission: {mode: always_allow}
`, finished: true},
		{name: "interaction discovery", second: "list_interaction_handlers", input: `{}`,
			extra: `tools:
  list_interaction_handlers:
    type: built_in
    permission: {mode: always_allow}
`},
		{name: "tool discovery", second: "tool_search", input: `{"pattern":".*"}`,
			extra: `tools:
  tool_search:
    type: built_in
    permission: {mode: always_allow}
`, finished: true},
		{name: "deferred phase", second: "tool_search", input: `{"pattern":"*"}`,
			extra: `tools:
  tool_search:
    type: built_in
    permission: {mode: always_allow}
`},
		{name: "permission", second: "list_interaction_handlers", input: `{}`,
			extra: `tools:
  list_interaction_handlers:
    type: built_in
    permission: {mode: always_ask}
`},
		{name: "async", second: "web_search", input: `{"query":"test"}`,
			extra: `tools:
  web_search:
    type: built_in
    permission: {mode: always_allow}
`},
		{name: "background", second: "spawn_agent", input: `{"agent":"fork","message":"work"}`},
		{name: "family locks", second: "send_agent_message", input: `{"agent_id":"agent_test","message":"work"}`},
		{name: "resource locks", second: "set_interaction_handler", input: `{"handler":null,"args":{}}`,
			extra: `tools:
  set_interaction_handler:
    type: built_in
    permission: {mode: always_allow}
`},
		{name: "custom", second: "custom_echo", input: `{}`,
			extra: `tools:
  custom_echo:
    type: custom
    permission: {mode: always_allow}
    description: Echo
    input_schema: {type: object}
`},
		{name: "malformed", second: "list_agents", input: `{"unexpected":true}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, agentID := databaseToolClaimFixture(t, []model.ToolCall{
				{ID: "first", Name: "list_agents", Input: json.RawMessage(`{}`)},
				{ID: "second", Name: test.second, Input: json.RawMessage(test.input)},
			}, test.extra)
			input := kernelTestClaimInput(f.Now)
			input.DatabaseTools = tools.PlanDatabaseTool
			claim, found, err := f.Store.Execution().ClaimNextAgentWork(t.Context(), input)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, executionstore.AgentWorkTool, claim.Kind)
			var results, leases, wakes, attempts int
			require.NoError(t, f.Pool.QueryRow(t.Context(), `SELECT
    (SELECT count(*) FROM tool_call_results WHERE agent_id=$1),
    (SELECT count(*) FROM agent_runtime_locks WHERE agent_id=$1),
    (SELECT count(*) FROM agent_wakeups WHERE agent_id=$1),
    (SELECT count(*) FROM model_call_contexts WHERE agent_id=$1)`, agentID).Scan(&results, &leases, &wakes, &attempts))
			require.Equal(t, 1, attempts)
			if test.finished {
				require.Equal(t, executionstore.StepFinished, claim.StepDisposition)
				require.Equal(t, 2, results)
				require.Zero(t, leases)
				require.Equal(t, 1, wakes)
				for _, state := range []string{"awaiting_authorization", "ready", "completed"} {
					var count int
					require.NoError(t, f.Pool.QueryRow(t.Context(),
						`SELECT count(*) FROM event_webhook_deliveries WHERE agent_id=$1 AND tool_state=$2`,
						agentID, state).Scan(&count))
					require.Equal(t, 2, count, state)
				}
				next, found, err := f.Store.Execution().ClaimNextAgentWork(t.Context(), kernelTestClaimInput(f.Now))
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, executionstore.AgentWorkModel, next.Kind)
			} else {
				require.Equal(t, executionstore.StepRetained, claim.StepDisposition)
				require.Zero(t, results, fmt.Sprintf("%s must not partially execute", test.name))
				require.Equal(t, 1, leases)
				require.Zero(t, wakes)
			}
		})
	}
}

func TestDatabaseToolClaimOverlappingReplay(t *testing.T) {
	f, agentID := databaseToolClaimFixture(t, []model.ToolCall{{ID: "list", Name: "list_agents", Input: json.RawMessage(`{}`)}}, "")
	ctx := t.Context()
	selected := make(chan executionstore.ClaimedAgentWork, 1)
	planned := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	input := kernelTestClaimInput(f.Now)
	input.DatabaseTools = func(
		claim executionstore.ClaimedAgentWork, call executionstore.ToolCallRecord,
	) (executionstore.DatabaseToolPlan, bool, error) {
		plan, eligible, err := tools.PlanDatabaseTool(claim, call)
		if err != nil || !eligible {
			return nil, false, err
		}
		return func(
			ctx context.Context, reader *executionstore.ToolCallReader,
		) (executionstore.ToolCallCompletionInput, bool, error) {
			selected <- claim
			select {
			case <-release:
			case <-ctx.Done():
				return executionstore.ToolCallCompletionInput{}, false, ctx.Err()
			}
			return plan(ctx, reader)
		}, true, nil
	}
	claimed := integrationdb.RunAsync(func() (executionstore.ClaimedAgentWork, error) {
		claim, _, err := f.Store.Execution().ClaimNextAgentWork(ctx, input)
		return claim, err
	})
	var claim executionstore.ClaimedAgentWork
	select {
	case claim = <-selected:
	case <-time.After(3 * time.Second):
		t.Fatal("claim did not reach database tool execution")
	}
	replayed := integrationdb.RunAsync(func() (executionstore.ExecuteToolCallResult, error) {
		return f.Store.Execution().ExecuteToolCall(ctx, executionstore.ExecuteToolCallInput{
			RootAgentID: claim.RootAgentID, ProjectID: claim.ProjectID, AgentID: claim.AgentID,
			ToolCallID: claim.Tool.Prepared.FirstCall.ID, RuntimeLockID: claim.RuntimeLock.ID,
			BatchOutputID: claim.Tool.ModelOutputID, Authorize: true, ResolveRecorded: true,
		}, func(reader *executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			call, err := reader.GetToolCall(ctx)
			if err != nil {
				return nil, err
			}
			if call.State != executionstore.ToolCallStateAwaitingAuthorization {
				return nil, fmt.Errorf("unexpected initial state %s", call.State)
			}
			close(planned)
			return executionstore.CompleteToolCallInExecution(executionstore.ToolCallCompletionInput{
				Outcome: executionstore.ToolResultOutcomeSucceeded, ResultContentParts: json.RawMessage(`[{"type":"text","text":"replay"}]`),
			}), nil
		})
	})
	select {
	case <-planned:
	case <-time.After(3 * time.Second):
		t.Fatal("replay did not observe the uncommitted tool")
	}
	close(release)
	original := integrationdb.Await(t, claimed, "database claim")
	require.NoError(t, original.Err)
	require.Equal(t, executionstore.StepFinished, original.Value.StepDisposition)
	replay := integrationdb.Await(t, replayed, "overlapping tool replay")
	require.NoError(t, replay.Err)
	require.False(t, replay.Value.Applied)
	require.Equal(t, executionstore.StepFinished, replay.Value.StepDisposition)
	var results, deliveries int
	require.NoError(t, f.Pool.QueryRow(ctx, `SELECT
  (SELECT count(*) FROM tool_call_results WHERE agent_id=$1),
  (SELECT count(*) FROM event_webhook_deliveries WHERE agent_id=$1 AND tool_state IS NOT NULL)`,
		agentID).Scan(&results, &deliveries))
	require.Equal(t, 1, results)
	require.Equal(t, 3, deliveries)
}

func TestDatabaseToolClaimInvalidLaterMemberFallsBack(t *testing.T) {
	for _, mode := range []string{"ordinary", "inline", "planner error", "handler error"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			f, agentID := databaseToolClaimFixture(t, []model.ToolCall{
				{ID: "first", Name: "list_agents", Input: json.RawMessage(`{}`)},
				{ID: "second", Name: "list_interaction_handlers", Input: json.RawMessage(`{"cursor":"%"}`)},
			}, `tools:
  list_interaction_handlers:
    type: built_in
    permission: {mode: always_allow}
`)
			input := kernelTestClaimInput(f.Now)
			var planned, executed int
			if mode != "ordinary" {
				input.DatabaseTools = func(
					claim executionstore.ClaimedAgentWork, call executionstore.ToolCallRecord,
				) (executionstore.DatabaseToolPlan, bool, error) {
					planned++
					if call.ProviderCallID == "second" {
						if mode == "planner error" {
							return nil, false, fmt.Errorf("invalid cursor: %w", storeerr.ErrInvalidRequest)
						}
						if mode == "handler error" {
							return func(
								context.Context, *executionstore.ToolCallReader,
							) (executionstore.ToolCallCompletionInput, bool, error) {
								executed++
								return executionstore.ToolCallCompletionInput{}, false, storeerr.ErrInvalidRequest
							}, true, nil
						}
					}
					return tools.PlanDatabaseTool(claim, call)
				}
			}
			claim, found, err := f.Store.Execution().ClaimNextAgentWork(ctx, input)
			var deferred *executionstore.ClaimDeferredError
			require.False(t, errors.As(err, &deferred))
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, executionstore.StepRetained, claim.StepDisposition)
			if mode != "ordinary" {
				require.Equal(t, 2, planned)
			}
			if mode == "handler error" {
				require.Equal(t, 1, executed)
			}
			var results int
			require.NoError(t, f.Pool.QueryRow(ctx,
				`SELECT count(*) FROM tool_call_results WHERE agent_id=$1`, agentID).Scan(&results))
			require.Zero(t, results)
			executor := AgentExecutor{Store: f.Store}
			disposition, err := executor.ExecuteToolWorkStep(ctx, ToolWorkExecution{
				RootAgentID: claim.RootAgentID, Prepared: claim.Tool.Prepared,
				ProjectID: claim.ProjectID, AgentID: claim.AgentID, TurnID: claim.Tool.TurnID,
				ModelCallContextID: claim.Tool.ModelCallContextID, ModelOutputID: claim.Tool.ModelOutputID,
				SourceEventID: claim.Tool.SourceEventID, RuntimeLockID: claim.RuntimeLock.ID,
			})
			require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
			require.ErrorContains(t, err, `dispatch tool "second"`)
			require.Equal(t, executionstore.StepRetained, disposition)
			first, err := f.Store.Execution().GetToolCall(ctx, claim.ProjectID, agentID, claim.Tool.Prepared.FirstCall.ID)
			require.NoError(t, err)
			require.Equal(t, executionstore.ToolCallStateCompleted, first.State)
			require.Equal(t, executionstore.ToolResultOutcomeSucceeded, first.Outcome)
			require.NotEmpty(t, first.ResultContentParts)
			require.NoError(t, f.Pool.QueryRow(ctx,
				`SELECT count(*) FROM tool_call_results WHERE agent_id=$1`, agentID).Scan(&results))
			require.Equal(t, 1, results)
		})
	}
}
