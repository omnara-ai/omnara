//go:build integration

package agentexecution

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
)

type reconstructionQueries struct{ *executiondb.Queries }

const loadExecutionBoundaries = `
SELECT coalesce((SELECT max(e.sequence) FROM agent_events e
                 JOIN agent_inputs i ON i.agent_id=e.agent_id AND i.id=e.agent_input_id
                 WHERE i.project_id=$1 AND i.agent_id=$2 AND i.input_kind='control'
                   AND i.state='resolved' AND i.control_type='cancel_current'),0)::bigint AS stop_sequence,
       coalesce((SELECT t.id FROM agent_turns t WHERE t.agent_id=$2 ORDER BY t.turn_sequence DESC LIMIT 1),
                 '00000000-0000-0000-0000-000000000000'::uuid)::uuid AS turn_id
`

type LoadExecutionBoundariesParams struct {
	ProjectID uuid.UUID
	AgentID   uuid.UUID
}

type LoadExecutionBoundariesRow struct {
	StopSequence int64
	TurnID       uuid.UUID
}

func (q *reconstructionQueries) LoadExecutionBoundaries(
	ctx context.Context, db executiondb.DBTX, arg LoadExecutionBoundariesParams,
) (LoadExecutionBoundariesRow, error) {
	row := db.QueryRow(ctx, loadExecutionBoundaries, arg.ProjectID, arg.AgentID)
	var i LoadExecutionBoundariesRow
	err := row.Scan(&i.StopSequence, &i.TurnID)
	return i, err
}

const reconstructExecutionOutputs = `
SELECT o.id,o.model_call_context_id,o.stop_reason,e.id AS event_id,e.sequence,e.created_at
FROM model_outputs o JOIN agent_events e ON e.agent_id=o.agent_id AND e.model_output_id=o.id
WHERE o.agent_id=$1 AND e.turn_id=$2 AND e.sequence>=$3
ORDER BY e.sequence
`

type ReconstructExecutionOutputsParams struct {
	AgentID      uuid.UUID
	TurnID       uuid.UUID
	StopSequence int64
}

type ReconstructExecutionOutputsRow struct {
	ID                 uuid.UUID
	ModelCallContextID uuid.UUID
	StopReason         string
	EventID            uuid.UUID
	Sequence           int64
	CreatedAt          time.Time
}

func (q *reconstructionQueries) ReconstructExecutionOutputs(
	ctx context.Context, db executiondb.DBTX, arg ReconstructExecutionOutputsParams,
) ([]ReconstructExecutionOutputsRow, error) {
	rows, err := db.Query(ctx, reconstructExecutionOutputs, arg.AgentID, arg.TurnID, arg.StopSequence)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[ReconstructExecutionOutputsRow])
}

const loadExecutionTurn = `
SELECT t.id,semantic.sequence AS semantic_sequence,semantic.created_at AS semantic_time,
       coalesce((SELECT e.sequence FROM agent_events e WHERE e.agent_id=t.agent_id AND e.turn_id=t.id AND
 e.is_opening_event
        ORDER BY e.sequence LIMIT 1),0)::bigint AS first_opening_sequence,
       coalesce((SELECT e.sequence FROM agent_events e WHERE e.agent_id=t.agent_id AND e.turn_id=t.id AND
 e.is_opening_event
        ORDER BY e.sequence DESC LIMIT 1),0)::bigint AS last_opening_sequence,
       coalesce((SELECT e.sequence FROM agent_events e JOIN agent_inputs i ON i.agent_id=e.agent_id AND
 i.id=e.agent_input_id
                 WHERE e.agent_id=t.agent_id AND e.turn_id=t.id AND e.is_opening_event AND i.input_kind='content'
                 ORDER BY e.sequence LIMIT 1),0)::bigint AS first_content_sequence
FROM agent_turns t JOIN agent_events semantic ON semantic.agent_id=t.agent_id AND
 semantic.id=t.latest_semantic_event_id
WHERE t.agent_id=$1 AND t.id=$2
`

type LoadExecutionTurnParams struct {
	AgentID uuid.UUID
	ID      uuid.UUID
}

type LoadExecutionTurnRow struct {
	ID                   uuid.UUID
	SemanticSequence     int64
	SemanticTime         time.Time
	FirstOpeningSequence int64
	LastOpeningSequence  int64
	FirstContentSequence int64
}

func (q *reconstructionQueries) LoadExecutionTurn(
	ctx context.Context, db executiondb.DBTX, arg LoadExecutionTurnParams,
) (LoadExecutionTurnRow, error) {
	row := db.QueryRow(ctx, loadExecutionTurn, arg.AgentID, arg.ID)
	var i LoadExecutionTurnRow
	err := row.Scan(
		&i.ID,
		&i.SemanticSequence,
		&i.SemanticTime,
		&i.FirstOpeningSequence,
		&i.LastOpeningSequence,
		&i.FirstContentSequence,
	)
	return i, err
}

const reconstructExecutionCheckpoint = `
SELECT c.id FROM context_checkpoints c JOIN agent_events e ON e.agent_id=c.agent_id AND e.context_checkpoint_id=c.id
WHERE c.agent_id=$1 AND e.turn_id=$2 AND e.sequence>$3 AND e.sequence>=$4
ORDER BY e.sequence DESC LIMIT 1
`

type ReconstructExecutionCheckpointParams struct {
	AgentID         uuid.UUID
	TurnID          uuid.UUID
	NormalWatermark int64
	StopSequence    int64
}

func (q *reconstructionQueries) ReconstructExecutionCheckpoint(
	ctx context.Context, db executiondb.DBTX, arg ReconstructExecutionCheckpointParams,
) (uuid.UUID, error) {
	row := db.QueryRow(ctx, reconstructExecutionCheckpoint,
		arg.AgentID,
		arg.TurnID,
		arg.NormalWatermark,
		arg.StopSequence,
	)
	var id uuid.UUID
	err := row.Scan(&id)
	return id, err
}

const reconstructExecutionContexts = `
SELECT c.id,c.agent_id,c.turn_id,c.operation_kind,c.attempt_number,c.input_event_sequence,
       c.source_event_sequence_end,c.state,c.recovery_kind,c.retry_at,c.opening_input_ids,c.opening_event_sequence
FROM model_call_contexts c WHERE c.agent_id=$1
ORDER BY c.input_event_sequence,c.attempt_number
`

type ReconstructExecutionContextsParams struct {
	AgentID uuid.UUID
}

type ReconstructExecutionContextsRow struct {
	ID                     uuid.UUID
	AgentID                uuid.UUID
	TurnID                 uuid.UUID
	OperationKind          string
	AttemptNumber          int32
	InputEventSequence     int64
	SourceEventSequenceEnd *int64
	State                  string
	RecoveryKind           *string
	RetryAt                *time.Time
	OpeningInputIds        []uuid.UUID
	OpeningEventSequence   int64
}

func (q *reconstructionQueries) ReconstructExecutionContexts(
	ctx context.Context, db executiondb.DBTX, arg ReconstructExecutionContextsParams,
) ([]ReconstructExecutionContextsRow, error) {
	rows, err := db.Query(ctx, reconstructExecutionContexts, arg.AgentID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[ReconstructExecutionContextsRow])
}

const validateExecutionLineage = `WITH snapshots AS (
    SELECT c.opening_input_ids,c.opening_event_sequence,c.input_event_sequence AS watermark
    FROM model_call_contexts c WHERE c.agent_id=$1
    UNION ALL
    SELECT i.opening_input_ids,i.opening_event_sequence,e.sequence
    FROM agent_inputs i JOIN agent_events e ON e.agent_id=i.agent_id AND e.id=i.admitted_event_id
    WHERE i.agent_id=$1 AND i.input_kind='config_change' AND i.state='resolved'
    UNION ALL
    SELECT c.opening_input_ids,c.opening_event_sequence,e.sequence
    FROM context_checkpoints c JOIN agent_events e ON e.agent_id=c.agent_id AND e.context_checkpoint_id=c.id
    WHERE c.agent_id=$1
)
SELECT coalesce(NOT EXISTS (
    SELECT 1 FROM model_call_contexts c WHERE c.agent_id=$1
      AND c.turn_id IS DISTINCT FROM (SELECT e.turn_id FROM agent_events e
          WHERE e.agent_id=c.agent_id AND e.is_opening_event AND e.sequence<=c.input_event_sequence
          ORDER BY e.sequence DESC LIMIT 1)
) AND NOT EXISTS (
    SELECT 1 FROM snapshots s WHERE EXISTS (
        SELECT 1 FROM (
            SELECT o.ordinality,e.sequence,lag(e.sequence) OVER (ORDER BY o.ordinality) AS previous
            FROM unnest(s.opening_input_ids) WITH ORDINALITY o(id,ordinality)
            LEFT JOIN agent_inputs i ON i.agent_id=$1 AND i.id=o.id AND i.state='resolved' AND i.input_kind='content'
            LEFT JOIN agent_events e ON e.agent_id=i.agent_id AND e.id=i.admitted_event_id AND e.is_opening_event
        ) opening WHERE sequence IS NULL OR sequence>s.watermark
            OR previous>=sequence OR (ordinality=1 AND sequence<>s.opening_event_sequence)
    )
),false)::boolean AS valid
`

type ValidateExecutionLineageParams struct {
	AgentID uuid.UUID
}

func (q *reconstructionQueries) ValidateExecutionLineage(
	ctx context.Context, db executiondb.DBTX, arg ValidateExecutionLineageParams,
) (bool, error) {
	row := db.QueryRow(ctx, validateExecutionLineage, arg.AgentID)
	var valid bool
	err := row.Scan(&valid)
	return valid, err
}

const reconstructExecutionConfig = `
SELECT i.id FROM agent_inputs i JOIN agent_events e ON e.agent_id=i.agent_id AND e.id=i.admitted_event_id
WHERE i.agent_id=$1 AND e.turn_id=$2 AND i.input_kind='config_change' AND i.state='resolved'
  AND e.sequence>$3 AND e.sequence>=$4
  AND e.sequence>=$5 AND $5::bigint>0
ORDER BY e.sequence DESC LIMIT 1
`

type ReconstructExecutionConfigParams struct {
	AgentID              uuid.UUID
	TurnID               uuid.UUID
	NormalWatermark      int64
	StopSequence         int64
	FirstContentSequence int64
}

func (q *reconstructionQueries) ReconstructExecutionConfig(
	ctx context.Context, db executiondb.DBTX, arg ReconstructExecutionConfigParams,
) (uuid.UUID, error) {
	row := db.QueryRow(ctx, reconstructExecutionConfig,
		arg.AgentID,
		arg.TurnID,
		arg.NormalWatermark,
		arg.StopSequence,
		arg.FirstContentSequence,
	)
	var id uuid.UUID
	err := row.Scan(&id)
	return id, err
}

const loadExecutionBatch = `
SELECT EXISTS(SELECT 1 FROM tool_calls c WHERE c.agent_id=$1 AND c.model_output_id=$2) AS has_tools,
       EXISTS(SELECT 1 FROM tool_calls c WHERE c.agent_id=$1 AND c.model_output_id=$2 AND c.state<>'completed') AS
 incomplete,
       EXISTS(SELECT 1 FROM tool_calls c WHERE c.agent_id=$1 AND c.model_output_id=$2 AND
              (c.state='awaiting_authorization' OR (c.state='ready' AND c.type IN ('built_in','mcp')))) AS runnable
`

type LoadExecutionBatchParams struct {
	AgentID       uuid.UUID
	ModelOutputID uuid.UUID
}

type LoadExecutionBatchRow struct {
	HasTools   bool
	Incomplete bool
	Runnable   bool
}

func (q *reconstructionQueries) LoadExecutionBatch(
	ctx context.Context, db executiondb.DBTX, arg LoadExecutionBatchParams,
) (LoadExecutionBatchRow, error) {
	row := db.QueryRow(ctx, loadExecutionBatch, arg.AgentID, arg.ModelOutputID)
	var i LoadExecutionBatchRow
	err := row.Scan(&i.HasTools, &i.Incomplete, &i.Runnable)
	return i, err
}

const loadExecutionBatchCompletion = `
SELECT count(c.id)::bigint AS calls,count(r.id)::bigint AS results,count(e.id)::bigint AS events,
       coalesce(max(e.sequence),0)::bigint AS last_result_sequence,
       coalesce(max(r.completed_at),'epoch'::timestamptz)::timestamptz AS ready_at
FROM tool_calls c LEFT JOIN tool_call_results r ON r.agent_id=c.agent_id AND r.tool_call_id=c.id
LEFT JOIN agent_events e ON e.agent_id=r.agent_id AND e.tool_call_result_id=r.id
WHERE c.agent_id=$1 AND c.model_output_id=$2
`

type LoadExecutionBatchCompletionParams struct {
	AgentID       uuid.UUID
	ModelOutputID uuid.UUID
}

type LoadExecutionBatchCompletionRow struct {
	Calls              int64
	Results            int64
	Events             int64
	LastResultSequence int64
	ReadyAt            time.Time
}

func (q *reconstructionQueries) LoadExecutionBatchCompletion(
	ctx context.Context, db executiondb.DBTX, arg LoadExecutionBatchCompletionParams,
) (LoadExecutionBatchCompletionRow, error) {
	row := db.QueryRow(ctx, loadExecutionBatchCompletion, arg.AgentID, arg.ModelOutputID)
	var i LoadExecutionBatchCompletionRow
	err := row.Scan(
		&i.Calls,
		&i.Results,
		&i.Events,
		&i.LastResultSequence,
		&i.ReadyAt,
	)
	return i, err
}
