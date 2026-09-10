//go:build integration

package integrationstore

import "context"

func (s *Store) IntegrationSetTargetRefGenerator(generator func(string) (string, error)) {
	s.targetRefGenerator = generator
}

func (s *Store) DeleteIntegrationInstallOnceForIntegration(
	ctx context.Context,
	projectID, installID ID,
) error {
	return s.deleteIntegrationInstallOnce(ctx, projectID, installID)
}
