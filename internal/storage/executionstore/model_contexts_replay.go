package executionstore

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

// ProviderReplayRejections lists, per rejected model slug, the newest replay
// rejection on the model call's provider route at or before its input frontier.
type ProviderReplayRejections struct {
	ProviderModelSlug string
	APIFormat         modelprotocol.APIFormat
	// CutoffBySlug maps each rejected model slug to its inclusive event cutoff.
	CutoffBySlug map[string]int64
}

func (s *Store) ListProviderReplayRejections(
	ctx context.Context,
	projectID, agentID, modelCallContextID uuid.UUID,
) (ProviderReplayRejections, error) {
	rows, err := s.q.ListProviderReplayRejectionCutoffs(
		ctx,
		dbsqlc.ListProviderReplayRejectionCutoffsParams{
			ProjectID:          projectID,
			AgentID:            agentID,
			ModelCallContextID: modelCallContextID,
		},
	)
	if err == nil && len(rows) == 0 {
		err = pgx.ErrNoRows
	}
	if err != nil {
		return ProviderReplayRejections{}, fmt.Errorf("list provider replay rejections: %w", err)
	}
	rejections := ProviderReplayRejections{
		ProviderModelSlug: rows[0].CurrentProviderModelSlug,
		APIFormat:         modelprotocol.APIFormat(rows[0].CurrentApiFormat),
		CutoffBySlug:      make(map[string]int64, len(rows)),
	}
	for _, row := range rows {
		if row.CutoffEventSequence > 0 {
			rejections.CutoffBySlug[row.RejectedProviderModelSlug] = row.CutoffEventSequence
		}
	}
	return rejections, nil
}
