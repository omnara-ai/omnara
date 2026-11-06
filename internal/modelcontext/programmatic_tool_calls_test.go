package modelcontext

import (
	"context"
	"encoding/json"
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
	programmaticWebExample = "\"$OMNARA_BIN\" call web_search '{\"query\":\"omnara changelog\",\"num_results\":3}' |\n" +
		"  jq -c '.results[]? | {url}' |\n" +
		"  while read -r input; do printf '%s' \"$input\" | \"$OMNARA_BIN\" call web_fetch - | " +
		"jq -r '.title'; done\n"
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

	contract := webToolsContract(t)
	contract.ProgrammaticToolCalls = &agentconfig.ProgrammaticToolCallsCompiled{Enabled: true}
	enabled := descriptions(contract)
	want := entry.Description + programmaticToolCallsRunCommandDescription +
		programmaticToolCallsTypesHeading + programmaticWebSearchType + programmaticWebFetchType + "```" +
		programmaticToolCallsSearchDescription +
		programmaticToolCallsExamplesHeading + programmaticWebExample + "```"
	if got := enabled[toolcatalog.ToolNameRunCommand]; got != want {
		t.Fatalf("run_command description = %q, want %q", got, want)
	}
	for _, name := range []string{toolcatalog.ToolNameWebSearch, toolcatalog.ToolNameWebFetch} {
		if strings.Contains(enabled[name], "OMNARA_BIN") {
			t.Fatalf("%s description mentions programmatic tool calls: %q", name, enabled[name])
		}
	}

	contract.ProgrammaticToolCalls.Exclude = []string{toolcatalog.ToolNameWebFetch}
	excluded := descriptions(contract)
	want = entry.Description + programmaticToolCallsRunCommandDescription +
		programmaticToolCallsTypesHeading + programmaticWebSearchType + "```" +
		programmaticToolCallsSearchDescription
	if got := excluded[toolcatalog.ToolNameRunCommand]; got != want {
		t.Fatalf("run_command description without web_fetch = %q, want %q", got, want)
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
