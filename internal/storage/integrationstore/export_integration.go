//go:build integration

package integrationstore

import (
	"context"

	"github.com/google/uuid"
)

func (s *Store) IntegrationSetTargetRefGenerator(generator func(string) (string, error)) {
	s.targetRefGenerator = generator
}

func (s *Store) DeleteIntegrationConnectionOnceForIntegration(
	ctx context.Context,
	projectID, connectionID uuid.UUID,
) error {
	return s.deleteIntegrationConnectionOnce(ctx, projectID, connectionID)
}
