package toolcatalog

import (
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
		{
			name:        ToolNameListMachines,
			description: []string{"machine_ref", "availability"},
		},
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
