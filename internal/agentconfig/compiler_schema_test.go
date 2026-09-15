package agentconfig

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/stretchr/testify/require"
)

func TestCompileCustomToolValidatesTheFullExecutionSchema(t *testing.T) {
	t.Parallel()
	for name, schema := range map[string]string{
		"nested type":              `{"type":"object","properties":{"value":{"type":"not-a-type"}}}`,
		"nested numeric assertion": `{"type":"object","properties":{"value":{"type":"number","minimum":"zero"}}}`,
		"invalid regex":            `{"type":"object","properties":{"value":{"type":"string","pattern":"["}}}`,
		"composition":              `{"type":"object","allOf":[{"properties":{"value":{"type":123}}}]}`,
		"empty union":              `{"type":"object","oneOf":[]}`,
		"conditional":              `{"type":"object","if":{"required":true},"then":false}`,
		"invalid closure":          `{"type":"object","additionalProperties":"no"}`,
		"missing local ref":        `{"type":"object","properties":{"value":{"$ref":"#/$defs/missing"}}}`,
		"remote ref":               `{"type":"object","properties":{"value":{"$ref":"https://example.invalid/schema"}}}`,
		"file ref":                 `{"type":"object","properties":{"value":{"$ref":"file:///nonexistent-schema.json"}}}`,
		"duplicate required":       `{"type":"object","properties":{"value":{}},"required":["value","value"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			source := validAgentSource("tools:\n  custom:\n    type: custom\n    description: Custom tool.\n" +
				"    input_schema: " + schema + "\n")
			_, err := Compile(SourceFormatYAML, []byte(source), CompileOptions{})
			require.Error(t, err)
			var validation *ValidationError
			require.True(t, errors.As(err, &validation), "%v", err)
			require.NotEmpty(t, validation.Issues)
			require.Equal(t, "/tools/custom/input_schema", validation.Issues[0].Path)
			require.Greater(t, validation.Issues[0].Line, 0)
		})
	}
}

func TestCompileCustomToolPreservesLocalComposedExecutionSchema(t *testing.T) {
	t.Parallel()
	schema := `{"type":"object","$defs":{"value":{"type":"integer","minimum":2}},"properties":{"value":{"$ref":"#/$defs/value"},"kind":{"enum":["a","b"]}},"required":["kind"],"allOf":[{"if":{"properties":{"kind":{"const":"a"}}},"then":{"required":["value"]}}],"additionalProperties":false}`
	source := validAgentSource("tools:\n  custom:\n    type: custom\n    description: Custom tool.\n" +
		"    permission:\n      mode: always_ask\n    input_schema: " + schema + "\n")
	result, err := Compile(SourceFormatYAML, []byte(source), CompileOptions{})
	require.NoError(t, err)
	stored := result.Compiled.Tools["custom"].InputSchema
	require.JSONEq(t, schema, string(stored))
	require.False(t, strings.Contains(string(stored), "omnara_channel"), "source compilation retains the execution schema")
	contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, result.CompilerVersion, result.Hash)
	require.NoError(t, err)
	require.Len(t, contract.Tools, 1)
	require.Equal(t, stored, contract.Tools[0].InputSchema)
	for input, valid := range map[string]bool{
		`{"kind":"a","value":2}`:      true,
		`{"kind":"b"}`:                true,
		`{"kind":"a"}`:                false,
		`{"kind":"a","value":1}`:      false,
		`{"kind":"b","unknown":true}`: false,
	} {
		err := jsonschema.Validate(stored, json.RawMessage(input))
		require.Equal(t, valid, err == nil, "input %s: %v", input, err)
	}
}
