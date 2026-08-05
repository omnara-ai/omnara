package modelcontext

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func webToolsContract(t *testing.T) agentconfig.RuntimeContract {
	t.Helper()
	catalog, err := toolcatalog.Default()
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	tools := make([]agentconfig.RuntimeTool, 0, 3)
	for _, name := range []string{"web_search", "web_fetch", "run_command"} {
		entry, ok := catalog.Lookup(name)
		if !ok {
			t.Fatalf("catalog entry %q missing", name)
		}
		tools = append(tools, agentconfig.RuntimeTool{
			Name:        name,
			Type:        toolcatalog.ToolTypeBuiltIn,
			Permission:  entry.DefaultPermission,
			InputSchema: entry.InputSchema,
		})
	}
	return agentconfig.RuntimeContract{Tools: tools}
}

func TestRuntimeContractToolSpecsRenderWebToolDescriptions(t *testing.T) {
	now := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)
	specs, err := RuntimeContractToolSpecs(
		context.Background(),
		nil,
		storage.ID{},
		storage.ID{},
		webToolsContract(t),
		now,
	)
	if err != nil {
		t.Fatalf("specs: %v", err)
	}
	var searchDescription string
	var fetchDescription string
	var fetchSchema string
	for _, spec := range specs {
		if spec.Name == "web_search" {
			searchDescription = spec.Description
		}
		if spec.Name == "web_fetch" {
			fetchDescription = spec.Description
			fetchSchema = string(spec.InputSchema)
		}
	}
	if !strings.Contains(searchDescription, "2026") {
		t.Fatalf("web_search description missing injected year: %q", searchDescription)
	}
	if !strings.Contains(fetchDescription, "Fetch a public http(s) URL") ||
		!strings.Contains(fetchDescription, "localhost") {
		t.Fatalf("web_fetch description missing public-fetch guidance: %q", fetchDescription)
	}
	if !strings.Contains(fetchSchema, `"url"`) || !strings.Contains(fetchSchema, `"timeout_seconds"`) {
		t.Fatalf("web_fetch schema missing expected fields: %s", fetchSchema)
	}
}
