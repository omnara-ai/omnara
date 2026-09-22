package agentconfig

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type AppCapabilitiesSource struct {
	Tools               map[string]AgentConfigToolSource          `json:"tools,omitempty"`
	InteractionHandlers map[string]AgentConfigAppCapabilitySource `json:"interaction_handlers,omitempty"`
}

func CompileAppCapabilitiesSource(source AppCapabilitiesSource, opts CompileOptions) (Compiled, error) {
	raw, err := json.Marshal(source)
	if err != nil {
		return Compiled{}, err
	}
	schema, err := compiledToolSourceSchema()
	if err != nil {
		return Compiled{}, err
	}
	if err := validateSourceSchema(schema, raw, nil); err != nil {
		return Compiled{}, err
	}
	opts = cacheAppResolver(opts)
	compiled := Compiled{}
	if len(source.Tools) > 0 {
		compiled.Tools = map[string]ToolCompiled{}
	}
	catalog, err := toolcatalog.Default()
	if err != nil {
		return Compiled{}, err
	}
	for _, key := range slices.Sorted(maps.Keys(source.Tools)) {
		sourceTool := source.Tools[key]
		var tool ToolCompiled
		switch {
		case toolcatalog.UsesAppToolNamespace(key):
			tool, err = compileAppTool(key, sourceTool, opts)
		case toolcatalog.IsInteractionHandlerTool(key):
			tool, err = compileBuiltInTool(key, sourceTool, sourceTool.Enabled == nil || *sourceTool.Enabled, catalog)
		default:
			return Compiled{}, fmt.Errorf("composition only accepts app tools and interaction helpers: %q", key)
		}
		if err != nil {
			return Compiled{}, err
		}
		compiled.Tools[key] = tool
	}
	if err := compileAppCapabilities(
		AgentConfigSource{InteractionHandlers: source.InteractionHandlers},
		opts,
		&compiled,
	); err != nil {
		return Compiled{}, err
	}
	return compiled, nil
}

func DeriveWithAppCapabilities(base Compiled, source AppCapabilitiesSource, opts CompileOptions) (Compiled, error) {
	source.Tools = maps.Clone(source.Tools)
	source.InteractionHandlers = maps.Clone(source.InteractionHandlers)
	for key := range base.Tools {
		delete(source.Tools, key)
	}
	for key := range base.InteractionHandlers {
		delete(source.InteractionHandlers, key)
	}
	additions, err := CompileAppCapabilitiesSource(source, opts)
	if err != nil {
		return Compiled{}, err
	}
	raw, err := json.Marshal(base)
	if err != nil {
		return Compiled{}, err
	}
	var derived Compiled
	if err := json.Unmarshal(raw, &derived); err != nil {
		return Compiled{}, err
	}
	if len(additions.Tools) > 0 && derived.Tools == nil {
		derived.Tools = map[string]ToolCompiled{}
	}
	maps.Copy(derived.Tools, additions.Tools)
	if len(additions.InteractionHandlers) > 0 && derived.InteractionHandlers == nil {
		derived.InteractionHandlers = map[string]AppCapabilityCompiled{}
	}
	maps.Copy(derived.InteractionHandlers, additions.InteractionHandlers)
	if err := validateCompiledApps(derived); err != nil {
		return Compiled{}, err
	}
	return derived, nil
}
