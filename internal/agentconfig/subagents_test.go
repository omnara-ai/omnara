package agentconfig

import (
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func subagentCompileOptions() CompileOptions {
	return CompileOptions{
		ResolveModelSelection: func(providerConfig string, configuredModelName string) (ResolvedModelSelection, error) {
			return ResolvedModelSelection{ConfiguredModelID: "11111111-1111-1111-1111-111111111111"}, nil
		},
		ResolveAgentProfileName: func(profileName string) (string, error) {
			if profileName != "research-agent" {
				return "", errNotFoundProfile
			}
			return "aprf_abcdefghijklmnopqrstuvwxyz", nil
		},
	}
}

type profileNotFoundError struct{}

func (profileNotFoundError) Error() string { return "profile not found" }

var errNotFoundProfile = profileNotFoundError{}

func TestCompileYAMLSubagentsCompilesKeysAndImplicitTools(t *testing.T) {
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
      name: gpt-mini
    instruction:
      append: Report as bullets.
    max_concurrent: 2
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
	if researcher.Type != SubagentTypeProfile || researcher.ProfileID != "aprf_abcdefghijklmnopqrstuvwxyz" {
		t.Fatalf("researcher = %+v", researcher)
	}
	if researcher.Model == nil || researcher.Model.Name != "gpt-mini" ||
		researcher.InstructionAppend != "Report as bullets." {
		t.Fatalf("researcher overrides = %+v", researcher)
	}
	if researcher.MaxConcurrent == nil || *researcher.MaxConcurrent != 2 ||
		researcher.ArchiveAfterIdleMinutes == nil || *researcher.ArchiveAfterIdleMinutes != 30 {
		t.Fatalf("researcher limits = %+v", researcher)
	}
	fork := result.Compiled.Subagents["fork"]
	if fork.Type != SubagentTypeSelf || fork.ProfileID != "" || fork.Model == nil ||
		fork.Model.Reasoning == nil || fork.Model.Reasoning.Effort != "low" {
		t.Fatalf("fork = %+v", fork)
	}
	if result.Compiled.MaxSubagents == nil || *result.Compiled.MaxSubagents != 5 {
		t.Fatalf("max_subagents = %v", result.Compiled.MaxSubagents)
	}
	contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, CompilerVersion, result.Hash)
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
      name: gpt-small
    instruction:
      append: Be brief.
max_subagents: 2
`)), subagentCompileOptions())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	base := result.Compiled
	var resolvedBase string
	var resolvedOverride SubagentModelCompiled
	child, err := SubagentCompiledFrom(base, base.Subagents["fork"], func(
		baseConfiguredModelID string,
		override SubagentModelCompiled,
	) (ResolvedModelSelection, error) {
		resolvedBase = baseConfiguredModelID
		resolvedOverride = override
		return ResolvedModelSelection{ConfiguredModelID: "22222222-2222-2222-2222-222222222222"}, nil
	})
	if err != nil {
		t.Fatalf("derive self fork: %v", err)
	}
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
	if resolvedBase != base.Model.ConfiguredModelID || resolvedOverride.Name != "gpt-small" {
		t.Fatalf("model resolution = base %q override %+v", resolvedBase, resolvedOverride)
	}
	if child.Model.ConfiguredModelID != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("child model = %+v", child.Model)
	}
	if !strings.HasSuffix(child.Instruction, "\n\nBe brief.") {
		t.Fatalf("instruction = %q", child.Instruction)
	}
	profileChild, err := SubagentCompiledFrom(base, SubagentCompiled{Type: SubagentTypeProfile}, nil)
	if err != nil {
		t.Fatalf("derive profile child: %v", err)
	}
	if profileChild.Subagents == nil || profileChild.Model != base.Model {
		t.Fatalf("profile children keep their own subagents block and model: %+v", profileChild)
	}
}

func TestSubagentCompiledFromRejectsToollessModelOverride(t *testing.T) {
	result, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
tools:
  run_command: {}
subagents:
  fork:
    type: self
    model:
      name: gpt-no-tools
`)), subagentCompileOptions())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	supportsTools := false
	_, err = SubagentCompiledFrom(result.Compiled, result.Compiled.Subagents["fork"], func(
		string, SubagentModelCompiled,
	) (ResolvedModelSelection, error) {
		return ResolvedModelSelection{
			ConfiguredModelID: "22222222-2222-2222-2222-222222222222",
			SupportsTools:     &supportsTools,
		}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "does not support tools") {
		t.Fatalf("derive with tool-less model: err = %v", err)
	}
}
