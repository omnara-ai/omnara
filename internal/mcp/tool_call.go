package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

type ToolCallInput struct {
	OrgID      storage.ID
	ProjectID  storage.ID
	AgentID    storage.ID
	Conn       executionstore.MCPConnectionRecord
	Server     agentconfig.RuntimeMCPServer
	Name       string
	Arguments  json.RawMessage
	BeforeSend func(context.Context) error
}

func (m Manager) CallTool(ctx context.Context, input ToolCallInput) (*sdkmcp.CallToolResult, error) {
	if input.Conn.State != executionstore.MCPConnectionStateReady {
		return nil, fmt.Errorf("mcp server %q is not ready", input.Conn.ServerKey)
	}
	result, err := m.callTool(ctx, input, input.Conn)
	var rpcErr *RPCError
	switch {
	case errors.Is(err, ErrSessionExpired):
		if err := m.beforeSend(ctx, input); err != nil {
			return nil, err
		}
		refresh, refreshErr := m.refreshExpired(ctx, input.OrgID, input.ProjectID, input.AgentID, input.Conn, input.Server)
		if refreshErr != nil {
			return nil, refreshErr
		}
		result, err = m.callTool(ctx, input, refresh.Conn)
		if !errors.Is(err, ErrSessionExpired) {
			return result, err
		}
		if _, _, expireErr := m.Execution.MarkMCPConnectionExpired(
			ctx,
			input.ProjectID,
			input.AgentID,
			refresh.Conn.ID,
			refresh.Conn.Generation,
		); expireErr != nil {
			return nil, expireErr
		}
		return nil, &InitializationError{Cause: err, Recorded: true, Err: err}
	case errors.As(err, &rpcErr) && rpcErr.Code == CodeHeaderMismatch:
		refreshed, refreshErr := m.refreshCatalogNow(
			ctx, input.OrgID, input.ProjectID, input.AgentID, input.Conn, input.Server,
		)
		if refreshErr != nil || !refreshed.Ready {
			return nil, err
		}
		return m.callTool(ctx, input, refreshed.Conn)
	default:
		return result, err
	}
}

func (m Manager) callTool(
	ctx context.Context,
	input ToolCallInput,
	conn executionstore.MCPConnectionRecord,
) (*sdkmcp.CallToolResult, error) {
	call := ToolCall{Name: input.Name, Arguments: input.Arguments}
	requestID := NextStatelessRequestID()
	sessionID := ""
	if IsStatelessProtocolVersion(conn.ProtocolVersion) {
		headers, err := catalogToolHeaders(conn, input.Name)
		if err != nil {
			return nil, err
		}
		call.Headers = headers
	} else {
		seq, err := m.Execution.NextMCPRequestSequence(ctx, input.ProjectID, input.AgentID, conn.ID)
		if err != nil {
			return nil, err
		}
		requestID = seq
		sessionID = conn.MCPSessionID
	}
	wireConn, _, err := m.Connection(
		ctx, input.OrgID, input.ProjectID, conn, input.Server, sessionID, conn.ProtocolVersion,
	)
	if err != nil {
		return nil, err
	}
	if err := m.beforeSend(ctx, input); err != nil {
		return nil, err
	}
	return m.Client.CallTool(ctx, wireConn, requestID, call)
}

func (m Manager) beforeSend(ctx context.Context, input ToolCallInput) error {
	if input.BeforeSend == nil {
		return nil
	}
	return input.BeforeSend(ctx)
}

func catalogToolHeaders(conn executionstore.MCPConnectionRecord, remoteName string) ([]ToolHeader, error) {
	tools, err := decodeToolsSnapshot(conn.ToolsSnapshot)
	if err != nil {
		return nil, fmt.Errorf("decode mcp tools snapshot for %q: %w", conn.ServerKey, err)
	}
	for _, tool := range tools {
		if tool != nil && tool.Name == remoteName {
			return ToolHeaders(tool)
		}
	}
	return nil, nil
}
