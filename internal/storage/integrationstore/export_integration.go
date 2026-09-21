//go:build integration

package integrationstore

import (
	"context"

	"github.com/google/uuid"
)

func (s *Store) DeleteProjectAppOnceForIntegration(
	ctx context.Context,
	projectID, appID uuid.UUID,
) error {
	app, err := s.GetProjectApp(ctx, projectID, appID)
	if err != nil {
		return err
	}
	return s.deleteProjectAppOnce(ctx, app.OrgID, projectID, appID)
}
