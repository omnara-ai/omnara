package toolcatalog

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/jsonschema"
)

func TestFileToolPathSchemas(t *testing.T) {
	catalog, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	for _, toolName := range []string{ToolNameUploadFile, ToolNameDownloadFile, ToolNameWriteFile} {
		t.Run(toolName, func(t *testing.T) {
			entry, ok := catalog.Lookup(toolName)
			if !ok {
				t.Fatal("tool missing from catalog")
			}
			validator, err := jsonschema.Compile(entry.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			artifactPath := "/artifacts/art_z3jehcyd5n6a2bfgik7mv4qtrw"
			for _, path := range []string{
				"/artifacts", artifactPath, "/artifacts/art_Z3JEHCYD5N6A2BFGIK7MV4QTRW",
				"", "/artifacts/", "/skills/example", artifactPath + "/", artifactPath + "/nested",
				"/artifacts/art_short", "prefix" + artifactPath,
				"/memory/team/nested/note.md", "/memory/team", "/memory/team/",
			} {
				input := map[string]string{"path": path}
				wantValid := false
				switch toolName {
				case ToolNameUploadFile:
					input["source"] = "file.txt"
					wantValid = path == ArtifactVFSRoot
				case ToolNameDownloadFile:
					input["destination"] = "file.txt"
					wantValid = path == artifactPath
				case ToolNameWriteFile:
					input["content"] = ""
				}
				wantValid = wantValid || path == "/memory/team/nested/note.md"
				raw, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				if err := validator.Validate(raw); (err == nil) != wantValid {
					t.Errorf("path %q: error = %v, want valid = %t", path, err, wantValid)
				}
			}
		})
	}
}

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
			description: []string{"machine_id", "availability"},
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
