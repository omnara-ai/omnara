//go:build integration

package kernel

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/mcp"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestAgentExecutorInitializesMCPConnectionsBeforeGeneration(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	user, err := storagetest.CreateVerifiedUser(
		ctx,
		fixture.Pool,
		storagetest.CreateVerifiedUserInput{Email: "kernel-mcp@example.com", DisplayName: "Kernel MCP User"},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	sourceYAML := `
name: Kernel MCP
instruction: Use MCP tools later.
model:
  provider_config: openai-prod
  name: test-model
mcp:
  docs:
    url: https://example.com/mcp
    permission:
      mode: always_allow
`
	agent := fixture.createConfigAndProfileBookmark(t, ctx, "Kernel MCP", "kernel-mcp-agent", sourceYAML, now)
	launch, err := fixture.Store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      kernelTestProjectID,
			ProfileID:      agent.ID,
			AgentConfigID:  agent.CurrentConfigID,
			LaunchedBy:     kernelTestUserPrincipal(user.ID),
			IdempotencyKey: "kernel-mcp-agent",
		},
	)
	if err != nil {
		t.Fatalf("launch agent: %v", err)
	}
	if len(launch.MCPConnections) != 1 || launch.MCPConnections[0].State != executionstore.MCPConnectionStateInitializing {
		t.Fatalf("expected initializing mcp connection from launch, got %+v", launch.MCPConnections)
	}
	input := fixture.admitContentInputTurn(t, ctx, launch.Agent.ID, kernelTestUserID, "hello", now.Add(2*time.Millisecond))
	modelClient := &sequenceKernelModel{
		providerModelSlug: "test-model",
		responses:         []model.Response{{ID: "resp-mcp", Content: []model.ResponsePart{{Type: "text", Text: "done"}}, StopReason: model.StopReasonEndTurn}},
	}
	mcpClient := &fakeKernelMCPClient{
		agentID:         "remote-session",
		protocolVersion: mcp.ProtocolVersion,
		tools: []*sdkmcp.Tool{{
			Name:        "greet",
			Description: "say hi",
			InputSchema: map[string]any{
				"type": "object",
			},
		}},
		failInitializeSequences: map[string][]error{
			"https://example.com/mcp": {
				&mcp.HTTPError{Status: http.StatusServiceUnavailable},
				&mcp.HTTPError{Status: http.StatusServiceUnavailable},
			},
		},
	}
	executor := AgentExecutor{
		Store:                    fixture.Store,
		ModelResolver:            liveTestModelResolver(fixture.Store, modelClient),
		MCP:                      mcpClient,
		Now:                      func() time.Time { return now.Add(3 * time.Millisecond) },
		MCPInitializationBackoff: func(int) time.Duration { return 0 },
	}
	if err := executor.ExecuteModelWork(ctx, input); err != nil {
		t.Fatalf("execute turn: %v", err)
	}
	if mcpClient.initializeCount != 3 || mcpClient.notifyCount != 1 || mcpClient.listToolsCount != 1 {
		t.Fatalf(
			"unexpected mcp calls: initialize=%d notify=%d list=%d",
			mcpClient.initializeCount,
			mcpClient.notifyCount,
			mcpClient.listToolsCount,
		)
	}
	if modelClient.preparedCount() != 1 {
		t.Fatalf("expected one model prepare after mcp init, got %d", modelClient.preparedCount())
	}
	conn, found, err := fixture.Store.Execution().GetMCPConnection(ctx, kernelTestProjectID, launch.Agent.ID, "docs")
	if err != nil {
		t.Fatalf("load mcp connection: %v", err)
	}
	if !found {
		t.Fatal("expected mcp connection")
	}
	if conn.State != executionstore.MCPConnectionStateReady || conn.MCPSessionID != "remote-session" ||
		conn.ProtocolVersion != mcp.ProtocolVersion {
		t.Fatalf("unexpected initialized mcp connection: %+v", conn)
	}
	if conn.RequestSequence != 2 {
		t.Fatalf("request sequence = %d, want 2 after tools/list", conn.RequestSequence)
	}
	var snapshot []struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		InputSchema map[string]any `json:"inputSchema"`
	}
	if err := json.Unmarshal(conn.ToolsSnapshot, &snapshot); err != nil {
		t.Fatalf("decode tools snapshot: %v", err)
	}
	if len(snapshot) != 1 || snapshot[0].Name != "greet" || snapshot[0].Description != "say hi" {
		t.Fatalf("unexpected tools snapshot: %s", conn.ToolsSnapshot)
	}
}

func TestAgentExecutorModelRetryDoesNotReinitializeFailedMCPConnection(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	user, err := storagetest.CreateVerifiedUser(
		ctx,
		fixture.Pool,
		storagetest.CreateVerifiedUserInput{
			Email:       "kernel-mcp-connect-retry@example.com",
			DisplayName: "Kernel MCP Connect Retry User",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	sourceYAML := `
name: Kernel MCP Connect Retry
instruction: Continue even if MCP cannot connect.
model:
  provider_config: openai-prod
  name: test-model
mcp:
  docs:
    url: https://example.com/mcp
    permission:
      mode: always_allow
`
	agent := fixture.createConfigAndProfileBookmark(
		t,
		ctx,
		"Kernel MCP Connect Retry",
		"kernel-mcp-connect-retry-agent",
		sourceYAML,
		now,
	)
	launch, err := fixture.Store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      kernelTestProjectID,
			ProfileID:      agent.ID,
			AgentConfigID:  agent.CurrentConfigID,
			LaunchedBy:     kernelTestUserPrincipal(user.ID),
			IdempotencyKey: "kernel-mcp-connect-retry-agent",
		},
	)
	if err != nil {
		t.Fatalf("launch agent: %v", err)
	}
	input := fixture.admitContentInputTurn(t, ctx, launch.Agent.ID, kernelTestUserID, "hello", now.Add(2*time.Millisecond))
	modelClient := &sequenceKernelModel{
		providerModelSlug: "test-model",
		errs: []error{model.ProviderError{
			Kind:    model.ErrorKindTransient,
			Source:  "test-provider",
			Message: "retry model call",
		}},
		responses: []model.Response{
			{ID: "resp-mcp-connect-retry", Content: []model.ResponsePart{{Type: "text", Text: "continued"}}, StopReason: model.StopReasonEndTurn},
		},
	}
	mcpClient := &fakeKernelMCPClient{
		protocolVersion: mcp.ProtocolVersion,
		failInitializeSequences: map[string][]error{
			"https://example.com/mcp": {
				&mcp.HTTPError{Status: http.StatusServiceUnavailable},
				&mcp.HTTPError{Status: http.StatusServiceUnavailable},
				&mcp.HTTPError{Status: http.StatusServiceUnavailable},
			},
		},
	}
	currentNow := now.Add(3 * time.Millisecond)
	executor := AgentExecutor{
		Store:                    fixture.Store,
		ModelResolver:            liveTestModelResolver(fixture.Store, modelClient),
		MCP:                      mcpClient,
		Now:                      func() time.Time { return currentNow },
		MCPInitializationBackoff: func(int) time.Duration { return 0 },
		ModelRetryDelay:          immediateKernelModelRetryDelay,
	}
	if err := executor.ExecuteModelWork(ctx, input); err != nil {
		t.Fatalf("execute first model attempt: %v", err)
	}
	retry := continueTurnOnNewLeaseForKernelTest(t, ctx, fixture, input, now.Add(time.Hour))
	currentNow = retry.Now
	if err := executor.ExecuteModelWork(ctx, retry); err != nil {
		t.Fatalf("execute retried model call: %v", err)
	}
	if mcpClient.initializeCount != 3 || mcpClient.notifyCount != 0 || mcpClient.listToolsCount != 0 {
		t.Fatalf(
			"unexpected mcp calls: initialize=%d notify=%d list=%d",
			mcpClient.initializeCount,
			mcpClient.notifyCount,
			mcpClient.listToolsCount,
		)
	}
	if modelClient.preparedCount() != 2 || len(modelClient.prepared[0].ToolSpecs) != 0 ||
		len(modelClient.prepared[1].ToolSpecs) != 0 {
		t.Fatalf(
			"model retry should continue without mcp tools, prepared=%d first=%+v retry=%+v",
			modelClient.preparedCount(),
			modelClient.prepared[0].ToolSpecs,
			modelClient.prepared[1].ToolSpecs,
		)
	}
	conn, found, err := fixture.Store.Execution().GetMCPConnection(ctx, kernelTestProjectID, launch.Agent.ID, "docs")
	if err != nil || !found {
		t.Fatalf("load mcp connection: found=%t err=%v", found, err)
	}
	if conn.State != executionstore.MCPConnectionStateFailed ||
		!strings.Contains(conn.InitializeError, "unexpected HTTP status 503") {
		t.Fatalf("mcp connection should store exhausted retry failure, got %+v", conn)
	}
}

func TestAgentExecutorMCPInitializationFailsWhenListToolsFails(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	user, err := storagetest.CreateVerifiedUser(
		ctx,
		fixture.Pool,
		storagetest.CreateVerifiedUserInput{
			Email:       "kernel-mcp-list-tools-failure@example.com",
			DisplayName: "Kernel MCP List Tools Failure User",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	sourceYAML := `
name: Kernel MCP List Tools Failure
instruction: Continue even if MCP tools/list fails.
model:
  provider_config: openai-prod
  name: test-model
mcp:
  docs:
    url: https://example.com/mcp
    permission:
      mode: always_allow
`
	agent := fixture.createConfigAndProfileBookmark(
		t,
		ctx,
		"Kernel MCP List Tools Failure",
		"kernel-mcp-list-tools-failure-agent",
		sourceYAML,
		now,
	)
	launch, err := fixture.Store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      kernelTestProjectID,
			ProfileID:      agent.ID,
			AgentConfigID:  agent.CurrentConfigID,
			LaunchedBy:     kernelTestUserPrincipal(user.ID),
			IdempotencyKey: "kernel-mcp-list-tools-failure-agent",
		},
	)
	if err != nil {
		t.Fatalf("launch agent: %v", err)
	}
	input := fixture.admitContentInputTurn(t, ctx, launch.Agent.ID, kernelTestUserID, "hello", now.Add(2*time.Millisecond))
	modelClient := &sequenceKernelModel{
		providerModelSlug: "test-model",
		responses: []model.Response{
			{ID: "resp-mcp-list-tools-failure", Content: []model.ResponsePart{{Type: "text", Text: "continued"}}, StopReason: model.StopReasonEndTurn},
		},
	}
	mcpClient := &fakeKernelMCPClient{
		protocolVersion:    mcp.ProtocolVersion,
		initializeAgentIDs: []string{"remote-session-1", "remote-session-2", "remote-session-3"},
		listToolsErrors: []error{
			&mcp.HTTPError{Status: http.StatusServiceUnavailable},
			&mcp.HTTPError{Status: http.StatusServiceUnavailable},
			&mcp.HTTPError{Status: http.StatusServiceUnavailable},
		},
	}
	executor := AgentExecutor{
		Store:                    fixture.Store,
		ModelResolver:            liveTestModelResolver(fixture.Store, modelClient),
		MCP:                      mcpClient,
		Now:                      func() time.Time { return now.Add(3 * time.Millisecond) },
		MCPInitializationBackoff: func(int) time.Duration { return 0 },
	}
	if err := executor.ExecuteModelWork(ctx, input); err != nil {
		t.Fatalf("execute turn: %v", err)
	}
	if mcpClient.initializeCount != 3 || mcpClient.notifyCount != 3 || mcpClient.listToolsCount != 3 {
		t.Fatalf(
			"unexpected mcp calls: initialize=%d notify=%d list=%d",
			mcpClient.initializeCount,
			mcpClient.notifyCount,
			mcpClient.listToolsCount,
		)
	}
	if modelClient.preparedCount() != 1 || len(modelClient.prepared[0].ToolSpecs) != 0 {
		t.Fatalf(
			"model should continue without mcp tools, prepared=%d tools=%+v",
			modelClient.preparedCount(),
			modelClient.prepared[0].ToolSpecs,
		)
	}
	conn, found, err := fixture.Store.Execution().GetMCPConnection(ctx, kernelTestProjectID, launch.Agent.ID, "docs")
	if err != nil || !found {
		t.Fatalf("load mcp connection: found=%t err=%v", found, err)
	}
	if conn.State != executionstore.MCPConnectionStateFailed || !strings.Contains(conn.InitializeError, "list mcp tools") ||
		!strings.Contains(conn.InitializeError, "unexpected HTTP status 503") {
		t.Fatalf("mcp connection should store tools/list failure, got %+v", conn)
	}
}

func TestAgentExecutorRefreshesExpiredMCPConnectionForAsyncToolCall(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	user, err := storagetest.CreateVerifiedUser(
		ctx,
		fixture.Pool,
		storagetest.CreateVerifiedUserInput{
			Email:       "kernel-mcp-refresh@example.com",
			DisplayName: "Kernel MCP Refresh User",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	sourceYAML := `
name: Kernel MCP Refresh
instruction: Use MCP tools.
model:
  provider_config: openai-prod
  name: test-model
mcp:
  docs:
    url: https://example.com/mcp
    permission:
      mode: always_allow
`
	agent := fixture.createConfigAndProfileBookmark(
		t,
		ctx,
		"Kernel MCP Refresh",
		"kernel-mcp-refresh-agent",
		sourceYAML,
		now,
	)
	launch, err := fixture.Store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      kernelTestProjectID,
			ProfileID:      agent.ID,
			AgentConfigID:  agent.CurrentConfigID,
			LaunchedBy:     kernelTestUserPrincipal(user.ID),
			IdempotencyKey: "kernel-mcp-refresh-agent",
		},
	)
	if err != nil {
		t.Fatalf("launch agent: %v", err)
	}
	input := fixture.admitContentInputTurn(
		t,
		ctx,
		launch.Agent.ID,
		kernelTestUserID,
		"use mcp",
		now.Add(2*time.Millisecond),
	)
	modelClient := &sequenceKernelModel{
		providerModelSlug: "test-model",
		responses: []model.Response{
			{
				ID:         "resp-mcp-tool",
				StopReason: model.StopReasonToolUse,
				Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{
					{
						ID:    "call_mcp_greet",
						Name:  toolcatalog.MCPRuntimeToolName("docs", "greet"),
						Input: json.RawMessage(`{"name":"Ada"}`),
					},
				}),
			},
			{ID: "resp-mcp-final", Content: []model.ResponsePart{{Type: "text", Text: "done after mcp"}}, StopReason: model.StopReasonEndTurn},
		},
	}
	mcpClient := &fakeKernelMCPClient{
		protocolVersion:    mcp.ProtocolVersion,
		initializeAgentIDs: []string{"remote-session-1", "remote-session-2"},
		tools: []*sdkmcp.Tool{
			{Name: "greet", Description: "say hi", InputSchema: map[string]any{"type": "object"}},
		},
		callToolErrors: []error{mcp.ErrSessionExpired, nil},
		callToolResult: &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "hello"}},
		},
	}
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		MCP:           mcpClient,
		ToolExecutor:  tools.Executor{Store: fixture.Store, MCP: mcpClient},
		Now:           func() time.Time { return now.Add(3 * time.Millisecond) },
	}
	_ = executeAsyncToolTurn(t, ctx, fixture, executor, input)
	if mcpClient.initializeCount != 2 || mcpClient.notifyCount != 2 || mcpClient.listToolsCount != 2 ||
		mcpClient.callToolCount != 2 {
		t.Fatalf(
			"unexpected mcp calls: initialize=%d notify=%d list=%d call=%d",
			mcpClient.initializeCount,
			mcpClient.notifyCount,
			mcpClient.listToolsCount,
			mcpClient.callToolCount,
		)
	}
	if len(mcpClient.callToolConns) != 2 || mcpClient.callToolConns[0].MCPSessionID != "remote-session-1" ||
		mcpClient.callToolConns[1].MCPSessionID != "remote-session-2" {
		t.Fatalf("tool calls did not use refreshed mcp session: %+v", mcpClient.callToolConns)
	}
	conn, found, err := fixture.Store.Execution().GetMCPConnection(ctx, kernelTestProjectID, launch.Agent.ID, "docs")
	if err != nil || !found {
		t.Fatalf("load refreshed mcp connection: found=%t err=%v", found, err)
	}
	if conn.State != executionstore.MCPConnectionStateReady || conn.MCPSessionID != "remote-session-2" ||
		conn.InitializeError != "" {
		t.Fatalf("unexpected refreshed mcp connection: %+v", conn)
	}
}

func TestAgentExecutorMCPRefreshFailureCompletesToolAndRemovesMCPTools(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	user, err := storagetest.CreateVerifiedUser(
		ctx,
		fixture.Pool,
		storagetest.CreateVerifiedUserInput{
			Email:       "kernel-mcp-refresh-failure@example.com",
			DisplayName: "Kernel MCP Refresh Failure User",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	sourceYAML := `
name: Kernel MCP Refresh Failure
instruction: Use MCP tools.
model:
  provider_config: openai-prod
  name: test-model
mcp:
  docs:
    url: https://example.com/mcp
    permission:
      mode: always_allow
`
	agent := fixture.createConfigAndProfileBookmark(
		t,
		ctx,
		"Kernel MCP Refresh Failure",
		"kernel-mcp-refresh-failure-agent",
		sourceYAML,
		now,
	)
	launch, err := fixture.Store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      kernelTestProjectID,
			ProfileID:      agent.ID,
			AgentConfigID:  agent.CurrentConfigID,
			LaunchedBy:     kernelTestUserPrincipal(user.ID),
			IdempotencyKey: "kernel-mcp-refresh-failure-agent",
		},
	)
	if err != nil {
		t.Fatalf("launch agent: %v", err)
	}
	input := fixture.admitContentInputTurn(
		t,
		ctx,
		launch.Agent.ID,
		kernelTestUserID,
		"use mcp failure",
		now.Add(2*time.Millisecond),
	)
	modelClient := &sequenceKernelModel{
		providerModelSlug: "test-model",
		errs: []error{
			nil,
			model.ProviderError{
				Kind:    model.ErrorKindTransient,
				Source:  "test-provider",
				Message: "retry the post-tool model call",
			},
			nil,
		},
		responses: []model.Response{
			{
				ID:         "resp-mcp-failed-tool",
				StopReason: model.StopReasonToolUse,
				Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{
					{
						ID:    "call_mcp_failed_greet",
						Name:  toolcatalog.MCPRuntimeToolName("docs", "greet"),
						Input: json.RawMessage(`{"name":"Ada"}`),
					},
				}),
			},
			{ID: "resp-mcp-failed-final", Content: []model.ResponsePart{{Type: "text", Text: "continued after mcp failure"}}, StopReason: model.StopReasonEndTurn},
		},
	}
	mcpClient := &fakeKernelMCPClient{
		agentID:            "remote-session-recovered",
		protocolVersion:    mcp.ProtocolVersion,
		initializeAgentIDs: []string{"remote-session-1"},
		tools: []*sdkmcp.Tool{
			{Name: "greet", Description: "say hi", InputSchema: map[string]any{"type": "object"}},
		},
		callToolErrors: []error{mcp.ErrSessionExpired},
		failInitializeSequences: map[string][]error{
			"https://example.com/mcp": {nil, &mcp.HTTPError{Status: http.StatusUnauthorized}},
		},
	}
	currentNow := now.Add(3 * time.Millisecond)
	executor := AgentExecutor{
		Store:           fixture.Store,
		ModelResolver:   liveTestModelResolver(fixture.Store, modelClient),
		MCP:             mcpClient,
		ToolExecutor:    tools.Executor{Store: fixture.Store, MCP: mcpClient},
		Now:             func() time.Time { return currentNow },
		ModelRetryDelay: immediateKernelModelRetryDelay,
	}
	continuation := executeAsyncToolTurn(t, ctx, fixture, executor, input)
	retry := continueTurnOnNewLeaseForKernelTest(
		t, ctx, fixture, continuation, now.Add(time.Hour),
	)
	currentNow = retry.Now
	if err := executor.ExecuteModelWork(ctx, retry); err != nil {
		t.Fatalf("execute retried continuation: %v", err)
	}
	if modelClient.preparedCount() != 3 {
		t.Fatalf("prepared %d requests, want tool call and two continuation attempts", modelClient.preparedCount())
	}
	if len(modelClient.prepared[0].ToolSpecs) != 1 ||
		modelClient.prepared[0].ToolSpecs[0].Name != toolcatalog.MCPRuntimeToolName("docs", "greet") {
		t.Fatalf("first request should include mcp tool, got %+v", modelClient.prepared[0].ToolSpecs)
	}
	if len(modelClient.prepared[1].ToolSpecs) != 0 {
		t.Fatalf("first continuation attempt should remove failed mcp tools, got %+v", modelClient.prepared[1].ToolSpecs)
	}
	if len(modelClient.prepared[2].ToolSpecs) != 0 {
		t.Fatalf("retried continuation should keep failed mcp tools removed, got %+v", modelClient.prepared[2].ToolSpecs)
	}
	if mcpClient.initializeCount != 2 {
		t.Fatalf("same-turn model retry reinitialized failed mcp connection: initialize=%d, want 2", mcpClient.initializeCount)
	}
	conn, found, err := fixture.Store.Execution().GetMCPConnection(ctx, kernelTestProjectID, launch.Agent.ID, "docs")
	if err != nil || !found {
		t.Fatalf("load failed mcp connection: found=%t err=%v", found, err)
	}
	if conn.State != executionstore.MCPConnectionStateFailed ||
		!strings.Contains(conn.InitializeError, "unexpected HTTP status 401") {
		t.Fatalf("unexpected failed mcp connection: %+v", conn)
	}
	var toolResult string
	if err := fixture.Pool.QueryRow(ctx, `
SELECT block.structured_data::text
FROM tool_call_read_projection call
JOIN tool_call_results result
	  ON result.agent_id = call.agent_id
 AND result.tool_call_id = call.id
JOIN content_blocks block
	  ON block.agent_id = result.agent_id
 AND block.owner_tool_call_result_id = result.id
 AND block.block_kind = 'structured_data'
WHERE call.project_id = $1
  AND call.agent_id = $2
  AND call.provider_call_id = 'call_mcp_failed_greet'
  AND call.state = 'completed'
ORDER BY block.ordinal
LIMIT 1
`, kernelTestProjectID, launch.Agent.ID).Scan(&toolResult); err != nil {
		t.Fatalf("load mcp failure tool result: %v", err)
	}
	if !strings.Contains(toolResult, `"error_code": "mcp_connection_failed"`) &&
		!strings.Contains(toolResult, `"error_code":"mcp_connection_failed"`) {
		t.Fatalf("tool result should report mcp connection failure, got %s", toolResult)
	}
}

func TestAgentExecutorMCPInitializationFailuresStoreConnectionErrorWithoutBlocking(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	user, err := storagetest.CreateVerifiedUser(
		ctx,
		fixture.Pool,
		storagetest.CreateVerifiedUserInput{
			Email:       "kernel-mcp-failure@example.com",
			DisplayName: "Kernel MCP Failure User",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	sourceYAML := `
name: Kernel MCP Failure
instruction: Continue even if one MCP server fails.
model:
  provider_config: openai-prod
  name: test-model
mcp:
  bad:
    url: https://example.com/bad-mcp
    permission:
      mode: always_allow
  good:
    url: https://example.com/good-mcp
    permission:
      mode: always_allow
`
	agent := fixture.createConfigAndProfileBookmark(
		t,
		ctx,
		"Kernel MCP Failure",
		"kernel-mcp-failure-agent",
		sourceYAML,
		now,
	)
	launch, err := fixture.Store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      kernelTestProjectID,
			ProfileID:      agent.ID,
			AgentConfigID:  agent.CurrentConfigID,
			LaunchedBy:     kernelTestUserPrincipal(user.ID),
			IdempotencyKey: "kernel-mcp-failure-agent",
		},
	)
	if err != nil {
		t.Fatalf("launch agent: %v", err)
	}
	if len(launch.MCPConnections) != 2 {
		t.Fatalf("expected two mcp connections, got %+v", launch.MCPConnections)
	}
	input := fixture.admitContentInputTurn(t, ctx, launch.Agent.ID, kernelTestUserID, "hello", now.Add(2*time.Millisecond))
	modelClient := &sequenceKernelModel{
		providerModelSlug: "test-model",
		responses:         []model.Response{{ID: "resp-mcp-failure", Content: []model.ResponsePart{{Type: "text", Text: "continued"}}, StopReason: model.StopReasonEndTurn}},
	}
	mcpClient := &fakeKernelMCPClient{
		agentID:         "remote-session",
		protocolVersion: mcp.ProtocolVersion,
		tools: []*sdkmcp.Tool{
			{Name: "greet", Description: "say hi", InputSchema: map[string]any{"type": "object"}},
		},
		failInitializeEndpoints: map[string]error{
			"https://example.com/bad-mcp": &mcp.HTTPError{Status: http.StatusUnauthorized},
		},
	}
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		MCP:           mcpClient,
		Now:           func() time.Time { return now.Add(3 * time.Millisecond) },
	}
	if err := executor.ExecuteModelWork(ctx, input); err != nil {
		t.Fatalf("execute turn: %v", err)
	}
	if modelClient.preparedCount() != 1 {
		t.Fatalf("expected model generation to continue, got %d prepares", modelClient.preparedCount())
	}
	if len(modelClient.prepared[0].ToolSpecs) != 1 ||
		modelClient.prepared[0].ToolSpecs[0].Name != toolcatalog.MCPRuntimeToolName("good", "greet") {
		t.Fatalf("model should only receive ready mcp tools, got %+v", modelClient.prepared[0].ToolSpecs)
	}
	if mcpClient.initializeCount != 2 {
		t.Fatalf(
			"non-retryable auth failure should only be attempted once per server, got initialize=%d",
			mcpClient.initializeCount,
		)
	}
	good, found, err := fixture.Store.Execution().GetMCPConnection(ctx, kernelTestProjectID, launch.Agent.ID, "good")
	if err != nil || !found {
		t.Fatalf("load good mcp connection: found=%t err=%v", found, err)
	}
	if good.State != executionstore.MCPConnectionStateReady {
		t.Fatalf("good mcp connection should be ready, got %+v", good)
	}
	bad, found, err := fixture.Store.Execution().GetMCPConnection(ctx, kernelTestProjectID, launch.Agent.ID, "bad")
	if err != nil || !found {
		t.Fatalf("load bad mcp connection: found=%t err=%v", found, err)
	}
	if bad.State != executionstore.MCPConnectionStateFailed ||
		!strings.Contains(bad.InitializeError, "unexpected HTTP status 401") {
		t.Fatalf("bad mcp connection should store initialize failure, got %+v", bad)
	}
	var count int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
	FROM agent_events event
	JOIN agents agent ON agent.id = event.agent_id
	WHERE agent.project_id = $1
	  AND event.agent_id = $2
	  AND event.event_kind NOT IN ('agent_input', 'model_output', 'tool_result')
`, kernelTestProjectID, launch.Agent.ID).Scan(&count); err != nil {
		t.Fatalf("query unsupported agent events: %v", err)
	}
	if count != 0 {
		t.Fatalf("mcp initialization failure should not append unsupported inline error events, got %d", count)
	}
}
