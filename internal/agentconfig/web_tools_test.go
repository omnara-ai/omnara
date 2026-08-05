package agentconfig

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func TestDefaultCatalogWebToolSchemas(t *testing.T) {
	catalog, err := toolcatalog.Default()
	if err != nil {
		t.Fatalf("default tool catalog: %v", err)
	}
	search, ok := catalog.Lookup("web_search")
	if !ok {
		t.Fatal("web_search catalog entry missing")
	}
	if search.DefaultPermission.Mode != toolpermission.ModeAlwaysAllow {
		t.Fatalf(
			"web_search default permission = %s, want %s (read-only tool)",
			search.DefaultPermission.Mode,
			toolpermission.ModeAlwaysAllow,
		)
	}
	assertObjectSchema(
		t,
		"web_search",
		search.InputSchema,
		[]string{"query"},
		[]string{"query", "num_results", "recency", "domains"},
	)

	fetch, ok := catalog.Lookup("web_fetch")
	if !ok {
		t.Fatal("web_fetch catalog entry missing")
	}
	if fetch.DefaultPermission.Mode != toolpermission.ModeAlwaysAllow {
		t.Fatalf(
			"web_fetch default permission = %s, want %s (read-only tool)",
			fetch.DefaultPermission.Mode,
			toolpermission.ModeAlwaysAllow,
		)
	}
	assertObjectSchema(t, "web_fetch", fetch.InputSchema, []string{"url"}, []string{"url", "format", "timeout_seconds"})
}

func assertObjectSchema(t *testing.T, name string, raw json.RawMessage, required []string, properties []string) {
	t.Helper()
	var schema struct {
		Type                 string                     `json:"type"`
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties bool                       `json:"additionalProperties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("%s schema decode: %v", name, err)
	}
	if schema.Type != "object" || schema.AdditionalProperties {
		t.Fatalf("%s schema must be a closed object: %s", name, raw)
	}
	if len(schema.Required) != len(required) {
		t.Fatalf("%s required = %v, want %v", name, schema.Required, required)
	}
	for index, field := range required {
		if schema.Required[index] != field {
			t.Fatalf("%s required = %v, want %v", name, schema.Required, required)
		}
	}
	if len(schema.Properties) != len(properties) {
		t.Fatalf("%s properties = %d entries, want %d (%s)", name, len(schema.Properties), len(properties), raw)
	}
	for _, field := range properties {
		if _, ok := schema.Properties[field]; !ok {
			t.Fatalf("%s schema missing property %q", name, field)
		}
	}
}

func TestCompileAgentConfigWithWebTools(t *testing.T) {
	compiled, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
tools:
  web_search: {}
  web_fetch: {}
`)), CompileOptions{})
	if err != nil {
		t.Fatalf("compile web tools: %v", err)
	}
	contract, err := RuntimeContractFromCompiled(json.RawMessage(compiled.CanonicalJSON), CompilerVersion, compiled.Hash)
	if err != nil {
		t.Fatalf("runtime contract: %v", err)
	}
	byName := map[string]RuntimeTool{}
	for _, tool := range contract.Tools {
		byName[tool.Name] = tool
	}
	search, ok := byName["web_search"]
	if !ok || search.Type != toolcatalog.ToolTypeBuiltIn ||
		search.Permission.Mode != toolpermission.ModeAlwaysAllow {
		t.Fatalf("web_search runtime tool = %+v ok=%v", search, ok)
	}
	fetch, ok := byName["web_fetch"]
	if !ok || fetch.Type != toolcatalog.ToolTypeBuiltIn ||
		fetch.Permission.Mode != toolpermission.ModeAlwaysAllow {
		t.Fatalf("web_fetch runtime tool = %+v ok=%v", fetch, ok)
	}
	if len(search.InputSchema) == 0 || len(fetch.InputSchema) == 0 {
		t.Fatal("web tool runtime contracts must carry the registry input schemas")
	}
}
