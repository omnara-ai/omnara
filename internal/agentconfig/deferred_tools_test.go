package agentconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestDeferredToolsCompile(t *testing.T) {
	base, err := Compile(SourceFormatYAML, []byte(validAgentSource("")), CompileOptions{})
	require.NoError(t, err)
	for _, test := range []struct {
		name  string
		extra string
		want  *DeferredToolsCompiled
	}{
		{name: "absent"},
		{name: "tool_add", extra: "deferred_tools: {mode: tool_add}"},
		{
			name:  "code_mode",
			extra: "deferred_tools: {mode: code_mode}",
			want:  &DeferredToolsCompiled{Mode: DeferredToolsModeCodeMode},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := Compile(SourceFormatYAML, []byte(validAgentSource(test.extra)), CompileOptions{})
			require.NoError(t, err)
			require.Equal(t, test.want, result.Compiled.DeferredTools)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(result.CanonicalJSON, &fields))
			if test.want == nil {
				require.NotContains(t, fields, "deferred_tools")
				require.Equal(t, base.Hash, result.Hash, "the default mode compiles byte-identically to no section")
				require.Equal(t, string(base.CanonicalJSON), string(result.CanonicalJSON))
			} else {
				require.Equal(t, `{"mode":"code_mode"}`, string(fields["deferred_tools"]))
			}
			contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, result.Hash)
			require.NoError(t, err)
			require.Equal(t, test.want, contract.DeferredTools)
			require.Equal(t, test.want != nil, contract.CodeMode())
		})
	}
}

func TestDeferredToolsRejectInvalidSources(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		path   string
	}{
		{name: "null", source: "null", path: "/deferred_tools"},
		{name: "missing mode", source: "{}", path: "/deferred_tools/mode"},
		{name: "unknown mode", source: "{mode: programmatic}", path: "/deferred_tools/mode"},
		{name: "mode not string", source: "{mode: true}", path: "/deferred_tools/mode"},
		{name: "unknown field", source: "{mode: code_mode, exclude: [web_search]}", path: "/deferred_tools/exclude"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Compile(
				SourceFormatYAML, []byte(validAgentSource("deferred_tools: "+test.source)), CompileOptions{},
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

func TestDeferredToolsRoundTripThroughSource(t *testing.T) {
	result, err := Compile(SourceFormatYAML, []byte(validAgentSource("deferred_tools: {mode: code_mode}")),
		CompileOptions{})
	require.NoError(t, err)
	source, err := SourceFromCompiled(result.Compiled, SourceNames{
		Model: func(uuid.UUID) (string, string, error) { return "openai-prod", "gpt-test", nil },
	})
	require.NoError(t, err)
	require.Equal(t, &DeferredToolsSource{Mode: DeferredToolsModeCodeMode}, source.DeferredTools)
	for _, format := range []SourceFormat{SourceFormatYAML, SourceFormatJSON} {
		encoded, err := EncodeSource(source, format)
		require.NoError(t, err)
		recompiled, err := Compile(format, []byte(encoded), CompileOptions{})
		require.NoError(t, err)
		require.Equal(t, result.Hash, recompiled.Hash)
	}
}

func TestDeferredToolsModeIsNotInheritedBySelfSubagents(t *testing.T) {
	result, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
deferred_tools: {mode: code_mode}
subagents:
  fork: {type: self}
`)), subagentCompileOptions())
	require.NoError(t, err)
	require.NotNil(t, result.Compiled.DeferredTools)
	child := SubagentCompiledFrom(result.Compiled, result.Compiled.Subagents["fork"], SubagentDepth{Depth: 1})
	require.Nil(t, child.DeferredTools)
	require.NotNil(t, result.Compiled.DeferredTools, "deriving a subagent leaves the parent unchanged")
	encoded, err := EncodeCompiled(child)
	require.NoError(t, err)
	require.NotContains(t, string(encoded.CanonicalJSON), "deferred_tools")
}

func TestRuntimeContractRejectsInvalidCompiledDeferredTools(t *testing.T) {
	base := `{"instruction":"Help.","model":{},"deferred_tools":%s}`
	for _, test := range []struct {
		section string
		valid   bool
	}{
		{section: `{"mode":"code_mode"}`, valid: true},
		{section: `{"mode":"tool_add"}`},
		{section: `{"mode":"other"}`},
		{section: `{"mode":"code_mode","unknown":true}`},
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
