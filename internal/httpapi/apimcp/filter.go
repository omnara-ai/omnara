package apimcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	logpkg "github.com/omnara-ai/omnara/internal/log"
)

const (
	methodListTools   = "tools/list"
	methodCallTool    = "tools/call"
	cacheScopePrivate = "private"
)

func grantMiddleware(resolve GrantResolver, operationByTool map[string]string) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			logpkg.Attach(ctx, logpkg.Fields{"mcp.method": method})
			switch method {
			case methodListTools:
				result, err := next(ctx, method, request)
				if err != nil {
					return nil, err
				}
				listed, ok := result.(*mcp.ListToolsResult)
				if !ok {
					return result, nil
				}
				return filterListedTools(ctx, listed, resolve(ctx), operationByTool)
			case methodCallTool:
				call, ok := request.(*mcp.CallToolRequest)
				if !ok {
					return next(ctx, method, request)
				}
				logpkg.Attach(ctx, logpkg.Fields{"mcp.tool": call.Params.Name})
				operationID, known := operationByTool[call.Params.Name]
				if !known {
					return next(ctx, method, request)
				}
				allowed, err := resolve(ctx).Allows(ctx, operationID)
				if err != nil {
					logpkg.Error(ctx, fmt.Errorf("resolve tool grants: %w", err))
					return nil, internalError()
				}
				if !allowed {
					return nil, unknownToolError(call.Params.Name)
				}
				return next(ctx, method, request)
			default:
				return next(ctx, method, request)
			}
		}
	}
}

func filterListedTools(
	ctx context.Context,
	listed *mcp.ListToolsResult,
	grants Grants,
	operationByTool map[string]string,
) (*mcp.ListToolsResult, error) {
	kept := make([]*mcp.Tool, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		operationID, known := operationByTool[tool.Name]
		if known {
			allowed, err := grants.Allows(ctx, operationID)
			if err != nil {
				logpkg.Error(ctx, fmt.Errorf("resolve tool grants: %w", err))
				return nil, internalError()
			}
			if !allowed {
				continue
			}
		}
		kept = append(kept, tool)
	}
	filtered := *listed
	filtered.Tools = kept
	filtered.CacheScope = cacheScopePrivate
	return &filtered, nil
}
