package agentconfig

import (
	"strings"
	"testing"
)

func TestSubagentSourceFromRegeneratesCompilableYAML(t *testing.T) {
	source := validAgentSource(`
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
`)
	result, err := Compile(SourceFormatYAML, []byte(source), subagentCompileOptions())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	parsed, err := ParseSource(SourceFormatYAML, []byte(source))
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	child := SubagentSourceFrom(parsed, result.Compiled.Subagents["fork"], SubagentDepth{Depth: 1})
	if child.Subagents != nil || child.MaxSubagents != nil {
		t.Fatalf("self fork kept subagents: %+v", child)
	}
	if _, ok := child.Tools["spawn_agent"]; ok {
		t.Fatal("self fork kept spawn_agent tool")
	}
	if _, ok := parsed.Tools["spawn_agent"]; !ok {
		t.Fatal("deriving the child mutated the base tools")
	}
	if child.Model.Name != "gpt-small" || child.Model.ProviderConfig != "openai-prod" {
		t.Fatalf("child model = %+v", child.Model)
	}
	if child.Instruction != "Help the user make progress.\n\nBe brief." {
		t.Fatalf("instruction = %q", child.Instruction)
	}

	encoded, err := EncodeSourceYAML(child)
	if err != nil {
		t.Fatalf("encode yaml: %v", err)
	}
	if !strings.HasPrefix(encoded, "instruction: |-\n  Help the user make progress.\n\n  Be brief.\nmodel:\n") {
		t.Fatalf("encoded yaml = %q", encoded)
	}
	if strings.Contains(encoded, "subagents") || strings.Contains(encoded, "spawn_agent") {
		t.Fatalf("encoded yaml kept spawning config: %q", encoded)
	}
	recompiled, err := Compile(SourceFormatYAML, []byte(encoded), subagentCompileOptions())
	if err != nil {
		t.Fatalf("recompile regenerated yaml: %v\n%s", err, encoded)
	}
	expected, err := SubagentCompiledFrom(
		result.Compiled, result.Compiled.Subagents["fork"], SubagentDepth{Depth: 1},
		func(string, SubagentModelCompiled) (ResolvedModelSelection, error) {
			return ResolvedModelSelection{ConfiguredModelID: recompiled.Compiled.Model.ConfiguredModelID}, nil
		},
	)
	if err != nil {
		t.Fatalf("derive compiled child: %v", err)
	}
	expectedEncoded, err := EncodeCompiled(expected)
	if err != nil {
		t.Fatalf("encode expected: %v", err)
	}
	if recompiled.Hash != expectedEncoded.Hash {
		t.Fatalf("regenerated yaml compiles to\n%s\nwant\n%s", recompiled.CanonicalJSON, expectedEncoded.CanonicalJSON)
	}

	maxDepth := 2
	deeper := SubagentSourceFrom(
		parsed, SubagentCompiled{Type: SubagentTypeProfile}, SubagentDepth{MaxDepth: &maxDepth, Depth: 1},
	)
	if deeper.Subagents == nil || deeper.MaxDepth == nil || *deeper.MaxDepth != 2 || deeper.Model != parsed.Model {
		t.Fatalf("children below the depth limit keep subagents and carry max_depth: %+v", deeper)
	}
	deeperYAML, err := EncodeSourceYAML(deeper)
	if err != nil {
		t.Fatalf("encode deeper yaml: %v", err)
	}
	if !strings.Contains(deeperYAML, "max_depth: 2\n") || !strings.Contains(deeperYAML, "subagents:\n") {
		t.Fatalf("deeper yaml = %q", deeperYAML)
	}
}
