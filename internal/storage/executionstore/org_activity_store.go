package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type CountOrgActivityInput struct {
	ProjectIDs []uuid.UUID
	Window     UsageWindow
}

type OrgActivity struct {
	AgentsCreated int64
	MessagesSent  int64
}

func (s *Store) CountOrgActivity(ctx context.Context, input CountOrgActivityInput) (OrgActivity, error) {
	if input.Window.Since == nil {
		return OrgActivity{}, errors.New("activity since is required")
	}
	if err := input.Window.validate(); err != nil {
		return OrgActivity{}, err
	}
	if len(input.ProjectIDs) == 0 {
		return OrgActivity{}, nil
	}
	row, err := s.q.CountOrgActivity(ctx, dbsqlc.CountOrgActivityParams{
		ProjectIds: input.ProjectIDs,
		Since:      *input.Window.Since,
		Until:      input.Window.Until,
	})
	if err != nil {
		return OrgActivity{}, fmt.Errorf("count org activity: %w", err)
	}
	return OrgActivity{AgentsCreated: row.AgentsCreated, MessagesSent: row.MessagesSent}, nil
}
