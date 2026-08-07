package kernel

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/mcp"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	mcpInitResultFailed    = "failed"
	mcpInitResultSucceeded = "succeeded"
)

type mcpInitializationMode uint8

const (
	mcpInitializationNone mcpInitializationMode = iota
	mcpInitializationOpening
	mcpInitializationResume
)

func shouldInitializeMCPConnection(mode mcpInitializationMode, state executionstore.MCPConnectionState) bool {
	switch mode {
	case mcpInitializationOpening:
		return mcp.ShouldInitialize(state)
	case mcpInitializationResume:
		return state == executionstore.MCPConnectionStateInitializing
	default:
		return false
	}
}

func (e AgentExecutor) ensureMCPConnections(
	ctx context.Context,
	orgID storage.ID,
	input ModelWorkExecution,
	contract agentconfig.RuntimeContract,
	mode mcpInitializationMode,
) error {
	if len(contract.MCPServers) == 0 {
		connections, err := e.Store.Execution().ListAgentMCPConnections(ctx, input.ProjectID, input.AgentID)
		if err != nil {
			return err
		}
		if len(connections) == 0 {
			return nil
		}
	}
	connections, err := e.Store.Execution().ReconcileAgentMCPConnections(
		ctx,
		input.ProjectID,
		input.AgentID,
		contract.MCPServers,
	)
	if err != nil {
		return err
	}
	pending := make([]executionstore.MCPConnectionRecord, 0, len(connections))
	for _, conn := range connections {
		if shouldInitializeMCPConnection(mode, conn.State) {
			pending = append(pending, conn)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	if e.MCP == nil {
		return errors.New("kernel mcp client is required to initialize mcp connections")
	}
	servers := make(map[string]agentconfig.RuntimeMCPServer, len(contract.MCPServers))
	for _, server := range contract.MCPServers {
		servers[server.ServerKey] = server
	}
	manager := mcp.Manager{
		Execution:       e.Store.Execution(),
		Secrets:         e.Store.Secrets(),
		Client:          e.MCP,
		Backoff:         e.MCPInitializationBackoff,
		OAuthHTTPClient: e.MCPAuthHTTPClient,
	}
	errs := make(chan error, len(pending))
	var wg sync.WaitGroup
	for index, conn := range pending {
		wg.Add(1)
		go func() {
			var resultErr error
			defer wg.Done()
			defer func() {
				if recovered := recover(); recovered != nil {
					resultErr = errors.Join(
						resultErr,
						fmt.Errorf(
							"initialize MCP connection %q panicked: %v",
							conn.ServerKey,
							recovered,
						),
					)
				}
				if resultErr != nil {
					errs <- resultErr
				}
			}()
			server, ok := servers[conn.ServerKey]
			if !ok {
				resultErr = errors.New("mcp connection has no matching runtime server config")
				return
			}
			result, err := manager.InitializePending(
				ctx,
				orgID,
				input.ProjectID,
				input.AgentID,
				conn,
				server,
			)
			if !result.Changed {
				return
			}
			cause := mcp.InitializationCause(err)
			if err != nil {
				if !mcp.InitializationRecorded(err) {
					resultErr = err
				}
				logConn := result.Conn
				if logConn.ID == storage.NilID {
					logConn = conn
				}
				logent.MCPInitialization(ctx, index, logConn, mcpInitResultFailed, cause)
				return
			}
			logent.MCPInitialization(ctx, index, result.Conn, mcpInitResultSucceeded, nil)
		}()
	}
	wg.Wait()
	close(errs)
	var joined error
	for err := range errs {
		joined = errors.Join(joined, err)
	}
	return joined
}
