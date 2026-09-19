package agentconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestDeriveAppResourcesPreservesDisabledCustomToolGlobalPolicy(t *testing.T) {
	t.Parallel()
	for _, disabledResource := range []bool{false, true} {
		for _, mode := range []string{
			"disabled", "deferred_only", toolpermission.ModeAlwaysAsk, toolpermission.ModeAlwaysDeny,
		} {
			for _, deferred := range []bool{false, true} {
				name := fmt.Sprintf("resource_disabled=%v/%s/deferred=%v", disabledResource, mode, deferred)
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					disabled := false
					tool := customAppTestTool()
					tool.Deferred = true
					inactive := appPolicyTestResource(t, tool)
					if disabledResource {
						inactive.Enabled = &disabled
					} else {
						tool.Enabled = &disabled
						inactive.Tools["ticket"] = tool
					}
					override := AgentConfigToolSource{Deferred: deferred}
					wantMode := toolpermission.ModeAlwaysAllow
					switch mode {
					case "disabled":
						override.Enabled = &disabled
					case "deferred_only":
					default:
						permission := toolpermission.DefaultSelection(mode)
						override.Permission = &permission
						wantMode = mode
					}
					source := appTestSource(map[string]AgentConfigAppResourceSource{"inactive": inactive})
					source.Tools = map[string]AgentConfigToolSource{"ticket": override}
					result, err := compileAppTest(t, source, appTestOptions())
					require.NoError(t, err)
					base, contract := roundTripAppPolicy(t, result.Compiled)
					require.NotContains(t, base.Tools, "ticket")
					require.Contains(t, base.AppToolPolicies, "ticket")
					require.NotContains(t, contract.configuredTools, "ticket")
					require.Empty(t, contract.Tools)
					requireAppPolicySubagentsStripped(t, base)

					// Neither unrelated nor inactive attachments consume the override.
					for _, step := range []struct {
						key      string
						resource AgentConfigAppResourceSource
					}{
						{"unrelated", AgentConfigAppResourceSource{Definition: appdefinition.Slack}},
						{"still_inactive", inactive},
					} {
						before, err := EncodeCompiled(base)
						require.NoError(t, err)
						derived, err := DeriveWithAppResources(base,
							map[string]AgentConfigAppResourceSource{step.key: step.resource}, appTestOptions())
						require.NoError(t, err)
						after, err := EncodeCompiled(base)
						require.NoError(t, err)
						require.Equal(t, before.CanonicalJSON, after.CanonicalJSON, "must not mutate the pinned base")
						base, contract = roundTripAppPolicy(t, derived)
						require.Equal(t, result.Compiled.AppToolPolicies, base.AppToolPolicies)
						require.Empty(t, contract.Tools)
					}

					// Two bundles share the override both before and after consumption.
					for _, stage := range []string{"first", "second"} {
						first, second := customAppTestTool(), customAppTestTool()
						first.Deferred, second.Deferred = true, true
						if override.Permission != nil {
							ask := toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)
							second.Permission = &ask
						}
						derived, err := DeriveWithAppResources(base, map[string]AgentConfigAppResourceSource{
							stage + "_one": appPolicyTestResource(
								t,
								first,
							),
							stage + "_two": appPolicyTestResource(t, second),
						}, appTestOptions())
						require.NoError(t, err)
						base, contract = roundTripAppPolicy(t, derived)
						require.Empty(t, base.AppToolPolicies)
						entry := base.Tools["ticket"]
						require.Equal(t, mode != "disabled", entry.Enabled)
						require.Equal(t, wantMode, entry.Permission.Mode)
						require.Equal(t, deferred, entry.Deferred)
						require.False(t, entry.AppOrigin.Base)
						found := false
						for _, runtimeTool := range contract.Tools {
							if runtimeTool.Name == "ticket" {
								found = true
								// Deny/ask remain ordinary runtime policy; explicit disable hides the tool.
								require.Equal(t, wantMode, runtimeTool.Permission.Mode)
								require.Equal(t, deferred, runtimeTool.Deferred)
							}
						}
						require.Equal(t, mode != "disabled", found)
						requireAppPolicySubagentsStripped(t, base)
					}
				})
			}
		}
	}
}

func TestRetainedAppToolPolicyDoesNotResolveDefinitionConflicts(t *testing.T) {
	t.Parallel()
	disabled := false
	inactive := appPolicyTestResource(t, customAppTestTool())
	inactive.Enabled = &disabled
	source := appTestSource(map[string]AgentConfigAppResourceSource{"inactive": inactive})
	deny := toolpermission.DefaultSelection(toolpermission.ModeAlwaysDeny)
	source.Tools = map[string]AgentConfigToolSource{"ticket": {Permission: &deny}}
	result, err := compileAppTest(t, source, appTestOptions())
	require.NoError(t, err)
	base, _ := roundTripAppPolicy(t, result.Compiled)
	for _, field := range []string{"description", "schema"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			first, second := customAppTestTool(), customAppTestTool()
			if field == "description" {
				second.Description = "Another definition"
			} else {
				second.InputSchema["required"] = []any{}
			}
			_, err := DeriveWithAppResources(base, map[string]AgentConfigAppResourceSource{
				"one": appPolicyTestResource(t, first), "two": appPolicyTestResource(t, second),
			}, appTestOptions())
			require.ErrorContains(t, err, "conflict")
		})
	}
}

func TestCompiledAppToolPoliciesValidateClosedContract(t *testing.T) {
	t.Parallel()
	base, err := compileAppTest(t, appTestSource(nil), appTestOptions())
	require.NoError(t, err)
	for _, test := range []struct {
		name, key, policy, want string
	}{
		{"invalid_name", "bad-name", `{}`, "invalid or reserved name"},
		{"empty_name", "", `{}`, "invalid or reserved name"},
		{"long_name", strings.Repeat("a", 65), `{}`, "invalid or reserved name"},
		{"mcp_name", "mcp__crm__ticket", `{}`, "invalid or reserved name"},
		{"wire_name", toolcatalog.ToolNameCallDeferredTool, `{}`, "invalid or reserved name"},
		{"builtin_name", toolcatalog.ToolNameSlackRead, `{}`, "collides with a built-in tool"},
		{"unknown_field", "ticket", `{"connection":"no-authority"}`, "unknown field"},
		{"definition", "ticket", `{"type":"custom"}`, "unknown field"},
		{"unknown_permission_field", "ticket", `{"permission":{"mode":"always_deny","unknown":true}}`, "unknown field"},
		{"invalid_mode", "ticket", `{"permission":{"mode":"invented"}}`, "unsupported permission mode"},
		{
			"invalid_parameters", "ticket",
			`{"permission":{"mode":"always_deny","parameters":{"extra":true}}}`, "parameters",
		},
		{"invalid_enabled", "ticket", `{"enabled":"false"}`, "cannot unmarshal"},
		{"invalid_deferred", "ticket", `{"deferred":1}`, "cannot unmarshal"},
		{"invalid_shape", "ticket", `[]`, "cannot unmarshal"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var document map[string]any
			require.NoError(t, json.Unmarshal(base.CanonicalJSON, &document))
			document["app_tool_policies"] = map[string]json.RawMessage{test.key: json.RawMessage(test.policy)}
			raw, err := json.Marshal(document)
			require.NoError(t, err)
			sum := sha256.Sum256(canonicalizeJSON(raw))
			_, err = RuntimeContractFromCompiled(raw, CompilerVersion, hex.EncodeToString(sum[:]))
			require.ErrorContains(t, err, test.want)
		})
	}
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("overlap_enabled=%v", enabled), func(t *testing.T) {
			t.Parallel()
			source := appTestSource(nil)
			tool := customAppTestTool()
			tool.Enabled = &enabled
			source.Tools = map[string]AgentConfigToolSource{"ticket": tool}
			result, err := compileAppTest(t, source, appTestOptions())
			require.NoError(t, err)
			result.Compiled.AppToolPolicies = map[string]AppToolPolicyCompiled{"ticket": {Enabled: &enabled}}
			encoded, err := EncodeCompiled(result.Compiled)
			require.NoError(t, err)
			_, err = RuntimeContractFromCompiled(encoded.CanonicalJSON, CompilerVersion, encoded.Hash)
			require.ErrorContains(t, err, "already has a compiled tool carrier")
		})
	}
	_, err = Compile(SourceFormatJSON, []byte(`{"instruction":"Help",`+
		`"model":{"provider_config":"test","name":"model"},"app_tool_policies":{"ticket":{}}}`), appTestOptions())
	require.ErrorContains(t, err, "unknown field", "retention is compiled-only, never new authoring syntax")
}

func appPolicyTestResource(t *testing.T, tool AgentConfigToolSource) AgentConfigAppResourceSource {
	t.Helper()
	resource := slackAppTestResource(t)
	resource.Tools = map[string]AgentConfigToolSource{"ticket": tool}
	return resource
}

func roundTripAppPolicy(t *testing.T, compiled Compiled) (Compiled, RuntimeContract) {
	t.Helper()
	encoded, err := EncodeCompiled(compiled)
	require.NoError(t, err)
	contract, err := RuntimeContractFromCompiled(encoded.CanonicalJSON, CompilerVersion, encoded.Hash)
	require.NoError(t, err)
	var reloaded Compiled
	require.NoError(t, json.Unmarshal(encoded.CanonicalJSON, &reloaded))
	again, err := EncodeCompiled(reloaded)
	require.NoError(t, err)
	require.Equal(t, encoded.CanonicalJSON, again.CanonicalJSON)
	require.Equal(t, encoded.Hash, again.Hash)
	return reloaded, contract
}

func requireAppPolicySubagentsStripped(t *testing.T, base Compiled) {
	t.Helper()
	for _, kind := range []string{SubagentTypeSelf, SubagentTypeProfile} {
		child, err := SubagentCompiledFrom(base, SubagentCompiled{Type: kind}, SubagentDepth{Depth: 1}, nil)
		require.NoError(t, err)
		require.Empty(t, child.AppToolPolicies)
		require.Empty(t, child.AppResources)
		require.NotContains(t, child.Tools, "ticket")
		child, contract := roundTripAppPolicy(t, child)
		require.NotContains(t, contract.configuredTools, "ticket")
		// Explicit new child authority receives its own defaults, not the parent's policy.
		derived, err := DeriveWithAppResources(child,
			map[string]AgentConfigAppResourceSource{"child": appPolicyTestResource(t, customAppTestTool())},
			appTestOptions())
		require.NoError(t, err)
		require.True(t, derived.Tools["ticket"].Enabled)
		require.Equal(t, toolpermission.ModeAlwaysAllow, derived.Tools["ticket"].Permission.Mode)
		require.False(t, derived.Tools["ticket"].Deferred)
	}
}
