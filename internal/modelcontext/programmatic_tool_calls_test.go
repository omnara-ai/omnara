package modelcontext

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestRuntimeContractToolSpecsDescribeProgrammaticToolCallsOnlyWhenEnabled(t *testing.T) {
	const suffix = " Commands can call this agent's other tools with `\"$OMNARA_BIN\" call <tool> '<json input>'` " +
		"(pass `-` to read the input from stdin). It prints the tool's structured JSON result to stdout " +
		"(`--json` prints the full result envelope), exits 1 if the tool fails or is denied, and exits 2 if the " +
		"tool cannot be called from commands. Find tools, their input schemas and the output schemas of the JSON " +
		"they print, including ones not loaded here, with `\"$OMNARA_BIN\" call tool_search '{\"pattern\":\"<regex>\"}'`. Results go only to the command, " +
		"not to you, so chain calls with pipes, jq, loops or scripts and print only what you need. This works " +
		"only on machines where programmatic tool calls are enabled."
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
	if got, want := enabled[toolcatalog.ToolNameRunCommand], entry.Description+suffix; got != want {
		t.Fatalf("run_command description = %q, want %q", got, want)
	}
	for _, name := range []string{toolcatalog.ToolNameWebSearch, toolcatalog.ToolNameWebFetch} {
		if strings.Contains(enabled[name], "OMNARA_BIN") {
			t.Fatalf("%s description mentions programmatic tool calls: %q", name, enabled[name])
		}
	}
}
