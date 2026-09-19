package agentconfig

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestInteractionDestinationToolsFollowHandlerConfig(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"none", "listener", "disabled handler", "handler", "overlap", "instance"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			resource := slackAppTestResource(t)
			resource.InteractionHandler = &appdefinition.InteractionHandler{Definition: appdefinition.SlackInteractions}
			source := appTestSource(map[string]AgentConfigAppResourceSource{"chat": resource})
			opts := appTestOptions()
			want := scenario == "handler" || scenario == "overlap" || scenario == "instance"
			switch scenario {
			case "none":
				source.AppResources = nil
			case "listener":
				resource.InteractionHandler = nil
				resource.Listener = &appdefinition.Listener{Events: []string{"message"}}
				source.AppResources["chat"] = resource
			case "disabled handler":
				disabled := false
				resource.Enabled = &disabled
				source.AppResources["chat"] = resource
			case "overlap":
				source.AppResources["other"] = resource
			case "instance":
				id := testMachineSourcePublicID(t, publicid.KindProjectApp, "interaction-app")
				opts.ResolveAppInstance = func(string) (AppInstanceResolution, error) {
					return AppInstanceResolution{AppInstanceID: id, Resource: resource}, nil
				}
				source.AppResources["chat"] = AgentConfigAppResourceSource{
					AppInstance: id, InteractionHandler: resource.InteractionHandler,
				}
			}
			result, err := compileAppTest(t, source, opts)
			require.NoError(t, err)
			contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, CompilerVersion, result.Hash)
			require.NoError(t, err)
			raw, err := json.Marshal(source)
			require.NoError(t, err)
			resolved, err := ToolsFromSourceWithOptions(SourceFormatJSON, raw, opts)
			require.NoError(t, err)
			for _, name := range toolcatalog.InteractionDestinationToolNames() {
				tool, exists := result.Compiled.Tools[name]
				require.Equal(t, want, exists)
				if !want {
					continue
				}
				require.Equal(t, toolpermission.ModeAlwaysAllow, tool.Permission.Mode)
				require.True(t, tool.Enabled)
				require.Contains(t, resolved, ResolvedTool{Name: name, Enabled: true, Permission: tool.Permission})
				require.NotNil(t, tool.AppOrigin)
				require.False(t, tool.AppOrigin.Base)
				require.NotEmpty(t, contract.Tools)
				child, err := SubagentCompiledFrom(result.Compiled,
					SubagentCompiled{Type: SubagentTypeSelf}, SubagentDepth{Depth: 1}, nil)
				require.NoError(t, err)
				require.NotContains(t, child.Tools, name)
				encoded, err := EncodeCompiled(child)
				require.NoError(t, err)
				_, err = RuntimeContractFromCompiled(encoded.CanonicalJSON, CompilerVersion, encoded.Hash)
				require.NoError(t, err)
			}
		})
	}
}

func TestInteractionDestinationToolsRespectGlobalPolicies(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{
		"disabled", toolpermission.ModeAlwaysDeny, toolpermission.ModeAlwaysAsk, toolpermission.ModeAlwaysAllow,
	} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			resource := slackAppTestResource(t)
			resource.InteractionHandler = &appdefinition.InteractionHandler{Definition: appdefinition.SlackInteractions}
			source := appTestSource(map[string]AgentConfigAppResourceSource{"chat": resource})
			source.Tools = map[string]AgentConfigToolSource{}
			for _, name := range toolcatalog.InteractionDestinationToolNames() {
				policy := toolpermission.DefaultSelection(mode)
				override := AgentConfigToolSource{Permission: &policy}
				if mode == "disabled" {
					disabled := false
					override = AgentConfigToolSource{Enabled: &disabled}
				}
				source.Tools[name] = override
			}
			result, err := compileAppTest(t, source, appTestOptions())
			require.NoError(t, err)
			contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, CompilerVersion, result.Hash)
			require.NoError(t, err)
			for _, name := range toolcatalog.InteractionDestinationToolNames() {
				tool := result.Compiled.Tools[name]
				require.Equal(t, mode != "disabled", tool.Enabled)
				if mode != "disabled" {
					require.Equal(t, mode, tool.Permission.Mode)
				} else {
					for _, runtimeTool := range contract.Tools {
						require.NotEqual(t, name, runtimeTool.Name)
					}
				}
			}
			// Explicit migrated policy remains valid without any handler resource.
			source.AppResources = nil
			_, err = compileAppTest(t, source, CompileOptions{})
			require.NoError(t, err)
		})
	}
}

func TestInteractionDestinationToolProvenance(t *testing.T) {
	t.Parallel()
	resource := slackAppTestResource(t)
	resource.InteractionHandler = &appdefinition.InteractionHandler{Definition: appdefinition.SlackInteractions}
	bundle := slackAppTestResource(t)
	bundle.Tools = map[string]AgentConfigToolSource{toolcatalog.ToolNameListInteractionDestinations: {}}
	source := appTestSource(map[string]AgentConfigAppResourceSource{"chat": resource, "bundle": bundle})
	result, err := compileAppTest(t, source, appTestOptions())
	require.NoError(t, err)
	_, err = RuntimeContractFromCompiled(result.CanonicalJSON, CompilerVersion, result.Hash)
	require.NoError(t, err)
	require.Equal(t, []string{"bundle", "chat"},
		result.Compiled.Tools[toolcatalog.ToolNameListInteractionDestinations].AppOrigin.ResourceKeys)
	// Retaining automatically derived tool provenance after removing the handler
	// is an invalid immutable contract, not a new source of implicit tools.
	changed := result.Compiled.AppResources["chat"]
	changed.InteractionHandler = nil
	result.Compiled.AppResources["chat"] = changed
	encoded, err := EncodeCompiled(result.Compiled)
	require.NoError(t, err)
	_, err = RuntimeContractFromCompiled(encoded.CanonicalJSON, CompilerVersion, encoded.Hash)
	require.ErrorContains(t, err, "invalid app provenance")
}

func TestExplicitInteractionPolicyRequiresHandlerAtRuntime(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"no resources", "disabled handler", "handler", "subagent"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			resource := slackAppTestResource(t)
			resource.InteractionHandler = &appdefinition.InteractionHandler{Definition: appdefinition.SlackInteractions}
			if scenario == "disabled handler" {
				enabled := false
				resource.Enabled = &enabled
			}
			source := appTestSource(map[string]AgentConfigAppResourceSource{"chat": resource})
			if scenario == "no resources" {
				source.AppResources = nil
			}
			source.Tools = map[string]AgentConfigToolSource{}
			for _, name := range toolcatalog.InteractionDestinationToolNames() {
				policy := toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow)
				source.Tools[name] = AgentConfigToolSource{Permission: &policy}
			}
			result, err := compileAppTest(t, source, appTestOptions())
			require.NoError(t, err)
			compiled := result.Compiled
			if scenario == "subagent" {
				compiled, err = SubagentCompiledFrom(
					compiled,
					SubagentCompiled{Type: SubagentTypeSelf},
					SubagentDepth{Depth: 1},
					nil,
				)
				require.NoError(t, err)
			}
			encoded, err := EncodeCompiled(compiled)
			require.NoError(t, err)
			contract, err := RuntimeContractFromCompiled(encoded.CanonicalJSON, CompilerVersion, encoded.Hash)
			require.NoError(t, err)
			for _, name := range toolcatalog.InteractionDestinationToolNames() {
				// Explicit policy remains durable and can become useful when a
				// handler is added; availability is decided by runtime resources.
				require.Contains(t, compiled.Tools, name)
				found := false
				for _, tool := range contract.Tools {
					found = found || tool.Name == name
				}
				require.Equal(t, scenario == "handler", found)
			}
		})
	}
}
