package agentconfig

import (
	"github.com/google/uuid"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func TestMissingDefaultToolNamesDoesNotModifySource(t *testing.T) {
	source, _, err := parseSource(SourceFormatYAML, []byte(validAgentSource(
		"machine_sources: [{machine_pool_name: pool}]\ntools: {run_command: {enabled: false}}\n",
	)))
	if err != nil {
		t.Fatal(err)
	}
	names := missingDefaultToolNames(source)
	if len(names) != 12 {
		t.Fatalf("expected 12 missing pool and retrieval tools, got %v", names)
	}
	for _, name := range names {
		if _, configured := source.Tools[name]; configured {
			t.Fatalf("returned an already configured tool: %s", name)
		}
	}
	compiled, err := compileTools(source)
	if err != nil || len(compiled) != 13 || compiled["run_command"].Enabled {
		t.Fatalf("compile tools: %+v, %v", compiled, err)
	}
	if len(source.Tools) != 1 || *source.Tools["run_command"].Enabled {
		t.Fatal("source tools were modified")
	}
}

func TestResolvedToolsMatchRuntime(t *testing.T) {
	skillID := testMachineSourcePublicID(t, publicid.KindSkill, "preview-skill")
	for _, source := range []string{
		"",
		"mcp: {docs: {url: https://example.com/mcp, default_enabled: false}}\n",
		"machine_sources: [{machine_name: build-box}]\n",
		"machine_sources: [{machine_pool_name: build-pool, max_machines: 0, initial_num_machines: 0}]\n",
		"skills: [" + skillID + "]\n",
		"subagents: {worker: {type: self}}\n",
		"subagents: {worker: {type: self}}\nskills: [" + skillID + "]\ntools:\n  spawn_agent: {enabled: false}\n  read_agent: {permission: {mode: always_ask}}\n",
		"machine_sources: [{machine_pool_name: build-pool}]\nskills: [" + skillID + "]\ntools:\n  run_command: {enabled: false}\n  delete_machine: {permission: {mode: always_ask}}\n  skill: {enabled: false}\n  send_integration_message: {enabled: false}\n",
		"tools:\n  send_integration_message: {}\n  custom_tool: {type: custom, description: Test, input_schema: {type: object}, permission: {mode: always_ask}}\n",
	} {
		t.Run(source, func(t *testing.T) {
			opts := testMachineSourceCompileOptions(t)
			opts.ResolveSkillID = func(id string) (SkillResolution, error) {
				return SkillResolution{ID: uuid.Must(publicid.Decode(publicid.KindSkill, id)), Name: "test-skill"}, nil
			}
			raw := []byte(validAgentSource(source))
			result, err := Compile(SourceFormatYAML, raw, opts)
			if err != nil {
				t.Fatal(err)
			}
			{
				contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, result.CompilerVersion, result.Hash)
				if err != nil {
					t.Fatal(err)
				}
				entries, err := ToolsFromSource(SourceFormatYAML, []byte(result.Source))
				if err != nil {
					t.Fatal(err)
				}
				for _, format := range []SourceFormat{SourceFormatYAML, SourceFormatJSON} {
					previewSource := raw
					if format == SourceFormatJSON {
						previewSource, _, err = sourceJSON(SourceFormatYAML, raw)
						if err != nil {
							t.Fatal(err)
						}
					}
					preview, err := ToolsFromSource(format, previewSource)
					if err != nil {
						t.Fatal(err)
					}
					if diff := cmp.Diff(entries, preview); diff != "" {
						t.Fatalf("source/compiled mismatch: %s", diff)
					}
				}
				want := make(map[string]toolpermission.Selection)
				for _, tool := range contract.Tools {
					want[tool.Name] = tool.Permission
				}
				got := make(map[string]toolpermission.Selection)
				for _, entry := range entries {
					if entry.Enabled {
						got[entry.Name] = entry.Permission
					}
					if configured, exists := result.Compiled.Tools[entry.Name]; exists && configured.Enabled != entry.Enabled {
						t.Fatalf("explicit enablement lost: %s", entry.Name)
					}
				}
				if diff := cmp.Diff(want, got); diff != "" {
					t.Fatalf("preview/runtime mismatch: %s", diff)
				}
			}
		})
	}
}

func TestToolPreviewDrafts(t *testing.T) {
	raw := `{"instruction":"", "model":{}, "machine_sources":[{"machine_pool_name":"pool"}], "tools":{"run_command":{"enabled":false,"permission":{"mode":"always_ask"}}}}`
	entries, err := ToolsFromSource(SourceFormatJSON, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 13 {
		t.Fatalf("got %d entries, want 13", len(entries))
	}
	for _, entry := range entries {
		if entry.Name == "run_command" && (entry.Enabled || entry.Permission.Mode != toolpermission.ModeAlwaysAsk) {
			t.Fatalf("lost disabled permission: %+v", entry)
		}
	}
	for _, raw := range []string{
		`{}`, `{"instruction":"", "model":{}, "mcp":{}}`,
	} {
		tools, err := ToolsFromSource(SourceFormatJSON, []byte(raw))
		if err != nil || len(tools) != 0 {
			t.Fatalf("empty draft: %+v, %v", tools, err)
		}
	}
	for _, raw := range []string{
		`null`, `[]`, `{`, `{} {}`, `{"tools":null}`, `{"tools":{"run_command":{"enabled":"false"}}}`,
		`{"tools":{"not_registered":{}}}`, `{"tools":{"run_command":{"permission":{"mode":"bogus"}}}}`,
		`{"machine_sources":[{}]}`, `{"skills":["invalid"]}`, `{"tools":{"run_command":{"typo":true}}}`,
		`{"subagents":{"worker":{"type":"profile"}}}`, `{"subagents":{"worker":{"type":"unknown"}}}`,
	} {
		if _, err := ToolsFromSource(SourceFormatJSON, []byte(raw)); err == nil {
			t.Fatalf("accepted invalid tool source: %s", raw)
		}
	}
	if _, err := ToolsFromSource(SourceFormatYAML, []byte("tools: {}\n---\ntools: {}")); err == nil {
		t.Fatal("accepted multiple YAML documents")
	}
}
