package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
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
	if len(parts) != 3 {
		t.Fatalf("normalized MCP parts = %s, want three ordered parts without structuredContent", raw)
	}
	if parts[0]["type"] != "text" || parts[0]["text"] != "first" ||
		parts[1]["type"] != "structured_data" ||
		parts[2]["type"] != "text" || parts[2]["text"] != "last" {
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

func TestMCPToolResultContentFallsBackToStructuredContentWithoutText(t *testing.T) {
	for name, content := range map[string][]sdkmcp.Content{
		"no content": nil,
		"blank text": {&sdkmcp.TextContent{Text: "  "}},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := (Executor{}).mcpToolResultContent(
				context.Background(),
				Turn{},
				uuid.New(),
				"docs",
				"search",
				&sdkmcp.CallToolResult{
					Content:           content,
					StructuredContent: map[string]any{"count": 2},
				},
			)
			if err != nil {
				t.Fatalf("normalize MCP result: %v", err)
			}
			raw, err := result.contentParts()
			if err != nil {
				t.Fatalf("marshal MCP result: %v", err)
			}
			if !strings.Contains(string(raw), `{"type":"structured_data","value":{"count":2}}`) {
				t.Fatalf("normalized MCP content = %s, want structuredContent fallback", raw)
			}
		})
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
