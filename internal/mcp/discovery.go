package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage"
)

type DiscoveredServer struct {
	ProtocolVersion string
	ServerInfo      sdkmcp.Implementation
	Tools           []*sdkmcp.Tool
}

func (m Manager) DiscoverTools(
	ctx context.Context,
	orgID, projectID storage.ID,
	endpointURL string,
	auth *agentconfig.RuntimeMCPAuth,
) (DiscoveredServer, error) {
	wireConn, err := m.connection(ctx, orgID, projectID, endpointURL, endpointURL, auth, "", "")
	if err != nil {
		return DiscoveredServer{}, err
	}
	mcpSessionID, result, err := m.Client.Initialize(ctx, wireConn, ProtocolVersion)
	if err != nil {
		return DiscoveredServer{}, fmt.Errorf("initialize mcp server: %w", ClarifyTransportError(err, endpointURL))
	}
	negotiatedProtocol := result.ProtocolVersion
	if negotiatedProtocol == "" {
		negotiatedProtocol = ProtocolVersion
	}
	wireConn.MCPSessionID = mcpSessionID
	wireConn.ProtocolVersion = negotiatedProtocol
	if err := m.Client.Notify(ctx, wireConn, "notifications/initialized", json.RawMessage(`{}`)); err != nil {
		return DiscoveredServer{}, fmt.Errorf(
			"send mcp initialized notification: %w",
			ClarifyTransportError(err, endpointURL),
		)
	}
	var requestID int64
	tools, err := listAllTools(ctx, m.Client, wireConn, func(context.Context) (int64, error) {
		requestID++
		return requestID, nil
	})
	if err != nil {
		return DiscoveredServer{}, fmt.Errorf("list mcp tools: %w", ClarifyTransportError(err, endpointURL))
	}
	var serverInfo sdkmcp.Implementation
	if err := json.Unmarshal(result.ServerInfo, &serverInfo); err != nil {
		return DiscoveredServer{}, fmt.Errorf("decode mcp server info: %w", err)
	}
	return DiscoveredServer{
		ProtocolVersion: negotiatedProtocol,
		ServerInfo:      serverInfo,
		Tools:           tools,
	}, nil
}
