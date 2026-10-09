package agentconfig

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

// TestSpecSchemaMatchesGoConstants pins the values the OpenAPI definition
// schema shares with Go code, so changing either side alone fails here.
func TestSpecSchemaMatchesGoConstants(t *testing.T) {
	raw, err := specJSONSchema(OpenAPIDefinitionComponent)
	if err != nil {
		t.Fatalf("load definition schema: %v", err)
	}
	var schema any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode definition schema: %v", err)
	}
	for pointer, want := range map[string]any{
		"/$defs/AgentConfigDefinitionTools/anyOf/0/propertyNames/allOf/0/anyOf/0/pattern": toolcatalog.ToolNamePattern,
		"/$defs/AgentConfigDefinitionInteractionHandlers/anyOf/0/propertyNames/pattern":   toolcatalog.IntegrationNamePattern,
		"/$defs/AgentConfigDefinitionGitCredentials/properties/integration/pattern":       toolcatalog.IntegrationNamePattern,
		"/$defs/AgentConfigDefinitionTools/anyOf/0/propertyNames/allOf/1/not/pattern":     "^" + toolcatalog.MCPRuntimeToolPrefix,
		"/$defs/AgentConfigDefinitionSubagents/anyOf/0/propertyNames/pattern":             toolcatalog.ToolNamePattern,
		"/$defs/AgentConfigDefinitionMCPServers/anyOf/0/propertyNames/pattern":            toolcatalog.MCPServerKeyPattern,
		"/$defs/AgentConfigDefinitionMCPServer/properties/tools/propertyNames/pattern":    toolcatalog.MCPRemoteToolNamePattern,
		"/$defs/AgentConfigDefinitionTool/properties/type/enum": []any{
			toolcatalog.ToolTypeBuiltIn, toolcatalog.ToolTypeCustom,
		},
		"/$defs/AgentConfigDefinitionTool/if/properties/type/const":          toolcatalog.ToolTypeCustom,
		"/$defs/AgentConfigDefinitionToolInputSchema/properties/type/const":  toolcatalog.ToolInputSchemaObject,
		"/$defs/AgentConfigDefinitionSubagent/properties/type/enum":          []any{SubagentTypeProfile, SubagentTypeSelf},
		"/$defs/AgentConfigDefinitionSubagent/if/properties/type/const":      SubagentTypeProfile,
		"/$defs/AgentConfigDefinitionMCPAuth/properties/type/enum":           []any{MCPAuthTypeBearer, MCPAuthTypeOAuth, MCPAuthTypeSigV4},
		"/$defs/AgentConfigDefinitionMachineSource/properties/cwd/maxLength": float64(MaxMachineCwdLength),
		"/$defs/ResourceNameReference/maxLength":                             float64(resourcename.MaxCodePoints),
		"/properties/max_depth/maximum":                                      float64(MaxSubagentDepth),
		"/$defs/AgentConfigDefinitionEventWebhook/properties/events/items/enum": []any{
			string(events.KindAgentInput), string(events.KindModelOutput), string(events.KindToolResult),
			string(events.KindContextCheckpoint), "tool_call_update",
		},
	} {
		got, ok := jsonPointerValue(schema, pointer)
		if !ok {
			t.Errorf("%s: missing from the definition schema", pointer)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %#v, want %#v", pointer, got, want)
		}
	}
}

func jsonPointerValue(node any, pointer string) (any, bool) {
	for token := range strings.SplitSeq(strings.TrimPrefix(pointer, "/"), "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		switch typed := node.(type) {
		case map[string]any:
			value, ok := typed[token]
			if !ok {
				return nil, false
			}
			node = value
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, false
			}
			node = typed[index]
		default:
			return nil, false
		}
	}
	return node, true
}
