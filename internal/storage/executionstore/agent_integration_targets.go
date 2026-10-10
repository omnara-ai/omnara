package executionstore

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
)

type IntegrationAccess struct{}

func (IntegrationAccess) ClearIntegrationTargetsFromAgents(
	ctx context.Context,
	unit *agentexecution.Unit,
	projectID, integrationID uuid.UUID,
) error {
	return unit.ClearIntegrationTargets(ctx, projectID, integrationID)
}
