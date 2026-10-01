package httpapi

import (
	"context"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/agentconfigcompile"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
)

func (s strictOpenAPIServer) ResolveAgentConfigTools(
	ctx context.Context, request openapi.ResolveAgentConfigToolsRequestObject,
) (openapi.ResolveAgentConfigToolsResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	entries, err := agentconfigcompile.ToolsFromSource(
		ctx, s.server.store, scope.project.OrgID, scope.project.ID, s.server.agentConfigOptions,
		agentconfig.SourceFormat(request.Body.SourceFormat), request.Body.Source,
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
