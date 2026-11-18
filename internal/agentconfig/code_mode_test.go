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
