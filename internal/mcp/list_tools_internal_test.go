package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type pagedToolsClient struct {
	Client
	pages      []ToolsPage
	requestIDs []int64
}

func (c *pagedToolsClient) ListTools(_ context.Context, _ Conn, requestID int64, cursor string) (ToolsPage, error) {
	c.requestIDs = append(c.requestIDs, requestID)
	index := 0
	if cursor != "" {
		if _, err := fmt.Sscanf(cursor, "%d", &index); err != nil {
			return ToolsPage{}, err
		}
	}
	if index >= len(c.pages) {
		return ToolsPage{}, fmt.Errorf("unexpected cursor %q", cursor)
	}
	return c.pages[index], nil
}

func (c *pagedToolsClient) Call(context.Context, Conn, string, json.RawMessage, int64) (json.RawMessage, error) {
	return nil, errors.New("unexpected call")
}

func sequentialRequestIDs() func(context.Context) (int64, error) {
	var next int64
	return func(context.Context) (int64, error) {
		next++
		return next, nil
	}
}

func TestListAllToolsFollowsCursors(t *testing.T) {
	client := &pagedToolsClient{pages: []ToolsPage{
		{Tools: []*sdkmcp.Tool{{Name: "a"}, {Name: "b"}}, NextCursor: "1"},
		{Tools: []*sdkmcp.Tool{{Name: "c"}}, NextCursor: "2"},
		{Tools: []*sdkmcp.Tool{{Name: "d"}}},
	}}
	tools, err := listAllTools(context.Background(), client, Conn{}, sequentialRequestIDs())
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if got := listedToolNames(tools); fmt.Sprint(got) != "[a b c d]" {
		t.Fatalf("tools = %v", got)
	}
	if fmt.Sprint(client.requestIDs) != "[1 2 3]" {
		t.Fatalf("request ids = %v", client.requestIDs)
	}
}

func TestListAllToolsStopsOnEmptyPageWithCursor(t *testing.T) {
	client := &pagedToolsClient{pages: []ToolsPage{
		{Tools: []*sdkmcp.Tool{{Name: "a"}}, NextCursor: "1"},
		{NextCursor: "1"},
	}}
	tools, err := listAllTools(context.Background(), client, Conn{}, sequentialRequestIDs())
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) != 1 || len(client.requestIDs) != 2 {
		t.Fatalf("tools = %v after %d requests", listedToolNames(tools), len(client.requestIDs))
	}
}

func TestListAllToolsRejectsOversizedCatalog(t *testing.T) {
	page := ToolsPage{NextCursor: "0"}
	for i := 0; i <= MaxListedTools/2; i++ {
		page.Tools = append(page.Tools, &sdkmcp.Tool{Name: fmt.Sprintf("tool_%d", i)})
	}
	client := &pagedToolsClient{pages: []ToolsPage{page}}
	_, err := listAllTools(context.Background(), client, Conn{}, sequentialRequestIDs())
	if err == nil || err.Error() != fmt.Sprintf("mcp: server exposes more than %d tools", MaxListedTools) {
		t.Fatalf("err = %v", err)
	}
}

func TestListAllToolsPropagatesRequestIDErrors(t *testing.T) {
	want := errors.New("no sequence")
	_, err := listAllTools(context.Background(), &pagedToolsClient{}, Conn{}, func(context.Context) (int64, error) {
		return 0, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
}

func listedToolNames(tools []*sdkmcp.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}
