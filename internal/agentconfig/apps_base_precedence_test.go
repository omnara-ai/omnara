package agentconfig

import (
	"fmt"
	"testing"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestAppMCPBundlesUseAuthoritativeBasePolicy(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"source", "derive_independent", "derive_app_only"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			disabled, enabled := false, true
			deny := toolpermission.DefaultSelection(toolpermission.ModeAlwaysDeny)
			server := AgentConfigMCPSource{
				URL: "https://example.com/mcp", DefaultEnabled: &disabled, Permission: &deny,
				Tools: map[string]AgentConfigMCPToolSource{
					"lookup": {Enabled: &enabled, Permission: &deny, Deferred: &disabled},
				},
			}
			resources := mcpPolicyTestBundles()
			source := appTestSource(nil)
			source.AppResources = map[string]AgentConfigAppResourceSource{}
			for key, resource := range resources {
				resource.Enabled = &disabled
				source.AppResources["inactive_"+key] = resource
			}
			independent := path != "derive_app_only"
			if independent {
				source.MCP = map[string]AgentConfigMCPSource{"crm": server}
			} else {
				source.AppResources["original"] = AgentConfigAppResourceSource{
					Definition: appdefinition.Slack, MCP: map[string]AgentConfigMCPSource{"crm": server},
				}
			}
			result, err := compileAppTest(t, source, appTestOptions())
			require.NoError(t, err)
			base, _ := roundTripAppPolicy(t, result.Compiled)
			before, err := EncodeCompiled(base)
			require.NoError(t, err)
			var derived Compiled
			if path == "source" {
				source.AppResources = resources
				result, err = compileAppTest(t, source, appTestOptions())
				derived = result.Compiled
			} else {
				derived, err = DeriveWithAppResources(base, resources, appTestOptions())
			}
			require.NoError(t, err)
			derived, contract := roundTripAppPolicy(t, derived)
			require.NotContains(t, derived.Tools, toolcatalog.ToolNameToolSearch)
			after, err := EncodeCompiled(base)
			require.NoError(t, err)
			require.Equal(t, before.CanonicalJSON, after.CanonicalJSON)
			want := base.MCP["crm"]
			want.AppOrigin = nil
			got := derived.MCP["crm"]
			require.Equal(t, independent, got.AppOrigin.Base)
			require.Contains(t, got.AppOrigin.ResourceKeys, "first")
			require.Contains(t, got.AppOrigin.ResourceKeys, "second")
			got.AppOrigin = nil
			require.Equal(t, want, got, "server and remote-tool policy must come from the authoritative base")
			require.Len(t, contract.MCPServers, 1)
			lookup, exposed := contract.MCPServers[0].ResolveTool("lookup")
			require.True(t, exposed)
			require.Equal(t, toolpermission.ModeAlwaysDeny, lookup.Permission.Mode)
			require.False(t, lookup.Deferred)
			_, exposed = contract.MCPServers[0].ResolveTool("bundle_only")
			require.False(t, exposed, "bundled remote-tool overrides cannot replace the base policy")
			for _, kind := range []string{SubagentTypeSelf, SubagentTypeProfile} {
				child, err := SubagentCompiledFrom(derived, SubagentCompiled{Type: kind}, SubagentDepth{Depth: 1}, nil)
				require.NoError(t, err)
				child, _ = roundTripAppPolicy(t, child)
				if independent {
					require.Equal(t, want, child.MCP["crm"])
				} else {
					require.NotContains(t, child.MCP, "crm")
				}
			}
		})
	}
}

func TestDeriveBuiltInBundlesUseAuthoritativeBasePolicy(t *testing.T) {
	t.Parallel()
	for _, name := range []string{toolcatalog.ToolNameWebSearch, toolcatalog.ToolNameSlackPostMessage} {
		for _, independent := range []bool{false, true} {
			for _, enabled := range []bool{false, true} {
				if !independent && !enabled {
					continue // Explicit global disable on an ordinary built-in is an independent base policy.
				}
				t.Run(fmt.Sprintf("%s/independent=%v/enabled=%v", name, independent, enabled), func(t *testing.T) {
					t.Parallel()
					deny := toolpermission.DefaultSelection(toolpermission.ModeAlwaysDeny)
					policy := AgentConfigToolSource{Enabled: &enabled, Permission: &deny}
					resource := slackAppTestResource(t)
					source := appTestSource(nil)
					if independent {
						source.Tools = map[string]AgentConfigToolSource{name: policy}
					} else {
						resource.Tools = map[string]AgentConfigToolSource{name: {Permission: &deny}}
						source.AppResources = map[string]AgentConfigAppResourceSource{"original": resource}
					}
					result, err := compileAppTest(t, source, appTestOptions())
					require.NoError(t, err)
					base, _ := roundTripAppPolicy(t, result.Compiled)
					resources := map[string]AgentConfigAppResourceSource{}
					for _, mode := range []string{toolpermission.ModeAlwaysAllow, toolpermission.ModeAlwaysAsk} {
						bundle := resource
						permission := toolpermission.DefaultSelection(mode)
						bundle.Tools = map[string]AgentConfigToolSource{name: {Permission: &permission, Deferred: true}}
						resources[mode] = bundle
					}
					disabled := false
					inactive := resource
					inactive.Enabled = &disabled
					inactive.Tools = map[string]AgentConfigToolSource{name: {}}
					base, err = DeriveWithAppResources(base,
						map[string]AgentConfigAppResourceSource{"inactive": inactive}, appTestOptions())
					require.NoError(t, err)
					base, _ = roundTripAppPolicy(t, base)
					for _, stage := range []string{"first", "second"} {
						additions := map[string]AgentConfigAppResourceSource{}
						for key, bundle := range resources {
							additions[stage+"_"+key] = bundle
						}
						derived, err := DeriveWithAppResources(base, additions, appTestOptions())
						require.NoError(t, err)
						var contract RuntimeContract
						base, contract = roundTripAppPolicy(t, derived)
						require.Equal(t, enabled, base.Tools[name].Enabled)
						require.Equal(t, toolpermission.ModeAlwaysDeny, base.Tools[name].Permission.Mode)
						require.False(t, base.Tools[name].Deferred)
						require.NotContains(t, base.Tools, toolcatalog.ToolNameToolSearch)
						require.Empty(t, base.AppToolPolicies, "built-in policy stays in its tool carrier")
						exposed := false
						for _, tool := range contract.Tools {
							if tool.Name == name {
								exposed = true
								require.Equal(t, toolpermission.ModeAlwaysDeny, tool.Permission.Mode)
							}
						}
						require.Equal(t, enabled, exposed)
					}
					for _, kind := range []string{SubagentTypeSelf, SubagentTypeProfile} {
						child, err := SubagentCompiledFrom(
							base,
							SubagentCompiled{Type: kind},
							SubagentDepth{Depth: 1},
							nil,
						)
						require.NoError(t, err)
						child, _ = roundTripAppPolicy(t, child)
						if independent && name == toolcatalog.ToolNameWebSearch {
							require.Equal(t, result.Compiled.Tools[name].Permission, child.Tools[name].Permission)
							require.Equal(t, enabled, child.Tools[name].Enabled)
						} else {
							require.NotContains(t, child.Tools, name, "app-only authority is not inherited")
						}
					}
				})
			}
		}
	}
}

func TestAuthoritativeMCPPolicyStillRejectsDefinitionConflicts(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"source", "derive"} {
		for _, field := range []string{"url", "auth"} {
			t.Run(path+"/"+field, func(t *testing.T) {
				t.Parallel()
				deny := toolpermission.DefaultSelection(toolpermission.ModeAlwaysDeny)
				source := appTestSource(nil)
				source.MCP = map[string]AgentConfigMCPSource{
					"crm": {URL: "https://example.com/mcp", Permission: &deny},
				}
				resources := mcpPolicyTestBundles()
				server := resources["second"].MCP["crm"]
				if field == "url" {
					server.URL = "https://other.example.com/mcp"
				} else {
					server.Auth = &AgentConfigMCPAuthSource{
						Type:     MCPAuthTypeBearer,
						SecretID: testMachineSourcePublicID(t, publicid.KindSecret, "other-auth"),
					}
				}
				resources["second"].MCP["crm"] = server
				var err error
				if path == "source" {
					source.AppResources = resources
					_, err = compileAppTest(t, source, appTestOptions())
				} else {
					base, compileErr := compileAppTest(t, source, appTestOptions())
					require.NoError(t, compileErr)
					_, err = DeriveWithAppResources(base.Compiled, resources, appTestOptions())
				}
				require.ErrorContains(t, err, "conflicts with the base MCP server definition")
			})
		}
	}
}

func TestDeriveBundleDefaultsWithoutBaseStillConflict(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"mcp", "builtin"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			base, err := compileAppTest(t, appTestSource(nil), appTestOptions())
			require.NoError(t, err)
			resources := mcpPolicyTestBundles()
			if kind == "builtin" {
				for key, resource := range resources {
					permission := resource.MCP["crm"].Permission
					resource = slackAppTestResource(t)
					resource.Tools = map[string]AgentConfigToolSource{
						toolcatalog.ToolNameWebSearch: {Permission: permission},
					}
					resources[key] = resource
				}
			}
			_, err = DeriveWithAppResources(base.Compiled, resources, appTestOptions())
			require.ErrorContains(t, err, "conflicting app", "the first new bundle cannot become authoritative")
		})
	}
}

func mcpPolicyTestBundles() map[string]AgentConfigAppResourceSource {
	resources := map[string]AgentConfigAppResourceSource{}
	for i, mode := range []string{toolpermission.ModeAlwaysAllow, toolpermission.ModeAlwaysAsk} {
		permission := toolpermission.DefaultSelection(mode)
		enabled := true
		resources[[]string{"first", "second"}[i]] = AgentConfigAppResourceSource{
			Definition: appdefinition.Slack,
			MCP: map[string]AgentConfigMCPSource{"crm": {
				URL: "https://example.com/mcp", Permission: &permission, Deferred: true,
				Tools: map[string]AgentConfigMCPToolSource{"bundle_only": {Enabled: &enabled, Permission: &permission}},
			}},
		}
	}
	return resources
}
