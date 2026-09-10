package apimcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	methodListTools = "tools/list"
	methodCallTool  = "tools/call"
)

func grantMiddleware(resolve GrantResolver, operationByTool map[string]string) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			switch method {
			case methodListTools:
				grants, err := resolve(ctx)
				if err != nil {
					return nil, fmt.Errorf("resolve tool grants: %w", err)
				}
				result, err := next(ctx, method, request)
				if err != nil {
					return nil, err
				}
				listed, ok := result.(*mcp.ListToolsResult)
				if !ok {
					return result, nil
				}
				return filterListedTools(listed, grants, operationByTool), nil
			case methodCallTool:
				call, ok := request.(*mcp.CallToolRequest)
				if !ok {
					return next(ctx, method, request)
				}
				grants, err := resolve(ctx)
				if err != nil {
					return nil, fmt.Errorf("resolve tool grants: %w", err)
				}
				operationID, known := operationByTool[call.Params.Name]
				if known && !grants.Allows(operationID) {
					return toolError(fmt.Sprintf("tool %q is not available to this credential", call.Params.Name)), nil
				}
				return next(ctx, method, request)
			default:
				return next(ctx, method, request)
			}
		}
	}
}

func filterListedTools(
	listed *mcp.ListToolsResult,
	grants Grants,
	operationByTool map[string]string,
) *mcp.ListToolsResult {
	kept := make([]*mcp.Tool, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		operationID, known := operationByTool[tool.Name]
		if known && !grants.Allows(operationID) {
			continue
		}
		kept = append(kept, tool)
	}
	filtered := *listed
	filtered.Tools = kept
	return &filtered
}
