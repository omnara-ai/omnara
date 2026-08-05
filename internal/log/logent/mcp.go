package logent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func MCPConnections(ctx context.Context, connections []executionstore.MCPConnectionRecord) {
	fields := log.Fields{"mcp.count": len(connections)}
	for i, conn := range connections {
		addMCPConnectionFields(fields, i, conn)
	}
	log.Attach(ctx, fields)
}

func MCPInitialization(
	ctx context.Context,
	index int,
	conn executionstore.MCPConnectionRecord,
	result string,
	err error,
) {
	var tools []json.RawMessage
	toolsCount := 0
	if len(conn.ToolsSnapshot) > 0 && json.Unmarshal(conn.ToolsSnapshot, &tools) == nil {
		toolsCount = len(tools)
	}
	fields := log.Fields{
		fmt.Sprintf("mcp.%d.initialization.result", index):      result,
		fmt.Sprintf("mcp.%d.initialization.tools_count", index): toolsCount,
	}
	addMCPConnectionFields(fields, index, conn)
	if err != nil {
		fields[fmt.Sprintf("mcp.%d.initialization.error", index)] = err.Error()
		log.Level(ctx, log.WarnLevel)
	}
	log.Attach(ctx, fields)
}

func addMCPConnectionFields(fields log.Fields, index int, conn executionstore.MCPConnectionRecord) {
	prefix := fmt.Sprintf("mcp.%d.", index)
	fields[prefix+"id"] = conn.ID
	fields[prefix+"server_key"] = conn.ServerKey
	fields[prefix+"endpoint_url"] = conn.EndpointURL
	fields[prefix+"state"] = string(conn.State)
	fields[prefix+"generation"] = conn.Generation
	fields[prefix+"protocol_version"] = conn.ProtocolVersion
}
