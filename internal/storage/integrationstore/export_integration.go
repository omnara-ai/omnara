//go:build integration

package integrationstore

import (
	"context"

	"github.com/google/uuid"
)

func (s *Store) DeleteProjectIntegrationOnceForIntegration(
	ctx context.Context,
	projectID, integrationID uuid.UUID,
) error {
	integration, err := s.GetProjectIntegration(ctx, projectID, integrationID)
	if err != nil {
		return err
	}
	return s.deleteProjectIntegrationOnce(ctx, integration.OrgID, projectID, integrationID)
}
