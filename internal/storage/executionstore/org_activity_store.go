package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type CountOrgActivityInput struct {
	OrgID      uuid.UUID
	ProjectIDs []uuid.UUID
	Window     UsageWindow
}

type OrgActivity struct {
	AgentsCreated int64
	MessagesSent  int64
	TokensUsed    int64
}

func (s *Store) CountOrgActivity(ctx context.Context, input CountOrgActivityInput) (OrgActivity, error) {
	if input.OrgID == uuid.Nil {
		return OrgActivity{}, errors.New("org is required")
	}
	if input.Window.Since == nil {
		return OrgActivity{}, errors.New("activity since is required")
	}
	if err := input.Window.validate(); err != nil {
		return OrgActivity{}, err
	}
	if len(input.ProjectIDs) == 0 {
		return OrgActivity{}, nil
	}
	agents, err := s.q.CountAgentsCreated(ctx, dbsqlc.CountAgentsCreatedParams{
		ProjectIds: input.ProjectIDs,
		Since:      *input.Window.Since,
		Until:      input.Window.Until,
	})
	if err != nil {
		return OrgActivity{}, fmt.Errorf("count agents created: %w", err)
	}
	messages, err := s.q.CountContentInputs(ctx, dbsqlc.CountContentInputsParams{
		ProjectIds: input.ProjectIDs,
		Since:      *input.Window.Since,
		Until:      input.Window.Until,
	})
	if err != nil {
		return OrgActivity{}, fmt.Errorf("count messages sent: %w", err)
	}
	tokens, err := s.q.SumTokensUsed(ctx, dbsqlc.SumTokensUsedParams{
		OrgID:      input.OrgID,
		ProjectIds: input.ProjectIDs,
		Since:      *input.Window.Since,
		Until:      input.Window.Until,
	})
	if err != nil {
		return OrgActivity{}, fmt.Errorf("sum tokens used: %w", err)
	}
	return OrgActivity{AgentsCreated: agents, MessagesSent: messages, TokensUsed: tokens}, nil
}
