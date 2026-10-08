package agentconfig

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	openapispec "github.com/omnara-ai/omnara/api/openapi"
	"gopkg.in/yaml.v3"
)

// The agent config definition schemas in the OpenAPI spec are the source of
// truth for agent configs: sources are validated against them before they are
// compiled.
const (
	OpenAPIDefinitionComponent      = "AgentConfigDefinition"
	OpenAPIToolsDefinitionComponent = "AgentConfigToolsDefinition"
)

const openAPISchemaRefPrefix = "#/components/schemas/"

var specComponentSchemas = sync.OnceValues(func() (map[string]any, error) {
	var spec struct {
		Components struct {
			Schemas map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(openapispec.YAML, &spec); err != nil {
		return nil, fmt.Errorf("parse openapi spec: %w", err)
	}
	return spec.Components.Schemas, nil
})

// specJSONSchema returns an OpenAPI component schema as a standalone JSON
// Schema document. Every component it references, directly or transitively,
// moves under $defs, and OpenAPI extension keywords are dropped.
func specJSONSchema(component string) ([]byte, error) {
	schemas, err := specComponentSchemas()
	if err != nil {
		return nil, err
	}
	root, ok := schemas[component]
	if !ok {
		return nil, fmt.Errorf("openapi spec has no %s schema", component)
	}
	referenced := map[string]bool{}
	pending := []string{}
	var convert func(node any) (any, error)
	convert = func(node any) (any, error) {
		switch typed := node.(type) {
		case map[string]any:
			out := make(map[string]any, len(typed))
			for key, value := range typed {
				if strings.HasPrefix(key, "x-") {
					continue
				}
				if key != "$ref" {
					converted, err := convert(value)
					if err != nil {
						return nil, err
					}
					out[key] = converted
					continue
				}
				ref, _ := value.(string)
				name, ok := strings.CutPrefix(ref, openAPISchemaRefPrefix)
				if !ok {
					return nil, fmt.Errorf("unsupported $ref %q in %s", ref, component)
				}
				if !referenced[name] {
					referenced[name] = true
					pending = append(pending, name)
				}
				out[key] = "#/$defs/" + name
			}
			return out, nil
		case []any:
			out := make([]any, len(typed))
			for i, item := range typed {
				converted, err := convert(item)
				if err != nil {
					return nil, err
				}
				out[i] = converted
			}
			return out, nil
		default:
			return node, nil
		}
	}
	converted, err := convert(root)
	if err != nil {
		return nil, err
	}
	document, ok := converted.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("openapi %s schema is not an object", component)
	}
	defs := map[string]any{}
	for len(pending) > 0 {
		name := pending[0]
		pending = pending[1:]
		schema, ok := schemas[name]
		if !ok {
			return nil, fmt.Errorf("openapi spec has no %s schema, referenced from %s", name, component)
		}
		if defs[name], err = convert(schema); err != nil {
			return nil, err
		}
	}
	if len(defs) > 0 {
		document["$defs"] = defs
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("marshal %s JSON schema: %w", component, err)
	}
	return canonicalizeJSON(raw), nil
}
