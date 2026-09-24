package agentconfig

import (
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func validateCompiledIntegrations(compiled Compiled) error {
	identities := map[string]uuid.UUID{}
	check := func(name string, id uuid.UUID) error {
		if id == uuid.Nil {
			return fmt.Errorf("integration %q: empty integration ID", name)
		}
		if previous, ok := identities[name]; ok && previous != id {
			return fmt.Errorf("integration %q has inconsistent pinned IDs", name)
		}
		identities[name] = id
		return nil
	}
	for key, tool := range compiled.Tools {
		if !toolcatalog.UsesIntegrationToolNamespace(key) {
			if tool.IntegrationID != uuid.Nil {
				return fmt.Errorf("ordinary tool %q has an integration ID", key)
			}
			continue
		}
		name, _, ok := toolcatalog.SplitIntegrationToolName(key)
		if !ok {
			return fmt.Errorf("invalid integration tool name %q", key)
		}
		if tool.Type != "" || tool.Description != "" || len(tool.InputSchema) > 0 {
			return fmt.Errorf("integration tool %q cannot redefine type, description or input_schema", key)
		}
		if _, err := toolpermission.ValidateSelection(tool.Permission, toolpermission.CommonModeDescriptors()); err != nil {
			return fmt.Errorf("integration tool %q permission: %w", key, err)
		}
		if err := check(name, tool.IntegrationID); err != nil {
			return err
		}
	}
	for key, capability := range compiled.InteractionHandlers {
		if err := toolcatalog.ValidateIntegrationName(key); err != nil {
			return err
		}
		if err := check(key, capability.IntegrationID); err != nil {
			return err
		}
	}
	return nil
}

func integrationToolsFromCompiled(compiled Compiled) map[string]ToolCompiled {
	result := map[string]ToolCompiled{}
	for key, tool := range compiled.Tools {
		if tool.IntegrationID != uuid.Nil {
			result[key] = tool
		}
	}
	return result
}

type PreparedIntegrationInteractionHandler struct {
	IntegrationID uuid.UUID
	integrationdefinition.PreparedInteractionHandler
}

func resolvedDefinition(
	integrationID uuid.UUID,
	integrations map[uuid.UUID]IntegrationResolution,
) (integrationdefinition.Definition, error) {
	integration, ok := integrations[integrationID]
	if !ok || integration.IntegrationID != integrationID {
		return integrationdefinition.Definition{}, fmt.Errorf("integration %q is unavailable", integrationID)
	}
	definition, ok := integrationdefinition.Lookup(integration.IntegrationType)
	if !ok {
		return integrationdefinition.Definition{}, fmt.Errorf("unknown integration type %q", integration.IntegrationType)
	}
	return definition, nil
}

func PrepareIntegrationTools(
	compiled Compiled,
	integrations map[uuid.UUID]IntegrationResolution,
) ([]RuntimeTool, error) {
	if err := validateCompiledIntegrations(compiled); err != nil {
		return nil, err
	}
	var result []RuntimeTool
	for _, key := range slices.Sorted(maps.Keys(compiled.Tools)) {
		tool := compiled.Tools[key]
		if tool.IntegrationID == uuid.Nil || !tool.Enabled {
			continue
		}
		definition, err := resolvedDefinition(tool.IntegrationID, integrations)
		if err != nil {
			continue
		}
		_, operation, _ := toolcatalog.SplitIntegrationToolName(key)
		metadata, ok := toolcatalog.LookupIntegrationTool(definition.IntegrationType, operation)
		if !ok {
			continue
		}
		entry, err := metadata.Prepare(key)
		if err != nil {
			return nil, err
		}
		runtime := runtimeBuiltInTool(entry, tool.Permission)
		runtime.Deferred = tool.Deferred
		result = append(result, runtime)
	}
	return result, nil
}
