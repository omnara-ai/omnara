package agentconfig

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuntimeContractPreservesExactValues(t *testing.T) {
	source := validAgentSource(`tools: {lookup: {type: custom, description: Example,
 input_schema: {type: object, properties: {value: {const: 9007199254740993}}}}}`)
	compiled, err := Compile(SourceFormatYAML, []byte(source), CompileOptions{})
	require.NoError(t, err)
	var spaced bytes.Buffer
	require.NoError(t, json.Indent(&spaced, compiled.CanonicalJSON, "", "  "))
	stored := bytes.Clone(spaced.Bytes())
	contract, err := RuntimeContractFromCompiled(stored, compiled.Hash)
	require.NoError(t, err)
	require.Equal(t, spaced.Bytes(), stored)
	tool, found := runtimeToolByName(contract, "lookup")
	require.True(t, found)
	require.Contains(t, string(tool.InputSchema), "9007199254740993")
	changed := bytes.ReplaceAll(compiled.CanonicalJSON, []byte("9007199254740993"), []byte("9007199254740992"))
	_, err = RuntimeContractFromCompiled(stored, DefinitionHash(changed))
	require.ErrorContains(t, err, "hash mismatch")
}
