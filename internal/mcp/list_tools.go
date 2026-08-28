package mcp

import (
	"context"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const MaxListedTools = 1000

func listAllTools(
	ctx context.Context,
	client Client,
	conn Conn,
	nextRequestID func(context.Context) (int64, error),
) ([]*sdkmcp.Tool, error) {
	var tools []*sdkmcp.Tool
	cursor := ""
	for {
		requestID, err := nextRequestID(ctx)
		if err != nil {
			return nil, err
		}
		page, err := client.ListTools(ctx, conn, requestID, cursor)
		if err != nil {
			return nil, err
		}
		tools = append(tools, page.Tools...)
		if len(tools) > MaxListedTools {
			return nil, fmt.Errorf("mcp: server exposes more than %d tools", MaxListedTools)
		}
		if page.NextCursor == "" || len(page.Tools) == 0 {
			return tools, nil
		}
		cursor = page.NextCursor
	}
}
