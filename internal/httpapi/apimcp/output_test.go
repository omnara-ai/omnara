package apimcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/omnara-ai/omnara/internal/testutil"
)

func TestEveryToolDeclaresAnObjectOutputSchema(t *testing.T) {
	t.Parallel()
	spec := loadSpec(t)
	operations := indexOperations(spec)
	session := connect(t, Tools, echoOptions(t))
	listed := listedToolsByName(t, session)
	for _, tool := range Tools {
		listedTool := listed[tool.Name]
		if listedTool == nil {
			t.Fatalf("%s not listed", tool.Name)
		}
		schema := testutil.RequireType[map[string]any](t, listedTool.OutputSchema)
		if schema["type"] != "object" {
			t.Errorf("%s output schema type = %v, want object", tool.Name, schema["type"])
		}
		encoded, err := json.Marshal(schema)
		if err != nil {
			t.Fatalf("marshal %s output schema: %v", tool.Name, err)
		}
		if referenced := referencedComponentNames(encoded); len(referenced) > 0 {
			t.Errorf("%s output schema still references components: %v", tool.Name, referenced)
		}
		compiler := jsonschema.NewCompiler()
		document, err := jsonschema.UnmarshalJSON(strings.NewReader(string(encoded)))
		if err != nil {
			t.Fatalf("decode %s output schema: %v", tool.Name, err)
		}
		if err := compiler.AddResource("output.json", document); err != nil {
			t.Fatalf("add %s output schema: %v", tool.Name, err)
		}
		if _, err := compiler.Compile("output.json"); err != nil {
			t.Errorf("%s output schema does not compile: %v", tool.Name, err)
		}
		if operations[operationKey(tool.OperationID)].method == http.MethodDelete {
			if schema["additionalProperties"] != false || schema["properties"] != nil {
				t.Errorf("%s returns no body, want the empty object schema: %v", tool.Name, schema)
			}
		}
	}
	launch := testutil.RequireType[map[string]any](t, listed["agents_launch"].OutputSchema)
	if branches, ok := launch["anyOf"].([]any); !ok || len(branches) != 2 {
		t.Errorf("agents_launch output schema should offer the created and idempotent-replay shapes: %v", launch["anyOf"])
	}
}

func TestEveryInputPropertyIsDescribed(t *testing.T) {
	t.Parallel()
	session := connect(t, Tools, echoOptions(t))
	for _, tool := range listTools(t, session).Tools {
		schema := testutil.RequireType[map[string]any](t, tool.InputSchema)
		assertPropertiesDescribed(t, tool.Name, schema)
		definitions, _ := schema[definitionsKey].(map[string]any)
		for name, definition := range definitions {
			if object, ok := definition.(map[string]any); ok {
				assertPropertiesDescribed(t, tool.Name+" $defs/"+name, object)
			}
		}
	}
}

func assertPropertiesDescribed(t *testing.T, where string, schema map[string]any) {
	t.Helper()
	properties, _ := schema["properties"].(map[string]any)
	for name, property := range properties {
		decoded, _ := property.(map[string]any)
		if description, _ := decoded["description"].(string); strings.TrimSpace(description) == "" {
			t.Errorf("%s property %q has no description", where, name)
		}
	}
}

func TestBodylessResponseReturnsEmptyStructuredContent(t *testing.T) {
	t.Parallel()
	session := connect(t, Tools, Options{
		APIBasePath: testBasePath,
		Dispatch: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	})
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "machines_delete",
		Arguments: map[string]any{"orgID": testOrgID, "machineID": "mch_abcdefghijklmnopqrstuvwxyz"},
	})
	if err != nil {
		t.Fatalf("call machines_delete: %v", err)
	}
	if result.IsError {
		t.Fatalf("machines_delete failed: %+v", result.Content)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if string(encoded) != "{}" {
		t.Fatalf("structured content = %s, want {}", encoded)
	}
	if text := testutil.RequireType[*mcp.TextContent](t, result.Content[0]).Text; text != "No Content" {
		t.Fatalf("text content = %q", text)
	}
}
