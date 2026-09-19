package agentconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func appTestOptions() CompileOptions {
	return CompileOptions{ResolveAppConnection: func(id, _ string) (string, error) { return id, nil }}
}

func appTestSource(resources map[string]AgentConfigAppResourceSource) AgentConfigSource {
	return AgentConfigSource{
		Instruction:  "Help.",
		Model:        AgentConfigModelSource{ProviderConfig: "test", Name: "model"},
		AppResources: resources,
	}
}

func compileAppTest(t *testing.T, source AgentConfigSource, opts CompileOptions) (Result, error) {
	t.Helper()
	raw, err := json.Marshal(source)
	require.NoError(t, err)
	return Compile(SourceFormatJSON, raw, opts)
}

func slackAppTestResource(t *testing.T) AgentConfigAppResourceSource {
	t.Helper()
	return AgentConfigAppResourceSource{
		Definition: appdefinition.Slack,
		Connection: testMachineSourcePublicID(t, publicid.KindIntegrationConnection, "slack-one"),
		Scope:      &appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "111.222"}},
	}
}

func customAppTestTool() AgentConfigToolSource {
	return AgentConfigToolSource{
		Type:        toolcatalog.ToolTypeCustom,
		Description: "Look up a ticket.",
		InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{"ticket": map[string]any{"type": "string"}},
			"required": []any{"ticket"}, "additionalProperties": false,
		},
	}
}

func TestAppCapabilitiesAreIndependent(t *testing.T) {
	for mask := range 16 {
		t.Run(fmt.Sprint(mask), func(t *testing.T) {
			resource := slackAppTestResource(t)
			if mask&1 != 0 {
				resource.Tools = map[string]AgentConfigToolSource{toolcatalog.ToolNameSlackPostMessage: {}}
			}
			if mask&2 != 0 {
				resource.Listener = &appdefinition.Listener{Events: []string{"message"}}
			}
			if mask&4 != 0 {
				resource.InteractionHandler = &appdefinition.InteractionHandler{
					Definition: appdefinition.SlackInteractions,
				}
			}
			if mask&8 != 0 {
				resource.Follow = &appdefinition.Follow{Replies: true}
			}
			result, err := compileAppTest(
				t,
				appTestSource(map[string]AgentConfigAppResourceSource{"chat": resource}),
				appTestOptions(),
			)
			require.NoError(t, err)
			contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, CompilerVersion, result.Hash)
			require.NoError(t, err)
			got := contract.AppResources["chat"]
			require.Equal(t, mask&2 != 0, got.Listener != nil)
			require.Equal(t, mask&4 != 0, got.InteractionHandler != nil)
			require.Equal(t, mask&8 != 0, got.Follow != nil)
			if mask&1 != 0 {
				require.Equal(t, []string{toolcatalog.ToolNameSlackPostMessage}, got.Tools)
			}
			switch {
			case mask&1 != 0 && mask&4 != 0:
				require.Len(t, contract.Tools, 5) // send, interaction selection, retrieval defaults
			case mask&4 != 0:
				require.Len(t, contract.Tools, 4)
			case mask&1 != 0:
				require.Len(t, contract.Tools, 3)
			default:
				require.Empty(t, contract.Tools)
			}
		})
	}
	resource := appPolicyTestResource(t, customAppTestTool())
	resource.MCP = map[string]AgentConfigMCPSource{"crm": {URL: "https://example.com/mcp"}}
	result, err := compileAppTest(
		t,
		appTestSource(map[string]AgentConfigAppResourceSource{"support": resource}),
		appTestOptions(),
	)
	require.NoError(t, err)
	require.Equal(t, resource.Connection, result.Compiled.AppResources["support"].ConnectionID)
	require.Equal(t, resource.Scope, result.Compiled.AppResources["support"].Scope)
	require.Empty(t, result.Compiled.AppResources["support"].Listener)
	require.Empty(t, result.Compiled.AppResources["support"].Follow)
	require.Empty(t, result.Compiled.AppResources["support"].InteractionHandler)
	require.Equal(t, toolcatalog.ToolTypeCustom, result.Compiled.Tools["ticket"].Type)
	require.Contains(t, result.Compiled.MCP, "crm")
}

func TestAppInstanceSelectsBundlesWithoutCopyingDefinitions(t *testing.T) {
	appID := testMachineSourcePublicID(t, publicid.KindProjectApp, "support")
	defaults := slackAppTestResource(t)
	defaults.Tools = map[string]AgentConfigToolSource{
		"ticket":                             customAppTestTool(),
		toolcatalog.ToolNameSlackPostMessage: {},
	}
	deferred := true
	defaults.MCP = map[string]AgentConfigMCPSource{
		"crm": {
			URL:   "https://example.com/mcp",
			Tools: map[string]AgentConfigMCPToolSource{"lookup": {Deferred: &deferred}},
		},
	}
	defaults.Listener = &appdefinition.Listener{Events: []string{"message"}}
	defaults.InteractionHandler = &appdefinition.InteractionHandler{Definition: appdefinition.SlackInteractions}
	opts := appTestOptions()
	opts.ResolveAppInstance = func(id string) (AppInstanceResolution, error) {
		require.Equal(t, appID, id)
		return AppInstanceResolution{AppInstanceID: id, Resource: defaults}, nil
	}
	source := appTestSource(map[string]AgentConfigAppResourceSource{"support": {AppInstance: appID}})
	empty, err := compileAppTest(t, source, opts)
	require.NoError(t, err)
	require.Empty(t, empty.Compiled.Tools)
	require.Empty(t, empty.Compiled.MCP)
	require.Nil(t, empty.Compiled.AppResources["support"].Listener)
	require.Nil(t, empty.Compiled.AppResources["support"].InteractionHandler)
	ask := toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)
	source.AppResources["support"] = AgentConfigAppResourceSource{
		AppInstance: appID,
		Scope:       &appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C456"}},
		Tools:       map[string]AgentConfigToolSource{"ticket": {Permission: &ask}},
		MCP:         map[string]AgentConfigMCPSource{"crm": {}},
	}
	result, err := compileAppTest(t, source, opts)
	require.NoError(t, err)
	require.Equal(t, "C456", result.Compiled.AppResources["support"].Scope.Slack.ChannelID)
	require.Empty(t, result.Compiled.AppResources["support"].Scope.Slack.ThreadTS)
	require.Equal(t, appID, result.Compiled.AppResources["support"].AppInstanceID)
	require.Equal(t, ask, result.Compiled.Tools["ticket"].Permission)
	require.Contains(t, result.Compiled.Tools, toolcatalog.ToolNameToolSearch)
	require.NotContains(t, result.Compiled.Tools, toolcatalog.ToolNameSlackPostMessage)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(result.Compiled.Tools["ticket"].InputSchema, &schema))
	require.Equal(t, customAppTestTool().InputSchema, schema) // no injected selector
	resolved, err := ToolsFromSourceWithOptions(SourceFormatJSON, []byte(result.Source), opts)
	require.NoError(t, err)
	require.Len(t, resolved, len(result.Compiled.Tools))
	disabled := false
	defaults.Enabled = &disabled
	disabledResult, err := compileAppTest(t, source, opts)
	require.NoError(t, err)
	require.False(t, disabledResult.Compiled.AppResources["support"].Enabled)
	require.Empty(t, disabledResult.Compiled.Tools)
	require.Empty(t, disabledResult.Compiled.MCP)
	defaults.Enabled = nil
	defaults.Scope.Slack.ChannelID = "CHANGED"
	deferred = false
	changed := defaults.Tools["ticket"]
	changed.InputSchema["type"] = "string"
	defaults.Tools["ticket"] = changed
	opts.ResolveAppInstance = func(string) (AppInstanceResolution, error) {
		return AppInstanceResolution{}, errors.New("instance deleted")
	}
	encoded, err := EncodeCompiled(result.Compiled)
	require.NoError(t, err)
	require.Equal(t, result.Hash, encoded.Hash)
	_, err = RuntimeContractFromCompiled(result.CanonicalJSON, CompilerVersion, result.Hash)
	require.NoError(t, err) // immutable snapshots never re-resolve the instance
	_, err = compileAppTest(t, source, opts)
	require.ErrorContains(t, err, "instance deleted")
}

func TestAppResourceSelectorsAndGlobalPolicy(t *testing.T) {
	for _, policy := range []string{"default", "disabled", toolpermission.ModeAlwaysAsk, toolpermission.ModeAlwaysDeny} {
		t.Run(policy, func(t *testing.T) {
			one, two := slackAppTestResource(t), slackAppTestResource(t)
			one.Tools = map[string]AgentConfigToolSource{toolcatalog.ToolNameSlackPostMessage: {}}
			two.Tools = map[string]AgentConfigToolSource{toolcatalog.ToolNameSlackPostMessage: {}}
			two.Connection = testMachineSourcePublicID(t, publicid.KindIntegrationConnection, "slack-two")
			source := appTestSource(map[string]AgentConfigAppResourceSource{"one": one, "two": two})
			if policy != "default" {
				override := AgentConfigToolSource{}
				if policy == "disabled" {
					enabled := false
					override.Enabled = &enabled
				} else {
					permission := toolpermission.DefaultSelection(policy)
					override.Permission = &permission
				}
				source.Tools = map[string]AgentConfigToolSource{toolcatalog.ToolNameSlackPostMessage: override}
			}
			result, err := compileAppTest(t, source, appTestOptions())
			require.NoError(t, err)
			contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, CompilerVersion, result.Hash)
			require.NoError(t, err)
			tool := result.Compiled.Tools[toolcatalog.ToolNameSlackPostMessage]
			require.Equal(t, []string{"one", "two"}, tool.AppOrigin.ResourceKeys)
			require.False(t, tool.AppOrigin.Base)
			if policy == "disabled" {
				require.False(t, tool.Enabled)
				require.Empty(t, contract.Tools)
				return
			}
			for _, runtime := range contract.Tools {
				if runtime.Name != toolcatalog.ToolNameSlackPostMessage {
					continue
				}
				if policy != "default" {
					require.Equal(t, policy, runtime.Permission.Mode)
				}
				validator, err := jsonschema.Compile(runtime.InputSchema)
				require.NoError(t, err)
				require.Error(t, validator.Validate([]byte(`{"text":"hello"}`)))
				require.Error(t, validator.Validate([]byte(`{"text":"hello","resource":"invented"}`)))
				require.NoError(t, validator.Validate([]byte(`{"text":"hello","resource":"two"}`)))
			}
			enabled := false
			two.Enabled = &enabled
			source.AppResources["two"] = two
			result, err = compileAppTest(t, source, appTestOptions())
			require.NoError(t, err)
			contract, err = RuntimeContractFromCompiled(result.CanonicalJSON, CompilerVersion, result.Hash)
			require.NoError(t, err)
			for _, runtime := range contract.Tools {
				if runtime.Name != toolcatalog.ToolNameSlackPostMessage {
					continue
				}
				validator, err := jsonschema.Compile(runtime.InputSchema)
				require.NoError(t, err)
				require.NoError(t, validator.Validate([]byte(`{"text":"hello"}`)))
				require.Error(t, validator.Validate([]byte(`{"text":"hello","resource":"two"}`)))
			}
		})
	}
}

func TestProviderPolicyWithoutResourcesAndHistoricalDecode(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		source := appTestSource(nil)
		source.Tools = map[string]AgentConfigToolSource{toolcatalog.ToolNameSlackPostMessage: {Enabled: &enabled}}
		opts := CompileOptions{ResolveModelSelection: func(string, string) (ResolvedModelSelection, error) {
			no := false
			return ResolvedModelSelection{SupportsTools: &no}, nil
		}}
		result, err := compileAppTest(t, source, opts)
		require.NoError(t, err)
		require.Equal(t, enabled, result.Compiled.Tools[toolcatalog.ToolNameSlackPostMessage].Enabled)
		require.Nil(t, result.Compiled.Tools[toolcatalog.ToolNameSlackPostMessage].AppOrigin)
		require.Len(t, result.Compiled.Tools, 1)
		contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, CompilerVersion, result.Hash)
		require.NoError(t, err)
		require.Empty(t, contract.Tools)
		contract, err = contract.WithImplicitBuiltInTool(toolcatalog.ToolNameSlackPostMessage)
		require.NoError(t, err)
		require.Empty(t, contract.Tools)
	}
	collision := appTestSource(nil)
	collision.Tools = map[string]AgentConfigToolSource{toolcatalog.ToolNameSlackPostMessage: customAppTestTool()}
	_, err := compileAppTest(t, collision, CompileOptions{})
	require.ErrorContains(t, err, "collides with a built-in")
}

func TestSubagentsStripOnlyAppContributions(t *testing.T) {
	resource := slackAppTestResource(t)
	resource.Tools = map[string]AgentConfigToolSource{"app_only": customAppTestTool(), "shared": customAppTestTool()}
	resource.MCP = map[string]AgentConfigMCPSource{
		"app-only": {URL: "https://example.com/mcp"},
		"shared":   {URL: "https://example.com/shared"},
	}
	slack := slackAppTestResource(t)
	slack.Tools = map[string]AgentConfigToolSource{toolcatalog.ToolNameSlackPostMessage: {}}
	source := appTestSource(
		map[string]AgentConfigAppResourceSource{"first": resource, "second": resource, "chat": slack},
	)
	deny := toolpermission.DefaultSelection(toolpermission.ModeAlwaysDeny)
	source.Tools = map[string]AgentConfigToolSource{
		"app_only": {Permission: &deny}, "shared": customAppTestTool(), "slack_notes": customAppTestTool(),
		toolcatalog.ToolNameSlackPostMessage: {}, toolcatalog.ToolNameReadAgent: {},
	}
	source.MCP = map[string]AgentConfigMCPSource{
		"shared":    {URL: "https://example.com/shared"},
		"unrelated": {URL: "https://example.com/base"},
	}
	result, err := compileAppTest(t, source, appTestOptions())
	require.NoError(t, err)
	for _, kind := range []string{SubagentTypeSelf, SubagentTypeProfile} {
		child, err := SubagentCompiledFrom(result.Compiled, SubagentCompiled{Type: kind}, SubagentDepth{Depth: 1}, nil)
		require.NoError(t, err)
		require.Empty(t, child.AppResources)
		require.NotContains(t, child.Tools, "app_only")
		require.NotContains(t, child.Tools, toolcatalog.ToolNameSlackPostMessage)
		require.Contains(t, child.Tools, "shared")
		require.Nil(t, child.Tools["shared"].AppOrigin)
		require.Equal(t, result.Compiled.Tools["slack_notes"], child.Tools["slack_notes"])
		require.Contains(t, child.Tools, toolcatalog.ToolNameReadAgent)
		require.NotContains(t, child.MCP, "app-only")
		require.Contains(t, child.MCP, "shared")
		require.Nil(t, child.MCP["shared"].AppOrigin)
		require.Equal(t, result.Compiled.MCP["unrelated"], child.MCP["unrelated"])
		encoded, err := EncodeCompiled(child)
		require.NoError(t, err)
		_, err = RuntimeContractFromCompiled(encoded.CanonicalJSON, CompilerVersion, encoded.Hash)
		require.NoError(t, err)
	}
	encoded, err := EncodeCompiled(result.Compiled)
	require.NoError(t, err)
	require.Equal(t, result.Hash, encoded.Hash)
}

func TestDisabledAppCustomToolsPreserveGlobalPolicy(t *testing.T) {
	for _, mode := range []string{toolpermission.ModeAlwaysAsk, toolpermission.ModeAlwaysDeny} {
		for _, disableResource := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/resource=%t", mode, disableResource), func(t *testing.T) {
				tool := customAppTestTool()
				resource := appPolicyTestResource(t, tool)
				policy := toolpermission.DefaultSelection(mode)
				source := appTestSource(map[string]AgentConfigAppResourceSource{"crm": resource})
				source.Tools = map[string]AgentConfigToolSource{"ticket": {Permission: &policy}}
				initial, err := compileAppTest(t, source, appTestOptions())
				require.NoError(t, err)
				disabled := false
				if disableResource {
					resource.Enabled = &disabled
				} else {
					tool.Enabled = &disabled
					resource.Tools["ticket"] = tool
				}
				source.AppResources["crm"] = resource
				result, err := compileAppTest(t, source, appTestOptions())
				require.NoError(t, err)
				_, exists := result.Compiled.Tools["ticket"]
				require.False(t, exists, "a disabled bundle must not contribute executable authority")
				_, err = RuntimeContractFromCompiled(result.CanonicalJSON, CompilerVersion, result.Hash)
				require.NoError(t, err)
				resource.Enabled, tool.Enabled = nil, nil
				resource.Tools["ticket"] = tool
				source.AppResources["crm"] = resource
				reenabled, err := compileAppTest(t, source, appTestOptions())
				require.NoError(t, err)
				require.Equal(t, initial.Hash, reenabled.Hash)
				require.Equal(t, mode, reenabled.Compiled.Tools["ticket"].Permission.Mode)
			})
		}
	}
}

func TestBundleKeepsImplicitBaseToolPolicyAndInheritance(t *testing.T) {
	source := appTestSource(nil)
	source.MachineSources = []AgentConfigMachineSource{{MachineName: "build-box"}}
	opts := testMachineSourceCompileOptions(t)
	opts.ResolveAppConnection = appTestOptions().ResolveAppConnection
	baseline, err := compileAppTest(t, source, opts)
	require.NoError(t, err)
	base := baseline.Compiled.Tools[toolcatalog.ToolNameRunCommand]
	mode := toolpermission.ModeAlwaysAllow
	if base.Permission.Mode == mode {
		mode = toolpermission.ModeAlwaysAsk
	}
	policy := toolpermission.DefaultSelection(mode)
	bundle := slackAppTestResource(t)
	bundle.Tools = map[string]AgentConfigToolSource{
		toolcatalog.ToolNameRunCommand:  {Permission: &policy},
		toolcatalog.ToolNameAskQuestion: {},
	}
	source.AppResources = map[string]AgentConfigAppResourceSource{"first": bundle, "second": bundle}
	result, err := compileAppTest(t, source, opts)
	require.NoError(t, err)
	tool := result.Compiled.Tools[toolcatalog.ToolNameRunCommand]
	require.Equal(t, base.Permission, tool.Permission)
	require.True(t, tool.AppOrigin.Base)
	require.Equal(t, []string{"first", "second"}, tool.AppOrigin.ResourceKeys)
	child, err := SubagentCompiledFrom(
		result.Compiled,
		SubagentCompiled{Type: SubagentTypeSelf},
		SubagentDepth{Depth: 1},
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, base, child.Tools[toolcatalog.ToolNameRunCommand])
	require.NotContains(t, child.Tools, toolcatalog.ToolNameAskQuestion, "bundle-only tools remain excluded")
}
