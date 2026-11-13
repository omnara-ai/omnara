package agentconfig

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/stretchr/testify/require"
)

func TestLegacyDefinitionVerificationKeepsLegacyRuntimeValues(t *testing.T) {
	source := validAgentSource(`tools: {lookup: {type: custom, description: Example,
 input_schema: {type: object, properties: {value: {const: 9007199254740993}}}}}`)
	compiled, err := Compile(SourceFormatYAML, []byte(source), CompileOptions{})
	require.NoError(t, err)
	exact := bytes.ReplaceAll(compiled.CanonicalJSON, []byte("9007199254740992"), []byte("9007199254740993"))
	exact, err = jsoncanonical.Canonical(exact)
	require.NoError(t, err)
	legacy := legacyCanonicalJSON(exact)
	require.NotEqual(t, exact, legacy)
	contract, err := RuntimeContractFromCompiled(exact, DefinitionHash(exact))
	require.NoError(t, err)
	for _, tool := range contract.Tools {
		if tool.Name == "lookup" {
			require.Contains(t, string(tool.InputSchema), "9007199254740993")
		}
	}
	var spaced bytes.Buffer
	require.NoError(t, json.Indent(&spaced, exact, "", "  "))
	stored := bytes.Clone(spaced.Bytes())
	contract, err = RuntimeContractFromCompiled(stored, DefinitionHash(legacy))
	require.Equal(t, spaced.Bytes(), stored)
	require.NoError(t, err)
	for _, tool := range contract.Tools {
		if tool.Name == "lookup" {
			require.Contains(t, string(tool.InputSchema), "9007199254740992")
		}
	}
	var changed map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(compiled.CanonicalJSON, &changed))
	changed["instruction"] = json.RawMessage(`"tampered"`)
	altered, err := json.Marshal(changed)
	require.NoError(t, err)
	_, err = RuntimeContractFromCompiled(altered, compiled.Hash)
	require.ErrorContains(t, err, "hash mismatch")
}
