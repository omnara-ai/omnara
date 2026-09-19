package agentconfig

import (
	"fmt"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

// AppToolPolicyCompiled retains an explicit global override when an inactive
// app declaration contributes no ToolCompiled to carry it. It contains neither
// a definition nor authority and is removed when a tool entry carries the policy.
// Deferred false still overrides a bundle's true, just as in source.Tools.
type AppToolPolicyCompiled struct {
	Enabled    *bool                     `json:"enabled,omitempty"`
	Permission *toolpermission.Selection `json:"permission,omitempty"`
	Deferred   bool                      `json:"deferred,omitempty"`
}

func (policy AppToolPolicyCompiled) toolSource() AgentConfigToolSource {
	return AgentConfigToolSource{Enabled: policy.Enabled, Permission: policy.Permission, Deferred: policy.Deferred}
}

func validateCompiledAppToolPolicies(compiled Compiled) error {
	catalog, err := toolcatalog.Default()
	if err != nil {
		return err
	}
	for name, policy := range compiled.AppToolPolicies {
		if !appResourceKeyPattern.MatchString(name) || toolcatalog.UsesMCPRuntimeNamespace(name) ||
			toolcatalog.IsReservedWireToolName(name) {
			return fmt.Errorf("app tool policy %q has an invalid or reserved name", name)
		}
		if _, exists := catalog.Lookup(name); exists {
			return fmt.Errorf("app tool policy %q collides with a built-in tool", name)
		}
		if _, exists := compiled.Tools[name]; exists {
			return fmt.Errorf("app tool policy %q already has a compiled tool carrier", name)
		}
		if policy.Permission != nil {
			if _, err := toolpermission.ValidateSelection(
				*policy.Permission, toolcatalog.CustomToolPermissionModes(),
			); err != nil {
				return fmt.Errorf("app tool policy %q: %w", name, err)
			}
		}
	}
	return nil
}
