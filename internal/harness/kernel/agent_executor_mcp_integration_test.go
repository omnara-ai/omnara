//go:build integration

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/mcp"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
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
	agent := fixture.createConfigAndProfileBookmark(t, ctx, "Kernel MCP", "kernel-mcp-agent", sourceYAML)
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
		responses: []model.Response{
			{ID: "resp-mcp", Content: []model.ResponsePart{{Type: "text", Text: "done"}}, StopReason: model.StopReasonEndTurn},
			{
				ID:         "resp-mcp-removed",
				Content:    []model.ResponsePart{{Type: "text", Text: "done without mcp"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	mcpClient := &fakeKernelMCPClient{
		agentID:         "remote-session",
		protocolVersion: mcp.LegacyProtocolVersion,
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
		conn.ProtocolVersion != mcp.LegacyProtocolVersion {
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
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		kernelTestProjectID,
		launch.Agent.ID,
		input.RuntimeLockID,
	); err != nil {
		t.Fatalf("release runtime before mcp config removal: %v", err)
	}
	nextConfig := fixture.kernelAgentConfigInput(t, ctx, "Kernel MCP Without MCP", "test-model")
	if _, err := fixture.Store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: nextConfig,
		AgentID:                launch.Agent.ID,
		Reason:                 "test_remove_mcp",
		IdempotencyKey:         "kernel-mcp-remove-live",
	}); err != nil {
		t.Fatalf("remove mcp config: %v", err)
	}
	if _, _, _, err := fixture.Store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      kernelTestProjectID,
			AgentID:        launch.Agent.ID,
			Actor:          kernelTestOmnaraActorParams(t, kernelTestUserID),
			ContentBlocks:  mustKernelJSON([]map[string]string{{"type": "text", "text": "continue"}}),
			IdempotencyKey: "kernel-mcp-remove-input",
		},
	); err != nil {
		t.Fatalf("create input after mcp config removal: %v", err)
	}
	claim, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		kernelTestClaimInput(input.Now.Add(3*time.Second)),
	)
	if err != nil {
		t.Fatalf("claim mcp config removal work: %v", err)
	}
	if !found || claim.AgentID != launch.Agent.ID || claim.Kind != executionstore.AgentWorkModel {
		t.Fatalf("unexpected mcp config removal work: found=%t claim=%+v", found, claim)
	}
	next := modelWorkExecutionFromClaimForKernelTest(claim, input.Now.Add(4*time.Second))
	if err := executor.ExecuteModelWork(ctx, next); err != nil {
		t.Fatalf("execute mcp config removal work: %v", err)
	}
	if modelClient.preparedCount() != 2 || len(modelClient.prepared[1].ToolSpecs) != 0 {
		t.Fatalf("expected second model prepare without mcp tools, got %+v", modelClient.prepared)
	}
	if mcpClient.initializeCount != 3 {
		t.Fatalf("initialize count after mcp removal = %d, want 3", mcpClient.initializeCount)
	}
	removed, found, err := fixture.Store.Execution().GetMCPConnection(
		ctx,
		kernelTestProjectID,
		launch.Agent.ID,
		"docs",
	)
	if err != nil {
		t.Fatalf("load removed mcp connection: %v", err)
	}
	if !found {
		t.Fatal("expected removed mcp connection history")
	}
	if removed.State != executionstore.MCPConnectionStateExpired || removed.MCPSessionID != "" ||
		string(removed.ToolsSnapshot) != "[]" || removed.Generation != conn.Generation+1 {
		t.Fatalf("unexpected removed mcp connection: %+v", removed)
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
			{
				ID:         "resp-mcp-connect-retry",
				Content:    []model.ResponsePart{{Type: "text", Text: "continued"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	mcpClient := &fakeKernelMCPClient{
		protocolVersion: mcp.LegacyProtocolVersion,
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
			{
				ID:         "resp-mcp-list-tools-failure",
				Content:    []model.ResponsePart{{Type: "text", Text: "continued"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	mcpClient := &fakeKernelMCPClient{
		protocolVersion:    mcp.LegacyProtocolVersion,
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
	if conn.State != executionstore.MCPConnectionStateFailed ||
		!strings.Contains(conn.InitializeError, "list mcp tools") ||
		!strings.Contains(conn.InitializeError, "unexpected HTTP status 503") {
		t.Fatalf("mcp connection should store tools/list failure, got %+v", conn)
	}
}

func TestAgentExecutorRecoversMCPConnectionBeforeContinuation(t *testing.T) {
	for _, tc := range []struct {
		name            string
		retryError      error
		interrupt       bool
		failBegin       bool
		wantInitializes int
		wantSession     string
	}{
		{name: "ready refresh is reused", wantInitializes: 2, wantSession: "session-2"},
		{
			name: "refreshed session expires again", retryError: mcp.ErrSessionExpired,
			wantInitializes: 3, wantSession: "session-3",
		},
		{name: "interrupted refresh", interrupt: true, wantInitializes: 3, wantSession: "session-3"},
		{name: "initialization storage failure", retryError: mcp.ErrSessionExpired, failBegin: true, wantInitializes: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newKernelFixture(t, ctx)
			profile := fixture.createConfigAndProfileBookmark(t, ctx, "MCP Recovery", "mcp-recovery", `
instruction: Use MCP tools.
model:
  provider_config: openai-prod
  name: test-model
mcp:
  docs:
    url: https://example.com/mcp
    permission:
      mode: always_allow
`)
			launch, err := fixture.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
				ProjectID: kernelTestProjectID, ProfileID: profile.ID, AgentConfigID: profile.CurrentConfigID,
				LaunchedBy: kernelTestUserPrincipal(kernelTestUserID), IdempotencyKey: "mcp-recovery",
			})
			require.NoError(t, err)
			input := fixture.admitContentInputTurn(t, ctx, launch.Agent.ID, kernelTestUserID, "use mcp", fixture.Now)
			toolName := toolcatalog.MCPRuntimeToolName("docs", "greet")
			modelClient := &sequenceKernelModel{providerModelSlug: "test-model", responses: []model.Response{
				{ID: "tool", StopReason: model.StopReasonToolUse, Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{
					{ID: "call_greet", Name: toolName, Input: json.RawMessage(`{"name":"Ada"}`)},
				})},
				{ID: "final", StopReason: model.StopReasonEndTurn, Content: []model.ResponsePart{{Type: "text", Text: "done"}}},
			}}
			mcpClient := &fakeKernelMCPClient{
				protocolVersion: mcp.LegacyProtocolVersion, initializeAgentIDs: []string{"session-1", "session-2", "session-3"},
				tools:          []*sdkmcp.Tool{{Name: "greet", InputSchema: map[string]any{"type": "object"}}},
				callToolErrors: []error{mcp.ErrSessionExpired, tc.retryError},
				callToolResult: &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "hello"}}},
			}
			executor := AgentExecutor{
				Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, modelClient), MCP: mcpClient,
				ToolExecutor: tools.Executor{Store: fixture.Store, MCP: mcpClient},
			}
			require.NoError(t, executor.ExecuteModelWork(ctx, input))
			scope := executeNextToolWork(t, ctx, fixture, executor, input)
			select {
			case <-scope.Done():
			case <-time.After(15 * time.Second):
				t.Fatal("async MCP tool work did not finish")
			}
			require.NoError(t, scope.Err())
			if tc.interrupt {
				conn, found, err := fixture.Store.Execution().GetMCPConnection(ctx, kernelTestProjectID, launch.Agent.ID, "docs")
				require.NoError(t, err)
				require.True(t, found)
				_, changed, err := fixture.Store.Execution().MarkMCPConnectionExpired(
					ctx, kernelTestProjectID, launch.Agent.ID, conn.ID, conn.Generation,
				)
				require.NoError(t, err)
				require.True(t, changed)
				_, changed, err = fixture.Store.Execution().BeginMCPConnectionInitialization(
					ctx, kernelTestProjectID, launch.Agent.ID, conn.ID,
				)
				require.NoError(t, err)
				require.True(t, changed)
			}
			if tc.failBegin {
				_, err := fixture.Pool.Exec(ctx, `ALTER TABLE agent_mcp_connections
					ADD CONSTRAINT test_block_initialization CHECK (state <> 'initializing') NOT VALID`)
				require.NoError(t, err)
			}
			next := executeNextModelWork(t, ctx, fixture, executor, input)
			require.Equal(t, executionstore.ModelWorkContinue, next.Kind)
			require.Equal(t, tc.wantInitializes, mcpClient.initializeCount)
			require.Equal(t, tc.wantInitializes, mcpClient.notifyCount)
			require.Equal(t, 1, mcpClient.listToolsCount)
			require.Equal(t, 2, mcpClient.callToolCount)
			require.Len(t, mcpClient.callToolConns, 2)
			require.Equal(t, "session-1", mcpClient.callToolConns[0].MCPSessionID)
			require.Equal(t, "session-2", mcpClient.callToolConns[1].MCPSessionID)
			if tc.failBegin {
				require.Len(t, modelClient.prepared, 1, "storage failure must prevent the model request")
				var code, recovery string
				require.NoError(t, fixture.Pool.QueryRow(ctx, `SELECT error_code, recovery_kind
					FROM model_call_contexts WHERE agent_id = $1 AND state = 'failed'`, launch.Agent.ID).
					Scan(&code, &recovery))
				require.Equal(t, preSendErrorCodeInitializeMCPFailed, code)
				require.Equal(t, "retry", recovery)
				return
			}
			require.Len(t, modelClient.prepared, 2)
			require.Contains(t, toolSpecSet(modelClient.prepared[1].ToolSpecs), toolName)
			conn, found, err := fixture.Store.Execution().GetMCPConnection(ctx, kernelTestProjectID, launch.Agent.ID, "docs")
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, executionstore.MCPConnectionStateReady, conn.State)
			require.Equal(t, tc.wantSession, conn.MCPSessionID)
			require.Empty(t, conn.InitializeError)
		})
	}
}

func TestAgentExecutorAppliesMCPConfigChangeAfterPendingTool(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	oldSource := `
instruction: Use MCP tools.
model:
  provider_config: openai-prod
  name: test-model
mcp:
  docs:
    url: https://old.example.com/mcp
    permission:
      mode: always_allow
`
	profile := fixture.createConfigAndProfileBookmark(
		t,
		ctx,
		"Kernel MCP Config Change",
		"kernel-mcp-config-change",
		oldSource,
	)
	launch, err := fixture.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      kernelTestProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     kernelTestUserPrincipal(kernelTestUserID),
		IdempotencyKey: "kernel-mcp-config-change-launch",
	})
	if err != nil {
		t.Fatalf("launch agent: %v", err)
	}
	newSource := strings.Replace(oldSource, "https://old.example.com/mcp", "https://new.example.com/mcp", 1)
	compiled := fixture.compileAgentYAMLResolved(t, ctx, newSource)
	nextConfig := executionstore.CreateAgentConfigInput{
		ProjectID:               kernelTestProjectID,
		Definition:              json.RawMessage(compiled.CanonicalJSON),
		Source:                  newSource,
		SourceFormat:            "yaml",
		ConfiguredModelID:       parseConfiguredModelID(t, compiled),
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiled.Hash,
	}
	var changeErr error
	modelClient := &sequenceKernelModel{
		providerModelSlug: "test-model",
		responses: []model.Response{
			{
				ID:         "resp-mcp-config-change-tool",
				StopReason: model.StopReasonToolUse,
				Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{{
					ID:    "call_mcp_config_change",
					Name:  toolcatalog.MCPRuntimeToolName("docs", "greet"),
					Input: json.RawMessage(`{"name":"Ada"}`),
				}}),
			},
			{
				ID:         "resp-mcp-config-change-final",
				Content:    []model.ResponsePart{{Type: "text", Text: "done"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
		afterRespond: func(response model.Response) {
			if response.ID != "resp-mcp-config-change-tool" {
				return
			}
			_, changeErr = fixture.Store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
				CreateAgentConfigInput: nextConfig,
				AgentID:                launch.Agent.ID,
				Reason:                 "test_mcp_config_change",
				IdempotencyKey:         "kernel-mcp-config-change-live",
			})
		},
	}
	mcpClient := &fakeKernelMCPClient{
		protocolVersion:    mcp.LegacyProtocolVersion,
		initializeAgentIDs: []string{"remote-session-1", "remote-session-2"},
		tools: []*sdkmcp.Tool{
			{Name: "greet", Description: "say hi", InputSchema: map[string]any{"type": "object"}},
		},
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
	input := fixture.admitContentInputTurn(
		t,
		ctx,
		launch.Agent.ID,
		kernelTestUserID,
		"use mcp while its config changes",
		now.Add(2*time.Millisecond),
	)
	executeAsyncToolTurn(t, ctx, fixture, executor, input)
	if changeErr != nil {
		t.Fatalf("change mcp config: %v", changeErr)
	}
	if mcpClient.initializeCount != 2 || mcpClient.callToolCount != 1 || len(mcpClient.callToolConns) != 1 {
		t.Fatalf(
			"unexpected mcp calls: initialize=%d call=%d conns=%+v",
			mcpClient.initializeCount,
			mcpClient.callToolCount,
			mcpClient.callToolConns,
		)
	}
	called := mcpClient.callToolConns[0]
	if called.EndpointURL != "https://old.example.com/mcp" || called.MCPSessionID != "remote-session-1" {
		t.Fatalf("pending tool used changed mcp connection: %+v", called)
	}
	conn, found, err := fixture.Store.Execution().GetMCPConnection(
		ctx,
		kernelTestProjectID,
		launch.Agent.ID,
		"docs",
	)
	if err != nil || !found {
		t.Fatalf("load changed mcp connection: found=%t err=%v", found, err)
	}
	if conn.EndpointURL != "https://new.example.com/mcp" || conn.State != executionstore.MCPConnectionStateReady ||
		conn.MCPSessionID != "remote-session-2" {
		t.Fatalf("new model round did not initialize changed mcp connection: %+v", conn)
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
			{
				ID:         "resp-mcp-failed-final",
				Content:    []model.ResponsePart{{Type: "text", Text: "continued after mcp failure"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	mcpClient := &fakeKernelMCPClient{
		agentID:            "remote-session-recovered",
		protocolVersion:    mcp.LegacyProtocolVersion,
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
		t.Fatalf(
			"same-turn model retry reinitialized failed mcp connection: initialize=%d, want 2",
			mcpClient.initializeCount,
		)
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
		responses: []model.Response{
			{
				ID:         "resp-mcp-failure",
				Content:    []model.ResponsePart{{Type: "text", Text: "continued"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	mcpClient := &fakeKernelMCPClient{
		agentID:         "remote-session",
		protocolVersion: mcp.LegacyProtocolVersion,
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

func TestAgentExecutorSharesStatelessCatalogAcrossAgents(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	user, err := storagetest.CreateVerifiedUser(
		ctx,
		fixture.Pool,
		storagetest.CreateVerifiedUserInput{
			Email:       "kernel-mcp-stateless@example.com",
			DisplayName: "Kernel MCP Stateless User",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	sourceYAML := `
instruction: Use MCP tools.
model:
  provider_config: openai-prod
  name: test-model
mcp:
  docs:
    url: https://stateless.example.com/mcp
    permission:
      mode: always_allow
`
	mcpClient := &fakeKernelMCPClient{
		stateless:  true,
		toolsTTLMs: 60_000,
		tools: []*sdkmcp.Tool{
			{Name: "greet", Description: "say hi", InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":   map[string]any{"type": "string"},
					"region": map[string]any{"type": "string", "x-mcp-header": "Region"},
				},
			}},
		},
		callToolResult: &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "hello"}},
		},
	}
	launchAgent := func(name, key string) executionstore.LaunchAgentResult {
		t.Helper()
		agent := fixture.createConfigAndProfileBookmark(t, ctx, name, key, sourceYAML, now)
		launch, err := fixture.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
			ProjectID:      kernelTestProjectID,
			ProfileID:      agent.ID,
			AgentConfigID:  agent.CurrentConfigID,
			LaunchedBy:     kernelTestUserPrincipal(user.ID),
			IdempotencyKey: key,
		})
		if err != nil {
			t.Fatalf("launch %s: %v", name, err)
		}
		return launch
	}
	first := launchAgent("Kernel MCP Stateless A", "kernel-mcp-stateless-a")
	second := launchAgent("Kernel MCP Stateless B", "kernel-mcp-stateless-b")

	firstInput := fixture.admitContentInputTurn(
		t, ctx, first.Agent.ID, kernelTestUserID, "use mcp", now.Add(2*time.Millisecond),
	)
	firstModel := &sequenceKernelModel{
		providerModelSlug: "test-model",
		responses: []model.Response{
			{
				ID:         "resp-stateless-tool",
				StopReason: model.StopReasonToolUse,
				Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{
					{
						ID:    "call_stateless_greet",
						Name:  toolcatalog.MCPRuntimeToolName("docs", "greet"),
						Input: json.RawMessage(`{"name":"Ada","region":"us-west1"}`),
					},
				}),
			},
			{
				ID:         "resp-stateless-final",
				Content:    []model.ResponsePart{{Type: "text", Text: "done after stateless mcp"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	firstExecutor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, firstModel),
		MCP:           mcpClient,
		ToolExecutor:  tools.Executor{Store: fixture.Store, MCP: mcpClient},
		Now:           func() time.Time { return now.Add(3 * time.Millisecond) },
	}
	_ = executeAsyncToolTurn(t, ctx, fixture, firstExecutor, firstInput)

	if mcpClient.discoverCount != 1 || mcpClient.listToolsCount != 1 || mcpClient.initializeCount != 0 ||
		mcpClient.notifyCount != 0 || mcpClient.callToolCount != 1 {
		t.Fatalf(
			"unexpected mcp calls after first agent: discover=%d list=%d initialize=%d notify=%d call=%d",
			mcpClient.discoverCount, mcpClient.listToolsCount, mcpClient.initializeCount,
			mcpClient.notifyCount, mcpClient.callToolCount,
		)
	}
	if len(mcpClient.callToolConns) != 1 || mcpClient.callToolConns[0].MCPSessionID != "" ||
		!mcpClient.callToolConns[0].Stateless() {
		t.Fatalf("stateless tool call used a session: %+v", mcpClient.callToolConns)
	}
	call := mcpClient.callToolCalls[0]
	if call.Name != "greet" || len(call.Headers) != 1 || call.Headers[0].Name != "Region" {
		t.Fatalf("stateless tool call did not carry header annotations: %+v", call)
	}
	firstConn, found, err := fixture.Store.Execution().GetMCPConnection(ctx, kernelTestProjectID, first.Agent.ID, "docs")
	if err != nil || !found {
		t.Fatalf("load first connection: found=%t err=%v", found, err)
	}
	if firstConn.State != executionstore.MCPConnectionStateReady || firstConn.MCPSessionID != "" ||
		firstConn.ProtocolVersion != mcp.StatelessProtocolVersion || !firstConn.UsesCatalog() ||
		firstConn.RequestSequence != 1 || firstConn.Instructions != "fake stateless server" {
		t.Fatalf("unexpected stateless connection: %+v", firstConn)
	}

	secondInput := fixture.admitContentInputTurn(
		t, ctx, second.Agent.ID, kernelTestUserID, "hello", now.Add(4*time.Millisecond),
	)
	secondModel := &sequenceKernelModel{
		providerModelSlug: "test-model",
		responses: []model.Response{
			{
				ID:         "resp-stateless-b",
				Content:    []model.ResponsePart{{Type: "text", Text: "done"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	secondExecutor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, secondModel),
		MCP:           mcpClient,
		Now:           func() time.Time { return now.Add(5 * time.Millisecond) },
	}
	if err := secondExecutor.ExecuteModelWork(ctx, secondInput); err != nil {
		t.Fatalf("execute second agent turn: %v", err)
	}
	if mcpClient.discoverCount != 1 || mcpClient.listToolsCount != 1 {
		t.Fatalf(
			"second agent should reuse the cached catalog: discover=%d list=%d",
			mcpClient.discoverCount, mcpClient.listToolsCount,
		)
	}
	if secondModel.preparedCount() != 1 || len(secondModel.prepared[0].ToolSpecs) != 1 ||
		secondModel.prepared[0].ToolSpecs[0].Name != toolcatalog.MCPRuntimeToolName("docs", "greet") {
		t.Fatalf("second agent did not expose the cached mcp tool: %+v", secondModel.prepared)
	}
	secondConn, found, err := fixture.Store.Execution().GetMCPConnection(ctx, kernelTestProjectID, second.Agent.ID, "docs")
	if err != nil || !found {
		t.Fatalf("load second connection: found=%t err=%v", found, err)
	}
	if !secondConn.UsesCatalog() || *secondConn.CatalogID != *firstConn.CatalogID ||
		secondConn.State != executionstore.MCPConnectionStateReady {
		t.Fatalf("second agent bound a different catalog: first=%+v second=%+v", firstConn, secondConn)
	}
}

func TestAgentExecutorSharesSessionBasedCatalogAcrossAgents(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	user, err := storagetest.CreateVerifiedUser(
		ctx,
		fixture.Pool,
		storagetest.CreateVerifiedUserInput{
			Email:       "kernel-mcp-legacy-shared@example.com",
			DisplayName: "Kernel MCP Legacy Shared User",
		},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	sourceYAML := `
instruction: Use MCP tools.
model:
  provider_config: openai-prod
  name: test-model
mcp:
  docs:
    url: https://legacy.example.com/mcp
    permission:
      mode: always_allow
`
	mcpClient := &fakeKernelMCPClient{
		agentID:            "shared-session",
		initializeAgentIDs: []string{"session-a", "session-b"},
		protocolVersion:    mcp.LegacyProtocolVersion,
		tools: []*sdkmcp.Tool{
			{Name: "greet", Description: "say hi", InputSchema: map[string]any{"type": "object"}},
		},
	}
	runAgent := func(name, key string, at time.Duration) executionstore.MCPConnectionRecord {
		t.Helper()
		agent := fixture.createConfigAndProfileBookmark(t, ctx, name, key, sourceYAML, now)
		launch, err := fixture.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
			ProjectID:      kernelTestProjectID,
			ProfileID:      agent.ID,
			AgentConfigID:  agent.CurrentConfigID,
			LaunchedBy:     kernelTestUserPrincipal(user.ID),
			IdempotencyKey: key,
		})
		if err != nil {
			t.Fatalf("launch %s: %v", name, err)
		}
		input := fixture.admitContentInputTurn(t, ctx, launch.Agent.ID, kernelTestUserID, "hello", now.Add(at))
		modelClient := &sequenceKernelModel{
			providerModelSlug: "test-model",
			responses: []model.Response{
				{
					ID:         "resp-" + key,
					Content:    []model.ResponsePart{{Type: "text", Text: "done"}},
					StopReason: model.StopReasonEndTurn,
				},
			},
		}
		executor := AgentExecutor{
			Store:         fixture.Store,
			ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
			MCP:           mcpClient,
			Now:           func() time.Time { return now.Add(at + time.Millisecond) },
		}
		if err := executor.ExecuteModelWork(ctx, input); err != nil {
			t.Fatalf("execute %s: %v", name, err)
		}
		if modelClient.preparedCount() != 1 || len(modelClient.prepared[0].ToolSpecs) != 1 {
			t.Fatalf("%s did not expose the mcp tool: %+v", name, modelClient.prepared)
		}
		conn, found, err := fixture.Store.Execution().GetMCPConnection(ctx, kernelTestProjectID, launch.Agent.ID, "docs")
		if err != nil || !found {
			t.Fatalf("load %s connection: found=%t err=%v", name, found, err)
		}
		return conn
	}
	first := runAgent("Kernel MCP Legacy A", "kernel-mcp-legacy-a", 2*time.Millisecond)
	second := runAgent("Kernel MCP Legacy B", "kernel-mcp-legacy-b", 4*time.Millisecond)

	if mcpClient.discoverCount != 2 || mcpClient.initializeCount != 2 || mcpClient.notifyCount != 2 ||
		mcpClient.listToolsCount != 1 {
		t.Fatalf(
			"unexpected mcp calls: discover=%d initialize=%d notify=%d list=%d",
			mcpClient.discoverCount, mcpClient.initializeCount, mcpClient.notifyCount, mcpClient.listToolsCount,
		)
	}
	if first.MCPSessionID != "session-a" || second.MCPSessionID != "session-b" {
		t.Fatalf("each agent must keep its own session: first=%q second=%q", first.MCPSessionID, second.MCPSessionID)
	}
	if !first.UsesCatalog() || !second.UsesCatalog() || *first.CatalogID != *second.CatalogID {
		t.Fatalf("agents did not share the catalog: first=%+v second=%+v", first, second)
	}
	if first.RequestSequence != 2 || second.RequestSequence != 1 {
		t.Fatalf(
			"request sequences: first=%d want 2 (tools/list), second=%d want 1 (cached)",
			first.RequestSequence, second.RequestSequence,
		)
	}
}

func TestMCPManagerCatalogRecoveryAndProtocolCutover(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	source := `
instruction: Use MCP tools.
model:
  provider_config: openai-prod
  name: test-model
mcp:
  docs:
    url: https://cutover.example.com/mcp
    permission:
      mode: always_allow
`
	profile := fixture.createConfigAndProfileBookmark(t, ctx, "MCP cutover", "mcp-cutover", source, fixture.Now)
	launch, err := fixture.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      kernelTestProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     kernelTestUserPrincipal(kernelTestUserID),
		IdempotencyKey: "mcp-cutover",
	})
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeKernelMCPClient{
		agentID:         "legacy-session",
		protocolVersion: mcp.LegacyProtocolVersion,
		tools:           []*sdkmcp.Tool{nil, {Name: "greet", InputSchema: map[string]any{"type": "object"}}},
	}
	var clockSkew time.Duration
	var nextClockSkews []time.Duration
	manager := mcp.Manager{
		Execution: fixture.Store.Execution(),
		Secrets:   fixture.Store.Secrets(),
		Client:    client,
		Backoff:   func(int) time.Duration { return 0 },
		Now: func() time.Time {
			if len(nextClockSkews) != 0 {
				skew := nextClockSkews[0]
				nextClockSkews = nextClockSkews[1:]
				return time.Now().Add(skew)
			}
			return time.Now().Add(clockSkew)
		},
	}
	server := agentconfig.RuntimeMCPServer{ServerKey: "docs", URL: "https://cutover.example.com/mcp", DefaultEnabled: true}
	expireTools := func() { clockSkew = mcp.DefaultCatalogMinFreshness + time.Minute }
	restoreClock := func() { clockSkew = 0 }
	load := func(t *testing.T) executionstore.MCPConnectionRecord {
		t.Helper()
		conn, found, err := fixture.Store.Execution().GetMCPConnection(ctx, kernelTestProjectID, launch.Agent.ID, "docs")
		if err != nil || !found {
			t.Fatalf("load connection: found=%v err=%v", found, err)
		}
		return conn
	}
	ensure := func(conn executionstore.MCPConnectionRecord) (mcp.ConnectionResult, error) {
		return manager.EnsureConnection(
			ctx, kernelTestOrgID, kernelTestProjectID, launch.Agent.ID, conn, server, mcp.TriggerTurnStart,
		)
	}
	ensureReady := func(t *testing.T, conn executionstore.MCPConnectionRecord) mcp.ConnectionResult {
		t.Helper()
		result, err := ensure(conn)
		if err != nil || !result.Ready {
			t.Fatalf("ensure ready: %+v %v", result, err)
		}
		return result
	}
	ensureFailed := func(t *testing.T, conn executionstore.MCPConnectionRecord, wantError string) {
		t.Helper()
		result, err := ensure(conn)
		if err == nil || result.Ready || result.Conn.State != executionstore.MCPConnectionStateFailed {
			t.Fatalf("ensure failed: %+v %v", result, err)
		}
		if got := load(t).InitializeError; !strings.Contains(got, wantError) {
			t.Fatalf("initialize error = %q, want containing %q", got, wantError)
		}
	}
	var legacy executionstore.MCPConnectionRecord

	t.Run("legacy initialize drops null tools and bounds the fetch", func(t *testing.T) {
		legacy = ensureReady(t, launch.MCPConnections[0]).Conn
		if legacy.MCPSessionID != "legacy-session" {
			t.Fatalf("session = %q", legacy.MCPSessionID)
		}
		if strings.Contains(string(legacy.ToolsSnapshot), "null") {
			t.Fatalf("null tool persisted: %s", legacy.ToolsSnapshot)
		}
		if client.listToolsDeadline.IsZero() || time.Until(client.listToolsDeadline) > 15*time.Second {
			t.Fatalf("catalog fetch bypassed 15s timeout: %v", client.listToolsDeadline)
		}
	})

	t.Run("legacy connection upgrades when the server turns stateless", func(t *testing.T) {
		client.stateless = true
		expireTools()
		upgraded := ensureReady(t, legacy).Conn
		if upgraded.ProtocolVersion != mcp.StatelessProtocolVersion || upgraded.MCPSessionID != "" {
			t.Fatalf("upgrade legacy: %+v", upgraded)
		}
	})

	t.Run("stale legacy record adopts the upgrade without listing tools", func(t *testing.T) {
		restoreClock()
		lists := client.listToolsCount
		adopted := ensureReady(t, legacy).Conn
		if adopted.ProtocolVersion != mcp.StatelessProtocolVersion || adopted.MCPSessionID != "" {
			t.Fatalf("adopt existing upgrade: %+v", adopted)
		}
		if client.listToolsCount != lists {
			t.Fatalf("lists = %d, want %d", client.listToolsCount, lists)
		}
	})

	t.Run("rollback to legacy fails without opening a session", func(t *testing.T) {
		client.stateless = false
		expireTools()
		initializations := client.initializeCount
		ensureFailed(t, load(t), "no longer speaks a stateless")
		if client.initializeCount != initializations {
			t.Fatalf("initializes = %d, want %d", client.initializeCount, initializations)
		}
	})

	t.Run("recovers once the server is stateless again", func(t *testing.T) {
		client.stateless = true
		restoreClock()
		ensureReady(t, load(t))
	})

	t.Run("preview failure supersedes the cached snapshot for every reader", func(t *testing.T) {
		client.listToolsErrors = []error{&mcp.HTTPError{Status: http.StatusUnauthorized, Body: []byte("token revoked")}}
		_, err := manager.DiscoverTools(ctx, kernelTestOrgID, kernelTestProjectID, server.URL, nil)
		if err == nil || !strings.Contains(err.Error(), "token revoked") {
			t.Fatalf("preview hid failure: %v", err)
		}
		failed := load(t)
		if failed.State != executionstore.MCPConnectionStateFailed ||
			!strings.Contains(failed.InitializeError, "token revoked") ||
			string(failed.ToolsSnapshot) != "[]" {
			t.Fatalf("newer error did not supersede catalog: %+v", failed)
		}
		if recovered := ensureReady(t, failed).Conn; recovered.InitializeError != "" {
			t.Fatalf("recover newer catalog: %+v", recovered)
		}
	})

	t.Run("detached connection reconnects instead of using an empty tool set", func(t *testing.T) {
		if _, err := fixture.Pool.Exec(
			ctx, `DELETE FROM mcp_server_catalogs WHERE id = $1`, *load(t).CatalogID,
		); err != nil {
			t.Fatal(err)
		}
		detached := load(t)
		if detached.UsesCatalog() {
			t.Fatal("catalog was not detached")
		}
		if reconnected := ensureReady(t, detached).Conn; !reconnected.UsesCatalog() {
			t.Fatalf("reconnect orphan: %+v", reconnected)
		}
	})

	t.Run("executor surfaces a recorded refresh failure on an established connection", func(t *testing.T) {
		reconciled, err := fixture.Store.Execution().ReconcileAgentMCPConnections(
			ctx, kernelTestProjectID, launch.Agent.ID, []agentconfig.RuntimeMCPServer{server},
		)
		if err != nil {
			t.Fatal(err)
		}
		established := ensureReady(t, reconciled[0]).Conn
		if _, err := fixture.Pool.Exec(ctx,
			`UPDATE mcp_server_catalogs
			 SET tools_expires_at = statement_timestamp() - interval '1 second'
			 WHERE id = $1`, *established.CatalogID,
		); err != nil {
			t.Fatal(err)
		}
		client.listToolsErrors = []error{
			&mcp.HTTPError{Status: http.StatusUnauthorized, Body: []byte("credentials rejected")},
		}
		executor := AgentExecutor{Store: fixture.Store, MCP: client}
		err = executor.ensureMCPConnections(
			ctx,
			kernelTestOrgID,
			ModelWorkExecution{ProjectID: kernelTestProjectID, AgentID: launch.Agent.ID},
			agentconfig.RuntimeContract{MCPServers: []agentconfig.RuntimeMCPServer{server}},
			mcp.TriggerTurnStart,
		)
		if err == nil || !strings.Contains(err.Error(), "credentials rejected") {
			t.Fatalf("executor hid an established connection failure: %v", err)
		}
		if failed := load(t); failed.State != executionstore.MCPConnectionStateFailed {
			t.Fatalf("failure not recorded: %+v", failed)
		}
		ensureReady(t, load(t))
	})

	t.Run("lease owner adopts a catalog refreshed while it waited", func(t *testing.T) {
		restoreClock()
		ready := ensureReady(t, load(t)).Conn
		nextClockSkews = []time.Duration{mcp.DefaultCatalogMinFreshness + time.Minute}
		lists := client.listToolsCount
		adopted := ensureReady(t, ready).Conn
		if len(nextClockSkews) != 0 {
			t.Fatal("stale clock reading was not consumed")
		}
		if client.listToolsCount != lists {
			t.Fatalf("lists = %d, want %d", client.listToolsCount, lists)
		}
		if adopted.CatalogRevision != ready.CatalogRevision {
			t.Fatalf("revision = %d, want %d", adopted.CatalogRevision, ready.CatalogRevision)
		}
		if catalog := load(t); catalog.State != executionstore.MCPConnectionStateReady {
			t.Fatalf("connection = %+v", catalog)
		}
	})

	t.Run("header mismatch retries only after the catalog changes", func(t *testing.T) {
		restoreClock()
		ready := ensureReady(t, load(t)).Conn
		mismatch := &mcp.RPCError{Code: mcp.CodeHeaderMismatch, Message: "stale headers", HTTPStatus: http.StatusBadRequest}
		client.callToolResult = &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "hi"}}}
		call := func(t *testing.T) (*sdkmcp.CallToolResult, error) {
			t.Helper()
			return manager.CallTool(ctx, mcp.ToolCallInput{
				OrgID:     kernelTestOrgID,
				ProjectID: kernelTestProjectID,
				AgentID:   launch.Agent.ID,
				Conn:      load(t),
				Server:    server,
				Name:      "greet",
				Arguments: json.RawMessage(`{}`),
			})
		}
		if _, err := fixture.Pool.Exec(ctx,
			`UPDATE mcp_server_catalogs
			 SET refresh_owner_token = $2,
			     refresh_lease_expires_at = statement_timestamp() + interval '1 minute'
			 WHERE id = $1`, *ready.CatalogID, uuid.New(),
		); err != nil {
			t.Fatal(err)
		}
		client.callToolErrors = []error{mismatch}
		calls, lists := client.callToolCount, client.listToolsCount
		if _, err := call(t); !errors.Is(err, mismatch) {
			t.Fatalf("call with a held refresh lease: %v", err)
		}
		if client.callToolCount != calls+1 || client.listToolsCount != lists {
			t.Fatalf("calls = %d lists = %d, want %d and %d", client.callToolCount, client.listToolsCount, calls+1, lists)
		}
		if _, err := fixture.Pool.Exec(ctx,
			`UPDATE mcp_server_catalogs
			 SET refresh_owner_token = NULL, refresh_lease_expires_at = NULL
			 WHERE id = $1`, *ready.CatalogID,
		); err != nil {
			t.Fatal(err)
		}
		client.callToolErrors = []error{mismatch}
		if _, err := call(t); err != nil {
			t.Fatalf("call after refresh: %v", err)
		}
		if client.callToolCount != calls+3 || client.listToolsCount != lists+1 {
			t.Fatalf("calls = %d lists = %d, want %d and %d", client.callToolCount, client.listToolsCount, calls+3, lists+1)
		}
		if refreshed := load(t); refreshed.CatalogRevision != ready.CatalogRevision+1 {
			t.Fatalf("revision = %d, want %d", refreshed.CatalogRevision, ready.CatalogRevision+1)
		}
	})

	t.Run("missing credential on a ready connection is recorded", func(t *testing.T) {
		secretID, err := publicid.Encode(publicid.KindSecret, uuid.New())
		if err != nil {
			t.Fatal(err)
		}
		server.Auth = &agentconfig.RuntimeMCPAuth{Type: agentconfig.MCPAuthTypeBearer, SecretID: secretID}
		ensureFailed(t, load(t), "read mcp auth secret")
	})
}
