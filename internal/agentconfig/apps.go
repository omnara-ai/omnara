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

type AgentConfigAppCapabilitySource struct {
	Config map[string]any `json:"config,omitempty"`
}
type AppCapabilityCompiled struct {
	AppID  string          `json:"app_id"`
	Config json.RawMessage `json:"config,omitempty"`
}

// AppResolution supplies compile/preparation metadata. Definition is never persisted in a
// compiled capability. The resolver must enforce project ownership and app state.
type AppResolution struct{ AppID, Definition string }

func cacheAppResolver(opts CompileOptions) CompileOptions {
	resolve := opts.ResolveAppName
	cache := map[string]AppResolution{}
	opts.ResolveAppName = func(name string) (AppResolution, error) {
		if app, ok := cache[name]; ok {
			return app, nil
		}
		if resolve == nil {
			return AppResolution{}, fmt.Errorf("apps require a ResolveAppName callback")
		}
		app, err := resolve(name)
		if err != nil {
			return AppResolution{}, err
		}
		if _, err := publicid.Decode(publicid.KindProjectApp, app.AppID); err != nil {
			return AppResolution{}, fmt.Errorf("invalid app ID: %w", err)
		}
		if _, ok := appdefinition.Lookup(app.Definition); !ok {
			return AppResolution{}, fmt.Errorf("unknown app definition %q", app.Definition)
		}
		cache[name] = app
		return app, nil
	}
	return opts
}

func sourceConfig(config map[string]any) (json.RawMessage, error) {
	if config == nil {
		return json.RawMessage(`{}`), nil
	}
	return json.Marshal(config)
}
func compileAppTool(name string, source AgentConfigToolSource, opts CompileOptions) (ToolCompiled, error) {
	appName, operation, ok := toolcatalog.SplitAppToolName(name)
	if !ok {
		return ToolCompiled{}, issuef(jsonPointer("tools", name), "invalid qualified app tool name")
	}
	if source.Type != "" || source.Description != "" || source.InputSchema != nil {
		return ToolCompiled{}, issuef(
			jsonPointer("tools", name),
			"app tools cannot redefine type, description or input_schema",
		)
	}
	app, err := opts.ResolveAppName(appName)
	if err != nil {
		return ToolCompiled{}, issueOr(jsonPointer("tools", name), err)
	}
	definition, ok := toolcatalog.LookupAppTool(app.Definition, operation)
	if !ok {
		return ToolCompiled{}, issuef(jsonPointer("tools", name), "app does not export operation %q", operation)
	}
	raw, err := sourceConfig(source.Config)
	if err != nil {
		return ToolCompiled{}, issueOr(jsonPointer("tools", name, "config"), err)
	}
	config, err := definition.CanonicalConfig(raw)
	if err != nil {
		return ToolCompiled{}, issueOr(jsonPointer("tools", name, "config"), err)
	}
	entry, err := definition.Prepare(name, config)
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
		AppID:      app.AppID,
		Config:     config,
		Enabled:    source.Enabled == nil || *source.Enabled,
		Permission: permission,
		Deferred:   source.Deferred,
	}, nil
}

func compileAppCapabilities(source AgentConfigSource, opts CompileOptions, compiled *Compiled) error {
	for _, field := range []string{"listeners", "interaction_handlers"} {
		entries := source.Listeners
		target := &compiled.Listeners
		if field == "interaction_handlers" {
			entries, target = source.InteractionHandlers, &compiled.InteractionHandlers
		}
		if len(entries) > 0 {
			*target = map[string]AppCapabilityCompiled{}
		}
		for _, key := range slices.Sorted(maps.Keys(entries)) {
			name, listener := key, ""
			if field == "listeners" {
				var ok bool
				name, listener, ok = toolcatalog.SplitAppListenerName(key)
				if !ok {
					return issuef(jsonPointer(field, key), "invalid app listener name")
				}
			} else if err := toolcatalog.ValidateAppName(name); err != nil {
				return issueOr(jsonPointer(field, key), err)
			}
			app, err := opts.ResolveAppName(name)
			if err != nil {
				return issueOr(jsonPointer(field, key), err)
			}
			definition, _ := appdefinition.Lookup(app.Definition)
			raw, err := sourceConfig(entries[key].Config)
			if err != nil {
				return issueOr(jsonPointer(field, key, "config"), err)
			}
			var config json.RawMessage
			if field == "listeners" {
				capability, ok := definition.Listeners[listener]
				if !ok {
					return issuef(jsonPointer(field, key), "app does not export listener %q", listener)
				}
				prepared, prepareErr := capability.Prepare(raw)
				config, err = prepared.Config, prepareErr
			} else {
				if definition.InteractionHandler == nil {
					return issuef(jsonPointer(field, key), "app does not export an interaction handler")
				}
				prepared, prepareErr := definition.InteractionHandler.Prepare(raw)
				config, err = prepared.Config, prepareErr
			}
			if err != nil {
				return issueOr(jsonPointer(field, key, "config"), err)
			}
			(*target)[key] = AppCapabilityCompiled{AppID: app.AppID, Config: config}
		}
	}
	return nil
}

// ReferencedAppIDs is the distinct project-scoped read set for preparation.
// Disabled tools grant no authority; listeners and handlers remain independent.
func ReferencedAppIDs(compiled Compiled) []string {
	ids := map[string]struct{}{}
	for _, tool := range compiled.Tools {
		if tool.Enabled && tool.AppID != "" {
			ids[tool.AppID] = struct{}{}
		}
	}
	for _, entries := range []map[string]AppCapabilityCompiled{compiled.Listeners, compiled.InteractionHandlers} {
		for _, capability := range entries {
			ids[capability.AppID] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(ids))
}
