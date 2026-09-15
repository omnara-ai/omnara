//go:build integration

package kernel

import (
	"encoding/json"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/mcp"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestLegacyUnfinishedApprovalExecutesWithoutToolDefinitionSnapshot(t *testing.T) {
	ctx := t.Context()
	fixture := newKernelFixture(t, ctx)
	profile := fixture.createConfigAndProfileBookmark(t, ctx, "Legacy Approval", "legacy-approval", `
instruction: List the available machines.
model:
  provider_config: openai-prod
  name: legacy-approval
tools:
  list_machines:
    permission:
      mode: always_ask
mcp:
  docs:
    url: https://example.com/mcp
    permission:
      mode: always_ask
`)
	launch, err := fixture.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: kernelTestProjectID, ProfileID: profile.ID, AgentConfigID: profile.CurrentConfigID,
		LaunchedBy: kernelTestUserPrincipal(kernelTestUserID), IdempotencyKey: "legacy-approval",
	})
	require.NoError(t, err)
	// These unrelated catalog entries must not block the built-in call. The SDK
	// serializes an absent schema as null, which uses the baseline object default.
	mcpClient := &fakeKernelMCPClient{
		agentID: "legacy-approval-session", protocolVersion: mcp.LegacyProtocolVersion,
		tools: []*sdkmcp.Tool{
			{Name: "malformed", InputSchema: json.RawMessage(`{"type":"invalid"}`)},
			{Name: "missing"},
		},
	}
	manager := mcp.Manager{Execution: fixture.Store.Execution(), Secrets: fixture.Store.Secrets(), Client: mcpClient}
	require.Len(t, launch.MCPConnections, 1)
	connection, err := manager.EnsureConnection(
		ctx, kernelTestOrgID, kernelTestProjectID, launch.Agent.ID, launch.MCPConnections[0],
		agentconfig.RuntimeMCPServer{ServerKey: "docs", URL: "https://example.com/mcp", DefaultEnabled: true},
		mcp.TriggerTurnStart,
	)
	require.NoError(t, err)
	require.True(t, connection.Ready)
	input := fixture.admitContentInputTurn(t, ctx, launch.Agent.ID, kernelTestUserID, "list machines", fixture.Now)
	config, err := fixture.Store.Execution().CaptureAgentConfigForEventWatermark(
		ctx, input.ProjectID, input.AgentID, input.OpeningEventSequence,
	)
	require.NoError(t, err)
	claim, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID: input.ProjectID, AgentID: input.AgentID, RuntimeLockID: input.RuntimeLockID,
		OpeningInputIDs: input.InputIDs, AgentConfigID: config.AgentConfig.ID,
		InputEventSequence: input.OpeningEventSequence,
	})
	require.NoError(t, err)
	call := model.ToolCall{ID: "legacy-call", Name: toolcatalog.ToolNameListMachines, Input: json.RawMessage(`{}`)}
	envelope, err := model.NewResponseEnvelopeForStorage(
		"legacy-approval", modelprotocol.APIFormatOpenAIResponses, modelprotocol.APIVariantDefault,
		model.Response{
			ID: "legacy-response", StopReason: model.StopReasonToolUse,
			Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{call}),
		},
	)
	require.NoError(t, err)
	// Seed the existing storage shape directly. No model request or tool-definition
	// retention runs before the new worker resumes this unfinished permission.
	_, records, err := fixture.Store.Execution().RecordToolCallSourceAndCompleteContext(
		ctx, executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID: input.ProjectID, AgentID: input.AgentID, RuntimeLockID: input.RuntimeLockID,
			ModelCallContextID: claim.Context.ID, ProviderResponse: envelope,
			ToolCallBindings: []executionstore.ToolCallBindingInput{{
				ProviderCallID: call.ID, Type: toolcatalog.ToolTypeBuiltIn,
			}},
		},
	)
	require.NoError(t, err)
	require.Len(t, records, 1)
	// list_machines authorizes the observation operation, not its empty arguments.
	authorization, err := toolpermission.NewAuthorization(call.Name, json.RawMessage(`{"mode":"list"}`))
	require.NoError(t, err)
	form, err := toolpermission.NewAllowDenyForm("Permission requested for "+call.Name, nil)
	require.NoError(t, err)
	permission, err := fixture.Store.Execution().CreatePermissionInteraction(
		ctx, executionstore.CreatePermissionInteractionInput{
			ProjectID: input.ProjectID, AgentID: input.AgentID, RuntimeLockID: input.RuntimeLockID,
			ToolCallID: records[0].ID,
			Request: toolpermission.Request{
				Permission:    toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk),
				Authorization: authorization, Form: form,
			},
		},
	)
	require.NoError(t, err)
	pending, err := fixture.Store.Execution().GetToolCall(ctx, input.ProjectID, input.AgentID, records[0].ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateAwaitingPermission, pending.State)
	require.Equal(t, executionstore.AgentInteractionStateOpen, permission.State)
	fixture.releaseModelRuntimeLock(t, ctx, input)

	allowToolForKernelTest(t, ctx, fixture, permission)
	ready, err := fixture.Store.Execution().GetToolCall(ctx, input.ProjectID, input.AgentID, records[0].ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateReady, ready.State)
	work := claimNextAgentWorkForKernelTest(t, ctx, fixture, input.AgentID, executionstore.AgentWorkTool)
	require.Equal(t, claim.Context.ID, work.Tool.ModelCallContextID)
	executor := AgentExecutor{Store: storage.NewStore(fixture.Pool)}
	specs, err := executor.modelContextToolRuntime(ctx, input.ProjectID, input.AgentID, claim.Context, fixture.Now)
	require.NoError(t, err)
	require.Len(t, specs, 3)
	byName := toolSpecSet(specs)
	require.Contains(t, byName, toolcatalog.MCPRuntimeToolName("docs", "malformed"))
	require.Contains(t, byName, toolcatalog.MCPRuntimeToolName("docs", "missing"))
	require.NoError(t, executor.ExecuteToolWork(ctx, ToolWorkExecution{
		ProjectID: work.ProjectID, AgentID: work.AgentID, TurnID: work.Tool.TurnID,
		ModelCallContextID: work.Tool.ModelCallContextID, ModelOutputID: work.Tool.ModelOutputID,
		SourceEventID: work.Tool.SourceEventID, RuntimeLockID: work.RuntimeLock.ID, Now: fixture.Now,
	}))
	completed, err := fixture.Store.Execution().GetToolCall(ctx, input.ProjectID, input.AgentID, records[0].ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateCompleted, completed.State)
	require.Equal(t, executionstore.ToolResultOutcomeSucceeded, completed.Outcome,
		"tool result: %s", completed.ResultContentParts)
	require.Equal(t, claim.Context.ID, completed.ModelCallContextID)
	require.True(t, jsoncanonical.Equal(call.Input, completed.Input))
	resolved, found, err := fixture.Store.Execution().GetAgentInteractionByToolCallKind(
		ctx, input.ProjectID, input.AgentID, records[0].ID, executionstore.AgentInteractionKindPermission,
	)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, permission.ID, resolved.ID, "the existing permission must be reused")
	require.Equal(t, executionstore.AgentInteractionStateResolved, resolved.State)
	require.Zero(t, mcpClient.callToolCount, "resuming a built-in must not dispatch unrelated MCP calls")
}

func TestLegacyUnfinishedMCPApprovalExecutesWithoutToolDefinitionSnapshot(t *testing.T) {
	testUnfinishedMCPApproval(t, nil)
}

func testUnfinishedMCPApproval(
	t *testing.T,
	beforeResume func(kernelFixture, executionstore.ModelCallContextRecord),
) {
	t.Helper()
	ctx := t.Context()
	fixture := newKernelFixture(t, ctx)
	profile := fixture.createConfigAndProfileBookmark(t, ctx, "Legacy MCP Approval", "legacy-mcp-approval", `
instruction: Look up the requested integer.
model:
  provider_config: openai-prod
  name: legacy-mcp-approval
mcp:
  docs:
    url: https://example.com/mcp
    permission:
      mode: always_ask
`)
	launch, err := fixture.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: kernelTestProjectID, ProfileID: profile.ID, AgentConfigID: profile.CurrentConfigID,
		LaunchedBy: kernelTestUserPrincipal(kernelTestUserID), IdempotencyKey: "legacy-mcp-approval",
	})
	require.NoError(t, err)
	catalogClient := &fakeKernelMCPClient{
		agentID: "legacy-mcp-approval-session", protocolVersion: mcp.LegacyProtocolVersion,
		tools: []*sdkmcp.Tool{{Name: "lookup", InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "count": {"type": "integer", "minimum": 2},
    "omnara_channel": {"type": "string"},
    "nested": {
      "type": "object",
      "properties": {"omnara_channel": {"type": "boolean"}},
      "required": ["omnara_channel"],
      "additionalProperties": false
    }
  },
  "required": ["count", "omnara_channel", "nested"],
  "additionalProperties": false
}`)}},
	}
	manager := mcp.Manager{Execution: fixture.Store.Execution(), Secrets: fixture.Store.Secrets(), Client: catalogClient}
	require.Len(t, launch.MCPConnections, 1)
	connection, err := manager.EnsureConnection(
		ctx, kernelTestOrgID, kernelTestProjectID, launch.Agent.ID, launch.MCPConnections[0],
		agentconfig.RuntimeMCPServer{ServerKey: "docs", URL: "https://example.com/mcp", DefaultEnabled: true},
		mcp.TriggerTurnStart,
	)
	require.NoError(t, err)
	require.True(t, connection.Ready)
	input := fixture.admitContentInputTurn(t, ctx, launch.Agent.ID, kernelTestUserID, "look up integer", fixture.Now)
	config, err := fixture.Store.Execution().CaptureAgentConfigForEventWatermark(
		ctx, input.ProjectID, input.AgentID, input.OpeningEventSequence,
	)
	require.NoError(t, err)
	claim, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID: input.ProjectID, AgentID: input.AgentID, RuntimeLockID: input.RuntimeLockID,
		OpeningInputIDs: input.InputIDs, AgentConfigID: config.AgentConfig.ID,
		InputEventSequence: input.OpeningEventSequence,
	})
	require.NoError(t, err)
	call := model.ToolCall{
		ID: "legacy-mcp-call", Name: toolcatalog.MCPRuntimeToolName("docs", "lookup"),
		Input: json.RawMessage(` { "count": 9007199254740993, "omnara_channel": "business-value", "nested": {"omnara_channel": false} } `),
	}
	envelope, err := model.NewResponseEnvelopeForStorage(
		"legacy-mcp-approval", modelprotocol.APIFormatOpenAIResponses, modelprotocol.APIVariantDefault,
		model.Response{
			ID: "legacy-mcp-response", StopReason: model.StopReasonToolUse,
			Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{call}),
		},
	)
	require.NoError(t, err)
	// Seed the legacy output and permission directly, without ExecuteModelWork or
	// any model-request tool-definition retention. MCP discovery is stored normally.
	_, records, err := fixture.Store.Execution().RecordToolCallSourceAndCompleteContext(
		ctx, executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID: input.ProjectID, AgentID: input.AgentID, RuntimeLockID: input.RuntimeLockID,
			ModelCallContextID: claim.Context.ID, ProviderResponse: envelope,
			ToolCallBindings: []executionstore.ToolCallBindingInput{{
				ProviderCallID: call.ID, Type: toolcatalog.ToolTypeMCP,
			}},
		},
	)
	require.NoError(t, err)
	require.Len(t, records, 1)
	authorization, err := toolpermission.NewAuthorization(call.Name, call.Input)
	require.NoError(t, err)
	form, err := toolpermission.NewAllowDenyForm("Permission requested for "+call.Name, nil)
	require.NoError(t, err)
	permission, err := fixture.Store.Execution().CreatePermissionInteraction(
		ctx, executionstore.CreatePermissionInteractionInput{
			ProjectID: input.ProjectID, AgentID: input.AgentID, RuntimeLockID: input.RuntimeLockID,
			ToolCallID: records[0].ID,
			Request: toolpermission.Request{
				Permission:    toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk),
				Authorization: authorization, Form: form,
			},
		},
	)
	require.NoError(t, err)
	pending, err := fixture.Store.Execution().GetToolCall(ctx, input.ProjectID, input.AgentID, records[0].ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateAwaitingPermission, pending.State)
	require.Equal(t, executionstore.AgentInteractionStateOpen, permission.State)
	require.True(t, jsoncanonical.Equal(call.Input, pending.Input))
	fixture.releaseModelRuntimeLock(t, ctx, input)
	if beforeResume != nil {
		beforeResume(fixture, claim.Context)
	}

	allowToolForKernelTest(t, ctx, fixture, permission)
	client := &fakeKernelMCPClient{
		callToolResult: &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "found"}}},
	}
	executor := AgentExecutor{Store: storage.NewStore(fixture.Pool), MCP: client}
	ready, err := executor.Store.Execution().GetToolCall(ctx, input.ProjectID, input.AgentID, records[0].ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateReady, ready.State)
	require.Equal(t, pending.Input, ready.Input)
	workClaim := claimNextAgentWorkForKernelTest(t, ctx, fixture, input.AgentID, executionstore.AgentWorkTool)
	require.Equal(t, claim.Context.ID, workClaim.Tool.ModelCallContextID)
	work := ToolWorkExecution{
		ProjectID: workClaim.ProjectID, AgentID: workClaim.AgentID, TurnID: workClaim.Tool.TurnID,
		ModelCallContextID: workClaim.Tool.ModelCallContextID, ModelOutputID: workClaim.Tool.ModelOutputID,
		SourceEventID: workClaim.Tool.SourceEventID, RuntimeLockID: workClaim.RuntimeLock.ID, Now: fixture.Now,
	}
	scope := tools.NewAsyncExecutionScope(nil)
	err = executor.ExecuteToolWork(tools.WithAsyncExecutionScope(ctx, scope), work)
	scope.Seal()
	require.NoError(t, err)
	select {
	case <-scope.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("legacy MCP dispatch did not finish")
	}
	require.NoError(t, scope.Err())
	require.Equal(t, 1, client.callToolCount)
	require.Len(t, client.callToolCalls, 1)
	require.Equal(t, "lookup", client.callToolCalls[0].Name)
	require.Equal(t, pending.Input, client.callToolCalls[0].Arguments,
		"dispatch must preserve the recorded bytes, including business channel fields and integer precision")
	require.Len(t, client.callToolConns, 1)
	require.Equal(t, connection.Conn.MCPSessionID, client.callToolConns[0].MCPSessionID)
	require.Zero(t, client.initializeCount, "a fresh executor must reuse the stored MCP connection")
	require.Zero(t, catalogClient.callToolCount)
	completed, err := executor.Store.Execution().GetToolCall(ctx, input.ProjectID, input.AgentID, records[0].ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateCompleted, completed.State)
	require.Equal(t, executionstore.ToolResultOutcomeSucceeded, completed.Outcome,
		"tool result: %s", completed.ResultContentParts)
	require.Equal(t, call.ID, completed.ProviderCallID)
	require.Equal(t, claim.Context.ID, completed.ModelCallContextID)
	require.Equal(t, pending.Input, completed.Input)
	resolved, found, err := executor.Store.Execution().GetAgentInteractionByToolCallKind(
		ctx, input.ProjectID, input.AgentID, records[0].ID, executionstore.AgentInteractionKindPermission,
	)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, permission.ID, resolved.ID)
	require.Equal(t, permission.Request, resolved.Request, "approval must preserve the original authorization")
	require.Equal(t, executionstore.AgentInteractionStateResolved, resolved.State)

	// Replaying completed work must use the durable result, without calling MCP again.
	require.NoError(t, executor.ExecuteToolWork(ctx, work))
	specs, err := executor.modelContextToolRuntime(ctx, input.ProjectID, input.AgentID, claim.Context, fixture.Now)
	require.NoError(t, err)
	replay, err := (tools.Executor{Store: executor.Store, MCP: client}).Dispatch(
		ctx, toolWorkTurn(work, kernelTestOrgID, specs), call,
	)
	require.NoError(t, err)
	require.Equal(t, tools.DispatchCompleted, replay.Disposition)
	require.Equal(t, completed.ResultContentParts, replay.ContentParts)
	require.Equal(t, 1, client.callToolCount, "completed replay must not dispatch again")
}
