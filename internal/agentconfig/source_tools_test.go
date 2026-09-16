package agentconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
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

func TestAddSourceToolsPreservesUnrelatedValues(t *testing.T) {
	for _, test := range []struct {
		name   string
		format SourceFormat
		source string
		keep   string
	}{
		{"yaml comments", SourceFormatYAML, "# keep\ninstruction: 'a < b & c'\ntools: {web_search: {}}\n", "# keep"},
		{"yaml no tools", SourceFormatYAML, "instruction: |\n  hello\n  world\n", "instruction: |"},
		{"yaml alias", SourceFormatYAML, "shared: &shared {web_search: {}}\ntools: *shared\n", ""},
		{"yaml merge", SourceFormatYAML, "shared: &shared {tools: {web_search: {}}}\n<<: *shared\n", ""},
		{"yaml tool merge", SourceFormatYAML, "shared: &shared {web_search: {}}\ntools: {<<: *shared}\n", ""},
		{"yaml markers", SourceFormatYAML, "---\ntools: {}\n...\n", ""},
		{"yaml CR line endings", SourceFormatYAML, "instruction: hello\rtools:\r  web_search: {}\r", ""},
		{
			"json missing tools", SourceFormatJSON,
			`{"z":9007199254740993,"a":"a < b & c"}`, `"z":9007199254740993,"a":"a < b & c"`,
		},
		{"json existing tools", SourceFormatJSON, `{"z":1,"tools": {"web_search":{}},"a":"<>&"}`, `"a":"<>&"`},
		{"json duplicate tools", SourceFormatJSON, `{"tools":{"skill":{"enabled":false}},"tools":{}}`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			original, _, err := sourceJSON(test.format, []byte(test.source))
			if err != nil {
				t.Fatal(err)
			}
			result, err := AddSourceTools(test.format, []byte(test.source), []string{"skill"})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(result), test.keep) {
				t.Fatalf("lost formatting %q: %s", test.keep, result)
			}
			updated, _, err := sourceJSON(test.format, result)
			if err != nil {
				t.Fatal(err)
			}
			var before, after map[string]json.RawMessage
			if err := json.Unmarshal(original, &before); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(updated, &after); err != nil {
				t.Fatal(err)
			}
			var tools map[string]json.RawMessage
			if err := json.Unmarshal(after["tools"], &tools); err != nil {
				t.Fatal(err)
			}
			if !sameSourceJSON(tools["skill"], []byte(`{}`)) {
				t.Fatalf("missing skill: %s", result)
			}
			delete(tools, "skill")
			if before["tools"] == nil {
				delete(after, "tools")
			} else {
				after["tools"], err = json.Marshal(tools)
				if err != nil {
					t.Fatal(err)
				}
			}
			restored, err := json.Marshal(after)
			if err != nil {
				t.Fatal(err)
			}
			if !sameSourceJSON(original, restored) {
				t.Fatalf("changed unrelated values: %s -> %s", original, restored)
			}
		})
	}
}

func TestAddSourceToolsPreservesYAMLBytes(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		want   string
	}{
		{
			"no tools",
			"# keep\n---\ninstruction: 'hello' # unchanged\n...\n",
			"# keep\n---\ntools: {skill: {}}\ninstruction: 'hello' # unchanged\n...\n",
		},
		{
			"flow without tools",
			"{instruction: 'hello'} # keep\n",
			"{tools: {skill: {}}, instruction: 'hello'} # keep\n",
		},
		{
			"inherited tools",
			"# keep\n<<: {tools: {web_search: {permission: {mode: always_ask}}}}\ninstruction: help\n",
			"# keep\ntools: {\"web_search\":{\"permission\":{\"mode\":\"always_ask\"}},\"skill\":{}}\n" +
				"<<: {tools: {web_search: {permission: {mode: always_ask}}}}\ninstruction: help\n",
		},
		{
			"block with four spaces",
			"tools:\n    # keep\n    web_search: {}\ninstruction: |+\n  hello\n\n",
			"tools:\n    # keep\n    skill: {}\n    web_search: {}\ninstruction: |+\n  hello\n\n",
		},
		{
			"flow with unicode prefix",
			"{instruction: '😃', tools: {web_search: {}}}\n",
			"{instruction: '😃', tools: {skill: {}, web_search: {}}}\n",
		},
		{
			"empty flow",
			"tools: {} # keep\n",
			"tools: {skill: {}} # keep\n",
		},
		{
			"CRLF",
			"tools:\r\n  web_search: {}\r\n",
			"tools:\r\n  skill: {}\r\n  web_search: {}\r\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := AddSourceTools(SourceFormatYAML, []byte(test.source), []string{"skill"})
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != test.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, test.want)
			}
		})
	}
}

func TestCompileStoresDefaultToolsInSource(t *testing.T) {
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
		if len(parsed.Tools) != 11 || len(result.Compiled.Tools) != 11 {
			t.Fatalf("defaults were not saved: %s", result.Source)
		}
		if *parsed.Tools["run_command"].Enabled || parsed.Tools["run_command"].Permission.Mode != "always_ask" {
			t.Fatal("explicit override lost")
		}
		if tool := parsed.Tools["delete_machine"]; tool.Enabled != nil || tool.Permission != nil {
			t.Fatal("default entry redundantly specifies enablement or permission")
		}
		repeated, err := Compile(format, []byte(result.Source), testMachineSourceCompileOptions(t))
		if err != nil || repeated.Source != result.Source || repeated.Hash != result.Hash {
			t.Fatalf("normalization not idempotent: %v", err)
		}
	}
}
