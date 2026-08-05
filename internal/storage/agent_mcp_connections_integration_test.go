//go:build integration

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func TestAgentMCPConnectionsLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool)
	user, err := store.CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{Email: "mcp-connection@example.com", DisplayName: "MCP Connection User"},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	agent := createLaunchTestAgent(t, ctx, store, "idem-mcp-connection-agent", `
name: MCP Connection Agent
instruction: Use MCP later.
model:
  provider_config: openai-prod
  name: gpt-test
mcp:
  docs:
    url: https://example.com/mcp
    permission:
      mode: always_ask
      parameters: {}
`)
	launch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      agent.ID,
			AgentConfigID:  agent.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-mcp-connection-agent",
		},
	)
	if err != nil {
		t.Fatalf("launch agent: %v", err)
	}
	if len(launch.MCPServers) != 1 || len(launch.MCPConnections) != 1 {
		t.Fatalf(
			"expected launch to load mcp config and create connection, servers=%+v connections=%+v",
			launch.MCPServers,
			launch.MCPConnections,
		)
	}

	conn := launch.MCPConnections[0]
	if conn.State != executionstore.MCPConnectionStateInitializing || conn.Generation != 1 || conn.RequestSequence != 1 {
		t.Fatalf("unexpected new mcp connection: %+v", conn)
	}
	wrongProjectID := seedAdditionalProjectForTest(t, ctx, pool, "mcp_connection_wrong_scope")
	if _, err := store.Execution().MarkMCPConnectionReady(ctx, executionstore.MarkMCPConnectionReadyInput{
		ProjectID:          wrongProjectID,
		AgentID:            launch.Agent.ID,
		ID:                 conn.ID,
		GenerationObserved: conn.Generation,
		MCPSessionID:       "wrong-project-session",
		ProtocolVersion:    "2025-11-25",
		ServerCapabilities: json.RawMessage(`{"tools":{}}`),
		ServerInfo:         json.RawMessage(`{"name":"wrong-project"}`),
		ToolsSnapshot:      json.RawMessage(`[{"name":"wrong-project"}]`),
	}); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("wrong-project ready error = %v, want state transition conflict", err)
	}
	if _, changed, err := store.Execution().BeginMCPConnectionInitialization(
		ctx,
		wrongProjectID,
		launch.Agent.ID,
		conn.ID,
	); err != nil || changed {
		t.Fatalf("wrong-project initialization changed=%t err=%v, want false/nil", changed, err)
	}
	if _, err := store.Execution().MarkMCPConnectionFailed(
		ctx,
		wrongProjectID,
		launch.Agent.ID,
		conn.ID,
		conn.Generation,
		"wrong project",
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("wrong-project failure error = %v, want state transition conflict", err)
	}
	if _, changed, err := store.Execution().MarkMCPConnectionExpired(
		ctx,
		wrongProjectID,
		launch.Agent.ID,
		conn.ID,
		conn.Generation,
	); err != nil || changed {
		t.Fatalf("wrong-project expiration changed=%t err=%v, want false/nil", changed, err)
	}
	if _, err := store.Execution().NextMCPRequestSequence(
		ctx,
		wrongProjectID,
		launch.Agent.ID,
		conn.ID,
	); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("wrong-project request sequence error = %v, want not found", err)
	}
	unchanged, found, err := store.Execution().GetMCPConnection(ctx, testProjectID, launch.Agent.ID, "docs")
	if err != nil || !found {
		t.Fatalf("load connection after wrong-project mutations: found=%t err=%v", found, err)
	}
	if unchanged.State != executionstore.MCPConnectionStateInitializing || unchanged.Generation != conn.Generation ||
		unchanged.RequestSequence != conn.RequestSequence {
		t.Fatalf("wrong-project mutations changed connection: before=%+v after=%+v", conn, unchanged)
	}
	replayedLaunch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      agent.ID,
			AgentConfigID:  agent.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-mcp-connection-agent",
		},
	)
	if err != nil {
		t.Fatalf("replay launch: %v", err)
	}
	if replayedLaunch.Created || replayedLaunch.Agent.ID != launch.Agent.ID ||
		len(replayedLaunch.MCPConnections) != 0 {
		t.Fatalf("launch replay should return only the current agent: %+v", replayedLaunch)
	}

	ready, err := store.Execution().MarkMCPConnectionReady(ctx, executionstore.MarkMCPConnectionReadyInput{
		ProjectID:          testProjectID,
		AgentID:            launch.Agent.ID,
		ID:                 conn.ID,
		GenerationObserved: conn.Generation,
		MCPSessionID:       "remote-session",
		ProtocolVersion:    "2025-11-25",
		ServerCapabilities: json.RawMessage(`{"tools":{}}`),
		ServerInfo:         json.RawMessage(`{"name":"fixture"}`),
		ToolsSnapshot:      json.RawMessage(`[{"name":"search"}]`),
	})
	if err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	if ready.State != executionstore.MCPConnectionStateReady || ready.MCPSessionID != "remote-session" ||
		ready.ProtocolVersion != "2025-11-25" {
		t.Fatalf("unexpected ready connection: %+v", ready)
	}
	seq, err := store.Execution().NextMCPRequestSequence(ctx, testProjectID, launch.Agent.ID, conn.ID)
	if err != nil {
		t.Fatalf("next request sequence: %v", err)
	}
	if seq != 1 {
		t.Fatalf("first request sequence = %d, want 1", seq)
	}
	expired, changed, err := store.Execution().MarkMCPConnectionExpired(
		ctx,
		testProjectID,
		launch.Agent.ID,
		conn.ID,
		ready.Generation,
	)
	if err != nil {
		t.Fatalf("mark expired: %v", err)
	}
	if !changed || expired.State != executionstore.MCPConnectionStateExpired || expired.Generation != ready.Generation+1 ||
		expired.MCPSessionID != "" {
		t.Fatalf("unexpected expired connection: changed=%t record=%+v", changed, expired)
	}
	if string(expired.ServerCapabilities) != "{}" || string(expired.ServerInfo) != "{}" ||
		string(expired.ToolsSnapshot) != "[]" {
		t.Fatalf("expired connection should clear ready metadata: %+v", expired)
	}
	if _, changed, err := store.Execution().MarkMCPConnectionExpired(
		ctx,
		testProjectID,
		launch.Agent.ID,
		conn.ID,
		ready.Generation,
	); err != nil ||
		changed {
		t.Fatalf("stale generation expiration should be no-op, changed=%t err=%v", changed, err)
	}
	reinitializing, changed, err := store.Execution().BeginMCPConnectionInitialization(
		ctx,
		testProjectID,
		launch.Agent.ID,
		conn.ID,
	)
	if err != nil {
		t.Fatalf("begin reinitialization: %v", err)
	}
	if !changed || reinitializing.State != executionstore.MCPConnectionStateInitializing ||
		reinitializing.Generation != expired.Generation {
		t.Fatalf("unexpected reinitializing connection: changed=%t record=%+v", changed, reinitializing)
	}
	if _, err := store.Execution().MarkMCPConnectionReady(ctx, executionstore.MarkMCPConnectionReadyInput{
		ProjectID:          testProjectID,
		AgentID:            launch.Agent.ID,
		ID:                 conn.ID,
		GenerationObserved: ready.Generation,
		MCPSessionID:       "stale-session",
		ProtocolVersion:    "2025-11-25",
		ServerCapabilities: json.RawMessage(`{"tools":{}}`),
		ServerInfo:         json.RawMessage(`{"name":"stale"}`),
		ToolsSnapshot:      json.RawMessage(`[{"name":"stale"}]`),
	}); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("stale generation ready should conflict, got %v", err)
	}
	if _, err := store.Execution().MarkMCPConnectionFailed(
		ctx,
		testProjectID,
		launch.Agent.ID,
		conn.ID,
		ready.Generation,
		"stale failure",
	); !errors.Is(
		err,
		storeerr.ErrStateTransitionConflict,
	) {
		t.Fatalf("stale generation failed should conflict, got %v", err)
	}
	current, found, err := store.Execution().GetMCPConnection(ctx, testProjectID, launch.Agent.ID, "docs")
	if err != nil || !found {
		t.Fatalf("load current mcp connection: found=%t err=%v", found, err)
	}
	if current.State != executionstore.MCPConnectionStateInitializing || current.Generation != expired.Generation ||
		current.MCPSessionID != "" ||
		current.InitializeError != "" {
		t.Fatalf("stale completion should not mutate current generation: %+v", current)
	}
	if _, found, err := store.Execution().GetMCPConnection(ctx, wrongProjectID, launch.Agent.ID, "docs"); err != nil || found {
		t.Fatalf("wrong project lookup should not find connection, found=%t err=%v", found, err)
	}
}

func TestRuntimeMCPServerResolveTool(t *testing.T) {
	t.Parallel()
	disabled := false
	allowPermission := toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow)
	server := agentconfig.RuntimeMCPServer{
		ServerKey:      "docs",
		URL:            "https://example.com/mcp",
		DefaultEnabled: true,
		Permission:     toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk),
		Tools: map[string]agentconfig.RuntimeMCPTool{
			"search": {
				RemoteName: "search",
				Permission: &allowPermission,
			},
			"delete": {RemoteName: "delete", Enabled: &disabled},
		},
	}
	if permission, ok := server.ResolveTool("search"); !ok ||
		permission.Mode != toolpermission.ModeAlwaysAllow {
		t.Fatalf("search resolution = permission=%+v ok=%t", permission, ok)
	}
	if permission, ok := server.ResolveTool("other"); !ok ||
		permission.Mode != toolpermission.ModeAlwaysAsk {
		t.Fatalf("default resolution = permission=%+v ok=%t", permission, ok)
	}
	if _, ok := server.ResolveTool("delete"); ok {
		t.Fatalf("delete should be disabled")
	}
}
