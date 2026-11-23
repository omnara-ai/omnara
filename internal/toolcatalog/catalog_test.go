package toolcatalog

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestDefaultCatalogDoesNotUseReservedMCPNamespace(t *testing.T) {
	catalog, err := Default()
	if err != nil {
		t.Fatalf("default catalog: %v", err)
	}
	for _, entry := range catalog.Entries() {
		if UsesMCPRuntimeNamespace(entry.Name) {
			t.Fatalf("built-in tool %q uses the reserved MCP tool namespace", entry.Name)
		}
	}
}

func TestEntryRejectsReservedMCPNamespace(t *testing.T) {
	catalog, err := Default()
	if err != nil {
		t.Fatalf("default catalog: %v", err)
	}
	entry, ok := catalog.Lookup(ToolNameWebSearch)
	if !ok {
		t.Fatal("web_search missing from default catalog")
	}
	entry.Name = MCPRuntimeToolPrefix + "reserved"
	if err := entry.validate(); err == nil || !strings.Contains(err.Error(), "reserved MCP tool namespace") {
		t.Fatalf("validation error = %v, want reserved MCP tool namespace rejection", err)
	}
}

func TestDefaultCatalogIncludesRuntimeDiscoveryGuidance(t *testing.T) {
	catalog, err := Default()
	if err != nil {
		t.Fatalf("default catalog: %v", err)
	}
	checks := []struct {
		name        string
		description []string
		schema      []string
	}{
		{name: ToolNameRunCommand, schema: []string{ToolNameListMachines}},
		{name: ToolNameInspectMachine, schema: []string{ToolNameListMachines}},
		{name: ToolNameListProcesses, description: []string{"process_id"}},
		{name: ToolNameReadProcess, schema: []string{ToolNameListProcesses}},
	}
	for _, check := range checks {
		entry, ok := catalog.Lookup(check.name)
		if !ok {
			t.Fatalf("%s missing from default catalog", check.name)
		}
		for _, needle := range check.description {
			if !strings.Contains(entry.Description, needle) {
				t.Fatalf("%s description does not contain %q: %s", check.name, needle, entry.Description)
			}
		}
		for _, needle := range check.schema {
			if !strings.Contains(string(entry.InputSchema), needle) {
				t.Fatalf("%s schema does not contain %q: %s", check.name, needle, entry.InputSchema)
			}
		}
	}
}

func TestEntryRequiresOutputSchemaIndependentOfCodeMode(t *testing.T) {
	catalog, err := Default()
	if err != nil {
		t.Fatalf("default catalog: %v", err)
	}
	for _, name := range []string{ToolNameStorageWrite, ToolNameWebSearch} {
		entry, ok := catalog.Lookup(name)
		if !ok {
			t.Fatalf("%s missing from default catalog", name)
		}
		if err := entry.validate(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, codeMode := range []bool{false, true} {
			missing := entry
			missing.CodeMode = codeMode
			missing.OutputSchema = nil
			if err := missing.validate(); err == nil || !strings.Contains(err.Error(), "output schema is required") {
				t.Fatalf("%s code mode %t without output schema: %v", name, codeMode, err)
			}
			invalid := missing
			invalid.OutputSchema = json.RawMessage(`{"type":"nope"}`)
			if err := invalid.validate(); err == nil || !strings.Contains(err.Error(), "output schema") {
				t.Fatalf("%s code mode %t with invalid output schema: %v", name, codeMode, err)
			}
		}
	}
}

func TestToolEntryRequiresOutputSchemaToBuild(t *testing.T) {
	_, err := toolEntry("lookup", "Look up a user.", nil, nil, func() (json.RawMessage, error) {
		return nil, errors.New("no schema")
	})
	if err == nil || !strings.Contains(err.Error(), "lookup output schema") {
		t.Fatalf("toolEntry error = %v", err)
	}
}

func TestCodeModeStaysOptIn(t *testing.T) {
	catalog, err := Default()
	if err != nil {
		t.Fatalf("default catalog: %v", err)
	}
	var codeMode []string
	for _, entry := range catalog.Entries() {
		if entry.CodeMode {
			codeMode = append(codeMode, entry.Name)
		}
	}
	want := []string{ToolNameStorageList, ToolNameStorageRead, ToolNameStorageSearch, ToolNameWebFetch,
		ToolNameWebSearch}
	if !slices.Equal(codeMode, want) {
		t.Fatalf("code-mode tools = %v, want %v", codeMode, want)
	}
}
