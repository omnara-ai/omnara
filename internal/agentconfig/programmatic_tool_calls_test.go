package agentconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestProgrammaticToolCallsCompile(t *testing.T) {
	base, err := Compile(SourceFormatYAML, []byte(validAgentSource("")), CompileOptions{})
	require.NoError(t, err)
	for _, test := range []struct {
		name  string
		extra string
		want  *ProgrammaticToolCallsCompiled
		json  string
	}{
		{name: "absent"},
		{name: "disabled", extra: "programmatic_tool_calls: {enabled: false}"},
		{name: "disabled with exclude", extra: "programmatic_tool_calls: {enabled: false, exclude: [web_search]}"},
		{
			name:  "enabled",
			extra: "programmatic_tool_calls: {enabled: true}",
			want:  &ProgrammaticToolCallsCompiled{Enabled: true},
			json:  `{"enabled":true}`,
		},
		{
			name:  "enabled with empty exclude",
			extra: "programmatic_tool_calls: {enabled: true, exclude: []}",
			want:  &ProgrammaticToolCallsCompiled{Enabled: true},
			json:  `{"enabled":true}`,
		},
		{
			name: "enabled with exclude",
			extra: "programmatic_tool_calls: {enabled: true, exclude: " +
				"[web_search, mcp__github__create_issue, int__engineering-team__post_message, ask_question]}",
			want: &ProgrammaticToolCallsCompiled{Enabled: true, Exclude: []string{
				"ask_question", "int__engineering-team__post_message", "mcp__github__create_issue", "web_search",
			}},
			json: `{"enabled":true,"exclude":["ask_question","int__engineering-team__post_message",` +
				`"mcp__github__create_issue","web_search"]}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := Compile(SourceFormatYAML, []byte(validAgentSource(test.extra)), CompileOptions{})
			require.NoError(t, err)
			require.Equal(t, test.want, result.Compiled.ProgrammaticToolCalls)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(result.CanonicalJSON, &fields))
			if test.want == nil {
				require.NotContains(t, fields, "programmatic_tool_calls")
				require.Equal(t, base.Hash, result.Hash, "a disabled section compiles byte-identically to no section")
				require.Equal(t, string(base.CanonicalJSON), string(result.CanonicalJSON))
			} else {
				require.JSONEq(t, test.json, string(fields["programmatic_tool_calls"]))
				require.Equal(t, test.json, string(fields["programmatic_tool_calls"]))
			}
			contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, result.Hash)
			require.NoError(t, err)
			require.Equal(t, test.want, contract.ProgrammaticToolCalls)
			require.Equal(t, test.want != nil, contract.ProgrammaticToolCallsEnabled())
		})
	}
}

func TestProgrammaticToolCallsRejectInvalidSources(t *testing.T) {
	tooMany := make([]string, MaxProgrammaticToolCallExcludes+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("tool_%d", i)
	}
	for _, test := range []struct {
		name   string
		source string
		path   string
		want   string
	}{
		{name: "null", source: "null", path: "/programmatic_tool_calls"},
		{name: "missing enabled", source: "{}", path: "/programmatic_tool_calls/enabled"},
		{name: "enabled not boolean", source: "{enabled: yes please}", path: "/programmatic_tool_calls/enabled"},
		{name: "unknown field", source: "{enabled: true, include: [web_search]}", path: "/programmatic_tool_calls/include"},
		{name: "exclude not array", source: "{enabled: true, exclude: web_search}", path: "/programmatic_tool_calls/exclude"},
		{
			name:   "empty name",
			source: "{enabled: true, exclude: [web_search, '']}",
			path:   "/programmatic_tool_calls/exclude/1",
		},
		{
			name:   "name too long",
			source: "{enabled: true, exclude: [" + strings.Repeat("a", 65) + "]}",
			path:   "/programmatic_tool_calls/exclude/0",
		},
		{
			name:   "invalid built-in name",
			source: "{enabled: true, exclude: [web-search]}",
			path:   "/programmatic_tool_calls/exclude/0",
			want:   "must match",
		},
		{
			name:   "invalid mcp name",
			source: "{enabled: true, exclude: [web_search, mcp__github]}",
			path:   "/programmatic_tool_calls/exclude/1",
			want:   "mcp__<server>__<tool>",
		},
		{
			name:   "invalid mcp server key",
			source: "{enabled: true, exclude: [mcp__git_hub__create_issue]}",
			path:   "/programmatic_tool_calls/exclude/0",
			want:   "mcp__<server>__<tool>",
		},
		{
			name:   "invalid integration name",
			source: "{enabled: true, exclude: [int__reviews]}",
			path:   "/programmatic_tool_calls/exclude/0",
			want:   "int__<integration>__<operation>",
		},
		{
			name:   "duplicate",
			source: "{enabled: true, exclude: [web_search, run_command, web_search]}",
			path:   "/programmatic_tool_calls/exclude/2",
			want:   `duplicate tool "web_search"`,
		},
		{
			name:   "duplicate when disabled",
			source: "{enabled: false, exclude: [web_search, web_search]}",
			path:   "/programmatic_tool_calls/exclude/1",
			want:   `duplicate tool "web_search"`,
		},
		{
			name:   "too many",
			source: "{enabled: true, exclude: [" + strings.Join(tooMany, ", ") + "]}",
			path:   "/programmatic_tool_calls/exclude",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Compile(
				SourceFormatYAML, []byte(validAgentSource("programmatic_tool_calls: "+test.source)), CompileOptions{},
			)
			var validationErr *ValidationError
			require.True(t, errors.As(err, &validationErr), "error %v", err)
			paths := make([]string, 0, len(validationErr.Issues))
			for _, issue := range validationErr.Issues {
				paths = append(paths, issue.Path)
			}
			require.Contains(t, paths, test.path, "error %v", err)
			if test.want != "" {
				require.ErrorContains(t, err, test.want)
			}
		})
	}
}

func TestProgrammaticToolCallsRoundTripThroughSource(t *testing.T) {
	result, err := Compile(SourceFormatYAML, []byte(validAgentSource(
		"programmatic_tool_calls: {enabled: true, exclude: [web_search, mcp__github__create_issue]}",
	)), CompileOptions{})
	require.NoError(t, err)
	source, err := SourceFromCompiled(result.Compiled, SourceNames{
		Model: func(uuid.UUID) (string, string, error) { return "openai-prod", "gpt-test", nil },
	})
	require.NoError(t, err)
	require.Equal(t, &ProgrammaticToolCallsSource{
		Enabled: true, Exclude: []string{"mcp__github__create_issue", "web_search"},
	}, source.ProgrammaticToolCalls)
	for _, format := range []SourceFormat{SourceFormatYAML, SourceFormatJSON} {
		encoded, err := EncodeSource(source, format)
		require.NoError(t, err)
		recompiled, err := Compile(format, []byte(encoded), CompileOptions{})
		require.NoError(t, err)
		require.Equal(t, result.Hash, recompiled.Hash)
	}
}

func TestProgrammaticToolCallsAreNotInheritedBySelfSubagents(t *testing.T) {
	result, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
programmatic_tool_calls: {enabled: true, exclude: [web_search]}
subagents:
  fork: {type: self}
`)), subagentCompileOptions())
	require.NoError(t, err)
	require.NotNil(t, result.Compiled.ProgrammaticToolCalls)
	child := SubagentCompiledFrom(result.Compiled, result.Compiled.Subagents["fork"], SubagentDepth{Depth: 1})
	require.Nil(t, child.ProgrammaticToolCalls)
	require.NotNil(t, result.Compiled.ProgrammaticToolCalls, "deriving a subagent leaves the parent unchanged")
	encoded, err := EncodeCompiled(child)
	require.NoError(t, err)
	require.NotContains(t, string(encoded.CanonicalJSON), "programmatic_tool_calls")
}

func TestRuntimeContractProgrammaticToolExcluded(t *testing.T) {
	require.True(t, RuntimeContract{}.ProgrammaticToolExcluded("web_search"), "disabled excludes every tool")
	contract := RuntimeContract{ProgrammaticToolCalls: &ProgrammaticToolCallsCompiled{
		Enabled: true, Exclude: []string{"mcp__github__create_issue", "web_search"},
	}}
	require.True(t, contract.ProgrammaticToolExcluded("web_search"))
	require.True(t, contract.ProgrammaticToolExcluded("mcp__github__create_issue"))
	require.False(t, contract.ProgrammaticToolExcluded("web_fetch"))
	require.False(t, contract.ProgrammaticToolExcluded("run_command"))
}

func TestRuntimeContractRejectsInvalidCompiledProgrammaticToolCalls(t *testing.T) {
	base := `{"instruction":"Help.","model":{},"programmatic_tool_calls":%s}`
	for _, test := range []struct {
		section string
		valid   bool
	}{
		{section: `{"enabled":true}`, valid: true},
		{section: `{"enabled":true,"exclude":["a_tool","b_tool"]}`, valid: true},
		{section: `{"enabled":false}`},
		{section: `{"enabled":true,"exclude":["b_tool","a_tool"]}`},
		{section: `{"enabled":true,"exclude":["a_tool","a_tool"]}`},
		{section: `{"enabled":true,"exclude":["bad-name"]}`},
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
