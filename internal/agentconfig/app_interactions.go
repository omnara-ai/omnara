package agentconfig

import (
	"slices"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func hasEnabledInteractionHandler(resources map[string]AppResourceCompiled) bool {
	for _, resource := range resources {
		if resource.Enabled && resource.InteractionHandler != nil {
			return true
		}
	}
	return false
}

// Interaction discovery depends on declared handlers, never targets created by
// input delivery. Global tool overrides retain their enabled and permission policy.
func compileInteractionDestinationTools(
	source AgentConfigSource, compiled *Compiled, catalog toolcatalog.Catalog,
) error {
	var keys []string
	for key, resource := range compiled.AppResources {
		if resource.Enabled && resource.InteractionHandler != nil {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	slices.Sort(keys)
	for _, name := range toolcatalog.InteractionDestinationToolNames() {
		entry, exists := compiled.Tools[name]
		if !exists {
			var err error
			entry, err = compileBuiltInTool(name, AgentConfigToolSource{}, true, catalog)
			if err != nil {
				return err
			}
		}
		_, explicit := source.Tools[name]
		origins := slices.Clone(keys)
		if entry.AppOrigin != nil {
			origins = append(origins, entry.AppOrigin.ResourceKeys...)
			explicit = explicit || entry.AppOrigin.Base
		}
		slices.Sort(origins)
		entry.AppOrigin = &AppToolOrigin{ResourceKeys: slices.Compact(origins), Base: explicit}
		compiled.Tools[name] = entry
	}
	return nil
}
