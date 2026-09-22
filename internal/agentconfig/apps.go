package agentconfig

import (
	"bytes"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

type AgentConfigAppCapabilitySource struct{}
type AppCapabilityCompiled struct {
	AppID uuid.UUID `json:"app_id"`
}

type AppResolution struct {
	AppID   uuid.UUID
	AppType appdefinition.Type
}

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
		if app.AppID == uuid.Nil {
			return AppResolution{}, fmt.Errorf("resolver returned an empty app ID")
		}
		if _, ok := appdefinition.Lookup(app.AppType); !ok {
			return AppResolution{}, fmt.Errorf("unknown app type %q", app.AppType)
		}
		cache[name] = app
		return app, nil
	}
	return opts
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
	definition, ok := toolcatalog.LookupAppTool(app.AppType, operation)
	if !ok {
		return ToolCompiled{}, issuef(jsonPointer("tools", name), "app does not export operation %q", operation)
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
		AppID:      app.AppID,
		Enabled:    source.Enabled == nil || *source.Enabled,
		Permission: permission,
		Deferred:   source.Deferred,
	}, nil
}

func compileAppCapabilities(source AgentConfigSource, opts CompileOptions, compiled *Compiled) error {
	const field = "interaction_handlers"
	if len(source.InteractionHandlers) > 0 {
		compiled.InteractionHandlers = map[string]AppCapabilityCompiled{}
	}
	for _, name := range slices.Sorted(maps.Keys(source.InteractionHandlers)) {
		if err := toolcatalog.ValidateAppName(name); err != nil {
			return issueOr(jsonPointer(field, name), err)
		}
		app, err := opts.ResolveAppName(name)
		if err != nil {
			return issueOr(jsonPointer(field, name), err)
		}
		definition, _ := appdefinition.Lookup(app.AppType)
		if definition.InteractionHandler == nil {
			return issuef(jsonPointer(field, name), "app does not export an interaction handler")
		}
		compiled.InteractionHandlers[name] = AppCapabilityCompiled{AppID: app.AppID}
	}
	return nil
}

func ReferencedAppIDs(compiled Compiled) []uuid.UUID {
	ids := map[uuid.UUID]struct{}{}
	for _, tool := range compiled.Tools {
		if tool.Enabled && tool.AppID != uuid.Nil {
			ids[tool.AppID] = struct{}{}
		}
	}
	for _, capability := range compiled.InteractionHandlers {
		ids[capability.AppID] = struct{}{}
	}
	return slices.SortedFunc(maps.Keys(ids), func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
}
