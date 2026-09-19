package agentconfig

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type AgentConfigAppResourceSource struct {
	Definition         string                            `json:"definition,omitempty"`
	AppInstance        string                            `json:"app_instance,omitempty"`
	Enabled            *bool                             `json:"enabled,omitempty"`
	Connection         string                            `json:"connection,omitempty"`
	Scope              *appdefinition.Scope              `json:"scope,omitempty"`
	Tools              map[string]AgentConfigToolSource  `json:"tools,omitempty"`
	MCP                map[string]AgentConfigMCPSource   `json:"mcp,omitempty"`
	Listener           *appdefinition.Listener           `json:"listener,omitempty"`
	Follow             *appdefinition.Follow             `json:"follow,omitempty"`
	InteractionHandler *appdefinition.InteractionHandler `json:"interaction_handler,omitempty"`
}

type AppResourceCompiled struct {
	Definition         string                            `json:"definition"`
	AppInstanceID      string                            `json:"app_instance_id,omitempty"`
	Enabled            bool                              `json:"enabled"`
	ConnectionID       string                            `json:"connection_id,omitempty"`
	Scope              *appdefinition.Scope              `json:"scope,omitempty"`
	Tools              []string                          `json:"tools,omitempty"`
	MCP                []string                          `json:"mcp,omitempty"`
	Listener           *appdefinition.Listener           `json:"listener,omitempty"`
	Follow             *appdefinition.Follow             `json:"follow,omitempty"`
	InteractionHandler *appdefinition.InteractionHandler `json:"interaction_handler,omitempty"`
}

// AppInstanceResolution is reusable setup, not a live execution dependency.
// The project-scoped resolver validates instance/connection availability. Source
// capabilities are always explicit; a source scope replaces the default whole.
type AppInstanceResolution struct {
	AppInstanceID string
	Resource      AgentConfigAppResourceSource
}

// AppToolOrigin records which attachments supplied an ordinary tool/server.
// Base means an independent base definition survives subagent stripping. A
// provider-tool policy entry is not an independent grant and never sets Base.
type AppToolOrigin struct {
	ResourceKeys []string `json:"resource_keys"`
	Base         bool     `json:"base,omitempty"`
}

func resolveAppResource(
	source AgentConfigAppResourceSource,
	opts CompileOptions,
) (AgentConfigAppResourceSource, AppResourceCompiled, error) {
	var out AppResourceCompiled
	fail := func(err error) (AgentConfigAppResourceSource, AppResourceCompiled, error) { return source, out, err }
	if (source.Definition == "") == (source.AppInstance == "") {
		return fail(fmt.Errorf("exactly one of definition or app_instance is required"))
	}
	if source.AppInstance != "" {
		if _, err := publicid.Decode(publicid.KindProjectApp, source.AppInstance); err != nil {
			return fail(issueAt("/app_instance", err))
		}
		if opts.ResolveAppInstance == nil {
			return fail(issuef("/app_instance", "requires a ResolveAppInstance callback"))
		}
		resolved, err := opts.ResolveAppInstance(source.AppInstance)
		if err != nil {
			return fail(issueAt("/app_instance", err))
		}
		if resolved.AppInstanceID != source.AppInstance {
			return fail(issuef("/app_instance", "resolver returned a different instance id"))
		}
		defaults := resolved.Resource
		if defaults.Definition == "" || defaults.AppInstance != "" {
			return fail(issuef("/app_instance", "resolver must return an inline resource definition"))
		}
		out.AppInstanceID = resolved.AppInstanceID
		source.Definition = defaults.Definition
		if source.Enabled == nil {
			source.Enabled = defaults.Enabled
		}
		if source.Connection == "" {
			source.Connection = defaults.Connection
		}
		if source.Scope == nil {
			source.Scope = defaults.Scope
		}
		selectedTools := make(map[string]AgentConfigToolSource, len(source.Tools))
		for name, selection := range source.Tools {
			base, ok := defaults.Tools[name]
			if !ok {
				return fail(issuef(jsonPointer("tools", name), "tool is not exported by the app instance"))
			}
			tool, err := selectAppTool(base, selection)
			if err != nil {
				return fail(issueAt(jsonPointer("tools", name), err))
			}
			selectedTools[name] = tool
		}
		source.Tools = selectedTools
		selectedMCP := make(map[string]AgentConfigMCPSource, len(source.MCP))
		for name, selection := range source.MCP {
			base, ok := defaults.MCP[name]
			if !ok {
				return fail(issuef(jsonPointer("mcp", name), "MCP server is not exported by the app instance"))
			}
			server, err := selectAppMCP(base, selection)
			if err != nil {
				return fail(issueAt(jsonPointer("mcp", name), err))
			}
			selectedMCP[name] = server
		}
		source.MCP = selectedMCP
	}
	definition, ok := appdefinition.Lookup(source.Definition)
	if !ok {
		return fail(issuef("/definition", "unknown app definition %q", source.Definition))
	}
	if source.Connection != "" {
		if _, err := publicid.Decode(publicid.KindIntegrationConnection, source.Connection); err != nil {
			return fail(issueAt("/connection", err))
		}
		if opts.ResolveAppConnection == nil {
			return fail(issuef("/connection", "requires a ResolveAppConnection callback"))
		}
		connectionID, err := opts.ResolveAppConnection(source.Connection, definition.Provider)
		if err != nil {
			return fail(issueAt("/connection", err))
		}
		if connectionID != source.Connection {
			return fail(issuef("/connection", "resolver returned a different connection id"))
		}
		out.ConnectionID = connectionID
	}
	// Detach even nested permission/MCP values from reusable resolver data.
	raw, err := json.Marshal(source)
	if err != nil {
		return fail(err)
	}
	var cloned AgentConfigAppResourceSource
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return fail(err)
	}
	out.Definition, out.Enabled = cloned.Definition, cloned.Enabled == nil || *cloned.Enabled
	out.Scope, out.Listener = cloned.Scope, cloned.Listener
	out.Follow, out.InteractionHandler = cloned.Follow, cloned.InteractionHandler
	return cloned, out, nil
}

// A selection may change policy, but cannot silently change the definition
// supplied by an instance. Empty definitions are useful for selecting by name.
func selectAppTool(base, selection AgentConfigToolSource) (AgentConfigToolSource, error) {
	if selection.Type != "" && selection.Type != base.Type &&
		!(selection.Type == toolcatalog.ToolTypeBuiltIn && base.Type == "") {
		return base, fmt.Errorf("tool type conflicts with the bundled definition")
	}
	if selection.Description != "" && selection.Description != base.Description {
		return base, fmt.Errorf("description conflicts with the bundled definition")
	}
	if selection.InputSchema != nil && !equalAppSchema(selection.InputSchema, base.InputSchema) {
		return base, fmt.Errorf("input_schema conflicts with the bundled definition")
	}
	if selection.Enabled != nil {
		base.Enabled = selection.Enabled
	}
	if selection.Permission != nil {
		base.Permission = selection.Permission
	}
	// Deferred is additive for bundle selections because the existing source
	// type uses a bool, not a tri-state. Top-level policy remains authoritative.
	base.Deferred = base.Deferred || selection.Deferred
	return base, nil
}

func equalAppSchema(left, right map[string]any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && slices.Equal(leftJSON, rightJSON)
}

func selectAppMCP(base, selection AgentConfigMCPSource) (AgentConfigMCPSource, error) {
	if (selection.URL != "" && selection.URL != base.URL) ||
		(selection.Auth != nil && !reflect.DeepEqual(selection.Auth, base.Auth)) {
		return base, fmt.Errorf("MCP connection conflicts with the bundled definition")
	}
	if selection.DefaultEnabled != nil {
		base.DefaultEnabled = selection.DefaultEnabled
	}
	if selection.Permission != nil {
		base.Permission = selection.Permission
	}
	base.Deferred = base.Deferred || selection.Deferred
	base.Tools = maps.Clone(base.Tools)
	for name, override := range selection.Tools {
		tool := base.Tools[name]
		if override.Enabled != nil {
			tool.Enabled = override.Enabled
		}
		if override.Permission != nil {
			tool.Permission = override.Permission
		}
		if override.Deferred != nil {
			tool.Deferred = override.Deferred
		}
		if base.Tools == nil {
			base.Tools = map[string]AgentConfigMCPToolSource{}
		}
		base.Tools[name] = tool
	}
	return base, nil
}

func (r AppResourceCompiled) Validate() error {
	definition, ok := appdefinition.Lookup(r.Definition)
	if !ok {
		return fmt.Errorf("unknown app definition %q", r.Definition)
	}
	if r.AppInstanceID != "" {
		if _, err := publicid.Decode(publicid.KindProjectApp, r.AppInstanceID); err != nil {
			return fmt.Errorf("app_instance_id: %w", err)
		}
	}
	if r.ConnectionID != "" {
		if _, err := publicid.Decode(publicid.KindIntegrationConnection, r.ConnectionID); err != nil {
			return fmt.Errorf("connection_id: %w", err)
		}
	}
	needsConnection := r.Listener != nil || r.Follow != nil || r.InteractionHandler != nil ||
		len(r.Tools) > 0
	if needsConnection && (r.ConnectionID == "" || r.Scope == nil) {
		return fmt.Errorf("selected capabilities require connection and scope")
	}
	if err := definition.ValidateCapabilities(r.Scope, r.Listener, r.Follow, r.InteractionHandler); err != nil {
		return err
	}
	for _, name := range r.Tools {
		if provider := toolcatalog.AppToolProvider(name); provider != "" && provider != definition.Provider {
			return fmt.Errorf("tool %q does not belong to provider %s", name, definition.Provider)
		}
	}
	return nil
}

func compileAppResources(source AgentConfigSource, opts CompileOptions, compiled *Compiled) error {
	if len(source.AppResources) == 0 {
		return nil
	}
	compiled.AppResources = make(map[string]AppResourceCompiled, len(source.AppResources))
	catalog, err := toolcatalog.Default()
	if err != nil {
		return err
	}
	// Only entries present before expansion are authoritative base policies.
	// The first new bundle must not become the policy for subsequent bundles.
	baseTools := maps.Clone(compiled.Tools)
	baseMCP := maps.Clone(compiled.MCP)
	bundleTools := map[string]ToolCompiled{}
	declaredTools := map[string]bool{}
	bundleMCP := map[string]MCPServerCompiled{}
	for _, key := range slices.Sorted(maps.Keys(source.AppResources)) {
		resource := source.AppResources[key]
		pointer := jsonPointer("app_resources", key)
		resource, out, err := resolveAppResource(resource, opts)
		if err != nil {
			return issueAt(pointer, err)
		}
		for _, name := range slices.Sorted(maps.Keys(resource.Tools)) {
			tool := resource.Tools[name]
			base, explicitlyConfigured := source.Tools[name]
			if !explicitlyConfigured {
				if policy, retained := compiled.AppToolPolicies[name]; retained {
					base, explicitlyConfigured = policy.toolSource(), true
				}
			}
			enabled := tool.Enabled == nil || *tool.Enabled
			var entry ToolCompiled
			if tool.Type == toolcatalog.ToolTypeCustom {
				entry, err = compileCustomTool(name, tool, enabled, catalog)
			} else {
				entry, err = compileBuiltInTool(name, tool, enabled, catalog)
			}
			if err != nil {
				return issueAt(pointer, err)
			}
			declaredTools[name] = true
			if !enabled || !out.Enabled {
				// A disabled bundle retains its authored policy overrides. Validate
				// those against the declaration without contributing an executable
				// definition or provenance that a subagent could inherit.
				if explicitlyConfigured {
					overridden, err := selectAppTool(tool, base)
					if err != nil {
						return issueAt(pointer+jsonPointer("tools", name), err)
					}
					var validated ToolCompiled
					if overridden.Type == toolcatalog.ToolTypeCustom {
						validated, err = compileCustomTool(name, overridden, false, catalog)
					} else {
						_, err = compileBuiltInTool(name, overridden, false, catalog)
					}
					if err != nil {
						return issueAt(pointer+jsonPointer("tools", name), err)
					}
					_, carriesPolicy := compiled.Tools[name]
					if !carriesPolicy && overridden.Type == toolcatalog.ToolTypeCustom {
						if compiled.AppToolPolicies == nil {
							compiled.AppToolPolicies = map[string]AppToolPolicyCompiled{}
						}
						policy := AppToolPolicyCompiled{Enabled: base.Enabled, Deferred: base.Deferred}
						if base.Permission != nil {
							policy.Permission = &validated.Permission
						}
						compiled.AppToolPolicies[name] = policy
					}
				}
			}
			if !enabled {
				continue
			}
			out.Tools = append(out.Tools, name)
			if !out.Enabled {
				continue
			}
			current, exists := compiled.Tools[name]
			independentBase := exists && (current.AppOrigin == nil || current.AppOrigin.Base) &&
				toolcatalog.AppToolProvider(name) == ""
			if prior, hasBase := baseTools[name]; hasBase && !explicitlyConfigured {
				// Pinned and resource-implied tools retain policy before bundle
				// comparison; provenance separately controls subagent inheritance.
				if prior.Type != entry.Type || prior.Description != entry.Description ||
					!slices.Equal(prior.InputSchema, entry.InputSchema) {
					return issuef(pointer+jsonPointer("tools", name), "conflicts with the base tool definition")
				}
				entry = prior
			}
			if explicitlyConfigured {
				// Global enabled/permission/deferred choices win; custom tool
				// definitions still have to identify the same ordinary tool.
				if exists &&
					(current.Type != entry.Type || current.Description != entry.Description ||
						!slices.Equal(current.InputSchema, entry.InputSchema)) {
					return issuef(pointer+jsonPointer("tools", name), "conflicts with the base tool definition")
				}
				overridden, err := selectAppTool(tool, base)
				if err != nil {
					return issueAt(pointer+jsonPointer("tools", name), err)
				}
				overridden.Deferred = base.Deferred
				if overridden.Type == toolcatalog.ToolTypeCustom {
					entry, err = compileCustomTool(
						name,
						overridden,
						overridden.Enabled == nil || *overridden.Enabled,
						catalog,
					)
				} else {
					entry, err = compileBuiltInTool(
						name,
						overridden,
						overridden.Enabled == nil || *overridden.Enabled,
						catalog,
					)
				}
				if err != nil {
					return err
				}
			}
			// Compare effective policy after global overrides. Provenance is
			// accumulated separately and is not part of a tool's definition.
			entry.AppOrigin = nil
			if prior, exists := bundleTools[name]; exists && !reflect.DeepEqual(prior, entry) {
				return issuef(pointer+jsonPointer("tools", name), "conflicting app tool definitions or permissions")
			}
			bundleTools[name] = entry
			entry.AppOrigin = &AppToolOrigin{ResourceKeys: []string{key}, Base: independentBase}
			if exists && current.AppOrigin != nil {
				entry.AppOrigin.ResourceKeys = append(slices.Clone(current.AppOrigin.ResourceKeys), key)
			}
			compiled.Tools[name] = entry
		}
		if err := validateAppMCPCredentials(resource.MCP); err != nil {
			return issueAt(pointer, err)
		}
		servers, err := compileMCPServers(resource.MCP, opts)
		if err != nil {
			return issueAt(pointer, err)
		}
		for _, name := range slices.Sorted(maps.Keys(servers)) {
			out.MCP = append(out.MCP, name)
			if !out.Enabled {
				continue
			}
			entry := servers[name]
			current, exists := compiled.MCP[name]
			base, hasBase := baseMCP[name]
			independentBase := hasBase && (base.AppOrigin == nil || base.AppOrigin.Base)
			if hasBase {
				// Only an identical server definition may be shared. Base
				// permissions/enablement override its bundled defaults.
				if base.URL != entry.URL || !reflect.DeepEqual(base.Auth, entry.Auth) {
					return issuef(pointer+jsonPointer("mcp", name), "conflicts with the base MCP server definition")
				}
				entry = base
			}
			entry.AppOrigin = nil
			if prior, exists := bundleMCP[name]; exists && !reflect.DeepEqual(prior, entry) {
				return issuef(pointer+jsonPointer("mcp", name), "conflicting app MCP definitions or permissions")
			}
			bundleMCP[name] = entry
			entry.AppOrigin = &AppToolOrigin{ResourceKeys: []string{key}, Base: independentBase}
			if exists && current.AppOrigin != nil {
				entry.AppOrigin.ResourceKeys = append(slices.Clone(current.AppOrigin.ResourceKeys), key)
			}
			if compiled.MCP == nil {
				compiled.MCP = map[string]MCPServerCompiled{}
			}
			compiled.MCP[name] = entry
		}
		if err := out.Validate(); err != nil {
			return issueAt(pointer, err)
		}
		compiled.AppResources[key] = out
	}
	// Keep overrides available throughout expansion so every bundle sees the
	// same policy, then retain only those without a ToolCompiled carrier.
	for name := range compiled.Tools {
		delete(compiled.AppToolPolicies, name)
	}
	for name := range source.Tools {
		if _, ok := compiled.Tools[name]; !ok && !declaredTools[name] {
			return issuef(jsonPointer("tools", name), "tool is not registered or declared by an app resource")
		}
	}
	// Apply the same retrieval/discovery defaults ordinary tool declarations
	// receive, after bundles are expanded. Explicit base overrides remain intact.
	if err := compileInteractionDestinationTools(source, compiled, catalog); err != nil {
		return err
	}
	effective := source
	effective.Tools = make(map[string]AgentConfigToolSource, len(compiled.Tools))
	for name, tool := range compiled.Tools {
		effective.Tools[name] = AgentConfigToolSource{Enabled: &tool.Enabled, Deferred: tool.Deferred}
	}
	effective.MCP = make(map[string]AgentConfigMCPSource, len(compiled.MCP))
	for name, server := range compiled.MCP {
		tools := map[string]AgentConfigMCPToolSource{}
		for name, tool := range server.Tools {
			tools[name] = AgentConfigMCPToolSource{Deferred: tool.Deferred}
		}
		effective.MCP[name] = AgentConfigMCPSource{Deferred: server.Deferred, Tools: tools}
	}
	for _, name := range missingDefaultToolNames(effective) {
		entry, err := compileBuiltInTool(name, AgentConfigToolSource{}, true, catalog)
		if err != nil {
			return err
		}
		compiled.Tools[name] = entry
	}
	return nil
}

// AppToolResources is the authority selector for a provider action. Global
// enablement/permission remains in Tools; resource declarations cannot override it.
func AppToolResources(resources map[string]AppResourceCompiled, toolName string) []string {
	var keys []string
	for key, resource := range resources {
		if resource.Enabled && slices.Contains(resource.Tools, toolName) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

// Policies can be retained without a currently attached resource. Such tools
// become available only when the capability they operate on actually exists.
func toolHasResources(name string, resources map[string]AppResourceCompiled) bool {
	if toolcatalog.IsInteractionDestinationTool(name) {
		return hasEnabledInteractionHandler(resources)
	}
	return toolcatalog.AppToolProvider(name) == "" || len(AppToolResources(resources, name)) > 0
}
