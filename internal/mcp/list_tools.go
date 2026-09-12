package mcp

import (
	"context"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const MaxListedTools = 1000

type ToolsListing struct {
	Tools []*sdkmcp.Tool
	Cache CacheHint
}

func listAllTools(
	ctx context.Context,
	client Client,
	conn Conn,
	nextRequestID func(context.Context) (int64, error),
) (ToolsListing, error) {
	var listing ToolsListing
	cursor := ""
	first := true
	for {
		requestID, err := nextRequestID(ctx)
		if err != nil {
			return ToolsListing{}, err
		}
		page, err := client.ListTools(ctx, conn, requestID, cursor)
		if err != nil {
			return ToolsListing{}, err
		}
		listing.Tools = append(listing.Tools, page.Tools...)
		pageTTL := max(page.TTLMs, 0)
		if first {
			listing.Cache = CacheHint{TTLMs: pageTTL, CacheScope: page.CacheScope}
		} else {
			listing.Cache.TTLMs = min(listing.Cache.TTLMs, pageTTL)
			if page.CacheScope == "private" {
				listing.Cache.CacheScope = page.CacheScope
			}
		}
		first = false
		if len(listing.Tools) > MaxListedTools {
			return ToolsListing{}, fmt.Errorf("mcp: server exposes more than %d tools", MaxListedTools)
		}
		if page.NextCursor == "" || len(page.Tools) == 0 {
			return listing, nil
		}
		cursor = page.NextCursor
	}
}
