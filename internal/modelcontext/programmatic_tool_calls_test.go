package modelcontext

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const (
	programmaticWebSearchType = "web_search: { query: string; results: Array<{ published?: string; " +
		"snippet: string; title: string; url: string }> | null };\n"
	programmaticWebFetchType = "web_fetch: { bytes: number; content: string; content_type: string; " +
		"final_url: string; status_code: number; title?: string; truncated: boolean; url: string };\n"
)

func TestRuntimeContractToolSpecsDescribeProgrammaticToolCallsOnlyWhenEnabled(t *testing.T) {
	catalog, err := toolcatalog.Default()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	entry, ok := catalog.Lookup(toolcatalog.ToolNameRunCommand)
	if !ok {
		t.Fatal("run_command catalog entry missing")
	}
	descriptions := func(contract agentconfig.RuntimeContract) map[string]string {
		t.Helper()
		specs, err := RuntimeContractToolSpecs(
			context.Background(), &fakeContextStore{}, uuid.UUID{}, uuid.UUID{}, contract, time.Time{},
		)
		if err != nil {
			t.Fatalf("specs: %v", err)
		}
		out := make(map[string]string, len(specs))
		for _, spec := range specs {
			out[spec.Name] = spec.Description
		}
		return out
	}

	disabled := descriptions(webToolsContract(t))
	if disabled[toolcatalog.ToolNameRunCommand] != entry.Description {
		t.Fatalf("run_command description changed without programmatic tool calls: %q",
			disabled[toolcatalog.ToolNameRunCommand])
	}

	deferring := func(names ...string) agentconfig.RuntimeContract {
		contract := webToolsContract(t)
		contract.CodeMode = &agentconfig.CodeModeCompiled{Enabled: true}
		for i := range contract.Tools {
			contract.Tools[i].CodeMode = contract.Tools[i].Name != toolcatalog.ToolNameRunCommand
			for _, name := range names {
				if contract.Tools[i].Name == name {
					contract.Tools[i].Deferred = true
				}
			}
		}
		return contract
	}

	want := entry.Description + programmaticToolCallsRunCommandDescription +
		programmaticToolCallsTypesHeading + programmaticWebSearchType + programmaticWebFetchType + "```" +
		programmaticToolCallsSearchDescription
	for name, enabled := range map[string]map[string]string{
		"nothing deferred": descriptions(deferring()),
		"one deferred":     descriptions(deferring(toolcatalog.ToolNameWebSearch)),
		"both deferred":    descriptions(deferring(toolcatalog.ToolNameWebSearch, toolcatalog.ToolNameWebFetch)),
	} {
		if got := enabled[toolcatalog.ToolNameRunCommand]; got != want {
			t.Fatalf("run_command description with %s = %q, want %q", name, got, want)
		}
		for _, tool := range []string{toolcatalog.ToolNameWebSearch, toolcatalog.ToolNameWebFetch} {
			if strings.Contains(enabled[tool], "omnarad call") {
				t.Fatalf("%s description mentions programmatic tool calls: %q", tool, enabled[tool])
			}
		}
	}
}

// Code mode and deferral are independent: each combination keeps the tool in the model's tools,
// and only the contract's code mode setting decides whether commands can call it.
func TestRuntimeContractToolSpecsKeepCodeModeIndependentOfDeferral(t *testing.T) {
	contract := webToolsContract(t)
	contract.CodeMode = &agentconfig.CodeModeCompiled{Enabled: true}
	for i := range contract.Tools {
		switch contract.Tools[i].Name {
		case toolcatalog.ToolNameWebSearch:
			contract.Tools[i].Deferred, contract.Tools[i].CodeMode = true, true
		case toolcatalog.ToolNameWebFetch:
			contract.Tools[i].CodeMode = true
		case toolcatalog.ToolNameRunCommand:
			contract.Tools[i].Deferred = true
		}
	}
	specs, err := RuntimeContractToolSpecs(
		context.Background(), &fakeContextStore{}, uuid.UUID{}, uuid.UUID{}, contract, time.Time{},
	)
	if err != nil {
		t.Fatalf("specs: %v", err)
	}
	if len(specs) != 3 {
		t.Fatalf("specs = %d, want every tool to stay a model tool", len(specs))
	}
	var callable, deferred []string
	for _, spec := range ProgramCallableTools(specs) {
		if len(spec.OutputSchema) == 0 {
			t.Fatalf("%s is callable without an output schema", spec.Name)
		}
		callable = append(callable, spec.Name)
	}
	for _, spec := range DeferredToolSpecs(specs) {
		deferred = append(deferred, spec.Name)
	}
	sort.Strings(callable)
	sort.Strings(deferred)
	if got := strings.Join(callable, ","); got != toolcatalog.ToolNameWebFetch+","+toolcatalog.ToolNameWebSearch {
		t.Fatalf("commands can call %q, want the web tools, deferred or not", got)
	}
	if got := strings.Join(deferred, ","); got != toolcatalog.ToolNameRunCommand+","+toolcatalog.ToolNameWebSearch {
		t.Fatalf("deferred %q, want run_command and web_search, callable or not", got)
	}
}

func TestRenderSchemaTypeScript(t *testing.T) {
	for _, test := range []struct {
		schema string
		want   string
	}{
		{`{}`, "unknown"},
		{`true`, "unknown"},
		{`{"type":"integer"}`, "number"},
		{`{"type":["null","string"]}`, "string | null"},
		{`{"enum":["day","week"]}`, `"day" | "week"`},
		{`{"const":3}`, "3"},
		{`{"type":"array"}`, "unknown[]"},
		{`{"type":"array","items":{"type":"boolean"}}`, "Array<boolean>"},
		{`{"type":"object"}`, "{ [key: string]: unknown }"},
		{`{"type":"object","properties":{},"additionalProperties":false}`, "{}"},
		{`{"type":"object","additionalProperties":{"type":"number"}}`, "{ [key: string]: number }"},
		{
			`{"type":"object","properties":{"b":{"type":"string"},"a-b":{"type":"number"}},"required":["b"]}`,
			`{ "a-b"?: number; b: string }`,
		},
		{
			`{"anyOf":[{"type":"string"},{"type":"null"},{"type":"string"},{"type":"number"}]}`,
			"string | number | null",
		},
	} {
		got, err := renderSchemaTypeScript(json.RawMessage(test.schema))
		if err != nil {
			t.Fatalf("render %s: %v", test.schema, err)
		}
		if got != test.want {
			t.Fatalf("render %s = %q, want %q", test.schema, got, test.want)
		}
	}
}
