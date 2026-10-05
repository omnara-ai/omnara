package agentconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// OpenAPI component names for the projected source schema. The root component
// types a full config definition; the tools component types the partial
// definition accepted by tool resolution.
const (
	OpenAPIDefinitionComponent      = "AgentConfigDefinition"
	OpenAPIToolsDefinitionComponent = "AgentConfigToolsDefinition"
)

var openAPIDefinitionComponentNames = map[string]string{
	"AgentConfigIntegrationCapabilitySource": "AgentConfigDefinitionIntegrationCapability",
	"AgentConfigMCPAuthSource":               "AgentConfigDefinitionMCPAuth",
	"AgentConfigMCPSource":                   "AgentConfigDefinitionMCPServer",
	"AgentConfigMCPToolSource":               "AgentConfigDefinitionMCPTool",
	"AgentConfigMachineSource":               "AgentConfigDefinitionMachineSource",
	"AgentConfigModelSource":                 "AgentConfigDefinitionModel",
	"AgentConfigSubagentInstructionSource":   "AgentConfigDefinitionSubagentInstruction",
	"AgentConfigSubagentModelSource":         "AgentConfigDefinitionSubagentModel",
	"AgentConfigSubagentSource":              "AgentConfigDefinitionSubagent",
	"AgentConfigToolSource":                  "AgentConfigDefinitionTool",
	"AgentToolInputSchema":                   "AgentConfigDefinitionToolInputSchema",
	"EventWebhook":                           "AgentConfigDefinitionEventWebhook",
	"GitCredentialsSource":                   "AgentConfigDefinitionGitCredentials",
	"ToolPermissionSelection":                "AgentConfigDefinitionToolPermission",
}

// openAPITypingOnlyDropKeywords are validation-only JSON Schema keywords. The
// OpenAPI projection exists for client typing and documentation; the compiler
// remains the authoritative validator, so these stay server-side.
var openAPITypingOnlyDropKeywords = map[string]bool{
	"allOf":             true,
	"dependentRequired": true,
	"else":              true,
	"if":                true,
	"not":               true,
	"oneOf":             true,
	"pattern":           true,
	"propertyNames":     true,
	"then":              true,
}

var openAPIToolsDefinitionProperties = []string{"interaction_handlers", "machine_sources", "mcp", "skills", "subagents", "tools"}

// OpenAPIDefinitionComponents projects the agent config source JSON Schema
// into OpenAPI component schemas keyed by component name.
func OpenAPIDefinitionComponents() (map[string]any, error) {
	raw, err := SourceJSONSchema()
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("decode agent config source schema: %w", err)
	}
	defs, _ := root["$defs"].(map[string]any)
	delete(root, "$defs")

	components := make(map[string]any, len(defs)+2)
	for _, name := range slices.Sorted(maps.Keys(defs)) {
		componentName, ok := openAPIDefinitionComponentNames[name]
		if !ok {
			return nil, fmt.Errorf("agent config source schema def %q has no OpenAPI component name", name)
		}
		projected, err := projectOpenAPISchema(defs[name])
		if err != nil {
			return nil, fmt.Errorf("project %s: %w", name, err)
		}
		components[componentName] = openAPIComponent(projected, "")
	}

	projectedRoot, err := projectOpenAPISchema(root)
	if err != nil {
		return nil, fmt.Errorf("project agent config source schema: %w", err)
	}
	definition, ok := projectedRoot.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("agent config source schema projected to %T, want object", projectedRoot)
	}
	components[OpenAPIDefinitionComponent] = openAPIComponent(
		definition,
		"Agent config definition: the JSON form of an agent config source. "+
			"The server validates it fully when compiling, including rules this schema does not express.",
	)

	rootProperties, _ := definition["properties"].(map[string]any)
	toolsProperties := make(map[string]any, len(openAPIToolsDefinitionProperties))
	for _, name := range openAPIToolsDefinitionProperties {
		property, ok := rootProperties[name]
		if !ok {
			return nil, fmt.Errorf("agent config source schema has no %q property", name)
		}
		toolsProperties[name] = property
	}
	components[OpenAPIToolsDefinitionComponent] = openAPIComponent(
		map[string]any{
			"type":                 "object",
			"properties":           toolsProperties,
			"additionalProperties": true,
		},
		"The parts of an agent config definition that determine its effective tools. Other fields are ignored.",
	)
	return components, nil
}

func openAPIComponent(schema any, description string) map[string]any {
	component, ok := schema.(map[string]any)
	if !ok {
		component = map[string]any{}
	}
	if description != "" {
		component["description"] = description
	}
	// Go handlers pass definitions to the compiler as raw JSON, so the
	// generated Go types stay opaque.
	component["x-go-type"] = "json.RawMessage"
	return component
}

func projectOpenAPISchema(node any) (any, error) {
	switch typed := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			if openAPITypingOnlyDropKeywords[key] {
				continue
			}
			switch key {
			case "$ref":
				ref, _ := value.(string)
				name, ok := strings.CutPrefix(ref, "#/$defs/")
				if !ok {
					return nil, fmt.Errorf("unsupported $ref %q", ref)
				}
				componentName, ok := openAPIDefinitionComponentNames[name]
				if !ok {
					return nil, fmt.Errorf("$ref %q has no OpenAPI component name", ref)
				}
				out[key] = "#/components/schemas/" + componentName
			case "properties":
				properties, _ := value.(map[string]any)
				projected := make(map[string]any, len(properties))
				for name, property := range properties {
					schema, err := projectOpenAPISchema(property)
					if err != nil {
						return nil, err
					}
					projected[name] = schema
				}
				out[key] = projected
			case "anyOf":
				schema, err := projectOpenAPIAnyOf(value)
				if err != nil {
					return nil, err
				}
				for name, item := range schema {
					out[name] = item
				}
			default:
				schema, err := projectOpenAPISchema(value)
				if err != nil {
					return nil, err
				}
				out[key] = schema
			}
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			schema, err := projectOpenAPISchema(item)
			if err != nil {
				return nil, err
			}
			out[i] = schema
		}
		return out, nil
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer, nil
		}
		return typed.Float64()
	default:
		return node, nil
	}
}

// projectOpenAPIAnyOf folds the nullable pattern anyOf: [{type: T, ...},
// {type: null}] into {type: [T, "null"], ...}, which generators handle as
// a plain optional value rather than a union.
func projectOpenAPIAnyOf(value any) (map[string]any, error) {
	projected, err := projectOpenAPISchema(value)
	if err != nil {
		return nil, err
	}
	options, _ := projected.([]any)
	if len(options) == 2 {
		for i, option := range options {
			if !isNullSchema(option) {
				continue
			}
			other, ok := options[1-i].(map[string]any)
			typeName, hasType := other["type"].(string)
			if !ok || !hasType {
				break
			}
			merged := maps.Clone(other)
			merged["type"] = []any{typeName, "null"}
			return merged, nil
		}
	}
	return map[string]any{"anyOf": projected}, nil
}

func isNullSchema(schema any) bool {
	typed, ok := schema.(map[string]any)
	return ok && len(typed) == 1 && typed["type"] == "null"
}
