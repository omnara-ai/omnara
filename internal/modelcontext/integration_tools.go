package modelcontext

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func describeIntegrationToolConversations(ctx context.Context, store IntegrationStore, projectID, agentID uuid.UUID,
	contract agentconfig.RuntimeContract, integrations map[uuid.UUID]agentconfig.IntegrationResolution,
	tools []agentconfig.RuntimeTool,
) error {
	descriptions := map[uuid.UUID]string{}
	for i := range tools {
		tool := &tools[i]
		id := contract.IntegrationTools[tool.Name].IntegrationID
		kind := integrations[id].IntegrationKind
		_, operation, _ := toolcatalog.SplitIntegrationToolName(tool.Name)
		definition, ok := toolcatalog.LookupIntegrationTool(kind, operation)
		if !ok || definition.Scope != toolcatalog.IntegrationToolScopeConversation {
			continue
		}
		description, cached := descriptions[id]
		if !cached {
			address, found, err := store.GetAgentIntegrationConversation(ctx, projectID, agentID, id)
			if err != nil {
				return fmt.Errorf("load integration tool conversation: %w", err)
			}
			description = "No conversation is assigned to this agent; this tool cannot run."
			if found {
				integration, _ := integrationdefinition.Lookup(kind)
				scope, err := integrationdefinition.ParseConversation(integration.Provider, address.Kind, address.Ref)
				if err != nil {
					return err
				}
				conversation, err := scope.ConversationJSON()
				if err != nil {
					return err
				}
				description = "Assigned conversation: " + string(conversation) +
					". Messages from other subscriptions do not change this tool's destination."
			}
			descriptions[id] = description
		}
		tool.Description += " " + description
	}
	return nil
}
