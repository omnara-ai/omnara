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
		contract.DeferredTools = &agentconfig.DeferredToolsCompiled{Mode: agentconfig.DeferredToolsModeCodeMode}
		for i := range contract.Tools {
			for _, name := range names {
				if contract.Tools[i].Name == name {
					contract.Tools[i].Deferred = true
				}
			}
		}
		return contract
	}

	none := descriptions(deferring())
	want := entry.Description + programmaticToolCallsRunCommandDescription + programmaticToolCallsSearchDescription
	if got := none[toolcatalog.ToolNameRunCommand]; got != want {
		t.Fatalf("run_command description with nothing deferred = %q, want %q", got, want)
	}

	enabled := descriptions(deferring(toolcatalog.ToolNameWebSearch, toolcatalog.ToolNameWebFetch))
	want = entry.Description + programmaticToolCallsRunCommandDescription +
		programmaticToolCallsTypesHeading + programmaticWebSearchType + programmaticWebFetchType + "```" +
		programmaticToolCallsSearchDescription
	if got := enabled[toolcatalog.ToolNameRunCommand]; got != want {
		t.Fatalf("run_command description = %q, want %q", got, want)
	}
	for _, name := range []string{toolcatalog.ToolNameWebSearch, toolcatalog.ToolNameWebFetch} {
		if strings.Contains(enabled[name], "omnarad call") {
			t.Fatalf("%s description mentions programmatic tool calls: %q", name, enabled[name])
		}
	}

	one := descriptions(deferring(toolcatalog.ToolNameWebSearch))
	want = entry.Description + programmaticToolCallsRunCommandDescription +
		programmaticToolCallsTypesHeading + programmaticWebSearchType + "```" +
		programmaticToolCallsSearchDescription
	if got := one[toolcatalog.ToolNameRunCommand]; got != want {
		t.Fatalf("run_command description with only web_search deferred = %q, want %q", got, want)
	}
}

func TestRuntimeContractToolSpecsMarkOnlyDeferredCallableToolsForCodeMode(t *testing.T) {
	contract := webToolsContract(t)
	for i := range contract.Tools {
		contract.Tools[i].Deferred = true
	}
	marked := func(contract agentconfig.RuntimeContract) []string {
		t.Helper()
		specs, err := RuntimeContractToolSpecs(
			context.Background(), &fakeContextStore{}, uuid.UUID{}, uuid.UUID{}, contract, time.Time{},
		)
		if err != nil {
			t.Fatalf("specs: %v", err)
		}
		var names []string
		for _, spec := range ProgramCallableTools(specs) {
			if len(spec.OutputSchema) == 0 {
				t.Fatalf("%s is callable without an output schema", spec.Name)
			}
			names = append(names, spec.Name)
		}
		return names
	}
	if got := marked(contract); len(got) != 0 {
		t.Fatalf("tool_add mode marked %v for code mode", got)
	}
	contract.DeferredTools = &agentconfig.DeferredToolsCompiled{Mode: agentconfig.DeferredToolsModeCodeMode}
	names := marked(contract)
	sort.Strings(names)
	got := strings.Join(names, ",")
	if got != toolcatalog.ToolNameWebFetch+","+toolcatalog.ToolNameWebSearch {
		t.Fatalf("code mode marked %q, want the deferred web tools only", got)
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
