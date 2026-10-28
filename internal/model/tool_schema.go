package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/modelcontext"
)

// claudeRejectedCompositions are the input schema keywords Claude rejects at the top
// level of a tool's input_schema ("does not support oneOf, allOf, or anyOf at the top
// level"). JSON Schema and MCP allow them, so MCP tools can carry them.
var claudeRejectedCompositions = []struct {
	keyword string
	phrase  string
}{
	{"anyOf", "at least one of"},
	{"oneOf", "exactly one of"},
	{"allOf", "all of"},
}

// ClaudeCompatibleToolSpecs drops top-level anyOf, oneOf and allOf from tool input
// schemas for routes that serve Claude. Clauses that only require arguments are kept
// as a sentence in the tool description; the tool still validates each call.
func ClaudeCompatibleToolSpecs(specs []modelcontext.ToolSpec) []modelcontext.ToolSpec {
	var out []modelcontext.ToolSpec
	for index, spec := range specs {
		schema, note, changed := withoutTopLevelCompositions(spec.InputSchema)
		if !changed {
			if out != nil {
				out = append(out, spec)
			}
			continue
		}
		if out == nil {
			out = append(make([]modelcontext.ToolSpec, 0, len(specs)), specs[:index]...)
		}
		spec.InputSchema = schema
		if note != "" {
			spec.Description = strings.TrimSpace(spec.Description + " " + note)
		}
		out = append(out, spec)
	}
	if out == nil {
		return specs
	}
	return out
}

func withoutTopLevelCompositions(schema json.RawMessage) (json.RawMessage, string, bool) {
	if !bytes.HasPrefix(bytes.TrimSpace(schema), []byte("{")) {
		return schema, "", false
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(schema, &decoded); err != nil {
		return schema, "", false
	}
	notes := make([]string, 0, len(claudeRejectedCompositions))
	changed := false
	for _, composition := range claudeRejectedCompositions {
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
		return schema, "", false
	}
	if _, typed := decoded["type"]; !typed {
		decoded["type"] = json.RawMessage(`"object"`)
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return schema, "", false
	}
	return encoded, strings.Join(notes, " "), true
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
