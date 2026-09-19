package agentconfig

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

// DeriveWithAppResources adds concrete attachments to a pinned compiled base.
// Only the new attachments are compiled/resolved: model, machines, skills and
// subagents retain their immutable identities. Existing tool/server policy wins.
// Resource names cannot replace existing authority; callers choose a new key.
func DeriveWithAppResources(
	base Compiled,
	resources map[string]AgentConfigAppResourceSource,
	opts CompileOptions,
) (Compiled, error) {
	encoded, err := EncodeCompiled(base)
	if err != nil {
		return Compiled{}, err
	}
	if _, err := RuntimeContractFromCompiled(encoded.CanonicalJSON, CompilerVersion, encoded.Hash); err != nil {
		return Compiled{}, err
	}
	var derived Compiled
	if err := json.Unmarshal(encoded.CanonicalJSON, &derived); err != nil {
		return Compiled{}, err
	}
	for key := range resources {
		if _, exists := derived.AppResources[key]; exists {
			return Compiled{}, fmt.Errorf("app resource %q already exists in the pinned base", key)
		}
	}
	// Expansion must see all pinned policies before comparing new bundles.
	// Existing provenance still determines whether subagents inherit a capability.
	addition := Compiled{
		Tools:           maps.Clone(derived.Tools),
		MCP:             maps.Clone(derived.MCP),
		AppToolPolicies: maps.Clone(derived.AppToolPolicies),
	}
	if addition.Tools == nil {
		addition.Tools = map[string]ToolCompiled{}
	}
	if err := compileAppResources(AgentConfigSource{AppResources: resources}, opts, &addition); err != nil {
		return Compiled{}, err
	}
	if derived.AppResources == nil {
		derived.AppResources = map[string]AppResourceCompiled{}
	}
	maps.Copy(derived.AppResources, addition.AppResources)
	if derived.Tools == nil {
		derived.Tools = map[string]ToolCompiled{}
	}
	for name, entry := range addition.Tools {
		if prior, exists := derived.Tools[name]; exists {
			if prior.Type != entry.Type || prior.Description != entry.Description ||
				!reflect.DeepEqual(prior.InputSchema, entry.InputSchema) {
				return Compiled{}, fmt.Errorf("app tool %q conflicts with the pinned base definition", name)
			}
			prior.AppOrigin = mergeAppOrigin(prior.AppOrigin, entry.AppOrigin, toolcatalog.AppToolProvider(name) == "")
			entry = prior
		}
		derived.Tools[name] = entry
	}
	derived.AppToolPolicies = addition.AppToolPolicies
	for name := range derived.Tools {
		delete(derived.AppToolPolicies, name)
	}
	if len(addition.MCP) > 0 && derived.MCP == nil {
		derived.MCP = map[string]MCPServerCompiled{}
	}
	for name, entry := range addition.MCP {
		if prior, exists := derived.MCP[name]; exists {
			if prior.URL != entry.URL || !reflect.DeepEqual(prior.Auth, entry.Auth) {
				return Compiled{}, fmt.Errorf("app MCP server %q conflicts with the pinned base definition", name)
			}
			prior.AppOrigin = mergeAppOrigin(prior.AppOrigin, entry.AppOrigin, true)
			entry = prior
		}
		derived.MCP[name] = entry
	}
	result, err := EncodeCompiled(derived)
	if err != nil {
		return Compiled{}, err
	}
	if _, err := RuntimeContractFromCompiled(result.CanonicalJSON, CompilerVersion, result.Hash); err != nil {
		return Compiled{}, err
	}
	return derived, nil
}

func mergeAppOrigin(base, added *AppToolOrigin, independentBase bool) *AppToolOrigin {
	if added == nil {
		return base
	}
	merged := &AppToolOrigin{Base: independentBase, ResourceKeys: slices.Clone(added.ResourceKeys)}
	if base != nil {
		merged.Base = base.Base
		merged.ResourceKeys = append(merged.ResourceKeys, base.ResourceKeys...)
	}
	slices.Sort(merged.ResourceKeys)
	merged.ResourceKeys = slices.Compact(merged.ResourceKeys)
	return merged
}
