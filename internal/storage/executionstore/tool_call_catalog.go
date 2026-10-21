package executionstore

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func (r *ToolCallReader) ResolveIntegrationDefinitions(
	ctx context.Context, projectID uuid.UUID, ids []uuid.UUID,
) (map[uuid.UUID]agentconfig.IntegrationResolution, error) {
	return integrationstore.ResolveIntegrationDefinitionsTx(ctx, r.transaction.q, projectID, ids)
}

func (r *ToolCallReader) GetAgentIntegrationConversation(
	ctx context.Context, projectID, agentID, integrationID uuid.UUID,
) (integrationstore.ConversationAddress, bool, error) {
	return integrationstore.GetAgentIntegrationConversationTx(ctx, r.transaction.q, projectID, agentID, integrationID)
}

func (r *ToolCallReader) ListAgentMCPConnections(
	ctx context.Context, projectID, agentID uuid.UUID,
) ([]MCPConnectionRecord, error) {
	store := Store{q: r.transaction.q}
	return store.ListAgentMCPConnections(ctx, projectID, agentID)
}
