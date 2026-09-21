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

// CompileAppCapabilitiesSource compiles app capabilities alone: no default
// tools or model/machine/skill resolution. The app resolver is project scoped.
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
	for _, key := range slices.Sorted(maps.Keys(source.Tools)) {
		if !toolcatalog.UsesAppToolNamespace(key) {
			return Compiled{}, fmt.Errorf("composition only accepts app tools: %q", key)
		}
		tool, err := compileAppTool(key, source.Tools[key], opts)
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

// DeriveWithAppCapabilities compiles only missing app capabilities into a pinned
// base. Existing entries win completely, including disabled tools and different
// destinations. Existing keys are removed before validation or resolution.
// Launcher subscriptions are admitted separately by storage.
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
