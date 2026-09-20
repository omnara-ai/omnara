package agentconfig

import (
	"bytes"
	"fmt"
	"reflect"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

type AppToolAuthority struct {
	Tool       ToolCompiled
	Definition toolcatalog.AppToolDefinition
}

// ResolveAppToolAuthority protects a pending call's immutable identity, effective
// config and permission. Live app state/credentials still gate provider I/O.
func ResolveAppToolAuthority(
	original, current RuntimeContract,
	name string,
	apps map[string]AppResolution,
) (AppToolAuthority, error) {
	_, operation, ok := toolcatalog.SplitAppToolName(name)
	if !ok {
		return AppToolAuthority{}, fmt.Errorf("invalid app tool %q", name)
	}
	before, was := original.AppTools[name]
	after, is := current.AppTools[name]
	if !was || !is || !before.Enabled || !after.Enabled || after.Permission.Mode == toolpermission.ModeAlwaysDeny {
		return AppToolAuthority{}, fmt.Errorf("app tool %q is unavailable in the original or current config", name)
	}
	if before.AppID != after.AppID || !reflect.DeepEqual(before.Permission, after.Permission) {
		return AppToolAuthority{}, fmt.Errorf("app tool identity or permission changed; submit a new call")
	}
	definition, err := resolvedDefinition(before.AppID, apps)
	if err != nil {
		return AppToolAuthority{}, err
	}
	metadata, ok := toolcatalog.LookupAppTool(definition.ID, operation)
	if !ok {
		return AppToolAuthority{}, fmt.Errorf("app does not export operation %q", operation)
	}
	first, err := metadata.CanonicalConfig(before.Config)
	if err != nil {
		return AppToolAuthority{}, err
	}
	second, err := metadata.CanonicalConfig(after.Config)
	if err != nil {
		return AppToolAuthority{}, err
	}
	if !bytes.Equal(first, second) {
		return AppToolAuthority{}, fmt.Errorf("app tool config changed; submit a new call")
	}
	before.Config = first
	return AppToolAuthority{Tool: before, Definition: metadata}, nil
}

// ResolveInteractionHandlerAuthority applies the same immutable-config rule to
// pending handler-selection calls and captured prompts. It never resolves a name
// to a replacement app. ResolveArgs performs selected-handler validation next.
func ResolveInteractionHandlerAuthority(
	original, current RuntimeContract,
	key string,
	apps map[string]AppResolution,
) (PreparedAppInteractionHandler, error) {
	before, was := original.InteractionHandlers[key]
	after, is := current.InteractionHandlers[key]
	if !was || !is || before.AppID != after.AppID {
		return PreparedAppInteractionHandler{}, fmt.Errorf("interaction handler changed or is unavailable")
	}
	definition, err := resolvedDefinition(before.AppID, apps)
	if err != nil {
		return PreparedAppInteractionHandler{}, err
	}
	if definition.InteractionHandler == nil {
		return PreparedAppInteractionHandler{}, fmt.Errorf("app has no interaction handler")
	}
	first, err := definition.InteractionHandler.Prepare(before.Config)
	if err != nil {
		return PreparedAppInteractionHandler{}, err
	}
	second, err := definition.InteractionHandler.Prepare(after.Config)
	if err != nil {
		return PreparedAppInteractionHandler{}, err
	}
	if !bytes.Equal(first.Config, second.Config) {
		return PreparedAppInteractionHandler{}, fmt.Errorf("interaction handler config changed")
	}
	return PreparedAppInteractionHandler{AppID: before.AppID, PreparedInteractionHandler: first}, nil
}
