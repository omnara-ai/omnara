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
