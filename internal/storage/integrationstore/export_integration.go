//go:build integration

package integrationstore

import (
	"context"

	"github.com/google/uuid"
)

func (s *Store) DeleteIntegrationInstallOnceForIntegration(
	ctx context.Context,
	projectID, installID uuid.UUID,
) error {
	return s.deleteIntegrationInstallOnce(ctx, projectID, installID)
}
