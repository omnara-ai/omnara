package agentconfig

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func configObject(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return fmt.Errorf("config must be an object: %w", err)
	}
	if object == nil {
		return fmt.Errorf("config must be an object")
	}
	return nil
}
func validateUnsupportedConfig(raw json.RawMessage) error {
	if err := configObject(raw); err != nil {
		return err
	}
	var object map[string]json.RawMessage
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &object)
	}
	if len(object) > 0 {
		return fmt.Errorf("tool does not support nonempty config")
	}
	return nil
}

// Structural validation has no database reads and needs no live app metadata.
func validateCompiledApps(compiled Compiled) error {
	identities := map[string]string{}
	check := func(name, id string, config json.RawMessage) error {
		if _, err := publicid.Decode(publicid.KindProjectApp, id); err != nil {
			return fmt.Errorf("app %q: invalid app ID: %w", name, err)
		}
		if previous, ok := identities[name]; ok && previous != id {
			return fmt.Errorf("app %q has inconsistent pinned IDs", name)
		}
		identities[name] = id
		return configObject(config)
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
		if err := check(name, tool.AppID, tool.Config); err != nil {
			return err
		}
	}
	for key, capability := range compiled.Listeners {
		name, _, ok := toolcatalog.SplitAppListenerName(key)
		if !ok {
			return fmt.Errorf("invalid listener key %q", key)
		}
		if err := check(name, capability.AppID, capability.Config); err != nil {
			return err
		}
	}
	for key, capability := range compiled.InteractionHandlers {
		if err := toolcatalog.ValidateAppName(key); err != nil {
			return err
		}
		if err := check(key, capability.AppID, capability.Config); err != nil {
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

type PreparedAppListener struct {
	AppID string
	appdefinition.PreparedListener
}
type PreparedAppInteractionHandler struct {
	AppID string
	appdefinition.PreparedInteractionHandler
}
type PreparedAppCapabilities struct {
	Tools               []RuntimeTool
	Listeners           map[string]PreparedAppListener
	InteractionHandlers map[string]PreparedAppInteractionHandler
	// Unavailable records capability paths that cannot be prepared (including
	// missing/disconnected apps). Other capabilities and dashboard use remain usable.
	Unavailable map[string]error
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

// PrepareAppCapabilities is pure. Callers load each ReferencedAppIDs entry once,
// enforcing project ownership and live status before supplying it in apps.
func PrepareAppCapabilities(compiled Compiled, apps map[string]AppResolution) (PreparedAppCapabilities, error) {
	if err := validateCompiledApps(compiled); err != nil {
		return PreparedAppCapabilities{}, err
	}
	result := PreparedAppCapabilities{
		Listeners:           map[string]PreparedAppListener{},
		InteractionHandlers: map[string]PreparedAppInteractionHandler{},
		Unavailable:         map[string]error{},
	}
	for _, key := range slices.Sorted(maps.Keys(compiled.Tools)) {
		tool := compiled.Tools[key]
		if tool.AppID == "" || !tool.Enabled {
			continue
		}
		definition, err := resolvedDefinition(tool.AppID, apps)
		if err != nil {
			result.Unavailable[jsonPointer("tools", key)] = err
			continue
		}
		_, operation, _ := toolcatalog.SplitAppToolName(key)
		metadata, ok := toolcatalog.LookupAppTool(definition.ID, operation)
		if !ok {
			result.Unavailable[jsonPointer("tools", key)] = fmt.Errorf("app does not export operation %q", operation)
			continue
		}
		entry, err := metadata.Prepare(key, tool.Config)
		if err != nil {
			result.Unavailable[jsonPointer("tools", key)] = err
			continue
		}
		runtime := runtimeBuiltInTool(entry, tool.Permission)
		runtime.AppID, runtime.Deferred = tool.AppID, tool.Deferred
		runtime.Config, err = metadata.CanonicalConfig(tool.Config)
		if err != nil {
			return PreparedAppCapabilities{}, err
		}
		result.Tools = append(result.Tools, runtime)
	}
	for key, capability := range compiled.Listeners {
		definition, err := resolvedDefinition(capability.AppID, apps)
		if err != nil {
			result.Unavailable[jsonPointer("listeners", key)] = err
			continue
		}
		_, name, _ := toolcatalog.SplitAppListenerName(key)
		metadata, ok := definition.Listeners[name]
		if !ok {
			result.Unavailable[jsonPointer("listeners", key)] = fmt.Errorf("app does not export listener %q", name)
			continue
		}
		prepared, err := metadata.Prepare(capability.Config)
		if err != nil {
			result.Unavailable[jsonPointer("listeners", key)] = err
			continue
		}
		result.Listeners[key] = PreparedAppListener{AppID: capability.AppID, PreparedListener: prepared}
	}
	for key, capability := range compiled.InteractionHandlers {
		definition, err := resolvedDefinition(capability.AppID, apps)
		if err != nil {
			result.Unavailable[jsonPointer("interaction_handlers", key)] = err
			continue
		}
		if definition.InteractionHandler == nil {
			result.Unavailable[jsonPointer("interaction_handlers", key)] = fmt.Errorf(
				"app does not export an interaction handler",
			)
			continue
		}
		prepared, err := definition.InteractionHandler.Prepare(capability.Config)
		if err != nil {
			result.Unavailable[jsonPointer("interaction_handlers", key)] = err
			continue
		}
		result.InteractionHandlers[key] = PreparedAppInteractionHandler{
			AppID:                      capability.AppID,
			PreparedInteractionHandler: prepared,
		}
	}
	return result, nil
}
