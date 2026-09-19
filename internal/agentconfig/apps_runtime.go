package agentconfig

import (
	"fmt"
	"regexp"
	"slices"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

var appResourceKeyPattern = regexp.MustCompile(toolcatalog.ToolNamePattern)

func sourceSelectsAppTool(source AgentConfigSource, name string) bool {
	for _, resource := range source.AppResources {
		if resource.Enabled != nil && !*resource.Enabled {
			continue
		}
		if tool, ok := resource.Tools[name]; ok && (tool.Enabled == nil || *tool.Enabled) {
			return true
		}
	}
	return false
}

// Validate both directions of provenance at the immutable contract boundary.
// A malformed snapshot must not gain authority or evade subagent stripping.
func validateCompiledApps(compiled Compiled) error {
	if err := validateCompiledAppToolPolicies(compiled); err != nil {
		return err
	}
	for key, resource := range compiled.AppResources {
		if !appResourceKeyPattern.MatchString(key) {
			return fmt.Errorf("invalid resource key %q", key)
		}
		if err := resource.Validate(); err != nil {
			return fmt.Errorf("resource %q: %w", key, err)
		}
		if err := uniqueAppNames(resource.Tools); err != nil {
			return fmt.Errorf("resource %q tools: %w", key, err)
		}
		if err := uniqueAppNames(resource.MCP); err != nil {
			return fmt.Errorf("resource %q MCP: %w", key, err)
		}
		if !resource.Enabled {
			continue
		}
		for _, name := range resource.Tools {
			tool, ok := compiled.Tools[name]
			if !ok || tool.AppOrigin == nil || !slices.Contains(tool.AppOrigin.ResourceKeys, key) {
				return fmt.Errorf("resource %q tool %q is missing its compiled definition/provenance", key, name)
			}
		}
		for _, name := range resource.MCP {
			server, ok := compiled.MCP[name]
			if !ok || server.AppOrigin == nil || !slices.Contains(server.AppOrigin.ResourceKeys, key) {
				return fmt.Errorf("resource %q MCP server %q is missing its compiled definition/provenance", key, name)
			}
		}
	}
	check := func(name string, origin *AppToolOrigin, mcp bool) error {
		if origin == nil {
			return nil
		}
		if len(origin.ResourceKeys) == 0 {
			return fmt.Errorf("%q has empty app provenance", name)
		}
		if err := uniqueAppNames(origin.ResourceKeys); err != nil {
			return fmt.Errorf("%q provenance: %w", name, err)
		}
		if !mcp && origin.Base && toolcatalog.AppToolProvider(name) != "" {
			return fmt.Errorf("provider tool %q cannot inherit base authority", name)
		}
		for _, key := range origin.ResourceKeys {
			resource, ok := compiled.AppResources[key]
			names := resource.Tools
			if mcp {
				names = resource.MCP
			}
			handlerTool := !mcp && toolcatalog.IsInteractionDestinationTool(name) && resource.InteractionHandler != nil
			if !ok || !resource.Enabled || (!slices.Contains(names, name) && !handlerTool) {
				return fmt.Errorf("%q has invalid app provenance %q", name, key)
			}
		}
		return nil
	}
	for name, tool := range compiled.Tools {
		if err := check(name, tool.AppOrigin, false); err != nil {
			return err
		}
	}
	for name, server := range compiled.MCP {
		if err := check(name, server.AppOrigin, true); err != nil {
			return err
		}
	}
	return nil
}

func uniqueAppNames(names []string) error {
	seen := map[string]bool{}
	for _, name := range names {
		if name == "" || seen[name] {
			return fmt.Errorf("empty or duplicate name %q", name)
		}
		seen[name] = true
	}
	return nil
}
