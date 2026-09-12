package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
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
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	wireConn, identity, err := m.connection(ctx, orgID, projectID, endpointURL, endpointURL, auth, "", "")
	if err != nil {
		return DiscoveredServer{}, err
	}
	fetch := m.negotiatedCatalogFetch(wireConn, m.ephemeralLegacyCatalogFetch(wireConn, endpointURL))
	if m.Execution == nil {
		contents, err := fetch(ctx, executionstore.MCPServerCatalogRecord{})
		if err != nil {
			return DiscoveredServer{}, ClarifyTransportError(err, endpointURL)
		}
		return newDiscoveredServer(
			contents.ProtocolVersion,
			contents.ServerInfo,
			dropToolsWithInvalidHeaders(ctx, contents.Listing.Tools),
		)
	}
	catalog, err := m.refreshCatalogUntil(ctx, identity, false, alwaysFetchCatalog, fetch)
	if err != nil {
		return DiscoveredServer{}, ClarifyTransportError(err, endpointURL)
	}
	tools, err := decodeToolsSnapshot(catalog.ToolsSnapshot)
	if err != nil {
		return DiscoveredServer{}, fmt.Errorf("decode cached mcp tools: %w", err)
	}
	return newDiscoveredServer(catalog.ProtocolVersion, catalog.ServerInfo, tools)
}

func (m Manager) ephemeralLegacyCatalogFetch(wireConn Conn, label string) catalogFetch {
	return func(ctx context.Context, current executionstore.MCPServerCatalogRecord) (catalogContents, error) {
		session, result, err := m.openLegacySession(ctx, wireConn)
		if err != nil {
			return catalogContents{}, err
		}
		var requestID int64
		return m.legacyCatalogFetch(legacySession{
			Conn:  session,
			Label: label,
			NextRequestID: func(context.Context) (int64, error) {
				requestID++
				return requestID, nil
			},
			ServerCapabilities: result.ServerCapabilities,
			ServerInfo:         result.ServerInfo,
		})(ctx, current)
	}
}

func newDiscoveredServer(
	protocolVersion string,
	serverInfoJSON json.RawMessage,
	tools []*sdkmcp.Tool,
) (DiscoveredServer, error) {
	var serverInfo sdkmcp.Implementation
	if err := json.Unmarshal(serverInfoJSON, &serverInfo); err != nil {
		return DiscoveredServer{}, fmt.Errorf("decode mcp server info: %w", err)
	}
	return DiscoveredServer{ProtocolVersion: protocolVersion, ServerInfo: serverInfo, Tools: tools}, nil
}
