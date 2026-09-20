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
		"listeners: {engineering-team__thread_messages: {}}",
		"interaction_handlers: {engineering-team: {}}",
		`tools:
  app__engineering-team__post_message:
    config: {channel_id: C123}
listeners:
  engineering-team__thread_messages:
    config: {conversations: [{channel_id: C456, thread_ts: "1.2"}]}
interaction_handlers:
  engineering-team: {config: {channel_id: C789}}`,
	} {
		t.Run(extra, func(t *testing.T) {
			opts, apps := appTestOptions(t)
			calls := 0
			resolve := opts.ResolveAppName
			opts.ResolveAppName = func(name string) (AppResolution, error) { calls++; return resolve(name) }
			result := compileAppTest(t, extra, opts)
			require.Equal(t, 1, calls, "resolve each distinct app once")
			require.NotContains(t, string(result.CanonicalJSON), "definition")
			require.NotContains(t, string(result.CanonicalJSON), "app_resources")
			contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, CompilerVersion, result.Hash)
			require.NoError(t, err)
			require.Equal(t, ReferencedAppIDs(result.Compiled), contract.ReferencedAppIDs())
			require.Len(t, contract.ReferencedAppIDs(), 1)
			for _, tool := range contract.Tools {
				require.Empty(t, tool.AppID, "decoding does not prepare app tools")
			}
			prepared, err := PrepareAppCapabilities(result.Compiled, apps)
			require.NoError(t, err)
			require.Empty(t, prepared.Unavailable)
			require.Len(t, prepared.Tools, len(contract.AppTools))
			require.Len(t, prepared.Listeners, len(result.Compiled.Listeners))
			require.Len(t, prepared.InteractionHandlers, len(result.Compiled.InteractionHandlers))
			unavailable, err := PrepareAppCapabilities(result.Compiled, nil)
			require.NoError(t, err)
			require.Empty(t, unavailable.Tools)
			require.Empty(t, unavailable.Listeners)
			require.Empty(t, unavailable.InteractionHandlers)
			require.NotEmpty(t, unavailable.Unavailable)
		})
	}
}

func TestAppSourceValidationAndUnsupportedConfig(t *testing.T) {
	opts, _ := appTestOptions(t)
	for _, extra := range []string{
		"tools: {app__engineering-team__post_message: {type: custom, description: x, input_schema: {type: object}}}",
		"tools: {app__engineering-team__post_message: {type: built_in}}",
		"tools: {app__engineering-team__missing: {}}",
		"tools: {app__missing__read: {}}",
		"tools: {app__engineering-team__read: {config: {resource: x}}}",
		"tools: {app__engineering-team__read: {config: null}}",
		"tools: {app__engineering-team__read: {config: []}}",
		"tools: {app__bad: {}}",
		"tools: {mcp__reserved__read: {}}",
		"tools: {app__" + strings.Repeat("a", 32) + "__" + strings.Repeat("b", 30) + ": {}}",
		"listeners: {engineering-team__unknown: {}}",
		"listeners: {engineering-team__thread_messages: {config: {events: [bogus]}}}",
		"listeners: {engineering-team__thread_messages: {enabled: false}}",
		"interaction_handlers: {engineering-team: {config: {channel_id: invalid}}}",
		"interaction_handlers: {engineering-team__redundant: {}}",
		"app_resources: {}",
		"tools: {web_search: {config: {channel_id: C123}}}",
		"tools: {custom__valid: {type: custom, description: x, input_schema: {type: object}, config: {key: value}}}",
	} {
		_, err := Compile(SourceFormatYAML, []byte(validAgentSource(extra)), opts)
		require.Error(t, err, extra)
	}
	for _, extra := range []string{
		"tools: {web_search: {config: {}}}",
		"tools: {custom__valid: {type: custom, description: x, input_schema: {type: object}, config: {}}}",
	} {
		compileAppTest(t, extra, opts)
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
		[]byte(validAgentSource("listeners: {engineering-team__thread_messages: {}}")),
		opts,
	)
	require.ErrorContains(t, err, "app ID")
}

func TestAppCompiledRejectsForgedStructuralAuthority(t *testing.T) {
	opts, _ := appTestOptions(t)
	result := compileAppTest(t, `tools: {app__engineering-team__read: {}}
listeners: {engineering-team__thread_messages: {}}`, opts)
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
		func(c *Compiled) {
			tool := c.Tools["app__engineering-team__read"]
			tool.Config = []byte(`null`)
			c.Tools["app__engineering-team__read"] = tool
		},
		func(c *Compiled) { tool := c.Tools["app__engineering-team__read"]; c.Tools["web_search"] = tool },
		func(c *Compiled) {
			capability := c.Listeners["engineering-team__thread_messages"]
			capability.AppID = testMachineSourcePublicID(t, publicid.KindProjectApp, "other")
			c.Listeners["engineering-team__thread_messages"] = capability
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
  app__engineering-team__post_message: {enabled: false, permission: {mode: always_deny}, config: {channel_id: C123}}
  ordinary__custom: {type: custom, description: Custom, input_schema: {type: object}}
listeners:
  engineering-team__thread_messages: {}
interaction_handlers:
  engineering-team: {config: {channel_id: C123}}
mcp:
  docs: {url: https://example.com/mcp}
subagents: {worker: {type: self}}`, opts).Compiled
	addition := AppCapabilitiesSource{
		Tools: map[string]AgentConfigToolSource{
			"app__engineering-team__post_message": {Config: map[string]any{"channel_id": "C456"}},
			"app__engineering-team__read":         {},
		},
		Listeners: map[string]AgentConfigAppCapabilitySource{
			"engineering-team__thread_messages": {Config: map[string]any{"malformed": "ignored"}},
		},
		InteractionHandlers: map[string]AgentConfigAppCapabilitySource{
			"engineering-team": {Config: map[string]any{"channel_id": "C456"}},
		},
	}
	derived, err := DeriveWithAppCapabilities(base, addition, opts)
	require.NoError(t, err)
	require.Equal(
		t,
		base.Tools["app__engineering-team__post_message"],
		derived.Tools["app__engineering-team__post_message"],
	)
	require.Equal(t, base.Listeners, derived.Listeners)
	require.Equal(t, base.InteractionHandlers, derived.InteractionHandlers)
	require.Contains(t, derived.Tools, "app__engineering-team__read")
	require.NotContains(t, base.Tools, "app__engineering-team__read")
	child, err := SubagentCompiledFrom(derived, SubagentCompiled{Type: SubagentTypeSelf}, SubagentDepth{}, nil)
	require.NoError(t, err)
	require.Empty(t, child.Listeners)
	require.Empty(t, child.InteractionHandlers)
	for key, tool := range child.Tools {
		require.Empty(t, tool.AppID)
		require.False(t, toolcatalog.UsesAppToolNamespace(key))
	}
	require.Contains(t, child.Tools, "ordinary__custom")
	require.Contains(t, child.MCP, "docs")
	require.Contains(t, child.Tools, toolcatalog.ToolNameListInteractionHandlers)
}

func TestPendingAppToolIdentityConfigAndPermission(t *testing.T) {
	opts, apps := appTestOptions(t)
	name := "app__engineering-team__post_message"
	original := compileAppTest(t, "tools: {"+name+": {config: {channel_id: C123, thread_ts: '1.2'}}}", opts).Compiled
	originalContract := runtimeAppTest(t, original)
	current := original
	current.Instruction = "unrelated edit"
	authority, err := ResolveAppToolAuthority(originalContract, runtimeAppTest(t, current), name, apps)
	require.NoError(t, err)
	require.Equal(t, original.Tools[name].AppID, authority.Tool.AppID)
	for _, mutate := range []func(*ToolCompiled){
		func(tool *ToolCompiled) { tool.AppID = "recreated" },
		func(tool *ToolCompiled) { tool.Config = []byte(`{"channel_id":"C456","thread_ts":"1.2"}`) },
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
	canonical := current
	canonical.Tools = map[string]ToolCompiled{name: current.Tools[name]}
	tool := canonical.Tools[name]
	tool.Config = []byte(`{ "thread_ts": "1.2", "channel_id": "C123" }`)
	canonical.Tools[name] = tool
	_, err = ResolveAppToolAuthority(originalContract, runtimeAppTest(t, canonical), name, apps)
	require.NoError(t, err)
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
		Listeners:           map[string]AgentConfigAppCapabilitySource{"engineering-team__thread_messages": {}},
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
		Type:   "invalid",
		Config: map[string]any{"malformed": make(chan int)},
	}
	source.Listeners["engineering-team__thread_messages"] = AgentConfigAppCapabilitySource{
		Config: map[string]any{"bad": true},
	}
	source.InteractionHandlers["engineering-team"] = AgentConfigAppCapabilitySource{
		Config: map[string]any{"channel_id": "invalid"},
	}
	derived, err := DeriveWithAppCapabilities(base, source, opts)
	require.NoError(t, err)
	require.Equal(t, base, derived)
	require.Equal(t, "invalid", source.Tools["app__engineering-team__read"].Type, "input maps must not be mutated")
}

func TestReferencedAppIDsExcludeDisabledToolsOnly(t *testing.T) {
	apps := map[string]AppResolution{}
	for index, name := range []string{"disabled", "shared", "enabled", "denied", "listener", "handler"} {
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
listeners:
  shared__thread_messages: {}
  listener__thread_messages: {}
interaction_handlers:
  shared: {}
  handler: {}`, opts)
	want := []string{
		apps["shared"].AppID,
		apps["enabled"].AppID,
		apps["denied"].AppID,
		apps["listener"].AppID,
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
	prepared, err := PrepareAppCapabilities(onlyDisabled.Compiled, nil)
	require.NoError(t, err)
	require.Empty(t, prepared.Tools)
	require.Empty(t, prepared.Unavailable, "disabled tools need no live app lookup")
}

func TestDisabledAppReferencesStillRequireConsistentPinnedIdentity(t *testing.T) {
	opts, _ := appTestOptions(t)
	result := compileAppTest(t, `tools:
  app__engineering-team__read: {enabled: false}
listeners:
  engineering-team__thread_messages: {}`, opts)
	tool := result.Compiled.Tools["app__engineering-team__read"]
	tool.AppID = testMachineSourcePublicID(t, publicid.KindProjectApp, "recreated")
	result.Compiled.Tools["app__engineering-team__read"] = tool
	encoded, err := EncodeCompiled(result.Compiled)
	require.NoError(t, err)
	_, err = RuntimeContractFromCompiled(encoded.CanonicalJSON, CompilerVersion, encoded.Hash)
	require.ErrorContains(t, err, "inconsistent pinned IDs")
	_, err = PrepareAppCapabilities(result.Compiled, nil)
	require.ErrorContains(t, err, "inconsistent pinned IDs")
}

func TestAppCompilationRejectsFixedChildWithoutParent(t *testing.T) {
	for _, test := range []struct{ definition, config, parent string }{
		{appdefinition.Slack, `{"thread_ts":"1.2"}`, "channel_id"},
		{appdefinition.GitHub, `{"pull_request":42}`, "repository_id"},
		{appdefinition.Discord, `{"thread_id":"789"}`, "channel_id"},
	} {
		t.Run(test.definition, func(t *testing.T) {
			opts, _ := appTestOptions(t)
			resolve := opts.ResolveAppName
			opts.ResolveAppName = func(name string) (AppResolution, error) {
				app, err := resolve(name)
				app.Definition = test.definition
				return app, err
			}
			definition, ok := appdefinition.Lookup(test.definition)
			require.True(t, ok)
			for _, operation := range definition.Tools {
				name := toolcatalog.AppToolName("engineering-team", operation)
				extra := fmt.Sprintf("tools: {%s: {config: %s}}", name, test.config)
				_, err := Compile(SourceFormatYAML, []byte(validAgentSource(extra)), opts)
				var validation *ValidationError
				require.ErrorAs(t, err, &validation)
				require.Len(t, validation.Issues, 1)
				require.Equal(t, "/tools/"+name+"/config", validation.Issues[0].Path)
				require.ErrorContains(t, err, test.parent)
			}
			if definition.InteractionHandler != nil {
				extra := fmt.Sprintf("interaction_handlers: {engineering-team: {config: %s}}", test.config)
				_, err := Compile(SourceFormatYAML, []byte(validAgentSource(extra)), opts)
				var validation *ValidationError
				require.ErrorAs(t, err, &validation)
				require.Len(t, validation.Issues, 1)
				require.Equal(t, "/interaction_handlers/engineering-team/config", validation.Issues[0].Path)
				require.ErrorContains(t, err, test.parent)
			}
		})
	}
}
