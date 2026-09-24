package agentconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func integrationTestOptions(t *testing.T) (CompileOptions, map[uuid.UUID]IntegrationResolution) {
	t.Helper()
	id := publicidTestID(120)
	integration := IntegrationResolution{IntegrationID: id, IntegrationType: integrationdefinition.SlackThread}
	return CompileOptions{ResolveIntegrationName: func(name string) (IntegrationResolution, error) {
		if name != "engineering-team" {
			return IntegrationResolution{}, fmt.Errorf("integration %s is unavailable", name)
		}
		return integration, nil
	}}, map[uuid.UUID]IntegrationResolution{id: integration}
}
func compileIntegrationTest(t *testing.T, extra string, opts CompileOptions) Result {
	t.Helper()
	result, err := Compile(SourceFormatYAML, []byte(validAgentSource(extra)), opts)
	require.NoError(t, err)
	return result
}

func TestIntegrationCapabilitiesCompileAndPrepareIndependently(t *testing.T) {
	for _, extra := range []string{
		"tools: {int__engineering-team__post_message: {}}",
		"interaction_handlers: {engineering-team: {}}",
		`tools:
  int__engineering-team__post_message: {}
interaction_handlers:
  engineering-team: {}`,
	} {
		t.Run(extra, func(t *testing.T) {
			opts, integrations := integrationTestOptions(t)
			calls := 0
			resolve := opts.ResolveIntegrationName
			opts.ResolveIntegrationName = func(name string) (IntegrationResolution, error) { calls++; return resolve(name) }
			result := compileIntegrationTest(t, extra, opts)
			require.Equal(t, 1, calls, "resolve each distinct integration once")
			contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, result.Hash)
			require.NoError(t, err)
			require.Equal(t, ReferencedIntegrationIDs(result.Compiled), contract.ReferencedIntegrationIDs())
			require.Len(t, contract.ReferencedIntegrationIDs(), 1)
			for _, tool := range contract.Tools {
				require.False(t, toolcatalog.UsesIntegrationToolNamespace(tool.Name), "decoding does not prepare integration tools")
			}
			prepared, err := PrepareIntegrationTools(result.Compiled, integrations)
			require.NoError(t, err)
			require.Len(t, prepared, len(contract.IntegrationTools))
			unavailable, err := PrepareIntegrationTools(result.Compiled, nil)
			require.NoError(t, err)
			require.Empty(t, unavailable)
		})
	}
}

func TestIntegrationSourceValidation(t *testing.T) {
	opts, _ := integrationTestOptions(t)
	for _, extra := range []string{
		"tools: {int__engineering-team__post_message: {type: custom, description: x, input_schema: {type: object}}}",
		"tools: {int__engineering-team__post_message: {type: built_in}}",
		"tools: {int__engineering-team__missing: {}}",
		"tools: {int__missing__read: {}}",
		"tools: {int__bad: {}}",
		"tools: {mcp__reserved__read: {}}",
		"tools: {int__" + strings.Repeat("a", 32) + "__" + strings.Repeat("b", 30) + ": {}}",
		"interaction_handlers: {engineering-team__redundant: {}}",
		"interaction_handlers: {engineering-team: null}",
	} {
		_, err := Compile(SourceFormatYAML, []byte(validAgentSource(extra)), opts)
		require.Error(t, err, extra)
	}
	_, err := Compile(
		SourceFormatYAML,
		[]byte(validAgentSource("tools: {int__engineering-team__read: {}}")),
		CompileOptions{},
	)
	require.ErrorContains(t, err, "ResolveIntegrationName")
	opts.ResolveIntegrationName = func(string) (IntegrationResolution, error) {
		return IntegrationResolution{IntegrationID: uuid.Nil, IntegrationType: integrationdefinition.SlackThread}, nil
	}
	_, err = Compile(
		SourceFormatYAML,
		[]byte(validAgentSource("interaction_handlers: {engineering-team: {}}")),
		opts,
	)
	require.ErrorContains(t, err, "integration ID")
}

func TestIntegrationCompiledRejectsForgedStructuralAuthority(t *testing.T) {
	opts, _ := integrationTestOptions(t)
	result := compileIntegrationTest(t, `tools: {int__engineering-team__read: {}}
interaction_handlers: {engineering-team: {}}`, opts)
	for _, mutate := range []func(*Compiled){
		func(c *Compiled) {
			tool := c.Tools["int__engineering-team__read"]
			tool.IntegrationID = uuid.Nil
			c.Tools["int__engineering-team__read"] = tool
		},
		func(c *Compiled) {
			tool := c.Tools["int__engineering-team__read"]
			tool.Type = "custom"
			c.Tools["int__engineering-team__read"] = tool
		},
		func(c *Compiled) { tool := c.Tools["int__engineering-team__read"]; c.Tools["web_search"] = tool },
		func(c *Compiled) {
			capability := c.InteractionHandlers["engineering-team"]
			capability.IntegrationID = publicidTestID(121)
			c.InteractionHandlers["engineering-team"] = capability
		},
	} {
		var compiled Compiled
		require.NoError(t, json.Unmarshal(result.CanonicalJSON, &compiled))
		mutate(&compiled)
		encoded, err := EncodeCompiled(compiled)
		require.NoError(t, err)
		_, err = RuntimeContractFromCompiled(encoded.CanonicalJSON, encoded.Hash)
		require.Error(t, err)
	}
}

func TestCompositionPreservesExistingEntriesAndSubagentsStripIntegrations(t *testing.T) {
	opts, _ := integrationTestOptions(t)
	base := compileIntegrationTest(t, `tools:
  int__engineering-team__post_message: {enabled: false, permission: {mode: always_deny}}
  ordinary__custom: {type: custom, description: Custom, input_schema: {type: object}}
  list_interaction_handlers: {}
  set_interaction_handler: {}
interaction_handlers:
  engineering-team: {}
mcp:
  docs: {url: https://example.com/mcp}
subagents: {worker: {type: self}}`, opts).Compiled
	addition := IntegrationCapabilitiesSource{
		Tools: map[string]AgentConfigToolSource{
			"int__engineering-team__post_message": {},
			"int__engineering-team__read":         {},
		},
		InteractionHandlers: map[string]AgentConfigIntegrationCapabilitySource{
			"engineering-team": {},
		},
	}
	derived, err := DeriveWithIntegrationCapabilities(base, addition, opts)
	require.NoError(t, err)
	require.Equal(
		t,
		base.Tools["int__engineering-team__post_message"],
		derived.Tools["int__engineering-team__post_message"],
	)
	require.Equal(t, base.InteractionHandlers, derived.InteractionHandlers)
	require.Contains(t, derived.Tools, "int__engineering-team__read")
	require.NotContains(t, base.Tools, "int__engineering-team__read")
	child := SubagentCompiledFrom(derived, SubagentCompiled{Type: SubagentTypeSelf}, SubagentDepth{})
	require.Empty(t, child.InteractionHandlers)
	for key, tool := range child.Tools {
		require.Empty(t, tool.IntegrationID)
		require.False(t, toolcatalog.UsesIntegrationToolNamespace(key))
	}
	require.Contains(t, child.Tools, "ordinary__custom")
	require.Contains(t, child.MCP, "docs")
	for _, name := range toolcatalog.InteractionHandlerToolNames() {
		require.NotContains(t, child.Tools, name)
	}
}

func TestIntegrationCompositionPreservesCompiledReferencesAndWebhook(t *testing.T) {
	opts, _ := integrationTestOptions(t)
	secretID := publicidTestID(140)
	base := Compiled{
		Instruction: "Keep the compiled policy.",
		Model:       ModelCompiled{ConfiguredModelID: publicidTestID(141)},
		MachineSources: []MachineSourceCompiled{{
			MachinePoolID: publicidTestID(142), MaxMachines: 1,
			SecretEnvOverlay: map[string]*uuid.UUID{"TOKEN": &secretID},
		}},
		MCP: map[string]MCPServerCompiled{"docs": {
			URL: "https://example.com/mcp", Auth: &MCPAuthCompiled{Type: "bearer", SecretID: secretID},
		}},
		Skills: []SkillCompiled{{ID: publicidTestID(143)}},
		Subagents: map[string]SubagentCompiled{"worker": {
			Type: SubagentTypeProfile, ProfileID: publicidTestID(144),
			Model: &ModelCompiled{ConfiguredModelID: publicidTestID(145)},
		}},
		EventWebhook: &EventWebhookCompiled{
			URL: "https://example.com/events", Events: []string{"model_output"}, SigningSecretID: secretID,
		},
	}
	derived, err := DeriveWithIntegrationCapabilities(base, IntegrationCapabilitiesSource{
		Tools: map[string]AgentConfigToolSource{
			"int__engineering-team__read": {}, "list_interaction_handlers": {}, "set_interaction_handler": {},
		},
		InteractionHandlers: map[string]AgentConfigIntegrationCapabilitySource{"engineering-team": {}},
	}, opts)
	require.NoError(t, err)
	remaining := derived
	remaining.Tools, remaining.InteractionHandlers = nil, nil
	require.Equal(t, base, remaining)

	encoded, err := EncodeCompiled(derived)
	require.NoError(t, err)
	var stored struct {
		Tools map[string]map[string]json.RawMessage `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(encoded.CanonicalJSON, &stored))
	require.JSONEq(t, `"`+
		publicidTestID(120).String()+`"`, string(stored.Tools["int__engineering-team__read"]["integration_id"]))
	require.NotContains(t, stored.Tools["list_interaction_handlers"], "integration_id")

	child := SubagentCompiledFrom(derived, derived.Subagents["worker"], SubagentDepth{})
	require.Empty(t, child.Tools)
	require.Empty(t, child.InteractionHandlers)
	require.Equal(t, *base.Subagents["worker"].Model, child.Model)
	require.Equal(t, base.EventWebhook, child.EventWebhook)
	require.Equal(t, base.MachineSources, child.MachineSources)
	require.Equal(t, base.MCP, child.MCP)
	require.Equal(t, base.Skills, child.Skills)
	derived.EventWebhook.Events[0] = "tool_call_update"
	require.Equal(t, []string{"model_output"}, base.EventWebhook.Events)
}

func TestPendingIntegrationToolIdentityAndPermission(t *testing.T) {
	opts, integrations := integrationTestOptions(t)
	name := "int__engineering-team__post_message"
	original := compileIntegrationTest(t, "tools: {"+name+": {}}", opts).Compiled
	originalContract := runtimeIntegrationTest(t, original)
	current := original
	current.Instruction = "unrelated edit"
	authority, err := ResolveIntegrationToolAuthority(
		originalContract,
		runtimeIntegrationTest(t, current),
		name,
		integrations,
	)
	require.NoError(t, err)
	require.Equal(t, original.Tools[name].IntegrationID, authority.Tool.IntegrationID)
	for _, mutate := range []func(*ToolCompiled){
		func(tool *ToolCompiled) { tool.IntegrationID = publicidTestID(122) },
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
		_, err := ResolveIntegrationToolAuthority(
			originalContract,
			RuntimeContract{IntegrationTools: changed.Tools},
			name,
			integrations,
		)
		require.Error(t, err)
	}
	_, err = ResolveIntegrationToolAuthority(originalContract, runtimeIntegrationTest(t, current), name, nil)
	require.Error(t, err)
}

func TestInteractionToolsAreExplicitAndRespectModelAndOverrides(t *testing.T) {
	for _, supports := range []bool{true, false} {
		opts := CompileOptions{ResolveModelSelection: func(string, string) (ResolvedModelSelection, error) {
			return ResolvedModelSelection{SupportsTools: &supports}, nil
		}}
		result := compileIntegrationTest(t, "", opts)
		for _, name := range toolcatalog.InteractionHandlerToolNames() {
			require.NotContains(t, result.Compiled.Tools, name)
		}
		disabled := compileIntegrationTest(
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
	contract, err := RuntimeContractFromCompiled(encoded.CanonicalJSON, encoded.Hash)
	require.NoError(t, err)
	require.Empty(t, contract.Tools)
	require.False(t, contract.RequiresModelToolSupport())
}

func TestIntegrationResolverErrorsRetainSourcePath(t *testing.T) {
	opts := CompileOptions{
		ResolveIntegrationName: func(string) (IntegrationResolution, error) {
			return IntegrationResolution{}, errors.New("disconnected")
		},
	}
	_, err := Compile(SourceFormatYAML, []byte(validAgentSource("tools: {int__engineering__read: {}}")), opts)
	require.ErrorContains(t, err, "disconnected")
	require.ErrorContains(t, err, "int__engineering__read")
}

func runtimeIntegrationTest(t *testing.T, compiled Compiled) RuntimeContract {
	t.Helper()
	encoded, err := EncodeCompiled(compiled)
	require.NoError(t, err)
	contract, err := RuntimeContractFromCompiled(encoded.CanonicalJSON, encoded.Hash)
	require.NoError(t, err)
	return contract
}

func TestCompositionDoesNotResolveExistingOrNonIntegrationMetadata(t *testing.T) {
	opts, _ := integrationTestOptions(t)
	source := IntegrationCapabilitiesSource{
		Tools:               map[string]AgentConfigToolSource{"int__engineering-team__read": {}},
		InteractionHandlers: map[string]AgentConfigIntegrationCapabilitySource{"engineering-team": {}},
	}
	base, err := CompileIntegrationCapabilitiesSource(source, opts)
	require.NoError(t, err)
	require.Len(t, base.Tools, 1)
	opts.ResolveIntegrationName = func(string) (IntegrationResolution, error) {
		t.Fatal("existing integration should not be resolved")
		return IntegrationResolution{}, nil
	}
	opts.ResolveModelSelection = func(string, string) (ResolvedModelSelection, error) {
		t.Fatal("model should not be resolved")
		return ResolvedModelSelection{}, nil
	}
	source.Tools["int__engineering-team__read"] = AgentConfigToolSource{
		Type: "invalid",
	}
	source.InteractionHandlers["engineering-team"] = AgentConfigIntegrationCapabilitySource{}
	derived, err := DeriveWithIntegrationCapabilities(base, source, opts)
	require.NoError(t, err)
	require.Equal(t, base, derived)
	require.Equal(t, "invalid", source.Tools["int__engineering-team__read"].Type, "input maps must not be mutated")
}

func TestCompositionAcceptsInteractionHelpersWithoutOtherBuiltIns(t *testing.T) {
	opts, _ := integrationTestOptions(t)
	for _, name := range append(toolcatalog.InteractionHandlerToolNames(), toolcatalog.ToolNameWebSearch) {
		compiled, err := CompileIntegrationCapabilitiesSource(IntegrationCapabilitiesSource{
			Tools: map[string]AgentConfigToolSource{name: {}},
		}, opts)
		if toolcatalog.IsInteractionHandlerTool(name) {
			require.NoError(t, err)
			require.Len(t, compiled.Tools, 1)
			require.True(t, compiled.Tools[name].Enabled)
		} else {
			require.ErrorContains(t, err, "composition only accepts integration tools and interaction helpers")
		}
	}
}

func TestReferencedIntegrationIDsExcludeDisabledToolsOnly(t *testing.T) {
	integrations := map[string]IntegrationResolution{}
	for index, name := range []string{"disabled", "shared", "enabled", "denied", "handler"} {
		id := publicidTestID(130 + index)
		integrations[name] = IntegrationResolution{IntegrationID: id, IntegrationType: integrationdefinition.SlackThread}
	}
	opts := CompileOptions{ResolveIntegrationName: func(name string) (IntegrationResolution, error) {
		integration, ok := integrations[name]
		require.True(t, ok)
		return integration, nil
	}}
	result := compileIntegrationTest(t, `tools:
  int__disabled__read: {enabled: false}
  int__shared__read: {enabled: false}
  int__enabled__read: {}
  int__enabled__post_message: {}
  int__denied__read: {permission: {mode: always_deny}}
interaction_handlers:
  shared: {}
  handler: {}`, opts)
	want := []uuid.UUID{
		integrations["shared"].IntegrationID,
		integrations["enabled"].IntegrationID,
		integrations["denied"].IntegrationID,
		integrations["handler"].IntegrationID,
	}
	slices.SortFunc(want, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	require.Equal(t, want, ReferencedIntegrationIDs(result.Compiled))
	contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, result.Hash)
	require.NoError(t, err)
	require.Equal(t, want, contract.ReferencedIntegrationIDs())
	require.Contains(
		t,
		contract.IntegrationTools,
		"int__disabled__read",
		"disabled entries remain pinned in the stored contract",
	)

	onlyDisabled := compileIntegrationTest(t, "tools: {int__disabled__read: {enabled: false}}", opts)
	require.Empty(t, ReferencedIntegrationIDs(onlyDisabled.Compiled))
	prepared, err := PrepareIntegrationTools(onlyDisabled.Compiled, nil)
	require.NoError(t, err)
	require.Empty(t, prepared)
}

func TestDisabledIntegrationReferencesStillRequireConsistentPinnedIdentity(t *testing.T) {
	opts, _ := integrationTestOptions(t)
	result := compileIntegrationTest(t, `tools:
  int__engineering-team__read: {enabled: false}
interaction_handlers:
  engineering-team: {}`, opts)
	tool := result.Compiled.Tools["int__engineering-team__read"]
	tool.IntegrationID = publicidTestID(122)
	result.Compiled.Tools["int__engineering-team__read"] = tool
	encoded, err := EncodeCompiled(result.Compiled)
	require.NoError(t, err)
	_, err = RuntimeContractFromCompiled(encoded.CanonicalJSON, encoded.Hash)
	require.ErrorContains(t, err, "inconsistent pinned IDs")
	_, err = PrepareIntegrationTools(result.Compiled, nil)
	require.ErrorContains(t, err, "inconsistent pinned IDs")
}
