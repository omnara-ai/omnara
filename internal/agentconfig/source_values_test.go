package agentconfig

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/stretchr/testify/require"
)

func TestSourceValuesSurviveCompilationRuntimeAndExport(t *testing.T) {
	schema := `{"type":"object","properties":{"<<":{"type":"string"},"value":{
 "enum":["1e400","-1e999","1e309","\u007f","\u0085",9007199254740993,0.123456789012345678901,-9223372036854775808,-0.0,1e-7]}}}`
	tool := `{"type":"custom","description":"Example lookup","input_schema":` + schema + `}`
	sources := map[SourceFormat]string{
		SourceFormatYAML: "instruction: Help.\nmodel: {provider_config: example, name: primary}\n" +
			"max_depth: 1.0\ntools: {lookup: " + tool + "}\n",
		SourceFormatJSON: `{"instruction":"Help.","model":{"provider_config":"example","name":"primary"},
 "max_depth":1.0,"tools":{"lookup":` + tool + `}}`,
	}
	var hash string
	for format, source := range sources {
		compiled, err := Compile(format, []byte(source), CompileOptions{})
		require.NoError(t, err)
		if hash == "" {
			hash = compiled.Hash
		}
		require.Equal(t, hash, compiled.Hash)
		require.Equal(t, source, compiled.Source)
		require.True(t, jsoncanonical.Equal([]byte(schema), compiled.Compiled.Tools["lookup"].InputSchema))
		contract, err := RuntimeContractFromCompiled(compiled.CanonicalJSON, compiled.Hash)
		require.NoError(t, err)
		tool, found := runtimeToolByName(contract, "lookup")
		require.True(t, found)
		require.True(t, jsoncanonical.Equal([]byte(schema), tool.InputSchema))
		exported, err := SourceFromCompiled(compiled.Compiled, SourceNames{
			Model: func(uuid.UUID) (string, string, error) { return "example", "primary", nil },
		})
		require.NoError(t, err)
		for _, exportFormat := range []SourceFormat{SourceFormatYAML, SourceFormatJSON} {
			text, err := EncodeSource(exported, exportFormat)
			require.NoError(t, err)
			recompiled, err := Compile(exportFormat, []byte(text), CompileOptions{})
			require.NoError(t, err)
			require.Equal(t, compiled.Hash, recompiled.Hash)
		}
	}
}

func TestYAMLValueSemanticsAndLimits(t *testing.T) {
	value, err := sourceJSONValueForTest(`
base: &base {large: 9007199254740993, decimal: 0.123456789012345678901, replace: 1}
merged: {<<: *base, replace: 2}
values: [010, 0x10, 0b10, 0o10, +1_000, .5, 01.2, 1.e2, 2026-01-02, "1e400"]
strings: [_1, ._5, .5_, 0xFFFFFFFFFFFFFFFFFF, !!str 1e400]
exact: [0777777777777777777777777, !!float 9007199254740993, !!float 010]
sequence: {<<: [{x: 1, y: 2}, {x: 3, z: 4}], y: null}`)
	require.NoError(t, err)
	require.True(t, jsoncanonical.Equal(value, []byte(`
{"base":{"large":9007199254740993,"decimal":0.123456789012345678901,"replace":1},
"merged":{"large":9007199254740993,"decimal":0.123456789012345678901,"replace":2},
"values":[8,16,2,8,1000,0.5,1.2,100,"2026-01-02T00:00:00Z","1e400"],
"strings":["_1","._5",".5_","0xFFFFFFFFFFFFFFFFFF","1e400"],
"exact":[777777777777777777777777,9007199254740993,8],
"sequence":{"x":1,"y":null,"z":4}}`)))
	for _, source := range []string{
		"a: &a [*a]", "a: {1: value}", "a: {b: 1, b: 2}", "a: 1e400",
		"a: 1e-400", "a: .nan", "a: !!int 1.5", "a: !!float nope",
		"a: {&key x: 1, *key: 2}", "a: {<<: {? [x]: y}}", "a: {1: x, <<: {? [x]: y}}",
		"a: &a {<<: *a}",
	} {
		_, err := sourceJSONValueForTest(source)
		require.Error(t, err, source)
	}
	var expansion strings.Builder
	expansion.WriteString("base: &base {\n")
	for index := range 300 {
		fmt.Fprintf(&expansion, "key%d: value,\n", index)
	}
	expansion.WriteString("}\nexpanded: [" + strings.Repeat("*base,", 300) + "]")
	_, err = sourceJSONValueForTest(expansion.String())
	require.ErrorContains(t, err, "excessive aliasing")
	_, err = ParseSource(SourceFormatYAML, []byte("instruction: first\ninstruction: second\n"))
	var duplicate *ValidationError
	require.ErrorAs(t, err, &duplicate)
	require.Positive(t, duplicate.Issues[0].Line)
	for _, format := range []SourceFormat{SourceFormatJSON, SourceFormatYAML} {
		_, err := ParseSource(format, []byte(`{"instruction":"first","instruction":"second",
 "model":{"provider_config":"Café","name":"primary"}}`))
		require.Error(t, err)
	}
}

func sourceJSONValueForTest(source string) ([]byte, error) {
	raw, _, err := sourceJSON(SourceFormatYAML, []byte(source))
	return raw, err
}

func TestCustomSchemasUseRuntimeValidationDuringCompile(t *testing.T) {
	for _, test := range []struct{ schema, path string }{
		{`{"type":"object","properties":{"value":{"type":"unknown"}}}`, "/properties/value/type"},
		{`{"type":"object","properties":{"value":{"pattern":"["}}}`, "/properties/value/pattern"},
		{`{"type":"object","properties":{"value":{"$ref":"https://example.test/schema"}}}`, ""},
	} {
		source := validAgentSource("tools: {lookup: {type: custom, description: Example, input_schema: " + test.schema + "}}")
		_, err := Compile(SourceFormatYAML, []byte(source), CompileOptions{})
		var validation *ValidationError
		require.ErrorAs(t, err, &validation)
		require.True(t, strings.HasPrefix(validation.Issues[0].Path, "/tools/lookup/input_schema"))
		if test.path != "" {
			require.Equal(t, "/tools/lookup/input_schema"+test.path, validation.Issues[0].Path)
		}
		_, err = ToolsFromSource(SourceFormatYAML, []byte(source), CompileOptions{})
		require.ErrorAs(t, err, &validation)
	}
}
