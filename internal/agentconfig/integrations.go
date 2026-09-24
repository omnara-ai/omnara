package agentconfig

import (
	"bytes"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

type AgentConfigIntegrationCapabilitySource struct{}
type IntegrationCapabilityCompiled struct {
	IntegrationID uuid.UUID `json:"integration_id"`
}

type IntegrationResolution struct {
	IntegrationID   uuid.UUID
	IntegrationType integrationdefinition.Type
}

func cacheIntegrationResolver(opts CompileOptions) CompileOptions {
	resolve := opts.ResolveIntegrationName
	cache := map[string]IntegrationResolution{}
	opts.ResolveIntegrationName = func(name string) (IntegrationResolution, error) {
		if integration, ok := cache[name]; ok {
			return integration, nil
		}
		if resolve == nil {
			return IntegrationResolution{}, fmt.Errorf("integrations require a ResolveIntegrationName callback")
		}
		integration, err := resolve(name)
		if err != nil {
			return IntegrationResolution{}, err
		}
		if integration.IntegrationID == uuid.Nil {
			return IntegrationResolution{}, fmt.Errorf("resolver returned an empty integration ID")
		}
		if _, ok := integrationdefinition.Lookup(integration.IntegrationType); !ok {
			return IntegrationResolution{}, fmt.Errorf("unknown integration type %q", integration.IntegrationType)
		}
		cache[name] = integration
		return integration, nil
	}
	return opts
}

func compileIntegrationTool(name string, source AgentConfigToolSource, opts CompileOptions) (ToolCompiled, error) {
	integrationName, operation, ok := toolcatalog.SplitIntegrationToolName(name)
	if !ok {
		return ToolCompiled{}, issuef(jsonPointer("tools", name), "invalid qualified integration tool name")
	}
	if source.Type != "" || source.Description != "" || source.InputSchema != nil {
		return ToolCompiled{}, issuef(
			jsonPointer("tools", name),
			"integration tools cannot redefine type, description or input_schema",
		)
	}
	integration, err := opts.ResolveIntegrationName(integrationName)
	if err != nil {
		return ToolCompiled{}, issueOr(jsonPointer("tools", name), err)
	}
	definition, ok := toolcatalog.LookupIntegrationTool(integration.IntegrationType, operation)
	if !ok {
		return ToolCompiled{}, issuef(jsonPointer("tools", name), "integration does not export operation %q", operation)
	}
	entry, err := definition.Prepare(name)
	if err != nil {
		return ToolCompiled{}, issueOr(jsonPointer("tools", name), err)
	}
	permission := entry.DefaultPermission
	if source.Permission != nil {
		permission, err = toolpermission.ValidateSelection(*source.Permission, entry.PermissionModes)
		if err != nil {
			return ToolCompiled{}, issueOr(jsonPointer("tools", name, "permission"), err)
		}
	}
	return ToolCompiled{
		IntegrationID: integration.IntegrationID,
		Enabled:       source.Enabled == nil || *source.Enabled,
		Permission:    permission,
		Deferred:      source.Deferred,
	}, nil
}

func compileIntegrationCapabilities(source AgentConfigSource, opts CompileOptions, compiled *Compiled) error {
	const field = "interaction_handlers"
	if len(source.InteractionHandlers) > 0 {
		compiled.InteractionHandlers = map[string]IntegrationCapabilityCompiled{}
	}
	for _, name := range slices.Sorted(maps.Keys(source.InteractionHandlers)) {
		if err := toolcatalog.ValidateIntegrationName(name); err != nil {
			return issueOr(jsonPointer(field, name), err)
		}
		integration, err := opts.ResolveIntegrationName(name)
		if err != nil {
			return issueOr(jsonPointer(field, name), err)
		}
		definition, _ := integrationdefinition.Lookup(integration.IntegrationType)
		if definition.InteractionHandler == nil {
			return issuef(jsonPointer(field, name), "integration does not export an interaction handler")
		}
		compiled.InteractionHandlers[name] = IntegrationCapabilityCompiled{IntegrationID: integration.IntegrationID}
	}
	return nil
}

func ReferencedIntegrationIDs(compiled Compiled) []uuid.UUID {
	ids := map[uuid.UUID]struct{}{}
	for _, tool := range compiled.Tools {
		if tool.Enabled && tool.IntegrationID != uuid.Nil {
			ids[tool.IntegrationID] = struct{}{}
		}
	}
	for _, capability := range compiled.InteractionHandlers {
		ids[capability.IntegrationID] = struct{}{}
	}
	return slices.SortedFunc(maps.Keys(ids), func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
}
