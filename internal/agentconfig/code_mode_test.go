package agentconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodeModeCompile(t *testing.T) {
	base, err := Compile(SourceFormatYAML, []byte(validAgentSource("")), CompileOptions{})
	require.NoError(t, err)
	for _, test := range []struct {
		name  string
		extra string
		want  *CodeModeCompiled
	}{
		{name: "absent"},
		{name: "disabled", extra: "code_mode: {enabled: false}"},
		{name: "enabled", extra: "code_mode: {enabled: true}", want: &CodeModeCompiled{Enabled: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := Compile(SourceFormatYAML, []byte(validAgentSource(test.extra)), CompileOptions{})
			require.NoError(t, err)
			require.Equal(t, test.want, result.Compiled.CodeMode)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(result.CanonicalJSON, &fields))
			if test.want == nil {
				require.NotContains(t, fields, "code_mode")
				require.Equal(t, base.Hash, result.Hash, "code mode off compiles byte-identically to no section")
				require.Equal(t, string(base.CanonicalJSON), string(result.CanonicalJSON))
			} else {
				require.Equal(t, `{"enabled":true}`, string(fields["code_mode"]))
			}
			contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, result.Hash)
			require.NoError(t, err)
			require.Equal(t, test.want, contract.CodeMode)
			require.Equal(t, test.want != nil, contract.CodeModeEnabled())
		})
	}
}

func TestCodeModeRejectsInvalidSources(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		path   string
	}{
		{name: "null", source: "null", path: "/code_mode"},
		{name: "missing enabled", source: "{}", path: "/code_mode/enabled"},
		{name: "enabled not boolean", source: "{enabled: code_mode}", path: "/code_mode/enabled"},
		{name: "unknown field", source: "{enabled: true, tools: [web_search]}", path: "/code_mode/tools"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Compile(
				SourceFormatYAML, []byte(validAgentSource("code_mode: "+test.source)), CompileOptions{},
			)
			var validationErr *ValidationError
			require.True(t, errors.As(err, &validationErr), "error %v", err)
			paths := make([]string, 0, len(validationErr.Issues))
			for _, issue := range validationErr.Issues {
				paths = append(paths, issue.Path)
			}
			require.Contains(t, paths, test.path, "error %v", err)
		})
	}
}

func TestCodeModeRoundTripsThroughSource(t *testing.T) {
	result, err := Compile(SourceFormatYAML, []byte(validAgentSource("code_mode: {enabled: true}")),
		CompileOptions{})
	require.NoError(t, err)
	source, err := SourceFromCompiled(result.Compiled, SourceNames{
		Model: func(uuid.UUID) (string, string, error) { return "openai-prod", "gpt-test", nil },
	})
	require.NoError(t, err)
	enabled := true
	require.Equal(t, &CodeModeSource{Enabled: &enabled}, source.CodeMode)
	for _, format := range []SourceFormat{SourceFormatYAML, SourceFormatJSON} {
		encoded, err := EncodeSource(source, format)
		require.NoError(t, err)
		recompiled, err := Compile(format, []byte(encoded), CompileOptions{})
		require.NoError(t, err)
		require.Equal(t, result.Hash, recompiled.Hash)
	}
}

func TestCodeModeIsInheritedBySubagents(t *testing.T) {
	result, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
code_mode: {enabled: true}
subagents:
  fork: {type: self}
`)), subagentCompileOptions())
	require.NoError(t, err)
	child := SubagentCompiledFrom(result.Compiled, result.Compiled.Subagents["fork"], SubagentDepth{Depth: 1})
	require.Equal(t, &CodeModeCompiled{Enabled: true}, child.CodeMode)
	encoded, err := EncodeCompiled(child)
	require.NoError(t, err)
	contract, err := RuntimeContractFromCompiled(encoded.CanonicalJSON, encoded.Hash)
	require.NoError(t, err)
	require.True(t, contract.CodeModeEnabled())
}

func TestRuntimeContractRejectsInvalidCompiledCodeMode(t *testing.T) {
	base := `{"instruction":"Help.","model":{},"code_mode":%s}`
	for _, test := range []struct {
		section string
		valid   bool
	}{
		{section: `{"enabled":true}`, valid: true},
		{section: `{"enabled":false}`},
		{section: `{}`},
		{section: `{"enabled":true,"unknown":true}`},
	} {
		t.Run(test.section, func(t *testing.T) {
			encoded := fmt.Appendf(nil, base, test.section)
			_, err := RuntimeContractFromCompiled(encoded, hashJSON(encoded))
			if test.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestCodeModeDefaultAllowedCompilesOnlyWhenFalse(t *testing.T) {
	on, err := Compile(SourceFormatYAML, []byte(validAgentSource("code_mode: {enabled: true}")), CompileOptions{})
	require.NoError(t, err)
	explicit, err := Compile(SourceFormatYAML,
		[]byte(validAgentSource("code_mode: {enabled: true, default_allowed: true}")), CompileOptions{})
	require.NoError(t, err)
	require.Equal(t, on.Hash, explicit.Hash)

	denied, err := Compile(SourceFormatYAML,
		[]byte(validAgentSource("code_mode: {enabled: true, default_allowed: false}")), CompileOptions{})
	require.NoError(t, err)
	require.Equal(t, &CodeModeCompiled{Enabled: true, DefaultAllowed: new(false)}, denied.Compiled.CodeMode)
	source, err := SourceFromCompiled(denied.Compiled, SourceNames{
		Model: func(uuid.UUID) (string, string, error) { return "openai-prod", "gpt-test", nil },
	})
	require.NoError(t, err)
	encoded, err := EncodeSource(source, SourceFormatYAML)
	require.NoError(t, err)
	recompiled, err := Compile(SourceFormatYAML, []byte(encoded), CompileOptions{})
	require.NoError(t, err)
	require.Equal(t, denied.Hash, recompiled.Hash)

	invalid := []byte(`{"instruction":"Help.","model":{},"code_mode":{"enabled":true,"default_allowed":true}}`)
	_, err = RuntimeContractFromCompiled(invalid, hashJSON(invalid))
	require.Error(t, err)
}

func TestCodeModePerToolControl(t *testing.T) {
	const tools = `
tools:
  web_search: {deferred: true}
  web_fetch: {code_mode: %t}
  storage_read: {deferred: true, code_mode: %t}
  run_command: {}
mcp:
  crm:
    url: https://example.com/mcp
    code_mode: %t
    deferred: true
    tools:
      search: {code_mode: %t}
  docs:
    url: https://example.com/docs
    tools:
      lookup: {code_mode: %t}
`
	for _, test := range []struct {
		name           string
		codeMode       string
		defaultAllowed bool
		on             bool
	}{
		{name: "allowed by default", codeMode: "code_mode: {enabled: true}", defaultAllowed: true, on: true},
		{name: "denied by default", codeMode: "code_mode: {enabled: true, default_allowed: false}", on: true},
		{name: "code mode off", codeMode: "code_mode: {enabled: false}"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Each explicit setting is the opposite of the default, so an ignored override fails.
			override := !test.defaultAllowed
			contract := compileRuntimeContract(t, test.codeMode+
				fmt.Sprintf(tools, override, override, override, !override, override))
			want := map[string]bool{
				"web_search":   test.on && test.defaultAllowed,
				"web_fetch":    test.on && override,
				"storage_read": test.on && override,
				"run_command":  false,
			}
			for name, callable := range want {
				tool, ok := runtimeToolByName(contract, name)
				require.True(t, ok, name)
				require.Equal(t, callable, tool.CodeMode, name)
			}
			webSearch, _ := runtimeToolByName(contract, "web_search")
			require.True(t, webSearch.Deferred, "code mode does not change deferral")

			mcp := func(server RuntimeMCPServer, remoteName string) bool {
				resolution, ok := server.ResolveTool(remoteName)
				require.True(t, ok)
				return contract.CodeModeAllows(resolution.CodeMode)
			}
			crm, docs := contract.MCPServers[0], contract.MCPServers[1]
			require.Equal(t, test.on && override, mcp(crm, "other"), "server setting")
			require.Equal(t, test.on && !override, mcp(crm, "search"), "tool setting beats the server's")
			require.Equal(t, test.on && override, mcp(docs, "lookup"), "tool setting beats the default")
			require.Equal(t, test.on && test.defaultAllowed, mcp(docs, "other"), "agent default")
		})
	}
}

func TestCodeModeSettingRejectedOnToolsThatDoNotSupportIt(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		path   string
	}{
		{
			name:   "built-in without code mode",
			source: "tools:\n  run_command: {code_mode: true}\n",
			path:   "/tools/run_command/code_mode",
		},
		{
			name:   "disallowing an unsupported built-in",
			source: "tools:\n  run_command: {code_mode: false}\n",
			path:   "/tools/run_command/code_mode",
		},
		{
			name: "custom tool",
			source: "tools:\n  create_ticket:\n    type: custom\n    code_mode: true\n" +
				"    description: Create a ticket.\n    input_schema: {type: object}\n",
			path: "/tools/create_ticket/code_mode",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Compile(SourceFormatYAML,
				[]byte(validAgentSource("code_mode: {enabled: true}\n"+test.source)), CompileOptions{})
			var validationErr *ValidationError
			require.True(t, errors.As(err, &validationErr), "error %v", err)
			paths := make([]string, 0, len(validationErr.Issues))
			for _, issue := range validationErr.Issues {
				paths = append(paths, issue.Path)
			}
			require.Contains(t, paths, test.path, "error %v", err)
		})
	}
}

func TestCodeModeToolSettingsRoundTripThroughSource(t *testing.T) {
	result, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
code_mode: {enabled: true, default_allowed: false}
tools:
  web_fetch: {code_mode: true}
mcp:
  crm:
    url: https://example.com/mcp
    code_mode: true
    tools:
      delete: {code_mode: false}
`)), CompileOptions{})
	require.NoError(t, err)
	source, err := SourceFromCompiled(result.Compiled, SourceNames{
		Model: func(uuid.UUID) (string, string, error) { return "openai-prod", "gpt-test", nil },
	})
	require.NoError(t, err)
	require.Equal(t, new(true), source.Tools["web_fetch"].CodeMode)
	require.Equal(t, new(true), source.MCP["crm"].CodeMode)
	require.Equal(t, new(false), source.MCP["crm"].Tools["delete"].CodeMode)
	for _, format := range []SourceFormat{SourceFormatYAML, SourceFormatJSON} {
		encoded, err := EncodeSource(source, format)
		require.NoError(t, err)
		recompiled, err := Compile(format, []byte(encoded), CompileOptions{})
		require.NoError(t, err)
		require.Equal(t, result.Hash, recompiled.Hash)
	}
}
