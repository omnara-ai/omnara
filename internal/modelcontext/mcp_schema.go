package modelcontext

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// topLevelCompositions are the input schema keywords Anthropic rejects at the top level
// of a tool's input_schema ("does not support oneOf, allOf, or anyOf at the top level").
// OpenAI-compatible routes such as OpenRouter forward the schema to Anthropic unchanged.
var topLevelCompositions = []struct {
	keyword string
	phrase  string
}{
	{"anyOf", "at least one of"},
	{"oneOf", "exactly one of"},
	{"allOf", "all of"},
}

// providerSafeMCPInputSchema drops top-level anyOf, oneOf and allOf from an MCP tool's
// input schema so every model provider accepts it. Clauses that only require arguments
// are kept as a sentence for the tool description; the MCP server still validates
// each call. A schema that is not a JSON object is returned unchanged.
func providerSafeMCPInputSchema(schema json.RawMessage) (json.RawMessage, string, error) {
	if !bytes.HasPrefix(bytes.TrimSpace(schema), []byte("{")) {
		return schema, "", nil
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(schema, &decoded); err != nil {
		return nil, "", fmt.Errorf("decode mcp input schema: %w", err)
	}
	notes := make([]string, 0, len(topLevelCompositions))
	changed := false
	for _, composition := range topLevelCompositions {
		raw, ok := decoded[composition.keyword]
		if !ok {
			continue
		}
		delete(decoded, composition.keyword)
		changed = true
		if note, ok := requiredOnlyNote(raw, composition.phrase); ok {
			notes = append(notes, note)
		}
	}
	if !changed {
		return schema, "", nil
	}
	if _, typed := decoded["type"]; !typed {
		decoded["type"] = json.RawMessage(`"object"`)
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return nil, "", fmt.Errorf("encode mcp input schema: %w", err)
	}
	return encoded, strings.Join(notes, " "), nil
}

// requiredOnlyNote describes clauses that only list required arguments, such as
// [{"required":["profile"]},{"required":["config"]}], as "Provide exactly one of: profile; config."
func requiredOnlyNote(raw json.RawMessage, phrase string) (string, bool) {
	var clauses []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &clauses); err != nil || len(clauses) == 0 {
		return "", false
	}
	options := make([]string, 0, len(clauses))
	for _, clause := range clauses {
		var required []string
		if len(clause) != 1 || json.Unmarshal(clause["required"], &required) != nil || len(required) == 0 {
			return "", false
		}
		options = append(options, strings.Join(required, " + "))
	}
	return fmt.Sprintf("Provide %s: %s.", phrase, strings.Join(options, "; ")), true
}
