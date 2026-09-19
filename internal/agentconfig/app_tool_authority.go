package agentconfig

import (
	"fmt"
	"reflect"
	"slices"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

// AppToolAuthority preserves what a historical call meant while requiring the
// agent's current config to still authorize it. Credentials and connection state
// are checked separately immediately before provider I/O.
type AppToolAuthority struct {
	ResourceKey string
	Original    AppResourceCompiled
	Current     AppResourceCompiled
}

func ResolveAppToolAuthority(
	original, current RuntimeContract,
	toolName, resourceKey string,
) (AppToolAuthority, error) {
	if toolcatalog.AppToolProvider(toolName) == "" {
		return AppToolAuthority{}, fmt.Errorf("%q is not a provider tool", toolName)
	}
	findTool := func(contract RuntimeContract) (RuntimeTool, bool) {
		for _, tool := range contract.Tools {
			if tool.Name == toolName {
				return tool, true
			}
		}
		return RuntimeTool{}, false
	}
	before, wasAvailable := findTool(original)
	after, isAvailable := findTool(current)
	if !wasAvailable || !isAvailable || after.Permission.Mode == toolpermission.ModeAlwaysDeny {
		return AppToolAuthority{}, fmt.Errorf("app tool %q is unavailable in the original or current config", toolName)
	}
	if !reflect.DeepEqual(before.Permission, after.Permission) {
		return AppToolAuthority{}, fmt.Errorf("app tool permission changed; submit a new call")
	}
	keys := AppToolResources(original.AppResources, toolName)
	if resourceKey == "" {
		if len(keys) != 1 {
			return AppToolAuthority{}, fmt.Errorf(
				"resource is required when the original call has %d destinations",
				len(keys),
			)
		}
		resourceKey = keys[0]
	}
	if !slices.Contains(keys, resourceKey) ||
		!slices.Contains(AppToolResources(current.AppResources, toolName), resourceKey) {
		return AppToolAuthority{}, fmt.Errorf("resource %q does not authorize %s", resourceKey, toolName)
	}
	prior, next := original.AppResources[resourceKey], current.AppResources[resourceKey]
	if prior.Definition != next.Definition || prior.ConnectionID != next.ConnectionID {
		return AppToolAuthority{}, fmt.Errorf("resource %q changed provider identity; submit a new call", resourceKey)
	}
	return AppToolAuthority{ResourceKey: resourceKey, Original: prior, Current: next}, nil
}

func (a AppToolAuthority) AllowsScope(scope appdefinition.Scope) bool {
	return a.Original.Scope != nil && a.Current.Scope != nil && a.Original.Scope.Contains(scope) &&
		a.Current.Scope.Contains(scope)
}

func (a AppToolAuthority) AllowsFollowingReplies() bool {
	return a.Original.Follow != nil && a.Original.Follow.Replies &&
		a.Current.Follow != nil && a.Current.Follow.Replies
}
