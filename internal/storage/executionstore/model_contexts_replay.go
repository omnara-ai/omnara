package executionstore

import (
	"context"
	"fmt"

	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func (s *Store) ModelCallOperationHasFailedWithErrorKind(
	ctx context.Context,
	projectID, agentID, modelCallContextID ID,
	errorKind modelprotocol.ErrorKind,
) (bool, error) {
	failed, err := s.q.ModelCallOperationHasFailedWithErrorKind(
		ctx,
		dbsqlc.ModelCallOperationHasFailedWithErrorKindParams{
			ProjectID:          projectID,
			AgentID:            agentID,
			ModelCallContextID: modelCallContextID,
			ErrorKind:          string(errorKind),
		},
	)
	if err != nil {
		return false, fmt.Errorf("check prior model call operation failures: %w", err)
	}
	return failed, nil
}
