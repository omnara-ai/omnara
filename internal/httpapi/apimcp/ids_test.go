package apimcp

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

var toolReference = regexp.MustCompile(`\b(whoami|[a-z]+(?:_[a-z]+)+)\b`)

func TestIDDescriptionsReferenceRealTools(t *testing.T) {
	t.Parallel()
	known := make(map[string]bool, len(Tools))
	for _, tool := range Tools {
		known[tool.Name] = true
	}
	for prefix, description := range idDescriptions {
		for _, name := range toolReference.FindAllString(description, -1) {
			if !strings.HasSuffix(name, "_id") && !known[name] {
				t.Errorf("%s description references unknown tool %q", prefix, name)
			}
		}
	}
}

func TestToolInputsUseMCPIDDescriptions(t *testing.T) {
	t.Parallel()
	session := connect(t, Tools, echoOptions(t))
	for _, tool := range listTools(t, session).Tools {
		encoded, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal %s: %v", tool.Name, err)
		}
		var schema any
		if err := json.Unmarshal(encoded, &schema); err != nil {
			t.Fatalf("decode %s: %v", tool.Name, err)
		}
		assertNoAPIIDDescriptions(t, tool.Name, schema)
	}
}

func assertNoAPIIDDescriptions(t *testing.T, tool string, value any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		if description, ok := typed["description"].(string); ok && apiIDDescription.MatchString(description) {
			t.Errorf("%s: %q has no MCP ID description", tool, description)
		}
		for _, child := range typed {
			assertNoAPIIDDescriptions(t, tool, child)
		}
	case []any:
		for _, item := range typed {
			assertNoAPIIDDescriptions(t, tool, item)
		}
	}
}
