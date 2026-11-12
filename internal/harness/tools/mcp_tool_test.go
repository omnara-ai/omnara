package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestMCPToolResultContentPreservesOrderWithoutProtocolEnvelope(t *testing.T) {
	size := int64(42)
	result := &sdkmcp.CallToolResult{
		Meta: sdkmcp.Meta{"internal": "not model content"},
		Content: []sdkmcp.Content{
			&sdkmcp.TextContent{
				Text: "first",
				Meta: sdkmcp.Meta{"internal": "not model content"},
			},
			&sdkmcp.ResourceLink{
				URI:      "file:///report.json",
				Name:     "report",
				MIMEType: "application/json",
				Size:     &size,
				Meta:     sdkmcp.Meta{"internal": "not model content"},
			},
			&sdkmcp.TextContent{Text: "last"},
		},
		StructuredContent: map[string]any{"count": 2, "valid": true},
	}

	content, err := (Executor{}).mcpToolResultContent(
		context.Background(),
		Turn{},
		uuid.New(),
		"docs",
		"search",
		result,
	)
	if err != nil {
		t.Fatalf("normalize MCP result: %v", err)
	}
	raw, err := content.contentParts()
	if err != nil {
		t.Fatalf("marshal MCP result: %v", err)
	}
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err != nil {
		t.Fatalf("decode normalized MCP result: %v", err)
	}
	if len(parts) != 4 {
		t.Fatalf("normalized MCP parts = %s, want four ordered parts", raw)
	}
	if parts[0]["type"] != "text" || parts[0]["text"] != "first" ||
		parts[1]["type"] != "structured_data" ||
		parts[2]["type"] != "text" || parts[2]["text"] != "last" ||
		parts[3]["type"] != "structured_data" {
		t.Fatalf("normalized MCP part order = %s", raw)
	}
	link, ok := parts[1]["value"].(map[string]any)
	if !ok || link["type"] != "mcp_resource_link" ||
		link["uri"] != "file:///report.json" ||
		link["name"] != "report" ||
		link["mime_type"] != "application/json" ||
		link["size"] != float64(size) {
		t.Fatalf("normalized MCP resource link = %+v", parts[1])
	}
	structured, ok := parts[3]["value"].(map[string]any)
	if !ok || structured["count"] != float64(2) || structured["valid"] != true {
		t.Fatalf("normalized MCP structured content = %+v", parts[3])
	}
	for _, forbidden := range []string{"_meta", "structuredContent", "isError", `"content"`} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("normalized MCP content leaked protocol field %q: %s", forbidden, raw)
		}
	}
}

func TestMCPToolResultContentReturnsFailureWithoutDiscardingContent(t *testing.T) {
	content, err := (Executor{}).mcpToolResultContent(
		context.Background(),
		Turn{},
		uuid.New(),
		"docs",
		"search",
		&sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: "the query was rejected"},
			},
			IsError: true,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "isError=true") {
		t.Fatalf("MCP error result error = %v, want isError failure", err)
	}
	raw, marshalErr := content.contentParts()
	if marshalErr != nil {
		t.Fatalf("marshal MCP error result: %v", marshalErr)
	}
	if string(raw) != `[{"type":"text","text":"the query was rejected"}]` {
		t.Fatalf("MCP error result content = %s", raw)
	}
}

func TestMCPToolResultContentReturnsEmptyFailureWithoutInventingContent(t *testing.T) {
	content, err := (Executor{}).mcpToolResultContent(
		context.Background(),
		Turn{},
		uuid.New(),
		"docs",
		"search",
		&sdkmcp.CallToolResult{IsError: true},
	)
	if err == nil || !strings.Contains(err.Error(), "isError=true") {
		t.Fatalf("MCP error result error = %v, want isError failure", err)
	}
	raw, marshalErr := content.contentParts()
	if marshalErr != nil {
		t.Fatalf("marshal MCP error result: %v", marshalErr)
	}
	if string(raw) != `[]` {
		t.Fatalf("MCP error result content = %s, want no invented content", raw)
	}
}

func TestMCPToolResultContentPreservesEmptyText(t *testing.T) {
	content, err := (Executor{}).mcpToolResultContent(
		context.Background(),
		Turn{},
		uuid.New(),
		"docs",
		"search",
		&sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: ""}},
		},
	)
	if err != nil {
		t.Fatalf("normalize empty MCP text: %v", err)
	}
	raw, err := content.contentParts()
	if err != nil {
		t.Fatalf("marshal empty MCP text: %v", err)
	}
	if string(raw) != `[{"type":"text","text":""}]` {
		t.Fatalf("normalized MCP content = %s", raw)
	}
}

func TestMCPToolResultContentDropsOnlyJSONEqualTextCopy(t *testing.T) {
	result, err := (Executor{}).mcpToolResultContent(
		context.Background(),
		Turn{},
		uuid.New(),
		"docs",
		"search",
		&sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: "{\n  \"valid\": true,\n  \"count\": 2.0\n}"},
				&sdkmcp.TextContent{Text: "Found 2 results."},
				&sdkmcp.TextContent{Text: `{"count":3,"valid":true}`},
				&sdkmcp.EmbeddedResource{Resource: &sdkmcp.ResourceContents{
					URI:  "file:///copy.json",
					Text: `{"count":2,"valid":true}`,
				}},
			},
			StructuredContent: map[string]any{"count": 2, "valid": true},
		},
	)
	if err != nil {
		t.Fatalf("normalize MCP result: %v", err)
	}
	raw, err := result.contentParts()
	if err != nil {
		t.Fatalf("marshal MCP result: %v", err)
	}
	want := `[{"type":"text","text":"Found 2 results."},` +
		`{"type":"text","text":"{\"count\":3,\"valid\":true}"},` +
		`{"type":"text","text":"{\"count\":2,\"valid\":true}"},` +
		`{"type":"structured_data","value":{"count":2,"valid":true}}]`
	if string(raw) != want {
		t.Fatalf("normalized MCP content = %s, want %s", raw, want)
	}
}

func TestMCPToolResultContentRejectsNonObjectStructuredContent(t *testing.T) {
	_, err := (Executor{}).mcpToolResultContent(
		context.Background(),
		Turn{},
		uuid.New(),
		"docs",
		"search",
		&sdkmcp.CallToolResult{StructuredContent: []string{"invalid"}},
	)
	if err == nil || !strings.Contains(err.Error(), "must be a JSON object") {
		t.Fatalf("non-object MCP structured content error = %v", err)
	}
}

func TestMCPToolResultContentPutsStructuredContentFirstForProgramCalls(t *testing.T) {
	result := &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{
			&sdkmcp.ResourceLink{URI: "file:///report.json", Name: "report"},
			&sdkmcp.TextContent{Text: "summary"},
		},
		StructuredContent: map[string]any{"count": 2},
	}
	content, err := (Executor{}).mcpToolResultContent(context.Background(),
		Turn{authority: programClaimAuthority{}}, uuid.New(), "docs", "search", result)
	if err != nil {
		t.Fatalf("normalize MCP result: %v", err)
	}
	raw, err := content.contentParts()
	if err != nil {
		t.Fatalf("marshal MCP result: %v", err)
	}
	var parts []struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		t.Fatalf("decode normalized MCP result: %v", err)
	}
	if len(parts) != 3 || parts[0].Type != "structured_data" || string(parts[0].Value) != `{"count":2}` ||
		parts[1].Type != "structured_data" || parts[2].Type != "text" {
		t.Fatalf("program MCP parts = %s, want structuredContent first, then content in order", raw)
	}
}

func TestProgramMCPCallsBindConnectionToCurrentServerConfig(t *testing.T) {
	server := func(url string) agentconfig.RuntimeMCPServer {
		return agentconfig.RuntimeMCPServer{ServerKey: "docs", URL: url,
			Auth: &agentconfig.RuntimeMCPAuth{Type: "bearer", SecretID: uuid.New()}}
	}
	historical, current := server("https://a.example/mcp"), server("https://b.example/mcp")
	connectionFor := func(server agentconfig.RuntimeMCPServer) executionstore.MCPConnectionRecord {
		hash, err := executionstore.MCPServerConfigHash(server)
		if err != nil {
			t.Fatalf("hash server config: %v", err)
		}
		return executionstore.MCPConnectionRecord{ServerKey: server.ServerKey, EndpointURL: server.URL, ConfigHash: hash}
	}
	turn := Turn{contract: &agentconfig.RuntimeContract{MCPServers: []agentconfig.RuntimeMCPServer{current}}}
	resolved, err := (Executor{}).runtimeMCPServerForTool(context.Background(), turn, "docs")
	if err != nil {
		t.Fatalf("resolve server: %v", err)
	}
	if resolved.URL != current.URL || resolved.Auth.SecretID != current.Auth.SecretID {
		t.Fatalf("resolved server = %+v, want the current config's", resolved)
	}
	if err := mcpConnectionServes(turn, connectionFor(current), resolved); err != nil {
		t.Fatalf("matching connection rejected: %v", err)
	}
	if err := mcpConnectionServes(turn, connectionFor(historical), resolved); err == nil {
		t.Fatal("current credentials paired with a connection initialized for another config")
	}
	if err := mcpConnectionServes(turn, connectionFor(current), historical); err == nil {
		t.Fatal("historical credentials paired with the current connection")
	}
}
