package executionstore

import (
	"context"
	"fmt"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func (s *Store) GetProviderReplaySuppressionCutoff(
	ctx context.Context,
	projectID, agentID, modelCallContextID ID,
) (int64, error) {
	cutoff, err := s.q.GetProviderReplaySuppressionCutoff(
		ctx,
		dbsqlc.GetProviderReplaySuppressionCutoffParams{
			ProjectID:          projectID,
			AgentID:            agentID,
			ModelCallContextID: modelCallContextID,
		},
	)
	if err != nil {
		return 0, fmt.Errorf("get provider replay suppression cutoff: %w", err)
	}
	return cutoff, nil
}
