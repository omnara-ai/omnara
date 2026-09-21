package agentconfig

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
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

func compileTools(
	source AgentConfigSource,
	opts CompileOptions,
	interactionDefaults bool,
) (map[string]ToolCompiled, error) {
	tools := maps.Clone(source.Tools)
	defaults := missingDefaultToolNames(source)
	if interactionDefaults {
		for _, name := range toolcatalog.InteractionHandlerToolNames() {
			if _, exists := tools[name]; !exists {
				defaults = append(defaults, name)
			}
		}
	}
	if tools == nil {
		tools = map[string]AgentConfigToolSource{}
	}
	for _, name := range defaults {
		tools[name] = AgentConfigToolSource{}
	}
	compiled := make(map[string]ToolCompiled, len(tools))
	catalog, err := toolcatalog.Default()
	if err != nil {
		return nil, err
	}
	for _, name := range slices.Sorted(maps.Keys(tools)) {
		tool := tools[name]
		enabled := tool.Enabled == nil || *tool.Enabled
		var entry ToolCompiled
		switch {
		case toolcatalog.UsesAppToolNamespace(name):
			entry, err = compileAppTool(name, tool, opts)
		case tool.Type == toolcatalog.ToolTypeCustom:
			entry, err = compileCustomTool(name, tool, enabled, catalog)
		default:
			entry, err = compileBuiltInTool(name, tool, enabled, catalog)
		}
		if err != nil {
			return nil, err
		}
		compiled[name] = entry
	}
	return compiled, nil
}

var compiledToolSourceSchema = sync.OnceValues(func() (*kjsonschema.Schema, error) {
	schema := agentConfigSourceSchema()
	schema.Required = nil
	for name := range *schema.Properties {
		if !toolSourceField(name) {
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
	return ToolsFromSourceWithOptions(format, raw, CompileOptions{})
}

// ToolsFromSourceWithOptions resolves qualified app operations using project-scoped names.
func ToolsFromSourceWithOptions(format SourceFormat, raw []byte, opts CompileOptions) ([]ResolvedTool, error) {
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
		if !toolSourceField(name) {
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
	opts = cacheAppResolver(opts)
	tools, err := compileTools(source, opts, true)
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

func toolSourceField(name string) bool {
	switch name {
	case "tools", "machine_sources", "skills", "subagents", "mcp", "interaction_handlers":
		return true
	default:
		return false
	}
}
