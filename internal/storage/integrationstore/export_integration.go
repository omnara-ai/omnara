//go:build integration

package integrationstore

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
)

func (s *Store) DeleteIntegrationOnceForIntegration(
	ctx context.Context,
	projectID, integrationID uuid.UUID,
) error {
	integration, err := s.GetIntegration(ctx, projectID, integrationID)
	if err != nil {
		return err
	}
	_, err = agentexecution.Retry(ctx, "test integration deletion", func(ctx context.Context) (struct{}, error) {
		return struct{}{}, s.deleteIntegrationOnce(ctx, integration.OrgID, projectID, integrationID)
	})
	return err
}
