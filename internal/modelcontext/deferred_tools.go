package modelcontext

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type ToolSearchDefinition struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
}

type ToolSearchResult struct {
	Pattern            string                 `json:"pattern"`
	ToolNames          []string               `json:"tool_names"`
	TotalDeferredTools int                    `json:"total_deferred_tools"`
	Tools              []ToolSearchDefinition `json:"tools"`
	CodeModeTools      []ToolSearchDefinition `json:"code_mode_tools,omitempty"`
}

func toolSearchDefinition(spec ToolSpec) ToolSearchDefinition {
	schema := spec.InputSchema
	if len(schema) == 0 {
		schema = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return ToolSearchDefinition{Name: spec.Name, Description: spec.Description, InputSchema: schema}
}

func CodeModeToolsNote(search ToolSearchResult) string {
	if len(search.CodeModeTools) == 0 {
		return ""
	}
	encoded, err := json.Marshal(search.CodeModeTools)
	if err != nil {
		return ""
	}
	return "Commands call these with `omnarad call <tool> '<json input>'`; they are not tools of yours: " +
		string(encoded)
}

func DeferredToolsEnabled(specs []ToolSpec) bool {
	for _, spec := range specs {
		if spec.Deferred {
			return true
		}
	}
	return false
}

func DeferredToolSpecs(specs []ToolSpec) []ToolSpec {
	var deferred []ToolSpec
	for _, spec := range specs {
		if spec.Deferred {
			deferred = append(deferred, spec)
		}
	}
	return deferred
}

func LoadedToolSpecs(specs []ToolSpec) []ToolSpec {
	loaded := make([]ToolSpec, 0, len(specs))
	for _, spec := range specs {
		if !spec.Deferred {
			loaded = append(loaded, spec)
		}
	}
	return loaded
}

func ToolSpecByName(specs []ToolSpec, name string) (ToolSpec, bool) {
	for _, spec := range specs {
		if spec.Name == name {
			return spec, true
		}
	}
	return ToolSpec{}, false
}

func DeferredToolSearchDefinitions(specs []ToolSpec, search ToolSearchResult) []ToolSearchDefinition {
	definitions := make([]ToolSearchDefinition, 0, len(search.Tools))
	for _, definition := range search.Tools {
		if spec, ok := ToolSpecByName(specs, definition.Name); ok && spec.Deferred {
			definitions = append(definitions, toolSearchDefinition(spec))
		}
	}
	return definitions
}

func SearchDeferredTools(specs []ToolSpec, pattern string, maxResults int) (ToolSearchResult, error) {
	if maxResults <= 0 {
		maxResults = toolcatalog.ToolSearchDefaultResults
	}
	deferred := DeferredToolSpecs(specs)
	pattern, matches, err := searchTools(deferred, pattern, min(maxResults, toolcatalog.ToolSearchMaxResults))
	if err != nil {
		return ToolSearchResult{}, err
	}
	result := ToolSearchResult{
		Pattern:            pattern,
		ToolNames:          []string{},
		TotalDeferredTools: len(deferred),
		Tools:              []ToolSearchDefinition{},
	}
	for _, spec := range matches {
		definition := toolSearchDefinition(spec)
		if spec.CodeMode {
			definition.OutputSchema = spec.OutputSchema
			result.CodeModeTools = append(result.CodeModeTools, definition)
			continue
		}
		result.ToolNames = append(result.ToolNames, spec.Name)
		result.Tools = append(result.Tools, definition)
	}
	return result, nil
}

type ProgramToolSearchResult struct {
	Pattern string                 `json:"pattern"`
	Tools   []ToolSearchDefinition `json:"tools"`
	Total   int                    `json:"total"`
}

func SearchTools(specs []ToolSpec, pattern string, maxResults int) (ProgramToolSearchResult, error) {
	pattern, matches, err := searchTools(specs, pattern, 0)
	if err != nil {
		return ProgramToolSearchResult{}, err
	}
	result := ProgramToolSearchResult{Pattern: pattern, Tools: []ToolSearchDefinition{}, Total: len(matches)}
	if maxResults > 0 && len(matches) > maxResults {
		matches = matches[:maxResults]
	}
	for _, spec := range matches {
		definition := toolSearchDefinition(spec)
		definition.OutputSchema = spec.OutputSchema
		result.Tools = append(result.Tools, definition)
	}
	return result, nil
}

func searchTools(specs []ToolSpec, pattern string, limit int) (string, []ToolSpec, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return "", nil, errors.New("pattern is required")
	}
	if len(pattern) > toolcatalog.ToolSearchMaxPatternLength {
		return "", nil, fmt.Errorf(
			"pattern must be at most %d characters",
			toolcatalog.ToolSearchMaxPatternLength,
		)
	}
	matcher, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return "", nil, fmt.Errorf("invalid regular expression pattern: %w", err)
	}
	var matches []ToolSpec
	for _, spec := range specs {
		if matcher.MatchString(toolSearchText(spec)) {
			matches = append(matches, spec)
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].Name < matches[j].Name })
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	return pattern, matches, nil
}

func toolSearchText(spec ToolSpec) string {
	var b strings.Builder
	b.WriteString(spec.Name)
	b.WriteString("\n")
	b.WriteString(spec.Description)
	var schema map[string]any
	if json.Unmarshal(spec.InputSchema, &schema) == nil {
		appendSchemaSearchText(&b, schema)
	}
	return b.String()
}

func appendSchemaSearchText(b *strings.Builder, schema map[string]any) {
	if description, ok := schema["description"].(string); ok {
		b.WriteString("\n")
		b.WriteString(description)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return
	}
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		b.WriteString("\n")
		b.WriteString(name)
		if property, ok := properties[name].(map[string]any); ok {
			appendSchemaSearchText(b, property)
			if items, ok := property["items"].(map[string]any); ok {
				appendSchemaSearchText(b, items)
			}
		}
	}
}

func ToolSearchResultFromToolResult(result ToolResultRef) (ToolSearchResult, bool) {
	if result.Name != toolcatalog.ToolNameToolSearch {
		return ToolSearchResult{}, false
	}
	var parts []struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
	}
	if json.Unmarshal(result.ContentParts, &parts) != nil {
		return ToolSearchResult{}, false
	}
	for _, part := range parts {
		if part.Type != "structured_data" {
			continue
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(part.Value, &fields) != nil {
			continue
		}
		if _, ok := fields["tool_names"]; !ok {
			continue
		}
		var search ToolSearchResult
		if json.Unmarshal(part.Value, &search) != nil {
			return ToolSearchResult{}, false
		}
		return search, true
	}
	return ToolSearchResult{}, false
}
