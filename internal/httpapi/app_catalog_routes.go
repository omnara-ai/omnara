package httpapi

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func (s strictOpenAPIServer) ListAppDefinitions(
	_ context.Context,
	_ openapi.ListAppDefinitionsRequestObject,
) (openapi.ListAppDefinitionsResponseObject, error) {
	data := make([]openapi.AppDefinition, 0)
	for _, definition := range appdefinition.All() {
		capabilities, err := appCapabilitiesResponse(definition.ID)
		if err != nil {
			return nil, err
		}
		data = append(data, openapi.AppDefinition{
			Id: definition.ID, Provider: openapi.IntegrationProvider(definition.Provider), Capabilities: capabilities,
		})
	}
	return openapi.ListAppDefinitions200JSONResponse{Data: data}, nil
}

func appCapabilitiesResponse(id string) (openapi.AppCapabilities, error) {
	result := openapi.AppCapabilities{
		Tools: make(map[string]openapi.AppCapabilityDefinition), Listeners: make(map[string]openapi.AppCapabilityDefinition),
	}
	definition, ok := appdefinition.Lookup(id)
	if !ok {
		return result, fmt.Errorf("unknown app definition %q", id)
	}
	for _, operation := range definition.Tools {
		tool, ok := toolcatalog.LookupAppTool(id, operation)
		if !ok {
			return result, fmt.Errorf("unknown app tool %s/%s", id, operation)
		}
		config, err := tool.ConfigSchema()
		if err != nil {
			return result, err
		}
		prepared, err := tool.Prepare(toolcatalog.AppToolName("app", operation), nil)
		if err != nil {
			return result, err
		}
		entry, err := appCapabilityResponse(config, prepared.InputSchema, tool.Description)
		if err != nil {
			return result, err
		}
		result.Tools[operation] = entry
	}
	for name, listener := range definition.Listeners {
		config, err := listener.ConfigSchema()
		if err != nil {
			return result, err
		}
		entry, err := appCapabilityResponse(config, nil, "")
		if err != nil {
			return result, err
		}
		result.Listeners[name] = entry
	}
	if handler := definition.InteractionHandler; handler != nil {
		config, err := handler.ConfigSchema()
		if err != nil {
			return result, err
		}
		prepared, err := handler.Prepare(nil)
		if err != nil {
			return result, err
		}
		entry, err := appCapabilityResponse(config, prepared.InputSchema, prepared.Description)
		if err != nil {
			return result, err
		}
		result.InteractionHandler = &entry
	}
	return result, nil
}

func appCapabilityResponse(config, input json.RawMessage, description string) (openapi.AppCapabilityDefinition, error) {
	var result openapi.AppCapabilityDefinition
	if err := json.Unmarshal(config, &result.ConfigSchema); err != nil {
		return result, err
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &result.InputSchema); err != nil {
			return result, err
		}
	}
	if description != "" {
		result.Description = &description
	}
	return result, nil
}
