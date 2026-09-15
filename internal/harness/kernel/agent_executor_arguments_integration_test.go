//go:build integration

package kernel

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/mcp"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func TestBuiltinAndCustomArgumentsValidationApprovalAndRawReplay(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	profile := fixture.createConfigAndProfileBookmark(t, ctx, "Runtime Arguments", "runtime-arguments", `
instruction: Use the declared tools.
model:
  provider_config: openai-prod
  name: runtime-arguments
tools:
  list_machines:
    permission:
      mode: always_ask
  lookup:
    type: custom
    description: Look up an integer.
    permission:
      mode: always_ask
    input_schema:
      type: object
      properties:
        count:
          type: integer
          minimum: 2
        omnara_channel:
          type: boolean
      required: [count, omnara_channel]
      additionalProperties: false
`)
	launch, err := fixture.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: kernelTestProjectID, ProfileID: profile.ID, AgentConfigID: profile.CurrentConfigID,
		LaunchedBy: kernelTestUserPrincipal(kernelTestUserID), IdempotencyKey: "runtime-arguments",
	})
	require.NoError(t, err)
	calls := []model.ToolCall{
		{ID: "builtin", Name: toolcatalog.ToolNameListMachines, Input: json.RawMessage(`{}`)},
		{ID: "custom", Name: "lookup", Input: json.RawMessage(`{"count":9007199254740993,"omnara_channel":false}`)},
		{ID: "bad_schema", Name: "lookup", Input: json.RawMessage(`{"count":1,"omnara_channel":false}`)},
		{ID: "undeclared_key", Name: toolcatalog.ToolNameListMachines, Input: json.RawMessage(`{"omnara_channel":false}`)},
	}
	modelClient := &sequenceKernelModel{providerModelSlug: "runtime-arguments", responses: []model.Response{{
		ID: "runtime-arguments", StopReason: model.StopReasonToolUse, Content: modeltest.ResponsePartsForToolCalls(calls),
	}}}
	input := fixture.admitContentInputTurn(t, ctx, launch.Agent.ID, kernelTestUserID, "use tools", fixture.Now)
	executor := AgentExecutor{Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, modelClient)}
	require.NoError(t, executor.ExecuteModelWork(ctx, input))
	work := nextToolWorkExecution(t, ctx, fixture, input)
	require.NoError(t, executor.ExecuteToolWork(ctx, work))
	var builtinRecord executionstore.ToolCallRecord
	var builtinPermission executionstore.AgentInteractionRecord
	var customRecord executionstore.ToolCallRecord
	var customPermission executionstore.AgentInteractionRecord
	for _, call := range calls {
		record, found, err := fixture.Store.Execution().GetToolCallByProviderCall(
			ctx, work.ProjectID, work.AgentID, work.ModelCallContextID, call.ID,
		)
		require.NoError(t, err)
		require.True(t, found)
		require.True(t, jsoncanonical.Equal(call.Input, record.Input),
			"recorded raw input must retain every business value and number identity")
		permission, found, err := fixture.Store.Execution().GetAgentInteractionByToolCallKind(
			ctx, work.ProjectID, work.AgentID, record.ID, executionstore.AgentInteractionKindPermission,
		)
		require.NoError(t, err)
		if call.ID == "bad_schema" || call.ID == "undeclared_key" {
			require.False(t, found, "invalid preparation must never prompt")
			require.Equal(t, executionstore.ToolCallStateCompleted, record.State)
			require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
			require.Contains(t, string(record.ResultContentParts), `"malformed"`)
			continue
		}
		require.True(t, found)
		require.Equal(t, executionstore.ToolCallStateAwaitingPermission, record.State)
		request, err := toolpermission.ParseRequest(permission.Request)
		require.NoError(t, err)
		if call.ID == "custom" {
			require.True(t, jsoncanonical.Equal(calls[1].Input, request.Authorization.Input))
			customRecord, customPermission = record, permission
		} else {
			builtinRecord, builtinPermission = record, permission
		}
	}
	require.NoError(t, fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx, work.ProjectID, work.AgentID, work.RuntimeLockID,
	))
	allowToolForKernelTest(t, ctx, fixture, builtinPermission)
	allowToolForKernelTest(t, ctx, fixture, customPermission)
	customReady, err := fixture.Store.Execution().GetToolCall(ctx, work.ProjectID, work.AgentID, customRecord.ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateReady, customReady.State)
	require.True(t, jsoncanonical.Equal(calls[1].Input, customReady.Input))
	claim := claimNextAgentWorkForKernelTest(t, ctx, fixture, work.AgentID, executionstore.AgentWorkTool)
	work.RuntimeLockID = claim.RuntimeLock.ID
	executor = AgentExecutor{Store: storage.NewStore(fixture.Pool)}
	require.NoError(t, executor.ExecuteToolWork(ctx, work))
	completed, err := fixture.Store.Execution().GetToolCall(ctx, work.ProjectID, work.AgentID, builtinRecord.ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolResultOutcomeSucceeded, completed.Outcome)
	contextRecord, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx, work.ProjectID, work.AgentID, work.ModelCallContextID,
	)
	require.NoError(t, err)
	require.True(t, found)
	specs, err := executor.modelContextToolRuntime(ctx, work.ProjectID, work.AgentID, contextRecord, work.Now)
	require.NoError(t, err)
	turn := toolWorkTurn(work, kernelTestOrgID, specs)
	replayed, err := (tools.Executor{Store: fixture.Store}).Dispatch(ctx, turn, calls[0])
	require.NoError(t, err)
	require.Equal(t, tools.DispatchCompleted, replayed.Disposition)
	require.Equal(t, completed.ResultContentParts, replayed.ContentParts)
	changed := calls[0]
	changed.Input = json.RawMessage(`{"omnara_channel":false}`)
	_, err = (tools.Executor{Store: fixture.Store}).Dispatch(ctx, turn, changed)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict,
		"recorded identity must be checked before validating execution input")
}

func TestMCPArgumentsValidationApprovalAndRawReplay(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	profile := fixture.createConfigAndProfileBookmark(t, ctx, "Runtime MCP", "runtime-mcp", `
instruction: Use MCP tools.
model:
  provider_config: openai-prod
  name: runtime-mcp
mcp:
  docs:
    url: https://example.com/mcp
    permission:
      mode: always_ask
`)
	launch, err := fixture.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: kernelTestProjectID, ProfileID: profile.ID, AgentConfigID: profile.CurrentConfigID,
		LaunchedBy: kernelTestUserPrincipal(kernelTestUserID), IdempotencyKey: "runtime-mcp",
	})
	require.NoError(t, err)
	name := toolcatalog.MCPRuntimeToolName("docs", "lookup")
	calls := []model.ToolCall{
		{ID: "mcp_valid", Name: name, Input: json.RawMessage(`{"count":9007199254740993,"omnara_channel":null}`)},
		{ID: "mcp_invalid", Name: name, Input: json.RawMessage(`{"count":1,"omnara_channel":null}`)},
	}
	client := &fakeKernelMCPClient{
		protocolVersion: mcp.LegacyProtocolVersion, agentID: "runtime-mcp-session",
		tools:          []*sdkmcp.Tool{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer","minimum":2},"omnara_channel":{"type":"null"}},"required":["count","omnara_channel"],"additionalProperties":false}`)}},
		callToolResult: &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "done"}}},
	}
	modelClient := &sequenceKernelModel{providerModelSlug: "runtime-mcp", responses: []model.Response{{
		ID: "mcp-response", StopReason: model.StopReasonToolUse, Content: modeltest.ResponsePartsForToolCalls(calls),
	}}}
	input := fixture.admitContentInputTurn(t, ctx, launch.Agent.ID, kernelTestUserID, "use mcp", fixture.Now)
	executor := AgentExecutor{
		Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, modelClient), MCP: client,
	}
	require.NoError(t, executor.ExecuteModelWork(ctx, input))
	work := nextToolWorkExecution(t, ctx, fixture, input)
	require.NoError(t, executor.ExecuteToolWork(ctx, work))
	record, found, err := fixture.Store.Execution().GetToolCallByProviderCall(
		ctx, work.ProjectID, work.AgentID, work.ModelCallContextID, calls[0].ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	permission, found, err := fixture.Store.Execution().GetAgentInteractionByToolCallKind(
		ctx, work.ProjectID, work.AgentID, record.ID, executionstore.AgentInteractionKindPermission,
	)
	require.NoError(t, err)
	require.True(t, found)
	request, err := toolpermission.ParseRequest(permission.Request)
	require.NoError(t, err)
	require.True(t, jsoncanonical.Equal(calls[0].Input, request.Authorization.Input))
	invalid, found, err := fixture.Store.Execution().GetToolCallByProviderCall(
		ctx, work.ProjectID, work.AgentID, work.ModelCallContextID, calls[1].ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.ToolCallStateCompleted, invalid.State)
	require.Contains(t, string(invalid.ResultContentParts), `"malformed"`)
	_, found, err = fixture.Store.Execution().GetAgentInteractionByToolCallKind(
		ctx, work.ProjectID, work.AgentID, invalid.ID, executionstore.AgentInteractionKindPermission,
	)
	require.NoError(t, err)
	require.False(t, found)
	require.Zero(t, client.callToolCount)
	require.NoError(t, fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx, work.ProjectID, work.AgentID, work.RuntimeLockID,
	))
	allowToolForKernelTest(t, ctx, fixture, permission)
	claim := claimNextAgentWorkForKernelTest(t, ctx, fixture, work.AgentID, executionstore.AgentWorkTool)
	work.RuntimeLockID = claim.RuntimeLock.ID
	executor = AgentExecutor{Store: storage.NewStore(fixture.Pool), MCP: client}
	scope := tools.NewAsyncExecutionScope(nil)
	err = executor.ExecuteToolWork(tools.WithAsyncExecutionScope(ctx, scope), work)
	scope.Seal()
	require.NoError(t, err)
	select {
	case <-scope.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("MCP dispatch did not finish")
	}
	require.NoError(t, scope.Err())
	require.Equal(t, 1, client.callToolCount)
	require.True(t, jsoncanonical.Equal(calls[0].Input, client.callToolCalls[0].Arguments))
	require.Equal(t, record.Input, client.callToolCalls[0].Arguments,
		"dispatch must pass the recorded arguments without rewriting them")
	completed, err := fixture.Store.Execution().GetToolCall(ctx, work.ProjectID, work.AgentID, record.ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolResultOutcomeSucceeded, completed.Outcome)
	require.True(t, jsoncanonical.Equal(calls[0].Input, completed.Input))
	contextRecord, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx, work.ProjectID, work.AgentID, work.ModelCallContextID,
	)
	require.NoError(t, err)
	require.True(t, found)
	specs, err := executor.modelContextToolRuntime(ctx, work.ProjectID, work.AgentID, contextRecord, work.Now)
	require.NoError(t, err)
	turn := toolWorkTurn(work, kernelTestOrgID, specs)
	replay, err := (tools.Executor{Store: fixture.Store, MCP: client}).Dispatch(ctx, turn, calls[0])
	require.NoError(t, err)
	require.Equal(t, completed.ResultContentParts, replay.ContentParts)
	require.Equal(t, 1, client.callToolCount, "completed replay must not dispatch again")
}

func allowToolForKernelTest(
	t *testing.T,
	ctx context.Context,
	fixture kernelFixture,
	permission executionstore.AgentInteractionRecord,
) {
	t.Helper()
	_, err := fixture.Store.Execution().ResolveAgentInteraction(ctx, executionstore.ResolveAgentInteractionInput{
		ProjectID: kernelTestProjectID, AgentID: permission.AgentID, ID: permission.ID,
		Resolution: interactionform.Resolution{
			Answers: []interactionform.Answer{{OptionIndices: []int{toolpermission.AllowOptionIndex}}},
		},
		Actor: kernelTestOmnaraActorParams(t, kernelTestUserID),
	})
	require.NoError(t, err)
}
