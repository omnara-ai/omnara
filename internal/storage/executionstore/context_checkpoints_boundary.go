package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

var ErrCheckpointBoundaryUnsafe = errors.New("context checkpoint boundary is unsafe")

func validateClosedCheckpointRangeTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID ID,
	start, end int64,
) error {
	count, err := dbsqlc.New(tx).CountCheckpointRangeEvents(
		ctx,
		dbsqlc.CountCheckpointRangeEventsParams{
			ProjectID:     projectID,
			AgentID:       agentID,
			StartSequence: start,
			EndSequence:   end,
		},
	)
	if err != nil {
		return fmt.Errorf("validate checkpoint range: %w", err)
	}
	if count != end-start+1 {
		return fmt.Errorf(
			"%w: range %d..%d is not a closed event range",
			ErrCheckpointBoundaryUnsafe,
			start,
			end,
		)
	}
	return nil
}

func validateCheckpointDoesNotCutOpenAuthoritiesTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID ID,
	start, end int64,
) error {
	count, err := dbsqlc.New(tx).CountCheckpointRangeOpenToolResults(
		ctx,
		dbsqlc.CountCheckpointRangeOpenToolResultsParams{
			ProjectID:     projectID,
			AgentID:       agentID,
			StartSequence: start,
			EndSequence:   end,
		},
	)
	if err != nil {
		return fmt.Errorf("validate checkpoint open tool results: %w", err)
	}
	if count != 0 {
		return fmt.Errorf(
			"%w: range %d..%d cuts an active tool call",
			ErrCheckpointBoundaryUnsafe,
			start,
			end,
		)
	}
	return nil
}
