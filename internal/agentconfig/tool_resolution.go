package agentconfig

import (
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"sync"

	kjsonschema "github.com/kaptinlin/jsonschema"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

type ResolvedTool struct {
	Name       string
	Enabled    bool
	Permission toolpermission.Selection
}

func compileTools(source AgentConfigSource) (map[string]ToolCompiled, error) {
	tools := maps.Clone(source.Tools)
	for _, name := range missingDefaultToolNames(source) {
		if tools == nil {
			tools = make(map[string]AgentConfigToolSource)
		}
		tools[name] = AgentConfigToolSource{}
	}
	compiled := make(map[string]ToolCompiled, len(tools))
	if len(tools) == 0 {
		return compiled, nil
	}
	catalog, err := toolcatalog.Default()
	if err != nil {
		return nil, err
	}
	for name, tool := range tools {
		enabled := tool.Enabled == nil || *tool.Enabled
		var entry ToolCompiled
		if tool.Type == toolcatalog.ToolTypeCustom {
			entry, err = compileCustomTool(name, tool, enabled, catalog)
		} else {
			entry, err = compileBuiltInTool(name, tool, enabled, catalog)
		}
		if err != nil {
			return nil, err
		}
		compiled[name] = entry
	}
	return compiled, nil
}

func isToolSourceField(name string) bool {
	switch name {
	case "tools", "machine_sources", "skills", "memory_stores", "subagents", "mcp":
		return true
	default:
		return false
	}
}

var compiledToolSourceSchema = sync.OnceValues(func() (*kjsonschema.Schema, error) {
	schema := agentConfigSourceSchema()
	schema.Required = nil
	for name := range *schema.Properties {
		if !isToolSourceField(name) {
			delete(*schema.Properties, name)
		}
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	return kjsonschema.NewCompiler().Compile(raw)
})

func ToolsFromSource(format SourceFormat, raw []byte) ([]ResolvedTool, error) {
	jsonSource, root, err := sourceJSON(format, raw)
	if err != nil {
		return nil, validationErrorFrom(err, root)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(jsonSource, &fields); err != nil {
		return nil, fmt.Errorf("parse agent config source: %w", err)
	}
	if fields == nil {
		return nil, fmt.Errorf("agent config source must be an object")
	}
	for name := range fields {
		if !isToolSourceField(name) {
			delete(fields, name)
		}
	}
	jsonSource, err = json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	jsonSource, err = canonicalizeSourceResourceReferences(jsonSource)
	if err != nil {
		return nil, validationErrorFrom(err, root)
	}
	schema, err := compiledToolSourceSchema()
	if err != nil {
		return nil, err
	}
	if err := validateSourceSchema(schema, jsonSource, root); err != nil {
		return nil, err
	}
	var source AgentConfigSource
	if err := json.Unmarshal(jsonSource, &source); err != nil {
		return nil, err
	}
	tools, err := compileTools(source)
	if err != nil {
		return nil, validationErrorFrom(err, root)
	}
	entries := make([]ResolvedTool, 0, len(tools))
	for name, tool := range tools {
		entries = append(entries, ResolvedTool{Name: name, Enabled: tool.Enabled, Permission: tool.Permission})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}
