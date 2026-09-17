package agentconfig

import (
	"github.com/google/uuid"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestDefaultMachineTools(t *testing.T) {
	machineTools := []string{
		"create_machine", "delete_machine", "download_file", "inspect_machine",
		"list_machines", "list_processes", "read_process", "run_command",
		"stop_process", "upload_file", "write_process",
	}
	allDisabled := "tools:\n"
	byoDisabled := "tools:\n"
	for _, name := range machineTools {
		allDisabled += "  " + name + ": {enabled: false}\n"
		if name != "create_machine" && name != "delete_machine" {
			byoDisabled += "  " + name + ": {enabled: false}\n"
		}
	}
	machine := "machine_sources:\n  - machine_name: build-box\n"
	pool := "machine_sources:\n  - machine_pool_name: build-pool\n"
	skillID := testMachineSourcePublicID(t, publicid.KindSkill, "test-skill")
	tests := []struct {
		name      string
		source    string
		wantTools []string
		wantAsk   string
	}{
		{name: "no sources"},
		{name: "empty sources", source: "machine_sources: []\n"},
		{name: "BYO machine", source: machine, wantTools: machineTools[2:]},
		{name: "pool", source: pool, wantTools: machineTools},
		{
			name: "zero-count pool", source: pool + "    initial_num_machines: 0\n    max_machines: 0\n",
			wantTools: machineTools,
		},
		{
			name: "multiple sources", source: machine + "  - machine_pool_name: build-pool\n",
			wantTools: machineTools,
		},
		{
			name: "explicit permission", source: machine + "tools:\n  run_command: {permission: {mode: always_ask}}\n",
			wantTools: machineTools[2:], wantAsk: "run_command",
		},
		{
			name: "explicit pool permission", source: pool + "tools:\n  delete_machine: {permission: {mode: always_ask}}\n",
			wantTools: machineTools, wantAsk: "delete_machine",
		},
		{
			name: "explicit disable", source: pool + "tools:\n  create_machine: {enabled: false}\n",
			wantTools: machineTools[1:],
		},
		{
			name: "explicit pool tools on BYO", source: machine + "tools:\n  create_machine: {}\n  delete_machine: {}\n",
			wantTools: machineTools,
		},
		{name: "all BYO tools disabled", source: machine + byoDisabled},
		{name: "all pool tools disabled", source: pool + allDisabled},
		{
			name: "explicit tool without sources", source: "tools:\n  run_command: {}\n",
			wantTools: []string{"run_command"},
		},
		{
			name: "machines and skills", source: machine + "skills: [" + skillID + "]\n",
			wantTools: []string{
				"download_file", "inspect_machine",
				"list_machines", "list_processes", "read_process", "run_command", "skill",
				"stop_process", "upload_file", "write_process",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wantTools := slices.Clone(test.wantTools)
			if len(wantTools) > 0 {
				wantTools = append(wantTools, "read_file", "search_files")
				slices.Sort(wantTools)
			}
			for _, supportsTools := range []bool{true, false} {
				opts := testMachineSourceCompileOptions(t)
				opts.ResolveModelSelection = func(_, _ string) (ResolvedModelSelection, error) {
					return ResolvedModelSelection{ConfiguredModelID: publicidTestID(93), SupportsTools: &supportsTools}, nil
				}
				opts.ResolveSkillID = func(id string) (SkillResolution, error) {
					return SkillResolution{ID: uuid.Must(publicid.Decode(publicid.KindSkill, id)), Name: "test-skill"}, nil
				}
				source := validAgentSource(test.source)
				result, err := Compile(SourceFormatYAML, []byte(source), opts)
				if !supportsTools && len(test.wantTools) > 0 {
					if err == nil || !strings.Contains(err.Error(), "does not support tools") {
						t.Fatalf("compile without model tool support: %v", err)
					}
					continue
				}
				if err != nil {
					t.Fatalf("compile: %v", err)
				}
				contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, result.CompilerVersion, result.Hash)
				if err != nil {
					t.Fatalf("runtime contract: %v", err)
				}
				var got []string
				for _, tool := range contract.Tools {
					got = append(got, tool.Name)
					wantPermission := toolpermission.ModeAlwaysAllow
					if tool.Name == test.wantAsk {
						wantPermission = toolpermission.ModeAlwaysAsk
					}
					if tool.Permission.Mode != wantPermission {
						t.Fatalf("%s permission = %s, want %s", tool.Name, tool.Permission.Mode, wantPermission)
					}
				}
				if diff := cmp.Diff(wantTools, got); diff != "" {
					t.Fatalf("tools mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestDefaultRetrievalTools(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		want   []string
	}{
		{"empty", "", nil},
		{"disabled tool", "tools: {web_fetch: {enabled: false}}\n", nil},
		{"enabled tool", "tools: {web_fetch: {}}\n", []string{"read_file", "search_files", "web_fetch"}},
		{"MCP default disabled", "mcp: {docs: {url: https://example.com/mcp, default_enabled: false}}\n",
			[]string{"read_file", "search_files"}},
		{"retrieval disabled", "tools: {web_fetch: {}, read_file: {enabled: false}, search_files: {enabled: false}}\n",
			[]string{"web_fetch"}},
		{"one retrieval tool", "tools: {read_file: {}}\n", []string{"read_file", "search_files"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := Compile(SourceFormatYAML, []byte(validAgentSource(test.source)), CompileOptions{})
			require.NoError(t, err)
			require.Equal(t, validAgentSource(test.source), result.Source)
			contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, result.CompilerVersion, result.Hash)
			require.NoError(t, err)
			var names []string
			for _, tool := range contract.Tools {
				names = append(names, tool.Name)
				require.Contains(t, result.Compiled.Tools, tool.Name)
			}
			require.Equal(t, test.want, names)
		})
	}
}

func TestRuntimeDoesNotAddRetrievalTools(t *testing.T) {
	result, err := Compile(SourceFormatYAML, []byte(validAgentSource("tools: {web_fetch: {}}\n")), CompileOptions{})
	require.NoError(t, err)
	delete(result.Compiled.Tools, "read_file")
	delete(result.Compiled.Tools, "search_files")
	encoded, err := EncodeCompiled(result.Compiled)
	require.NoError(t, err)
	contract, err := RuntimeContractFromCompiled(encoded.CanonicalJSON, CompilerVersion, encoded.Hash)
	require.NoError(t, err)
	require.Len(t, contract.Tools, 1)
	require.Equal(t, "web_fetch", contract.Tools[0].Name)
}
