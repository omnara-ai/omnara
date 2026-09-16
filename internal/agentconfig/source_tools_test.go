package agentconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func TestRuntimeDoesNotAddSourceDefaultTools(t *testing.T) {
	for _, test := range []struct {
		name     string
		compiled Compiled
		want     int
	}{
		{"no runtime skill defaults", Compiled{Skills: []SkillCompiled{{PublicID: "test-skill"}}}, 0},
		{"no runtime subagent defaults", Compiled{
			Skills:    []SkillCompiled{{PublicID: "test-skill"}},
			Subagents: map[string]SubagentCompiled{"worker": {Type: SubagentTypeSelf}},
		}, 0},
		{"disabled skill", Compiled{
			Skills: []SkillCompiled{{PublicID: "test-skill"}},
			Tools: map[string]ToolCompiled{"skill": {
				Enabled: false,
				Permission: toolpermission.Selection{
					Mode: toolpermission.ModeAlwaysAllow, Parameters: json.RawMessage(`{}`),
				},
			}},
		}, 0},
		{"no runtime machine defaults", Compiled{MachineSources: []MachineSourceCompiled{{MachinePoolID: "pool"}}}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(test.compiled)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(canonicalizeJSON(raw))
			contract, err := RuntimeContractFromCompiled(raw, CompilerVersion, hex.EncodeToString(sum[:]))
			if err != nil || len(contract.Tools) != test.want {
				t.Fatalf("runtime tools: %+v, %v", contract.Tools, err)
			}
		})
	}
}

func TestCompilePreservesSourceWithDefaultTools(t *testing.T) {
	for _, format := range []SourceFormat{SourceFormatYAML, SourceFormatJSON} {
		source := []byte(validAgentSource("machine_sources: [{machine_pool_name: build-pool}]\n" +
			"tools: {run_command: {enabled: false, permission: {mode: always_ask}}}\n"))
		if format == SourceFormatJSON {
			var err error
			source, _, err = sourceJSON(SourceFormatYAML, source)
			if err != nil {
				t.Fatal(err)
			}
		}
		result, err := Compile(format, source, testMachineSourceCompileOptions(t))
		if err != nil {
			t.Fatal(err)
		}
		parsed, _, err := parseSource(format, []byte(result.Source))
		if err != nil {
			t.Fatal(err)
		}
		if result.Source != string(source) || len(parsed.Tools) != 1 || len(result.Compiled.Tools) != 13 {
			t.Fatalf("source changed or compiled defaults missing: %s", result.Source)
		}
		if *parsed.Tools["run_command"].Enabled || parsed.Tools["run_command"].Permission.Mode != "always_ask" {
			t.Fatal("explicit override lost")
		}
		repeated, err := Compile(format, []byte(result.Source), testMachineSourceCompileOptions(t))
		if err != nil || repeated.Source != result.Source || repeated.Hash != result.Hash {
			t.Fatalf("compilation not repeatable: %v", err)
		}
	}
}
