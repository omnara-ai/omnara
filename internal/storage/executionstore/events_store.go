package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func (s *Store) MaxEventSequence(ctx context.Context, projectID, agentID ID) (int64, error) {
	if isNilID(projectID) || isNilID(agentID) {
		return 0, errors.New("project id and agent id are required")
	}
	sequence, err := s.q.MaxEventSequence(
		ctx,
		dbsqlc.MaxEventSequenceParams{ProjectID: projectID, AgentID: agentID},
	)
	if err != nil {
		return 0, fmt.Errorf("max event sequence: %w", err)
	}
	return sequence, nil
}

type CompactionSourceEventRecord struct {
	ID             ID              `json:"id"`
	Sequence       int64           `json:"sequence"`
	TurnID         ID              `json:"turn_id"`
	IsOpeningEvent bool            `json:"is_opening_event"`
	Kind           string          `json:"event_kind"`
	InputKind      string          `json:"input_kind,omitempty"`
	ToolName       string          `json:"tool_name,omitempty"`
	ProviderCallID string          `json:"provider_call_id,omitempty"`
	ToolOutcome    string          `json:"tool_outcome,omitempty"`
	ContentParts   json.RawMessage `json:"content_parts"`
	CreatedAt      time.Time       `json:"created_at"`
}

type CompactionAtomicGroupRecord struct {
	Kind          string `json:"kind"`
	StartSequence int64  `json:"start_sequence"`
	EndSequence   int64  `json:"end_sequence"`
}

func (s *Store) ListCompactionSourceEvents(
	ctx context.Context,
	projectID, agentID ID,
	afterSequence int64,
	limit int32,
) ([]CompactionSourceEventRecord, error) {
	if isNilID(projectID) || isNilID(agentID) {
		return nil, errors.New("project and agent are required")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.q.ListCompactionSourceEvents(
		ctx,
		dbsqlc.ListCompactionSourceEventsParams{
			ProjectID:     projectID,
			AgentID:       agentID,
			AfterSequence: afterSequence,
			PageLimit:     limit,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list compaction source events: %w", err)
	}
	out := make([]CompactionSourceEventRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, CompactionSourceEventRecord{
			ID:             row.ID,
			Sequence:       row.Sequence,
			TurnID:         row.TurnID,
			IsOpeningEvent: row.IsOpeningEvent,
			Kind:           row.EventKind,
			InputKind:      row.InputKind,
			ToolName:       row.ToolName,
			ProviderCallID: row.ProviderCallID,
			ToolOutcome:    row.ToolOutcome,
			ContentParts:   row.ContentParts,
			CreatedAt:      row.CreatedAt,
		})
	}
	return out, nil
}

func (s *Store) ListCompactionAtomicGroups(
	ctx context.Context,
	projectID, agentID ID,
	lastCheckpointEnd int64,
	inputEventSequence int64,
) ([]CompactionAtomicGroupRecord, error) {
	if isNilID(projectID) || isNilID(agentID) {
		return nil, errors.New("project and agent are required")
	}
	if inputEventSequence <= 0 {
		return nil, errors.New("input event sequence is required")
	}
	rows, err := s.q.ListCompactionAtomicGroups(
		ctx,
		dbsqlc.ListCompactionAtomicGroupsParams{
			ProjectID:          projectID,
			AgentID:            agentID,
			LastCheckpointEnd:  lastCheckpointEnd,
			InputEventSequence: inputEventSequence,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list compaction atomic groups: %w", err)
	}
	out := make([]CompactionAtomicGroupRecord, 0, len(rows))
	for _, row := range rows {
		out = append(
			out,
			CompactionAtomicGroupRecord{
				Kind:          row.GroupKind,
				StartSequence: row.StartSequence,
				EndSequence:   row.EndSequence,
			},
		)
	}
	return out, nil
}
