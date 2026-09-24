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
		capabilities, err := integrationCapabilitiesResponse(definition.IntegrationType)
		if err != nil {
			return nil, err
		}
		data = append(data, openapi.IntegrationDefinition{
			IntegrationType: openapi.IntegrationType(definition.IntegrationType), Capabilities: capabilities,
		})
	}
	return openapi.ListIntegrationDefinitions200JSONResponse{Data: data}, nil
}

func integrationCapabilitiesResponse(id integrationdefinition.Type) (openapi.IntegrationCapabilities, error) {
	result := openapi.IntegrationCapabilities{
		Tools:         make(map[string]openapi.IntegrationCapabilityDefinition),
		Subscriptions: make(map[string]openapi.IntegrationSubscriptionDefinition),
	}
	definition, ok := integrationdefinition.Lookup(id)
	if !ok {
		return result, fmt.Errorf("unknown integration type %q", id)
	}
	for _, operation := range definition.Tools {
		tool, ok := toolcatalog.LookupIntegrationTool(id, operation)
		if !ok {
			return result, fmt.Errorf("unknown integration tool %s/%s", id, operation)
		}
		prepared, err := tool.Prepare(toolcatalog.IntegrationToolName("integration", operation))
		if err != nil {
			return result, err
		}
		entry, err := integrationCapabilityResponse(prepared.InputSchema, prepared.Description)
		if err != nil {
			return result, err
		}
		result.Tools[operation] = entry
	}
	for name, subscription := range definition.Subscriptions {
		conversation, err := subscription.ConversationSchema()
		if err != nil {
			return result, err
		}
		entry := openapi.IntegrationSubscriptionDefinition{Events: subscription.Events}
		if err := json.Unmarshal(conversation, &entry.ConversationSchema); err != nil {
			return result, err
		}
		result.Subscriptions[name] = entry
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
