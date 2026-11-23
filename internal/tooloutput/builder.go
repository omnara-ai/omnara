package tooloutput

import (
	"encoding/json"
	"maps"
	"slices"
)

func mapSchema(build func() map[string]any) func() (json.RawMessage, error) {
	return func() (json.RawMessage, error) { return json.Marshal(build()) }
}

func object(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func extend(base map[string]any, properties map[string]any, required ...string) map[string]any {
	baseProperties, _ := base["properties"].(map[string]any)
	merged := maps.Clone(baseProperties)
	maps.Copy(merged, properties)
	baseRequired, _ := base["required"].([]string)
	return object(merged, slices.Concat(baseRequired, required)...)
}

func describe(description string, schema map[string]any) map[string]any {
	described := maps.Clone(schema)
	described["description"] = description
	return described
}

func titledSchema(title string, schema map[string]any) map[string]any {
	titled := maps.Clone(schema)
	titled["title"] = title
	return titled
}

func anyOf(description string, alternatives ...map[string]any) map[string]any {
	return map[string]any{"description": description, "anyOf": alternatives}
}

func stringType() map[string]any { return map[string]any{"type": "string"} }

func integerType() map[string]any { return map[string]any{"type": "integer"} }

func booleanType() map[string]any { return map[string]any{"type": "boolean"} }

func dateTime() map[string]any { return map[string]any{"type": "string", "format": "date-time"} }

func anyObject() map[string]any { return map[string]any{"type": "object"} }

func enum(values ...string) map[string]any { return map[string]any{"type": "string", "enum": values} }

func constant(value any) map[string]any { return map[string]any{"const": value} }

func arrayOf(items map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": items}
}

func nullable(schema map[string]any) map[string]any {
	return map[string]any{"anyOf": []any{schema, map[string]any{"type": "null"}}}
}

func offsetPage() map[string]any {
	return map[string]any{
		"has_more":    booleanType(),
		"next_offset": describe("Pass as offset to continue. Present only when has_more is true.", integerType()),
	}
}

func withOffsetPage(properties map[string]any) map[string]any {
	merged := maps.Clone(properties)
	maps.Copy(merged, offsetPage())
	return merged
}
