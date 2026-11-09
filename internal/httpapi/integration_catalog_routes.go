package httpapi

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func (s strictOpenAPIServer) ListIntegrationDefinitions(
	_ context.Context,
	_ openapi.ListIntegrationDefinitionsRequestObject,
) (openapi.ListIntegrationDefinitionsResponseObject, error) {
	data := make([]openapi.IntegrationDefinition, 0)
	for _, definition := range integrationdefinition.All() {
		capabilities, err := integrationCapabilitiesResponse(definition.IntegrationKind)
		if err != nil {
			return nil, err
		}
		data = append(data, openapi.IntegrationDefinition{
			IntegrationKind: openapi.IntegrationKind(definition.IntegrationKind), Capabilities: capabilities,
		})
	}
	return openapi.ListIntegrationDefinitions200JSONResponse{Data: data}, nil
}

func integrationCapabilitiesResponse(id integrationdefinition.Kind) (openapi.IntegrationCapabilities, error) {
	result := openapi.IntegrationCapabilities{
		Tools: make(map[string]openapi.IntegrationCapabilityDefinition),
	}
	definition, ok := integrationdefinition.Lookup(id)
	if !ok {
		return result, fmt.Errorf("unknown integration type %q", id)
	}
	for _, tool := range toolcatalog.IntegrationTools(id) {
		prepared, err := tool.Prepare(toolcatalog.IntegrationToolName("integration", tool.Operation))
		if err != nil {
			return result, err
		}
		entry, err := integrationCapabilityResponse(prepared.InputSchema, prepared.Description)
		if err != nil {
			return result, err
		}
		result.Tools[tool.Operation] = entry
	}
	if subscription := definition.Subscription; subscription != nil {
		conversation, err := subscription.ConversationSchema()
		if err != nil {
			return result, err
		}
		entry := openapi.IntegrationSubscriptionDefinition{}
		if err := json.Unmarshal(conversation, &entry.ConversationSchema); err != nil {
			return result, err
		}
		result.Subscription = &entry
	}
	if handler := definition.InteractionHandler; handler != nil {
		prepared, err := handler.Prepare()
		if err != nil {
			return result, err
		}
		entry, err := integrationCapabilityResponse(prepared.InputSchema, prepared.Description)
		if err != nil {
			return result, err
		}
		result.InteractionHandler = &entry
	}
	if settings := definition.Settings; settings != nil {
		schema, err := settings.Schema()
		if err != nil {
			return result, err
		}
		entry, err := integrationCapabilityResponse(schema, settings.Description)
		if err != nil {
			return result, err
		}
		result.Settings = &entry
	}
	if schedule := definition.Schedule; schedule != nil {
		entry, err := integrationCapabilityResponse(schedule.InputSchema, schedule.Description)
		if err != nil {
			return result, err
		}
		result.Schedule = &entry
	}
	return result, nil
}

func integrationCapabilityResponse(
	input json.RawMessage,
	description string,
) (openapi.IntegrationCapabilityDefinition, error) {
	var result openapi.IntegrationCapabilityDefinition
	if err := json.Unmarshal(input, &result.InputSchema); err != nil {
		return result, err
	}
	if description != "" {
		result.Description = &description
	}
	return result, nil
}
