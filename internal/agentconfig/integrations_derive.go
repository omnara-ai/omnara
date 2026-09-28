package agentconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

var ErrIntegrationCapabilityUnavailable = errors.New("pinned integration capability is unavailable for composition")

type IntegrationCapabilitiesSource struct {
	Tools               map[string]AgentConfigToolSource                  `json:"tools,omitempty"`
	InteractionHandlers map[string]AgentConfigIntegrationCapabilitySource `json:"interaction_handlers,omitempty"`
}

func CompileIntegrationCapabilitiesSource(source IntegrationCapabilitiesSource, opts CompileOptions) (Compiled, error) {
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
	opts = cacheIntegrationResolver(opts)
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
		case toolcatalog.UsesIntegrationToolNamespace(key):
			tool, err = compileIntegrationTool(key, sourceTool, opts)
		case toolcatalog.IsInteractionHandlerTool(key):
			tool, err = compileBuiltInTool(key, sourceTool, sourceTool.Enabled == nil || *sourceTool.Enabled, catalog)
		default:
			return Compiled{}, fmt.Errorf("composition only accepts integration tools and interaction helpers: %q", key)
		}
		if err != nil {
			return Compiled{}, err
		}
		compiled.Tools[key] = tool
	}
	if err := compileIntegrationCapabilities(
		AgentConfigSource{InteractionHandlers: source.InteractionHandlers},
		opts,
		&compiled,
	); err != nil {
		return Compiled{}, err
	}
	return compiled, nil
}

func DeriveWithIntegrationCapabilities(
	base Compiled,
	source IntegrationCapabilitiesSource,
	opts CompileOptions,
) (Compiled, error) {
	opts = cacheIntegrationResolver(opts)
	if err := validateCompositionIntegrationPins(base, source, opts); err != nil {
		return Compiled{}, err
	}
	source.Tools = maps.Clone(source.Tools)
	source.InteractionHandlers = maps.Clone(source.InteractionHandlers)
	for key := range base.Tools {
		delete(source.Tools, key)
	}
	for key := range base.InteractionHandlers {
		delete(source.InteractionHandlers, key)
	}
	additions, err := CompileIntegrationCapabilitiesSource(source, opts)
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
		derived.InteractionHandlers = map[string]IntegrationCapabilityCompiled{}
	}
	maps.Copy(derived.InteractionHandlers, additions.InteractionHandlers)
	if err := validateCompiledIntegrations(derived); err != nil {
		return Compiled{}, err
	}
	return derived, nil
}

func validateCompositionIntegrationPins(
	base Compiled, source IntegrationCapabilitiesSource, opts CompileOptions,
) error {
	required := map[string]bool{}
	for key := range source.Tools {
		name, _, integrationTool := toolcatalog.SplitIntegrationToolName(key)
		if !integrationTool {
			continue
		}
		tool, exists := base.Tools[key]
		if !exists || (tool.Enabled && tool.Permission.Mode != toolpermission.ModeAlwaysDeny) {
			required[name] = true
		}
	}
	for name := range source.InteractionHandlers {
		required[name] = true
	}
	check := func(name string, pinnedID uuid.UUID) error {
		if !required[name] {
			return nil
		}
		integration, err := opts.ResolveIntegrationName(name)
		if err != nil {
			return err
		}
		if pinnedID != integration.IntegrationID {
			return fmt.Errorf("%w: %q is pinned to a different integration",
				ErrIntegrationCapabilityUnavailable, name)
		}
		return nil
	}
	// Even disabled entries retain their pins: adding another capability under the
	// same name must not produce a config with two different integration identities.
	for _, key := range slices.Sorted(maps.Keys(base.Tools)) {
		if name, _, ok := toolcatalog.SplitIntegrationToolName(key); ok {
			if err := check(name, base.Tools[key].IntegrationID); err != nil {
				return err
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(base.InteractionHandlers)) {
		if err := check(name, base.InteractionHandlers[name].IntegrationID); err != nil {
			return err
		}
	}
	return nil
}
