package openapispec

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Public app setup uses the same capability language as agent config. Generate
// just this marked section so config changes cannot silently diverge from SDK
// types, and hand-authored routes retain their formatting and comments.
func TestGeneratedAppCapabilitySchemasAreCurrent(t *testing.T) {
	const start = "    # BEGIN GENERATED APP CAPABILITY SCHEMAS\n"
	const end = "    # END GENERATED APP CAPABILITY SCHEMAS\n"
	raw, err := os.ReadFile("../../internal/agentconfig/generated/agent_config.schema.json")
	require.NoError(t, err)
	var source struct {
		Defs map[string]map[string]any `json:"$defs"`
	}
	require.NoError(t, json.Unmarshal(raw, &source))
	components := map[string]any{}
	name := func(key string) string { return "Config" + strings.TrimPrefix(key, "AgentConfig") }
	var visit func(any)
	add := func(key string) {
		if _, seen := components[name(key)]; seen {
			return
		}
		definition, exists := source.Defs[key]
		require.True(t, exists, "missing config schema %q", key)
		components[name(key)] = definition
		visit(definition)
	}
	visit = func(value any) {
		switch node := value.(type) {
		case map[string]any:
			if ref, ok := node["$ref"].(string); ok {
				key, found := strings.CutPrefix(ref, "#/$defs/")
				require.True(t, found, "unexpected config schema reference %q", ref)
				node["$ref"] = "#/components/schemas/" + name(key)
				add(key)
			}
			for _, child := range node {
				visit(child)
			}
		case []any:
			for _, child := range node {
				visit(child)
			}
		}
	}
	for _, root := range []string{"AgentConfigAppCapabilitySource", "AgentConfigToolSource"} {
		add(root)
		definition, ok := components[name(root)].(map[string]any)
		require.True(t, ok)
		definition["x-go-type"] = "agentconfig." + root
		definition["x-go-type-import"] = map[string]any{"path": "github.com/omnara-ai/omnara/internal/agentconfig"}
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	require.NoError(t, encoder.Encode(components))
	require.NoError(t, encoder.Close())
	section := start + "    " + strings.ReplaceAll(strings.TrimSuffix(out.String(), "\n"), "\n", "\n    ") + "\n" + end
	spec, err := os.ReadFile("openapi.yaml")
	require.NoError(t, err)
	before, rest, found := strings.Cut(string(spec), start)
	var current, after string
	if found {
		var closed bool
		current, after, closed = strings.Cut(rest, end)
		require.True(t, closed, "unterminated generated app schema section")
		current = start + current + end
	}
	if os.Getenv("OMNARA_REGEN_APP_OPENAPI") == "1" {
		require.NoError(t, os.WriteFile("openapi.yaml", []byte(before+section+after), 0o644))
		return
	}
	require.Equal(
		t,
		section,
		current,
		"run OMNARA_REGEN_APP_OPENAPI=1 go test ./api/openapi -run TestGeneratedAppCapabilitySchemasAreCurrent",
	)
}

func TestAppCapabilityContracts(t *testing.T) {
	var decoded any
	require.NoError(t, yaml.Unmarshal(YAML, &decoded))
	raw, err := json.Marshal(decoded)
	require.NoError(t, err)
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	require.NoError(t, err)
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	const resource = "urn:omnara:app-capability-contract"
	require.NoError(t, compiler.AddResource(resource, document))
	for _, test := range []struct {
		name, schema, value string
		valid               bool
	}{
		{"handler selection", "ConfigAppCapabilitySource", `{}`, true},
		{"tool policy", "ConfigToolSource", `{"enabled":false,"deferred":true,"permission":{"mode":"always_ask"}}`, true},
		{"static catalog schema", "AppCapabilityDefinition", `{"input_schema":{"type":"object"}}`, true},
		{"catalog requires input schema", "AppCapabilityDefinition", `{}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			schema, err := compiler.Compile(resource + "#/components/schemas/" + test.schema)
			require.NoError(t, err)
			value, err := jsonschema.UnmarshalJSON(strings.NewReader(test.value))
			require.NoError(t, err)
			err = schema.Validate(value)
			if test.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
