package agentconfig

import (
	"fmt"
	"github.com/google/uuid"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func subagentCompileOptions() CompileOptions {
	return CompileOptions{
		ResolveModelSelection: func(providerConfig string, configuredModelName string) (ResolvedModelSelection, error) {
			if configuredModelName == "gpt-small" {
				return ResolvedModelSelection{ConfiguredModelID: uuid.MustParse("22222222-2222-2222-2222-222222222222")}, nil
			}
			return ResolvedModelSelection{ConfiguredModelID: uuid.MustParse("11111111-1111-1111-1111-111111111111")}, nil
		},
		ResolveAgentProfileName: func(profileName string) (uuid.UUID, error) {
			if profileName != "research-agent" {
				return uuid.Nil, errNotFoundProfile
			}
			return publicidTestID(94), nil
		},
	}
}

type profileNotFoundError struct{}

func (profileNotFoundError) Error() string { return "profile not found" }

var errNotFoundProfile = profileNotFoundError{}

func TestCompileYAMLSubagentsCompilesKeysAndExplicitTools(t *testing.T) {
	source := validAgentSource(`
tools:
  run_command: {}
  stop_agent:
    enabled: false
subagents:
  researcher:
    type: profile
    profile: research-agent
    description: Investigate.
    model:
      provider_config: openai-prod
      name: gpt-mini
    instruction:
      append: Report as bullets.
    max_instances: 2
    archive_after_idle_minutes: 30
  fork:
    type: self
    model:
      reasoning:
        effort: low
max_subagents: 5
`)
	result, err := Compile(SourceFormatYAML, []byte(source), subagentCompileOptions())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	researcher := result.Compiled.Subagents["researcher"]
	if researcher.Type != SubagentTypeProfile || researcher.ProfileID != publicidTestID(94) {
		t.Fatalf("researcher = %+v", researcher)
	}
	if researcher.Model == nil || researcher.Model.ConfiguredModelID == uuid.Nil ||
		researcher.InstructionAppend != "Report as bullets." {
		t.Fatalf("researcher overrides = %+v", researcher)
	}
	if researcher.MaxInstances == nil || *researcher.MaxInstances != 2 ||
		researcher.ArchiveAfterIdleMinutes == nil || *researcher.ArchiveAfterIdleMinutes != 30 {
		t.Fatalf("researcher limits = %+v", researcher)
	}
	fork := result.Compiled.Subagents["fork"]
	if fork.Type != SubagentTypeSelf || fork.ProfileID != uuid.Nil || fork.Model == nil ||
		fork.Model.Reasoning == nil || fork.Model.Reasoning.Effort != "low" {
		t.Fatalf("fork = %+v", fork)
	}
	if result.Compiled.MaxSubagents == nil || *result.Compiled.MaxSubagents != 5 {
		t.Fatalf("max_subagents = %v", result.Compiled.MaxSubagents)
	}
	contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, result.Hash)
	if err != nil {
		t.Fatalf("runtime contract: %v", err)
	}
	if got := strings.Join(contract.SubagentKeys(), ","); got != "fork,researcher" {
		t.Fatalf("keys = %q", got)
	}
	names := make(map[string]bool, len(contract.Tools))
	for _, tool := range contract.Tools {
		names[tool.Name] = true
	}
	for _, name := range []string{
		toolcatalog.ToolNameSpawnAgent,
		toolcatalog.ToolNameReadAgent,
		toolcatalog.ToolNameSendAgentMessage,
		toolcatalog.ToolNameListAgents,
		"run_command",
	} {
		if !names[name] {
			t.Fatalf("tool %q missing from contract: %v", name, names)
		}
	}
	if names[toolcatalog.ToolNameStopAgent] {
		t.Fatalf("stop_agent should stay disabled when configured with enabled: false")
	}
	for _, name := range toolcatalog.SubagentToolNames() {
		require.Contains(t, result.Compiled.Tools, name)
	}
	recompiled, err := Compile(SourceFormatYAML, []byte(result.Source), subagentCompileOptions())
	require.NoError(t, err)
	require.Equal(t, result.Source, recompiled.Source)
	require.Equal(t, result.Hash, recompiled.Hash)
}

func TestCompileYAMLSubagentsRejectsInvalidShapes(t *testing.T) {
	for _, test := range []struct {
		name  string
		extra string
		want  string
	}{
		{
			name: "profile key without profile",
			extra: `
subagents:
  researcher:
    type: profile
`,
			want: "subagents.researcher.profile: required",
		},
		{
			name: "self key with profile",
			extra: `
subagents:
  fork:
    type: self
    profile: research-agent
`,
			want: "subagents.fork",
		},
		{
			name: "unknown profile",
			extra: `
subagents:
  researcher:
    type: profile
    profile: missing
`,
			want: "profile not found",
		},
		{
			name: "subagent tool without subagents",
			extra: `
tools:
  spawn_agent: {}
`,
			want: "requires at least one entry under subagents",
		},
		{
			name: "custom tool named like a subagent tool",
			extra: `
tools:
  list_agents:
    type: custom
    description: mine
    input_schema:
      type: object
subagents:
  fork:
    type: self
`,
			want: "collides with a built-in tool",
		},
		{
			name: "max_subagents without subagents",
			extra: `
max_subagents: 3
`,
			want: "max_subagents",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Compile(SourceFormatYAML, []byte(validAgentSource(test.extra)), subagentCompileOptions())
			if err == nil {
				t.Fatal("expected compile error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error %q does not mention %q", err.Error(), test.want)
			}
		})
	}
}

func TestSubagentCompiledFromStripsSpawningFromSelfForks(t *testing.T) {
	result, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
tools:
  run_command: {}
  spawn_agent:
    permission:
      mode: always_ask
subagents:
  fork:
    type: self
    model:
      provider_config: openai-prod
      name: gpt-small
    instruction:
      append: Be brief.
max_subagents: 2
`)), subagentCompileOptions())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	base := result.Compiled
	child := SubagentCompiledFrom(base, base.Subagents["fork"], SubagentDepth{Depth: 1})
	if child.Subagents != nil || child.MaxSubagents != nil {
		t.Fatalf("self fork kept subagents: %+v", child)
	}
	if _, ok := child.Tools["spawn_agent"]; ok {
		t.Fatalf("self fork kept spawn_agent tool")
	}
	if _, ok := child.Tools["run_command"]; !ok {
		t.Fatalf("self fork lost run_command tool")
	}
	if _, ok := base.Tools["spawn_agent"]; !ok {
		t.Fatalf("deriving the child mutated the base tools")
	}
	if child.Model.ConfiguredModelID != uuid.MustParse("22222222-2222-2222-2222-222222222222") {
		t.Fatalf("child model = %+v", child.Model)
	}
	if !strings.HasSuffix(child.Instruction, "\n\nBe brief.") {
		t.Fatalf("instruction = %q", child.Instruction)
	}
	maxDepth := 2
	deeper := SubagentCompiledFrom(
		base, SubagentCompiled{Type: SubagentTypeProfile}, SubagentDepth{MaxDepth: &maxDepth, Depth: 1},
	)
	if deeper.Subagents == nil || deeper.MaxDepth == nil || *deeper.MaxDepth != 2 || deeper.Model != base.Model {
		t.Fatalf("children below the depth limit keep subagents and carry max_depth: %+v", deeper)
	}
	if _, ok := deeper.Tools["spawn_agent"]; !ok {
		t.Fatal("child below the depth limit lost spawn_agent tool")
	}
	for _, state := range []string{"absent", "disabled"} {
		t.Run(state, func(t *testing.T) {
			restricted := base
			restricted.Tools = copyTools(base.Tools)
			if state == "absent" {
				delete(restricted.Tools, toolcatalog.ToolNameSpawnAgent)
			} else {
				tool := restricted.Tools[toolcatalog.ToolNameSpawnAgent]
				tool.Enabled = false
				restricted.Tools[toolcatalog.ToolNameSpawnAgent] = tool
			}
			derived := SubagentCompiledFrom(
				restricted, SubagentCompiled{}, SubagentDepth{MaxDepth: &maxDepth, Depth: 1},
			)
			require.Equal(t, restricted.Tools, derived.Tools)
			require.Equal(t, restricted.Subagents, derived.Subagents)
		})
	}
	leaf := SubagentCompiledFrom(
		base, SubagentCompiled{Type: SubagentTypeProfile}, SubagentDepth{MaxDepth: &maxDepth, Depth: 2},
	)
	if leaf.Subagents != nil || leaf.MaxDepth == nil || *leaf.MaxDepth != 2 {
		t.Fatalf("children at the depth limit drop subagents but keep max_depth: %+v", leaf)
	}
	for _, name := range toolcatalog.SubagentToolNames() {
		if name == toolcatalog.ToolNameSpawnAgent {
			require.NotContains(t, leaf.Tools, name)
		} else {
			require.Equal(t, base.Tools[name], leaf.Tools[name])
		}
	}
}

func TestCompileAllowsSubagentReadToolsWithoutSubagents(t *testing.T) {
	result, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
tools:
  read_agent: {}
  list_agents: {}
`)), subagentCompileOptions())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, result.Hash)
	if err != nil {
		t.Fatalf("runtime contract: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range contract.Tools {
		names[tool.Name] = true
	}
	if !names[toolcatalog.ToolNameReadAgent] || !names[toolcatalog.ToolNameListAgents] {
		t.Fatalf("contract tools = %v, want read_agent and list_agents", names)
	}
	if names[toolcatalog.ToolNameSpawnAgent] || names[toolcatalog.ToolNameSendAgentMessage] {
		t.Fatalf("contract tools = %v, want no implicit subagent tools without subagents", names)
	}
}

func TestSubagentDefaultsRespectModelToolSupport(t *testing.T) {
	source := validAgentSource("subagents: {worker: {type: self}}\n")
	opts := subagentCompileOptions()
	opts.ResolveModelSelection = func(string, string) (ResolvedModelSelection, error) {
		supportsTools := false
		return ResolvedModelSelection{SupportsTools: &supportsTools}, nil
	}
	_, err := Compile(SourceFormatYAML, []byte(source), opts)
	require.ErrorContains(t, err, "does not support tools")
	source += "tools:\n"
	for _, name := range toolcatalog.SubagentToolNames() {
		source += "  " + name + ": {enabled: false}\n"
	}
	result, err := Compile(SourceFormatYAML, []byte(source), opts)
	require.NoError(t, err)
	contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, result.Hash)
	require.NoError(t, err)
	require.Empty(t, contract.Tools)
	require.False(t, contract.RequiresModelToolSupport())
}

func TestCompileMaxDepth(t *testing.T) {
	result, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
subagents:
  fork:
    type: self
max_depth: 3
`)), subagentCompileOptions())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if result.Compiled.MaxDepth == nil || *result.Compiled.MaxDepth != 3 {
		t.Fatalf("max_depth = %v", result.Compiled.MaxDepth)
	}
	contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, result.Hash)
	if err != nil {
		t.Fatalf("runtime contract: %v", err)
	}
	if contract.SubagentDepthLimit() != 3 {
		t.Fatalf("depth limit = %d", contract.SubagentDepthLimit())
	}
	_, err = Compile(SourceFormatYAML, []byte(validAgentSource(`
subagents:
  fork:
    type: self
max_depth: 9
`)), subagentCompileOptions())
	if err == nil || !strings.Contains(err.Error(), "max_depth") {
		t.Fatalf("max_depth above the cap: err = %v", err)
	}
}

func TestCompileSubagentModelSelection(t *testing.T) {
	for _, kind := range []string{SubagentTypeSelf, SubagentTypeProfile} {
		for _, selection := range []struct {
			model string
			valid bool
		}{
			{`{provider_config: openai-prod, name: gpt-small}`, true},
			{`{reasoning: {effort: low}}`, true},
			{`{}`, true},
			{`{name: gpt-small}`, false},
			{`{provider_config: openai-prod}`, false},
		} {
			t.Run(kind+selection.model, func(t *testing.T) {
				profile := ""
				if kind == SubagentTypeProfile {
					profile = "    profile: research-agent\n"
				}
				source := validAgentSource(fmt.Sprintf(
					"\nsubagents:\n  worker:\n    type: %s\n%s    model: %s\n", kind, profile, selection.model,
				))
				result, err := Compile(SourceFormatYAML, []byte(source), subagentCompileOptions())
				if !selection.valid {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				require.Equal(t, source, result.Source)
				model := result.Compiled.Subagents["worker"].Model
				if strings.Contains(selection.model, "gpt-small") {
					require.Equal(t, uuid.MustParse("22222222-2222-2222-2222-222222222222"), model.ConfiguredModelID)
				} else {
					require.Equal(t, uuid.Nil, model.ConfiguredModelID)
				}
				child := SubagentCompiledFrom(result.Compiled, result.Compiled.Subagents["worker"], SubagentDepth{Depth: 1})
				if model.ConfiguredModelID == uuid.Nil {
					require.Equal(t, result.Compiled.Model.ConfiguredModelID, child.Model.ConfiguredModelID)
				} else {
					require.Equal(t, model.ConfiguredModelID, child.Model.ConfiguredModelID)
				}
			})
		}
	}
}

func TestCompileSubagentModelResolutionErrorPath(t *testing.T) {
	opts := subagentCompileOptions()
	resolve := opts.ResolveModelSelection
	opts.ResolveModelSelection = func(provider, name string) (ResolvedModelSelection, error) {
		if name == "missing" {
			return ResolvedModelSelection{}, NewIssue("/model/name", fmt.Errorf("model not found"))
		}
		return resolve(provider, name)
	}
	_, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
subagents:
  worker:
    type: self
    model: {provider_config: openai-prod, name: missing}
`)), opts)
	require.ErrorContains(t, err, "subagents.worker.model.name")
}
