package agentconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func appTestOptions(t *testing.T) (CompileOptions, map[string]AppResolution) {
	t.Helper()
	id, err := publicid.Encode(publicid.KindProjectApp, publicidTestID(120))
	require.NoError(t, err)
	app := AppResolution{AppID: id, Definition: appdefinition.Slack}
	return CompileOptions{ResolveAppName: func(name string) (AppResolution, error) {
		if name != "engineering-team" {
			return AppResolution{}, fmt.Errorf("app %s is unavailable", name)
		}
		return app, nil
	}}, map[string]AppResolution{id: app}
}
func compileAppTest(t *testing.T, extra string, opts CompileOptions) Result {
	t.Helper()
	result, err := Compile(SourceFormatYAML, []byte(validAgentSource(extra)), opts)
	require.NoError(t, err)
	return result
}

func TestAppCapabilitiesCompileAndPrepareIndependently(t *testing.T) {
	for _, extra := range []string{
		"tools: {app__engineering-team__post_message: {}}",
		"interaction_handlers: {engineering-team: {}}",
		`tools:
  app__engineering-team__post_message: {}
interaction_handlers:
  engineering-team: {}`,
	} {
		t.Run(extra, func(t *testing.T) {
			opts, apps := appTestOptions(t)
			calls := 0
			resolve := opts.ResolveAppName
			opts.ResolveAppName = func(name string) (AppResolution, error) { calls++; return resolve(name) }
			result := compileAppTest(t, extra, opts)
			require.Equal(t, 1, calls, "resolve each distinct app once")
			contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, CompilerVersion, result.Hash)
			require.NoError(t, err)
			require.Equal(t, ReferencedAppIDs(result.Compiled), contract.ReferencedAppIDs())
			require.Len(t, contract.ReferencedAppIDs(), 1)
			for _, tool := range contract.Tools {
				require.False(t, toolcatalog.UsesAppToolNamespace(tool.Name), "decoding does not prepare app tools")
			}
			prepared, err := PrepareAppTools(result.Compiled, apps)
			require.NoError(t, err)
			require.Len(t, prepared, len(contract.AppTools))
			unavailable, err := PrepareAppTools(result.Compiled, nil)
			require.NoError(t, err)
			require.Empty(t, unavailable)
		})
	}
}

func TestAppSourceValidation(t *testing.T) {
	opts, _ := appTestOptions(t)
	for _, extra := range []string{
		"tools: {app__engineering-team__post_message: {type: custom, description: x, input_schema: {type: object}}}",
		"tools: {app__engineering-team__post_message: {type: built_in}}",
		"tools: {app__engineering-team__missing: {}}",
		"tools: {app__missing__read: {}}",
		"tools: {app__bad: {}}",
		"tools: {mcp__reserved__read: {}}",
		"tools: {app__" + strings.Repeat("a", 32) + "__" + strings.Repeat("b", 30) + ": {}}",
		"interaction_handlers: {engineering-team__redundant: {}}",
		"interaction_handlers: {engineering-team: null}",
	} {
		_, err := Compile(SourceFormatYAML, []byte(validAgentSource(extra)), opts)
		require.Error(t, err, extra)
	}
	_, err := Compile(
		SourceFormatYAML,
		[]byte(validAgentSource("tools: {app__engineering-team__read: {}}")),
		CompileOptions{},
	)
	require.ErrorContains(t, err, "ResolveAppName")
	opts.ResolveAppName = func(string) (AppResolution, error) {
		return AppResolution{AppID: "invalid", Definition: appdefinition.Slack}, nil
	}
	_, err = Compile(
		SourceFormatYAML,
		[]byte(validAgentSource("interaction_handlers: {engineering-team: {}}")),
		opts,
	)
	require.ErrorContains(t, err, "app ID")
}

func TestAppCompiledRejectsForgedStructuralAuthority(t *testing.T) {
	opts, _ := appTestOptions(t)
	result := compileAppTest(t, `tools: {app__engineering-team__read: {}}
interaction_handlers: {engineering-team: {}}`, opts)
	for _, mutate := range []func(*Compiled){
		func(c *Compiled) {
			tool := c.Tools["app__engineering-team__read"]
			tool.AppID = ""
			c.Tools["app__engineering-team__read"] = tool
		},
		func(c *Compiled) {
			tool := c.Tools["app__engineering-team__read"]
			tool.Type = "custom"
			c.Tools["app__engineering-team__read"] = tool
		},
		func(c *Compiled) { tool := c.Tools["app__engineering-team__read"]; c.Tools["web_search"] = tool },
		func(c *Compiled) {
			capability := c.InteractionHandlers["engineering-team"]
			capability.AppID = testMachineSourcePublicID(t, publicid.KindProjectApp, "other")
			c.InteractionHandlers["engineering-team"] = capability
		},
	} {
		var compiled Compiled
		require.NoError(t, json.Unmarshal(result.CanonicalJSON, &compiled))
		mutate(&compiled)
		encoded, err := EncodeCompiled(compiled)
		require.NoError(t, err)
		_, err = RuntimeContractFromCompiled(encoded.CanonicalJSON, CompilerVersion, encoded.Hash)
		require.Error(t, err)
	}
}

func TestCompositionPreservesExistingEntriesAndSubagentsStripApps(t *testing.T) {
	opts, _ := appTestOptions(t)
	base := compileAppTest(t, `tools:
  app__engineering-team__post_message: {enabled: false, permission: {mode: always_deny}}
  ordinary__custom: {type: custom, description: Custom, input_schema: {type: object}}
interaction_handlers:
  engineering-team: {}
mcp:
  docs: {url: https://example.com/mcp}
subagents: {worker: {type: self}}`, opts).Compiled
	addition := AppCapabilitiesSource{
		Tools: map[string]AgentConfigToolSource{
			"app__engineering-team__post_message": {},
			"app__engineering-team__read":         {},
		},
		InteractionHandlers: map[string]AgentConfigAppCapabilitySource{
			"engineering-team": {},
		},
	}
	derived, err := DeriveWithAppCapabilities(base, addition, opts)
	require.NoError(t, err)
	require.Equal(
		t,
		base.Tools["app__engineering-team__post_message"],
		derived.Tools["app__engineering-team__post_message"],
	)
	require.Equal(t, base.InteractionHandlers, derived.InteractionHandlers)
	require.Contains(t, derived.Tools, "app__engineering-team__read")
	require.NotContains(t, base.Tools, "app__engineering-team__read")
	child, err := SubagentCompiledFrom(derived, SubagentCompiled{Type: SubagentTypeSelf}, SubagentDepth{}, nil)
	require.NoError(t, err)
	require.Empty(t, child.InteractionHandlers)
	for key, tool := range child.Tools {
		require.Empty(t, tool.AppID)
		require.False(t, toolcatalog.UsesAppToolNamespace(key))
	}
	require.Contains(t, child.Tools, "ordinary__custom")
	require.Contains(t, child.MCP, "docs")
	require.Contains(t, child.Tools, toolcatalog.ToolNameListInteractionHandlers)
}

func TestPendingAppToolIdentityAndPermission(t *testing.T) {
	opts, apps := appTestOptions(t)
	name := "app__engineering-team__post_message"
	original := compileAppTest(t, "tools: {"+name+": {}}", opts).Compiled
	originalContract := runtimeAppTest(t, original)
	current := original
	current.Instruction = "unrelated edit"
	authority, err := ResolveAppToolAuthority(originalContract, runtimeAppTest(t, current), name, apps)
	require.NoError(t, err)
	require.Equal(t, original.Tools[name].AppID, authority.Tool.AppID)
	for _, mutate := range []func(*ToolCompiled){
		func(tool *ToolCompiled) { tool.AppID = "recreated" },
		func(tool *ToolCompiled) {
			tool.Permission = toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)
		},
		func(tool *ToolCompiled) { tool.Enabled = false },
	} {
		changed := current
		changed.Tools = map[string]ToolCompiled{name: current.Tools[name]}
		tool := changed.Tools[name]
		mutate(&tool)
		changed.Tools[name] = tool
		_, err := ResolveAppToolAuthority(originalContract, RuntimeContract{AppTools: changed.Tools}, name, apps)
		require.Error(t, err)
	}
	_, err = ResolveAppToolAuthority(originalContract, runtimeAppTest(t, current), name, nil)
	require.Error(t, err)
}

func TestInteractionDefaultsAreCompileTimeAndRespectModelAndOverrides(t *testing.T) {
	for _, supports := range []bool{true, false} {
		opts := CompileOptions{ResolveModelSelection: func(string, string) (ResolvedModelSelection, error) {
			return ResolvedModelSelection{SupportsTools: &supports}, nil
		}}
		result := compileAppTest(t, "", opts)
		for _, name := range toolcatalog.InteractionHandlerToolNames() {
			_, ok := result.Compiled.Tools[name]
			require.Equal(t, supports, ok)
		}
		disabled := compileAppTest(
			t,
			"tools: {list_interaction_handlers: {enabled: false, permission: {mode: always_deny}}, set_interaction_handler: {enabled: false}}",
			opts,
		)
		require.False(t, disabled.Compiled.Tools[toolcatalog.ToolNameListInteractionHandlers].Enabled)
		require.Equal(
			t,
			toolpermission.ModeAlwaysDeny,
			disabled.Compiled.Tools[toolcatalog.ToolNameListInteractionHandlers].Permission.Mode,
		)
		_, err := Compile(SourceFormatYAML, []byte(validAgentSource("tools: {list_interaction_handlers: {}}")), opts)
		if supports {
			require.NoError(t, err)
		} else {
			require.ErrorContains(t, err, "does not support tools")
		}
	}
	encoded, err := EncodeCompiled(Compiled{})
	require.NoError(t, err)
	contract, err := RuntimeContractFromCompiled(encoded.CanonicalJSON, CompilerVersion, encoded.Hash)
	require.NoError(t, err)
	require.Empty(t, contract.Tools)
	require.False(t, contract.RequiresModelToolSupport())
}

func TestAppResolverErrorsRetainSourcePath(t *testing.T) {
	opts := CompileOptions{
		ResolveAppName: func(string) (AppResolution, error) { return AppResolution{}, errors.New("disconnected") },
	}
	_, err := Compile(SourceFormatYAML, []byte(validAgentSource("tools: {app__engineering__read: {}}")), opts)
	require.ErrorContains(t, err, "disconnected")
	require.ErrorContains(t, err, "app__engineering__read")
}

func runtimeAppTest(t *testing.T, compiled Compiled) RuntimeContract {
	t.Helper()
	encoded, err := EncodeCompiled(compiled)
	require.NoError(t, err)
	contract, err := RuntimeContractFromCompiled(encoded.CanonicalJSON, CompilerVersion, encoded.Hash)
	require.NoError(t, err)
	return contract
}

func TestCompositionDoesNotResolveExistingOrNonAppMetadata(t *testing.T) {
	opts, _ := appTestOptions(t)
	source := AppCapabilitiesSource{
		Tools:               map[string]AgentConfigToolSource{"app__engineering-team__read": {}},
		InteractionHandlers: map[string]AgentConfigAppCapabilitySource{"engineering-team": {}},
	}
	base, err := CompileAppCapabilitiesSource(source, opts)
	require.NoError(t, err)
	require.Len(t, base.Tools, 1)
	opts.ResolveAppName = func(string) (AppResolution, error) {
		t.Fatal("existing app should not be resolved")
		return AppResolution{}, nil
	}
	opts.ResolveModelSelection = func(string, string) (ResolvedModelSelection, error) {
		t.Fatal("model should not be resolved")
		return ResolvedModelSelection{}, nil
	}
	source.Tools["app__engineering-team__read"] = AgentConfigToolSource{
		Type: "invalid",
	}
	source.InteractionHandlers["engineering-team"] = AgentConfigAppCapabilitySource{}
	derived, err := DeriveWithAppCapabilities(base, source, opts)
	require.NoError(t, err)
	require.Equal(t, base, derived)
	require.Equal(t, "invalid", source.Tools["app__engineering-team__read"].Type, "input maps must not be mutated")
}

func TestReferencedAppIDsExcludeDisabledToolsOnly(t *testing.T) {
	apps := map[string]AppResolution{}
	for index, name := range []string{"disabled", "shared", "enabled", "denied", "handler"} {
		id, err := publicid.Encode(publicid.KindProjectApp, publicidTestID(130+index))
		require.NoError(t, err)
		apps[name] = AppResolution{AppID: id, Definition: appdefinition.Slack}
	}
	opts := CompileOptions{ResolveAppName: func(name string) (AppResolution, error) {
		app, ok := apps[name]
		require.True(t, ok)
		return app, nil
	}}
	result := compileAppTest(t, `tools:
  app__disabled__read: {enabled: false}
  app__shared__read: {enabled: false}
  app__enabled__read: {}
  app__enabled__post_message: {}
  app__denied__read: {permission: {mode: always_deny}}
interaction_handlers:
  shared: {}
  handler: {}`, opts)
	want := []string{
		apps["shared"].AppID,
		apps["enabled"].AppID,
		apps["denied"].AppID,
		apps["handler"].AppID,
	}
	slices.Sort(want)
	require.Equal(t, want, ReferencedAppIDs(result.Compiled))
	contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, CompilerVersion, result.Hash)
	require.NoError(t, err)
	require.Equal(t, want, contract.ReferencedAppIDs())
	require.Contains(t, contract.AppTools, "app__disabled__read", "disabled entries remain pinned in the stored contract")

	onlyDisabled := compileAppTest(t, "tools: {app__disabled__read: {enabled: false}}", opts)
	require.Empty(t, ReferencedAppIDs(onlyDisabled.Compiled))
	prepared, err := PrepareAppTools(onlyDisabled.Compiled, nil)
	require.NoError(t, err)
	require.Empty(t, prepared)
}

func TestDisabledAppReferencesStillRequireConsistentPinnedIdentity(t *testing.T) {
	opts, _ := appTestOptions(t)
	result := compileAppTest(t, `tools:
  app__engineering-team__read: {enabled: false}
interaction_handlers:
  engineering-team: {}`, opts)
	tool := result.Compiled.Tools["app__engineering-team__read"]
	tool.AppID = testMachineSourcePublicID(t, publicid.KindProjectApp, "recreated")
	result.Compiled.Tools["app__engineering-team__read"] = tool
	encoded, err := EncodeCompiled(result.Compiled)
	require.NoError(t, err)
	_, err = RuntimeContractFromCompiled(encoded.CanonicalJSON, CompilerVersion, encoded.Hash)
	require.ErrorContains(t, err, "inconsistent pinned IDs")
	_, err = PrepareAppTools(result.Compiled, nil)
	require.ErrorContains(t, err, "inconsistent pinned IDs")
}
