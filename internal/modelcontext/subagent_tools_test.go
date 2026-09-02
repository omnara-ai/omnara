package modelcontext

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestSpawnAgentToolSpecInjectsHandles(t *testing.T) {
	catalog, err := toolcatalog.Default()
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := catalog.Lookup(toolcatalog.ToolNameSpawnAgent)
	contract := agentconfig.RuntimeContract{
		Subagents: map[string]agentconfig.SubagentCompiled{
			"researcher": {Type: agentconfig.SubagentTypeProfile, Description: "Investigate."},
			"fork":       {Type: agentconfig.SubagentTypeSelf},
		},
	}
	description, schema, err := spawnAgentToolSpec(entry.Description, entry.InputSchema, contract)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Properties struct {
			Agent struct {
				Enum []string `json:"enum"`
			} `json:"agent"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &decoded); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(decoded.Properties.Agent.Enum, ","); got != "fork,researcher" {
		t.Fatalf("enum = %q", got)
	}
	if !strings.Contains(description, "researcher (profile): Investigate.") ||
		!strings.Contains(description, "fork (self)") {
		t.Fatalf("description = %q", description)
	}
}
