package modelcontext

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProviderSafeMCPInputSchemaDropsTopLevelCompositions(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"profile": {"type": "string"}, "config": {"type": "string"}, "name": {"type": "string"}},
		"additionalProperties": false,
		"oneOf": [{"required": ["profile"]}, {"required": ["config", "name"]}],
		"not": {"required": ["name"]}
	}`)

	got, note, err := providerSafeMCPInputSchema(schema)
	require.NoError(t, err)
	require.Equal(t, "Provide exactly one of: profile; config + name.", note)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(got, &decoded))
	require.NotContains(t, decoded, "oneOf")
	require.Equal(t, "object", decoded["type"])
	require.Equal(t, false, decoded["additionalProperties"])
	require.Contains(t, decoded, "not")
	require.Len(t, decoded["properties"], 3)
}

func TestProviderSafeMCPInputSchemaDescribesOnlyRequiredClauses(t *testing.T) {
	schema := json.RawMessage(`{"anyOf": [{"properties": {"a": {"type": "string"}}}, {"required": ["b"]}]}`)

	got, note, err := providerSafeMCPInputSchema(schema)
	require.NoError(t, err)
	require.Empty(t, note)
	require.JSONEq(t, `{"type": "object"}`, string(got))
}

func TestProviderSafeMCPInputSchemaLeavesPlainSchemas(t *testing.T) {
	for _, schema := range []string{
		`{"type":"object","properties":{"a":{"anyOf":[{"type":"string"},{"type":"null"}]}}}`,
		`true`,
	} {
		got, note, err := providerSafeMCPInputSchema(json.RawMessage(schema))
		require.NoError(t, err)
		require.Empty(t, note)
		require.Equal(t, schema, string(got))
	}
}
