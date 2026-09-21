package agentconfig

import (
	"fmt"
	"maps"
	"slices"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

// Structural validation has no database reads and needs no live app metadata.
func validateCompiledApps(compiled Compiled) error {
	identities := map[string]string{}
	check := func(name, id string) error {
		if _, err := publicid.Decode(publicid.KindProjectApp, id); err != nil {
			return fmt.Errorf("app %q: invalid app ID: %w", name, err)
		}
		if previous, ok := identities[name]; ok && previous != id {
			return fmt.Errorf("app %q has inconsistent pinned IDs", name)
		}
		identities[name] = id
		return nil
	}
	for key, tool := range compiled.Tools {
		if !toolcatalog.UsesAppToolNamespace(key) {
			if tool.AppID != "" {
				return fmt.Errorf("ordinary tool %q has an app ID", key)
			}
			continue
		}
		name, _, ok := toolcatalog.SplitAppToolName(key)
		if !ok {
			return fmt.Errorf("invalid app tool name %q", key)
		}
		if tool.Type != "" || tool.Description != "" || len(tool.InputSchema) > 0 {
			return fmt.Errorf("app tool %q cannot redefine type, description or input_schema", key)
		}
		if _, err := toolpermission.ValidateSelection(tool.Permission, toolpermission.CommonModeDescriptors()); err != nil {
			return fmt.Errorf("app tool %q permission: %w", key, err)
		}
		if err := check(name, tool.AppID); err != nil {
			return err
		}
	}
	for key, capability := range compiled.InteractionHandlers {
		if err := toolcatalog.ValidateAppName(key); err != nil {
			return err
		}
		if err := check(key, capability.AppID); err != nil {
			return err
		}
	}
	return nil
}

func appToolsFromCompiled(compiled Compiled) map[string]ToolCompiled {
	result := map[string]ToolCompiled{}
	for key, tool := range compiled.Tools {
		if tool.AppID != "" {
			result[key] = tool
		}
	}
	return result
}

type PreparedAppInteractionHandler struct {
	AppID string
	appdefinition.PreparedInteractionHandler
}

func resolvedDefinition(appID string, apps map[string]AppResolution) (appdefinition.Definition, error) {
	app, ok := apps[appID]
	if !ok || app.AppID != appID {
		return appdefinition.Definition{}, fmt.Errorf("app %q is unavailable", appID)
	}
	definition, ok := appdefinition.Lookup(app.Definition)
	if !ok {
		return appdefinition.Definition{}, fmt.Errorf("unknown app definition %q", app.Definition)
	}
	return definition, nil
}

// PrepareAppTools is pure. Callers enforce project ownership and live app status
// before supplying metadata. Unavailable capabilities are omitted; handlers are
// prepared independently when listing or selecting them.
func PrepareAppTools(compiled Compiled, apps map[string]AppResolution) ([]RuntimeTool, error) {
	if err := validateCompiledApps(compiled); err != nil {
		return nil, err
	}
	var result []RuntimeTool
	for _, key := range slices.Sorted(maps.Keys(compiled.Tools)) {
		tool := compiled.Tools[key]
		if tool.AppID == "" || !tool.Enabled {
			continue
		}
		definition, err := resolvedDefinition(tool.AppID, apps)
		if err != nil {
			continue
		}
		_, operation, _ := toolcatalog.SplitAppToolName(key)
		metadata, ok := toolcatalog.LookupAppTool(definition.ID, operation)
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
