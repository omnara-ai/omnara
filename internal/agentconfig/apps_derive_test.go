package agentconfig

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestDeriveAppResourcesPreservesPinnedBasePolicyAndIdentity(t *testing.T) {
	disabled := false
	source := appTestSource(nil)
	source.Tools = map[string]AgentConfigToolSource{
		toolcatalog.ToolNameSlackPostMessage:          {Enabled: &disabled},
		toolcatalog.ToolNameSetInteractionDestination: {Enabled: &disabled},
	}
	base, err := compileAppTest(t, source, appTestOptions())
	require.NoError(t, err)
	base.Compiled.Model.ConfiguredModelID = testMachineSourcePublicID(t, publicid.KindConfiguredModel, "pinned-model")
	base.Compiled.MachineSources = []MachineSourceCompiled{
		{MachineID: testMachineSourcePublicID(t, publicid.KindMachine, "pinned-machine"), Cwd: "/workspace"},
	}
	base.Compiled.Skills = []SkillCompiled{{PublicID: testMachineSourcePublicID(t, publicid.KindSkill, "pinned-skill")}}
	base.Compiled.Subagents = map[string]SubagentCompiled{
		"reviewer": {
			Type:      SubagentTypeProfile,
			ProfileID: testMachineSourcePublicID(t, publicid.KindAgentProfile, "pinned-profile"),
		},
	}
	before, err := json.Marshal(base.Compiled)
	require.NoError(t, err)
	resource := slackAppTestResource(t)
	resource.Tools = map[string]AgentConfigToolSource{toolcatalog.ToolNameSlackPostMessage: {}}
	resource.InteractionHandler = &appdefinition.InteractionHandler{Definition: appdefinition.SlackInteractions}
	opts := appTestOptions()
	opts.ResolveModelSelection = func(string, string) (ResolvedModelSelection, error) {
		panic("must not re-resolve pinned model")
	}
	opts.ResolveMachineName = func(string) (string, error) { panic("must not re-resolve pinned machine") }
	opts.ResolveSkillID = func(string) (SkillResolution, error) { panic("must not re-resolve pinned skill") }
	opts.ResolveAgentProfileName = func(string) (string, error) { panic("must not re-resolve pinned subagent") }
	derived, err := DeriveWithAppResources(
		base.Compiled,
		map[string]AgentConfigAppResourceSource{"launch": resource},
		opts,
	)
	require.NoError(t, err)
	require.Equal(t, base.Compiled.Model, derived.Model)
	require.Equal(t, base.Compiled.Instruction, derived.Instruction)
	require.Equal(t, base.Compiled.MachineSources, derived.MachineSources)
	require.Equal(t, base.Compiled.Skills, derived.Skills)
	require.Equal(t, base.Compiled.Subagents, derived.Subagents)
	require.False(t, derived.Tools[toolcatalog.ToolNameSlackPostMessage].Enabled)
	require.False(t, derived.Tools[toolcatalog.ToolNameSlackPostMessage].AppOrigin.Base)
	require.False(t, derived.Tools[toolcatalog.ToolNameSetInteractionDestination].Enabled)
	after, err := json.Marshal(base.Compiled)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after))
	derived.AppResources["launch"].Scope.Slack.ChannelID = "C456"
	require.Equal(t, "C123", resource.Scope.Slack.ChannelID)
}

func TestDeriveAppResourcesKeepsExistingProvenanceAndRejectsReplacement(t *testing.T) {
	resource := slackAppTestResource(t)
	resource.Tools = map[string]AgentConfigToolSource{toolcatalog.ToolNameSlackPostMessage: {}}
	base, err := compileAppTest(
		t,
		appTestSource(map[string]AgentConfigAppResourceSource{"original": resource}),
		appTestOptions(),
	)
	require.NoError(t, err)
	derived, err := DeriveWithAppResources(
		base.Compiled,
		map[string]AgentConfigAppResourceSource{"launch": resource},
		appTestOptions(),
	)
	require.NoError(t, err)
	require.Equal(
		t,
		[]string{"launch", "original"},
		derived.Tools[toolcatalog.ToolNameSlackPostMessage].AppOrigin.ResourceKeys,
	)
	require.Equal(t, base.Compiled.AppResources["original"], derived.AppResources["original"])
	_, err = DeriveWithAppResources(
		base.Compiled,
		map[string]AgentConfigAppResourceSource{"original": resource},
		appTestOptions(),
	)
	require.ErrorContains(t, err, "already exists")
}

func TestDeriveAppResourcesCustomToolAndMCPConflicts(t *testing.T) {
	source := appTestSource(nil)
	source.Tools = map[string]AgentConfigToolSource{"ticket": customAppTestTool()}
	source.MCP = map[string]AgentConfigMCPSource{"crm": {URL: "https://example.com/mcp"}}
	base, err := compileAppTest(t, source, appTestOptions())
	require.NoError(t, err)
	resource := slackAppTestResource(t)
	resource.Tools, resource.MCP = source.Tools, source.MCP
	derived, err := DeriveWithAppResources(
		base.Compiled,
		map[string]AgentConfigAppResourceSource{"bundle": resource},
		appTestOptions(),
	)
	require.NoError(t, err)
	require.True(t, derived.Tools["ticket"].AppOrigin.Base)
	require.True(t, derived.MCP["crm"].AppOrigin.Base)
	tool := customAppTestTool()
	tool.Description = "Different contract"
	resource.Tools = map[string]AgentConfigToolSource{"ticket": tool}
	_, err = DeriveWithAppResources(
		base.Compiled,
		map[string]AgentConfigAppResourceSource{"bundle": resource},
		appTestOptions(),
	)
	require.ErrorContains(t, err, "conflicts")
	resource.Tools = source.Tools
	resource.MCP = map[string]AgentConfigMCPSource{"crm": {URL: "https://elsewhere.example/mcp"}}
	_, err = DeriveWithAppResources(
		base.Compiled,
		map[string]AgentConfigAppResourceSource{"bundle": resource},
		appTestOptions(),
	)
	require.ErrorContains(t, err, "conflicts")
}
