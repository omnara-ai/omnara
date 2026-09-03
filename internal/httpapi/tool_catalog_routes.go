package httpapi

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func (s strictOpenAPIServer) GetToolCatalog(
	_ context.Context,
	_ openapi.GetToolCatalogRequestObject,
) (openapi.GetToolCatalogResponseObject, error) {
	catalog, err := toolcatalog.Default()
	if err != nil {
		return nil, err
	}
	tools := make([]openapi.ToolCatalogEntry, 0, len(catalog.Entries()))
	for _, entry := range catalog.Entries() {
		defaultPermission, err := toolPermissionSelectionResponse(entry.DefaultPermission)
		if err != nil {
			return nil, err
		}
		modes, err := toolPermissionModeResponses(entry.PermissionModes)
		if err != nil {
			return nil, err
		}
		configurable := !toolcatalog.IsBindingManagedTool(entry.Name)
		tools = append(tools, openapi.ToolCatalogEntry{
			Name:              entry.Name,
			Description:       entry.Description,
			Configurable:      &configurable,
			DefaultPermission: defaultPermission,
			PermissionModes:   modes,
		})
	}
	customPermissions, err := toolPermissionProfileResponse(
		toolcatalog.DefaultCustomToolPermission(),
		toolcatalog.CustomToolPermissionModes(),
	)
	if err != nil {
		return nil, err
	}
	mcpPermissions, err := toolPermissionProfileResponse(
		toolcatalog.DefaultMCPToolPermission(),
		toolcatalog.MCPToolPermissionModes(),
	)
	if err != nil {
		return nil, err
	}
	return openapi.GetToolCatalog200JSONResponse(openapi.ToolCatalog{
		BuiltInTools:          tools,
		CustomToolPermissions: customPermissions,
		McpToolPermissions:    mcpPermissions,
	}), nil
}

func toolPermissionProfileResponse(
	selection toolpermission.Selection,
	descriptors []toolpermission.ModeDescriptor,
) (openapi.ToolPermissionProfile, error) {
	defaultPermission, err := toolPermissionSelectionResponse(selection)
	if err != nil {
		return openapi.ToolPermissionProfile{}, err
	}
	modes, err := toolPermissionModeResponses(descriptors)
	if err != nil {
		return openapi.ToolPermissionProfile{}, err
	}
	return openapi.ToolPermissionProfile{
		DefaultPermission: defaultPermission,
		PermissionModes:   modes,
	}, nil
}

func toolPermissionSelectionResponse(
	selection toolpermission.Selection,
) (openapi.ToolPermissionSelection, error) {
	parameters, err := jsonObject(selection.Parameters)
	if err != nil {
		return openapi.ToolPermissionSelection{}, fmt.Errorf(
			"decode permission mode %q parameters: %w",
			selection.Mode,
			err,
		)
	}
	return openapi.ToolPermissionSelection{
		Mode:       selection.Mode,
		Parameters: parameters,
	}, nil
}

func toolPermissionModeResponses(
	descriptors []toolpermission.ModeDescriptor,
) ([]openapi.ToolPermissionMode, error) {
	modes := make([]openapi.ToolPermissionMode, 0, len(descriptors))
	for _, descriptor := range descriptors {
		schema, err := jsonObject(descriptor.ParametersSchema)
		if err != nil {
			return nil, fmt.Errorf(
				"decode permission mode %q parameter schema: %w",
				descriptor.Name,
				err,
			)
		}
		modes = append(modes, openapi.ToolPermissionMode{
			Name:             descriptor.Name,
			Label:            descriptor.Label,
			Description:      descriptor.Description,
			ParametersSchema: schema,
		})
	}
	return modes, nil
}

func jsonObject(raw json.RawMessage) (map[string]interface{}, error) {
	var value map[string]interface{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, fmt.Errorf("expected JSON object")
	}
	return value, nil
}
