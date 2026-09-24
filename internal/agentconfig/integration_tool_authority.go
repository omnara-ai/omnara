package agentconfig

import (
	"fmt"
	"reflect"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

type IntegrationToolAuthority struct {
	Tool       ToolCompiled
	Definition toolcatalog.IntegrationToolDefinition
}

func ResolveIntegrationToolAuthority(
	original, current RuntimeContract,
	name string,
	integrations map[uuid.UUID]IntegrationResolution,
) (IntegrationToolAuthority, error) {
	_, operation, ok := toolcatalog.SplitIntegrationToolName(name)
	if !ok {
		return IntegrationToolAuthority{}, fmt.Errorf("invalid integration tool %q", name)
	}
	before, was := original.IntegrationTools[name]
	after, is := current.IntegrationTools[name]
	if !was || !is || !before.Enabled || !after.Enabled || after.Permission.Mode == toolpermission.ModeAlwaysDeny {
		return IntegrationToolAuthority{}, fmt.Errorf(
			"integration tool %q is unavailable in the original or current config",
			name,
		)
	}
	if before.IntegrationID != after.IntegrationID || !reflect.DeepEqual(before.Permission, after.Permission) {
		return IntegrationToolAuthority{}, fmt.Errorf("integration tool identity or permission changed; submit a new call")
	}
	definition, err := resolvedDefinition(before.IntegrationID, integrations)
	if err != nil {
		return IntegrationToolAuthority{}, err
	}
	metadata, ok := toolcatalog.LookupIntegrationTool(definition.IntegrationType, operation)
	if !ok {
		return IntegrationToolAuthority{}, fmt.Errorf("integration does not export operation %q", operation)
	}
	return IntegrationToolAuthority{Tool: before, Definition: metadata}, nil
}

func ResolveInteractionHandlerAuthority(
	original, current RuntimeContract,
	key string,
	integrations map[uuid.UUID]IntegrationResolution,
) (PreparedIntegrationInteractionHandler, error) {
	before, was := original.InteractionHandlers[key]
	after, is := current.InteractionHandlers[key]
	if !was || !is || before.IntegrationID != after.IntegrationID {
		return PreparedIntegrationInteractionHandler{}, fmt.Errorf("interaction handler changed or is unavailable")
	}
	definition, err := resolvedDefinition(before.IntegrationID, integrations)
	if err != nil {
		return PreparedIntegrationInteractionHandler{}, err
	}
	if definition.InteractionHandler == nil {
		return PreparedIntegrationInteractionHandler{}, fmt.Errorf("integration has no interaction handler")
	}
	prepared, err := definition.InteractionHandler.Prepare()
	if err != nil {
		return PreparedIntegrationInteractionHandler{}, err
	}
	return PreparedIntegrationInteractionHandler{
		IntegrationID:              before.IntegrationID,
		PreparedInteractionHandler: prepared,
	}, nil
}
