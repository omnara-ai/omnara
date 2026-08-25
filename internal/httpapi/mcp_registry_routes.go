package httpapi

import (
	"context"
	"errors"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/mcpregistry"
)

func (s strictOpenAPIServer) ListMCPServers(
	_ context.Context,
	request openapi.ListMCPServersRequestObject,
) (openapi.ListMCPServersResponseObject, error) {
	if s.server.mcpRegistry == nil {
		s.server.log.Error("mcp registry search requested without a loaded snapshot")
		return nil, apierror.FromCode(openapi.ErrorCodeInternalError, "mcp registry snapshot is not loaded")
	}
	limit, err := parseOpenAPIPageLimit(request.Params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	params := mcpregistry.SearchParams{Limit: limit}
	if request.Params.Q != nil {
		params.Query = *request.Params.Q
	}
	if request.Params.RemoteUrl != nil {
		params.RemoteURL = *request.Params.RemoteUrl
	}
	if request.Params.Cursor != nil {
		params.Cursor = *request.Params.Cursor
	}
	page, err := s.server.mcpRegistry.Search(params)
	if errors.Is(err, mcpregistry.ErrInvalidCursor) {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	if err != nil {
		s.server.log.Error("mcp registry search failed", "error", err)
		return nil, apierror.FromCode(openapi.ErrorCodeInternalError, "mcp registry search failed")
	}
	data := make([]openapi.MCPRegistryServer, 0, len(page.Servers))
	for _, server := range page.Servers {
		data = append(data, mcpRegistryServerResponse(server))
	}
	return openapi.ListMCPServers200JSONResponse(openapi.ListMCPServersResponse{
		Data:       data,
		NextCursor: nullableFromPtr(page.NextCursor),
	}), nil
}

func mcpRegistryServerResponse(server mcpregistry.Server) openapi.MCPRegistryServer {
	remotes := make([]openapi.MCPRegistryRemote, 0, len(server.Remotes))
	for _, remote := range server.Remotes {
		response := openapi.MCPRegistryRemote{Type: remote.Type, Url: remote.URL}
		if len(remote.Headers) > 0 {
			headers := make([]openapi.MCPRegistryHeader, 0, len(remote.Headers))
			for _, header := range remote.Headers {
				headers = append(headers, openapi.MCPRegistryHeader{
					Name:        header.Name,
					Description: optionalNonEmpty(header.Description),
					IsRequired:  header.IsRequired,
					IsSecret:    header.IsSecret,
				})
			}
			response.Headers = &headers
		}
		remotes = append(remotes, response)
	}
	icons := make([]openapi.MCPRegistryIcon, 0, len(server.Icons))
	for _, icon := range server.Icons {
		icons = append(icons, openapi.MCPRegistryIcon{
			Src:      icon.Src,
			MimeType: optionalNonEmpty(icon.MimeType),
			Sizes:    optionalNonEmptySlice(icon.Sizes),
			Theme:    optionalNonEmpty(icon.Theme),
		})
	}
	return openapi.MCPRegistryServer{
		Name:        server.Name,
		Title:       optionalNonEmpty(server.Title),
		Description: server.Description,
		Version:     server.Version,
		WebsiteUrl:  optionalNonEmpty(server.WebsiteURL),
		Status:      server.Status,
		UpdatedAt:   server.UpdatedAt,
		Remotes:     remotes,
		Icons:       icons,
	}
}

func optionalNonEmptySlice(values []string) *[]string {
	if len(values) == 0 {
		return nil
	}
	return &values
}

func optionalNonEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
