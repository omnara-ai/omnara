package executionstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type ContextCheckpointRecord struct {
	ID                             ID        `json:"id"`
	ProjectID                      ID        `json:"project_id"`
	AgentID                        ID        `json:"agent_id"`
	SummarizedThroughEventSequence int64     `json:"summarized_through_event_sequence"`
	ProducerModelCallContextID     ID        `json:"producer_model_call_context_id"`
	CheckpointEventID              ID        `json:"checkpoint_event_id"`
	CheckpointEventSequence        int64     `json:"checkpoint_event_sequence"`
	Summary                        string    `json:"summary"`
	CreatedAt                      time.Time `json:"created_at"`
}

func (s *Store) CountConsecutiveContextCheckpointLineage(
	ctx context.Context,
	projectID, agentID ID,
	inputEventSequence int64,
) (int, error) {
	if isNilID(projectID) || isNilID(agentID) || inputEventSequence <= 0 {
		return 0, errors.New("project, agent, and positive event sequence are required")
	}
	count, err := s.q.CountConsecutiveContextCheckpointLineage(
		ctx,
		dbsqlc.CountConsecutiveContextCheckpointLineageParams{
			ProjectID:          projectID,
			AgentID:            agentID,
			InputEventSequence: inputEventSequence,
		},
	)
	if err != nil {
		return 0, fmt.Errorf("count consecutive context checkpoint lineage: %w", err)
	}
	return int(count), nil
}

func (s *Store) GetContextCheckpoint(
	ctx context.Context,
	projectID, agentID, id ID,
) (ContextCheckpointRecord, bool, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(id) {
		return ContextCheckpointRecord{}, false, errors.New("project, agent, and checkpoint are required")
	}
	row, err := s.q.GetContextCheckpoint(ctx, dbsqlc.GetContextCheckpointParams{
		ProjectID: projectID,
		AgentID:   agentID,
		ID:        id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ContextCheckpointRecord{}, false, nil
	}
	if err != nil {
		return ContextCheckpointRecord{}, false, fmt.Errorf("get context checkpoint: %w", err)
	}
	return contextCheckpointRecordFromGetSQLC(row), true, nil
}

func (s *Store) GetLatestApplicableContextCheckpoint(
	ctx context.Context,
	projectID, agentID ID,
	maxEventSequence int64,
) (ContextCheckpointRecord, bool, error) {
	if isNilID(projectID) || isNilID(agentID) || maxEventSequence <= 0 {
		return ContextCheckpointRecord{}, false, errors.New("project, agent, and positive event sequence are required")
	}
	row, err := s.q.GetLatestApplicableContextCheckpoint(
		ctx,
		dbsqlc.GetLatestApplicableContextCheckpointParams{
			ProjectID:        projectID,
			AgentID:          agentID,
			MaxEventSequence: maxEventSequence,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ContextCheckpointRecord{}, false, nil
	}
	if err != nil {
		return ContextCheckpointRecord{}, false, fmt.Errorf("get latest context checkpoint: %w", err)
	}
	return contextCheckpointRecordFromLatestSQLC(row), true, nil
}

func getContextCheckpointByProducerContextTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID, modelCallContextID ID,
) (ContextCheckpointRecord, bool, error) {
	row, err := q.GetContextCheckpointByProducerContext(
		ctx,
		dbsqlc.GetContextCheckpointByProducerContextParams{
			ProjectID:                  projectID,
			AgentID:                    agentID,
			ProducerModelCallContextID: modelCallContextID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ContextCheckpointRecord{}, false, nil
	}
	if err != nil {
		return ContextCheckpointRecord{}, false, fmt.Errorf("get checkpoint by producer context: %w", err)
	}
	return contextCheckpointRecordFromProducerContextSQLC(row), true, nil
}

func (s *Store) GetContextCheckpointByProducerContext(
	ctx context.Context,
	projectID, agentID, modelCallContextID ID,
) (ContextCheckpointRecord, bool, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(modelCallContextID) {
		return ContextCheckpointRecord{}, false, errors.New("project, agent, and producer context are required")
	}
	return getContextCheckpointByProducerContextTx(ctx, s.q, projectID, agentID, modelCallContextID)
}
