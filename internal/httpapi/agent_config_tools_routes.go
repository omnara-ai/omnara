package httpapi

import (
	"context"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
)

func (s strictOpenAPIServer) ResolveAgentConfigTools(
	ctx context.Context, request openapi.ResolveAgentConfigToolsRequestObject,
) (openapi.ResolveAgentConfigToolsResponseObject, error) {
	if _, err := projectScopeFromContext(ctx); err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	entries, err := agentconfig.ToolsFromSource(
		agentconfig.SourceFormat(request.Body.SourceFormat), []byte(request.Body.Source),
	)
	if err != nil {
		return nil, agentConfigCompileError(err)
	}
	response := openapi.ResolvedAgentConfigTools{Tools: make([]openapi.ResolvedAgentConfigTool, 0, len(entries))}
	for _, entry := range entries {
		permission, err := toolPermissionSelectionResponse(entry.Permission)
		if err != nil {
			return nil, err
		}
		response.Tools = append(response.Tools, openapi.ResolvedAgentConfigTool{
			Name: entry.Name, Enabled: entry.Enabled, Permission: permission,
		})
	}
	return openapi.ResolveAgentConfigTools200JSONResponse(response), nil
}
