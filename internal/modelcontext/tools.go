package modelcontext

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func RuntimeContractToolSpecs(
	ctx context.Context,
	store Store,
	projectID, agentID storage.ID,
	contract agentconfig.RuntimeContract,
	now time.Time,
) ([]ToolSpec, error) {
	out := make([]ToolSpec, 0, len(contract.Tools))
	catalog, err := toolcatalog.Default()
	if err != nil {
		return nil, err
	}
	for _, tool := range contract.Tools {
		if tool.Name == "" {
			return nil, fmt.Errorf(
				"agent config for agent %s/%s has unnamed tool",
				projectID,
				agentID,
			)
		}
		description := tool.Description
		if description == "" && tool.Type == toolcatalog.ToolTypeBuiltIn {
			if entry, ok := catalog.Lookup(tool.Name); ok {
				description = entry.Description
			}
		}
		if tool.Name == toolcatalog.ToolNameWebSearch && !now.IsZero() {
			description = fmt.Sprintf(
				"%s The current year is %d - include it when searching for recent information.",
				description,
				now.UTC().Year(),
			)
		}
		spec := ToolSpec{
			Name:        tool.Name,
			Description: description,
			InputSchema: tool.InputSchema,
			Type:        tool.Type,
			Permission:  tool.Permission,
		}
		out = append(out, spec)
	}
	mcpSpecs, err := runtimeMCPToolSpecs(ctx, store, projectID, agentID, contract)
	if err != nil {
		return nil, err
	}
	out = append(out, mcpSpecs...)
	seen := make(map[string]struct{}, len(out))
	for _, spec := range out {
		if _, duplicate := seen[spec.Name]; duplicate {
			return nil, fmt.Errorf(
				"agent %s/%s exposes duplicate model-facing tool name %q",
				projectID,
				agentID,
				spec.Name,
			)
		}
		seen[spec.Name] = struct{}{}
	}
	return out, nil
}

type mcpToolSnapshot struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

func runtimeMCPToolSpecs(
	ctx context.Context,
	store Store,
	projectID, agentID storage.ID,
	contract agentconfig.RuntimeContract,
) ([]ToolSpec, error) {
	if len(contract.MCPServers) == 0 {
		return nil, nil
	}
	connections, err := store.ListAgentMCPConnections(ctx, projectID, agentID)
	if err != nil {
		return nil, err
	}
	connByServerKey := make(map[string]executionstore.MCPConnectionRecord, len(connections))
	for _, conn := range connections {
		connByServerKey[conn.ServerKey] = conn
	}
	var out []ToolSpec
	for _, server := range contract.MCPServers {
		conn, found := connByServerKey[server.ServerKey]
		if !found || conn.State != executionstore.MCPConnectionStateReady {
			continue
		}
		var tools []mcpToolSnapshot
		if err := json.Unmarshal(conn.ToolsSnapshot, &tools); err != nil {
			return nil, fmt.Errorf(
				"decode mcp tools snapshot for server %q: %w",
				server.ServerKey,
				err,
			)
		}
		for _, tool := range tools {
			permission, ok := server.ResolveTool(tool.Name)
			if !ok {
				continue
			}
			schema := tool.InputSchema
			if len(schema) == 0 || string(schema) == "null" {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			name := toolcatalog.MCPRuntimeToolName(server.ServerKey, tool.Name)
			if !toolcatalog.IsMCPRuntimeToolName(name) {
				continue
			}
			description := fmt.Sprintf("MCP tool %q from server %q.", tool.Name, server.ServerKey)
			if tool.Description != "" {
				description = fmt.Sprintf("%s %s", description, tool.Description)
			}
			out = append(
				out,
				ToolSpec{
					Name:        name,
					Description: description,
					InputSchema: schema,
					Type:        toolcatalog.ToolTypeMCP,
					Permission:  permission,
				},
			)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
