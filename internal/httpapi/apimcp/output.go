package apimcp

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Tools whose operation returns no body (204) return {}.
var emptyOutputSchema = map[string]any{
	"type":                 openapi3.TypeObject,
	"description":          "Empty on success: the operation returns no data.",
	"additionalProperties": false,
}

// outputSchemaFor builds an output schema from the operation's 2xx JSON responses.
func outputSchemaFor(spec *openapi3.T, op *openapi3.Operation) (schema map[string]any, empty bool, _ error) {
	codes := make([]string, 0)
	for code := range op.Responses.Map() {
		if len(code) == 3 && code[0] == '2' {
			codes = append(codes, code)
		}
	}
	slices.Sort(codes)
	branches := make([]json.RawMessage, 0, len(codes))
	for _, code := range codes {
		response := op.Responses.Value(code)
		if response == nil || response.Value == nil {
			continue
		}
		media := response.Value.Content.Get("application/json")
		if media == nil || media.Schema == nil {
			continue
		}
		branch, err := inlinedSchemaJSON(spec, media.Schema)
		if err != nil {
			return nil, false, fmt.Errorf("%s response schema: %w", code, err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(branch, &decoded); err != nil {
			return nil, false, err
		}
		if decoded["type"] != openapi3.TypeObject {
			return nil, false, fmt.Errorf("%s response schema must be an object", code)
		}
		if !slices.ContainsFunc(branches, func(seen json.RawMessage) bool { return string(seen) == string(branch) }) {
			branches = append(branches, branch)
		}
	}
	switch len(branches) {
	case 0:
		return emptyOutputSchema, true, nil
	case 1:
		schema, err := withDefinitions(spec, branches[0])
		return schema, false, err
	default:
		root, err := json.Marshal(map[string]any{"type": openapi3.TypeObject, "anyOf": branches})
		if err != nil {
			return nil, false, err
		}
		schema, err := withDefinitions(spec, root)
		return schema, false, err
	}
}

func withDefinitions(spec *openapi3.T, encoded json.RawMessage) (map[string]any, error) {
	definitions, err := collectDefinitions(spec, encoded)
	if err != nil {
		return nil, err
	}
	var schema map[string]any
	if err := json.Unmarshal(rewriteComponentRefs(encoded), &schema); err != nil {
		return nil, err
	}
	if len(definitions) > 0 {
		schema[definitionsKey] = definitions
	}
	encodedSchema, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal(encodedSchema, &decoded); err != nil {
		return nil, err
	}
	stripExtensions(decoded)
	return decoded, nil
}

// Keys in these maps are names, not keywords, so they are never stripped.
var schemaMapKeywords = []string{"properties", "patternProperties", "dependentSchemas", definitionsKey}

// stripExtensions removes OpenAPI x-* codegen hints.
func stripExtensions(schema map[string]any) {
	for key, child := range schema {
		if strings.HasPrefix(key, "x-") {
			delete(schema, key)
			continue
		}
		if slices.Contains(schemaMapKeywords, key) {
			if named, ok := child.(map[string]any); ok {
				for _, value := range named {
					stripExtensionsIn(value)
				}
			}
			continue
		}
		stripExtensionsIn(child)
	}
}

func stripExtensionsIn(value any) {
	switch typed := value.(type) {
	case map[string]any:
		stripExtensions(typed)
	case []any:
		for _, item := range typed {
			stripExtensionsIn(item)
		}
	}
}
