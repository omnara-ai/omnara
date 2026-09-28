package executionstore

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type IntegrationAccess struct{}

func (IntegrationAccess) ClearIntegrationTargetsFromAgents(
	ctx context.Context,
	tx pgx.Tx,
	projectID, integrationID uuid.UUID,
) error {
	err := dbsqlc.New(tx).ClearDeletedIntegrationTargetsFromAgents(
		ctx,
		dbsqlc.ClearDeletedIntegrationTargetsFromAgentsParams{
			ProjectID:     projectID,
			IntegrationID: integrationID,
		},
	)
	if err != nil {
		return fmt.Errorf("clear integration targets from agents: %w", err)
	}
	return nil
}
