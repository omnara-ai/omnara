package model

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/internal/modelcontext"
)

func TestClaudeCompatibleToolSpecsDropsTopLevelCompositions(t *testing.T) {
	plain := modelcontext.ToolSpec{Name: "plain", InputSchema: json.RawMessage(`{"type":"object"}`)}
	launch := modelcontext.ToolSpec{
		Name:        "launch",
		Description: "Launches an agent.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {"profile": {"type": "string"}, "config": {"type": "string"}, "name": {"type": "string"}},
			"additionalProperties": false,
			"oneOf": [{"required": ["profile"]}, {"required": ["config", "name"]}],
			"not": {"required": ["name"]}
		}`),
	}
	specs := []modelcontext.ToolSpec{plain, launch}

	got := ClaudeCompatibleToolSpecs(specs)
	require.Len(t, got, 2)
	require.Equal(t, plain, got[0])
	require.Equal(t, "Launches an agent. Provide exactly one of: profile; config + name.", got[1].Description)
	require.Contains(t, string(specs[1].InputSchema), "oneOf", "input specs are not mutated")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(got[1].InputSchema, &decoded))
	require.NotContains(t, decoded, "oneOf")
	require.Equal(t, "object", decoded["type"])
	require.Equal(t, false, decoded["additionalProperties"])
	require.Contains(t, decoded, "not")
	require.Len(t, decoded["properties"], 3)
}

func TestClaudeCompatibleToolSpecsDescribesOnlyRequiredClauses(t *testing.T) {
	specs := []modelcontext.ToolSpec{{
		Name:        "mixed",
		Description: "Mixed.",
		InputSchema: json.RawMessage(`{"anyOf": [{"properties": {"a": {"type": "string"}}}, {"required": ["b"]}]}`),
	}}

	got := ClaudeCompatibleToolSpecs(specs)
	require.Equal(t, "Mixed.", got[0].Description)
	require.JSONEq(t, `{"type": "object"}`, string(got[0].InputSchema))
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
