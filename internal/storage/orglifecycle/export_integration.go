//go:build integration

package orglifecycle

import (
	"context"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func (s *Service) DeleteProjectOnceForIntegration(
	ctx context.Context,
	orgID, projectID ID,
	actor *executionstore.ActorParams,
) ([]executionstore.MachineRecord, error) {
	return s.deleteProjectOnce(ctx, orgID, projectID, actor)
}

func (s *Service) DeleteOrganizationOnceForIntegration(
	ctx context.Context,
	orgID ID,
	actor *executionstore.ActorParams,
) ([]executionstore.MachineRecord, error) {
	return s.deleteOrganizationOnce(ctx, orgID, actor)
}
