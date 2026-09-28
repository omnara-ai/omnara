//go:build integration

package integrationstore

import (
	"context"

	"github.com/google/uuid"
)

func (s *Store) DeleteIntegrationOnceForIntegration(
	ctx context.Context,
	projectID, integrationID uuid.UUID,
) error {
	integration, err := s.GetIntegration(ctx, projectID, integrationID)
	if err != nil {
		return err
	}
	return s.deleteIntegrationOnce(ctx, integration.OrgID, projectID, integrationID)
}
