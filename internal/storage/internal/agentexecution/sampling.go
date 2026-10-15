package agentexecution

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
)

var ErrSampleIneligible = errors.New("agent is not eligible for execution head sampling")

type HeadDifference struct {
	Field   string `json:"field"`
	Stored  any    `json:"stored"`
	Rebuilt any    `json:"rebuilt"`
}

func SelectExecutionSamples(ctx context.Context, tx pgx.Tx, after uuid.UUID, n int32) ([]AgentRoute, error) {
	if n <= 0 {
		return nil, nil
	}
	q := executiondb.New()
	rows, err := q.ExecutionSampleCandidates(ctx, tx, executiondb.ExecutionSampleCandidatesParams{
		AfterID: after, SampleSize: n})
	if err != nil {
		return nil, err
	}
	if len(rows) < int(n) && after != uuid.Nil {
		more, err := q.ExecutionSampleCandidates(ctx, tx, executiondb.ExecutionSampleCandidatesParams{
			AfterID: uuid.Nil, SampleSize: n - int32(len(rows))})
		if err != nil {
			return nil, err
		}
		for _, row := range more {
			if bytes.Compare(row.ID[:], after[:]) > 0 {
				break
			}
			rows = append(rows, row)
		}
	}
	routes := make([]AgentRoute, 0, len(rows))
	for _, row := range rows {
		routes = append(routes, AgentRoute{AgentID: row.ID, ProjectID: row.ProjectID, RootAgentID: row.RootAgentID})
	}
	return routes, nil
}

func CompareExecutionHead(ctx context.Context, tx pgx.Tx, route AgentRoute) ([]HeadDifference, error) {
	row, err := executiondb.New().LoadExecutionBase(ctx, tx, executiondb.LoadExecutionBaseParams{
		ProjectID: route.ProjectID, AgentID: route.AgentID})
	if err != nil {
		return nil, err
	}
	if row.State != "active" {
		return nil, ErrSampleIneligible
	}
	if !row.HeadExists {
		return []HeadDifference{{Field: "head_missing", Stored: true, Rebuilt: false}}, nil
	}
	rebuilt, err := reconstructExecution(ctx, tx, route)
	if err != nil {
		return nil, err
	}
	stored := executionHead(row)
	if (rebuilt.Selection.Work == WorkInput || rebuilt.Selection.Work == WorkTool) &&
		stored.LogicalReadyAt != nil && rebuilt.Head.LogicalReadyAt != nil {
		stored.LogicalReadyAt = nil
		rebuilt.Head.LogicalReadyAt = nil
	}
	return compareHeads(stored, rebuilt.Head), nil
}

func compareHeads(stored, rebuilt ExecutionHead) []HeadDifference {
	fields := []HeadDifference{
		{"current_turn_id", stored.CurrentTurnID, rebuilt.CurrentTurnID},
		{"stop_sequence", stored.StopSequence, rebuilt.StopSequence},
		{"max_normal_input_sequence", stored.MaxNormalInputSequence, rebuilt.MaxNormalInputSequence},
		{"max_context_input_sequence", stored.MaxContextInputSequence, rebuilt.MaxContextInputSequence},
		{"normal_context_id", stored.NormalContextID, rebuilt.NormalContextID},
		{"compaction_context_id", stored.CompactionContextID, rebuilt.CompactionContextID},
		{"pending_tool_output_id", stored.PendingToolOutputID, rebuilt.PendingToolOutputID},
		{"pending_output_limit_id", stored.PendingOutputLimitID, rebuilt.PendingOutputLimitID},
		{"pending_config_input_id", stored.PendingConfigInputID, rebuilt.PendingConfigInputID},
		{"pending_checkpoint_id", stored.PendingCheckpointID, rebuilt.PendingCheckpointID},
		{"turn_continuable", stored.TurnContinuable, rebuilt.TurnContinuable},
		{"incomplete_tools", stored.IncompleteTools, rebuilt.IncompleteTools},
		{"logical_ready_at", headTime(stored.LogicalReadyAt), headTime(rebuilt.LogicalReadyAt)},
	}
	differences := make([]HeadDifference, 0)
	for _, field := range fields {
		if field.Stored != field.Rebuilt {
			differences = append(differences, field)
		}
	}
	return differences
}

func headTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}
