package model

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/internal/modelcontext"
)

func flattenedSpec(t *testing.T, description, schema string) (modelcontext.ToolSpec, map[string]any) {
	t.Helper()
	got := ClaudeCompatibleToolSpecs([]modelcontext.ToolSpec{{
		Name:        "tool",
		Description: description,
		InputSchema: json.RawMessage(schema),
	}})
	require.Len(t, got, 1)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(got[0].InputSchema, &decoded))
	for _, keyword := range claudeRejectedCompositions {
		require.NotContains(t, decoded, keyword)
	}
	require.Equal(t, "object", decoded["type"])
	return got[0], decoded
}

func TestClaudeCompatibleToolSpecsDescribesRequiredOnlyAlternatives(t *testing.T) {
	spec, decoded := flattenedSpec(t, "Launches an agent.", `{
		"type": "object",
		"properties": {"profile": {"type": "string"}, "config": {"type": "string"}, "name": {"type": "string"}},
		"additionalProperties": false,
		"oneOf": [{"required": ["profile"]}, {"required": ["config", "name"]}],
		"not": {"required": ["name"]}
	}`)

	require.Equal(t, "Launches an agent. Provide exactly one of: profile; config + name.", spec.Description)
	require.Equal(t, false, decoded["additionalProperties"])
	require.Contains(t, decoded, "not")
	require.Len(t, decoded["properties"], 3)
	require.NotContains(t, decoded, "required")
}

func TestClaudeCompatibleToolSpecsKeepsArgumentsDefinedInAllOf(t *testing.T) {
	spec, decoded := flattenedSpec(t, "Searches.", `{
		"type": "object",
		"properties": {"limit": {"type": "integer"}},
		"allOf": [
			{"properties": {"query": {"type": "string"}}, "required": ["query"]},
			{"$ref": "#/$defs/paging"}
		],
		"$defs": {"paging": {"properties": {"cursor": {"type": "string"}}}}
	}`)

	require.Equal(t, "Searches.", spec.Description)
	require.Equal(t, map[string]any{
		"limit":  map[string]any{"type": "integer"},
		"query":  map[string]any{"type": "string"},
		"cursor": map[string]any{"type": "string"},
	}, decoded["properties"])
	require.Equal(t, []any{"query"}, decoded["required"])
}

func TestClaudeCompatibleToolSpecsKeepsArgumentsDefinedInAlternatives(t *testing.T) {
	spec, decoded := flattenedSpec(t, "Sends.", `{
		"properties": {"channel": {"type": "string"}},
		"required": ["channel"],
		"anyOf": [
			{"properties": {"text": {"type": "string"}, "kind": {"const": "text"}}, "required": ["text", "kind"]},
			{"properties": {"file_id": {"type": "string"}, "kind": {"const": "file"}}, "required": ["file_id", "kind"]}
		]
	}`)

	require.Equal(t, "Sends. Provide at least one of: text; file_id.", spec.Description)
	require.Equal(t, map[string]any{
		"channel": map[string]any{"type": "string"},
		"text":    map[string]any{"type": "string"},
		"file_id": map[string]any{"type": "string"},
		"kind":    map[string]any{"anyOf": []any{map[string]any{"const": "text"}, map[string]any{"const": "file"}}},
	}, decoded["properties"])
	require.Equal(t, []any{"channel", "kind"}, decoded["required"])
}

func TestClaudeCompatibleToolSpecsDoesNotMutateInput(t *testing.T) {
	plain := modelcontext.ToolSpec{Name: "plain", InputSchema: json.RawMessage(`{"type":"object"}`)}
	composed := modelcontext.ToolSpec{
		Name:        "composed",
		InputSchema: json.RawMessage(`{"anyOf":[{"required":["a"]},{"required":["b"]}]}`),
	}
	specs := []modelcontext.ToolSpec{plain, composed}

	got := ClaudeCompatibleToolSpecs(specs)
	require.Equal(t, plain, got[0])
	require.Contains(t, string(specs[1].InputSchema), "anyOf")
	require.NotContains(t, string(got[1].InputSchema), "anyOf")
}

func TestClaudeCompatibleToolSpecsLeavesPlainSchemas(t *testing.T) {
	specs := []modelcontext.ToolSpec{
		{Name: "nested", InputSchema: json.RawMessage(
			`{"type":"object","properties":{"a":{"anyOf":[{"type":"string"},{"type":"null"}]}}}`,
		)},
		{Name: "boolean", InputSchema: json.RawMessage(`true`)},
		{Name: "empty"},
	}

	require.Equal(t, specs, ClaudeCompatibleToolSpecs(specs))
}
