package agentexecution

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
)

type ExecutionHead struct {
	AgentID                 uuid.UUID
	CurrentTurnID           uuid.UUID
	StopSequence            int64
	AnsweredThroughSequence int64
	MaxNormalInputSequence  int64
	MaxContextInputSequence int64
	NormalContextID         uuid.UUID
	CompactionContextID     uuid.UUID
	PendingToolOutputID     uuid.UUID
	PendingOutputLimitID    uuid.UUID
	PendingConfigInputID    uuid.UUID
	PendingCheckpointID     uuid.UUID
	TurnContinuable         bool
	IncompleteTools         bool
	LogicalReadyAt          *time.Time
}

type ExecutionSnapshot struct {
	Head        ExecutionHead
	View        ExecutionView
	Selection   Selection
	databaseNow time.Time
}

type executionLoader struct {
	q     *executiondb.Queries
	db    executiondb.DBTX
	route AgentRoute
}

func (h *Handle) executionLoader(ctx context.Context) (*executionLoader, error) {
	route, err := h.Route()
	if err != nil {
		return nil, err
	}
	l := &executionLoader{q: executiondb.New(), db: h.unit.DB(), route: route}
	scope, err := l.q.LoadExecutionScope(
		ctx,
		l.db,
		executiondb.LoadExecutionScopeParams{ProjectID: route.ProjectID, ID: route.AgentID},
	)
	if err != nil {
		return nil, fmt.Errorf("load execution scope: %w", err)
	}
	if scope != route.RootAgentID {
		return nil, fmt.Errorf("%w: agent %s root", ErrInvalidState, route.AgentID)
	}
	return l, nil
}

func (h *Handle) LoadExecution(ctx context.Context) (ExecutionSnapshot, error) {
	route, err := h.Route()
	if err != nil {
		return ExecutionSnapshot{}, err
	}
	var head *ExecutionHead
	m := h.mutation
	if m != nil {
		head = &m.head
		if m.loaded != nil {
			if m.loaded.Selection.Wait == WaitModelDeadline {
				now, err := executiondb.New().ExecutionDatabaseTime(ctx, h.unit.DB())
				if err != nil {
					return ExecutionSnapshot{}, err
				}
				m.loaded.databaseNow = now
				if err := m.selectLoaded(); err != nil {
					return ExecutionSnapshot{}, err
				}
			}
			return cloneExecutionSnapshot(*m.loaded), nil
		}
	}
	snapshot, err := loadExecution(ctx, h.unit.DB(), route, head)
	if err == nil {
		if m == nil {
			m = &executionMutation{}
			h.mutation = m
		}
		m.head = snapshot.Head
		m.loaded = &snapshot
		m.selectionChanged = false
	}
	return cloneExecutionSnapshot(snapshot), err
}

func executionHead(row executiondb.LoadExecutionBaseRow) ExecutionHead {
	return ExecutionHead{
		AgentID: row.AgentID, CurrentTurnID: valueOrZero(row.CurrentTurnID), StopSequence: row.StopSequence,
		AnsweredThroughSequence: row.AnsweredThroughSequence, MaxNormalInputSequence: row.MaxNormalInputSequence,
		MaxContextInputSequence: row.MaxContextInputSequence, NormalContextID: valueOrZero(row.NormalContextID),
		CompactionContextID: valueOrZero(
			row.CompactionContextID,
		), PendingToolOutputID: valueOrZero(row.PendingToolOutputID),
		PendingOutputLimitID: valueOrZero(
			row.PendingOutputLimitID,
		), PendingConfigInputID: valueOrZero(row.PendingConfigInputID),
		PendingCheckpointID: valueOrZero(row.PendingCheckpointID), TurnContinuable: row.TurnContinuable,
		IncompleteTools: row.IncompleteTools, LogicalReadyAt: row.LogicalReadyAt,
	}
}

func valueOrZero[T any](value *T) (zero T) {
	if value != nil {
		return *value
	}
	return zero
}

func (l *executionLoader) load(ctx context.Context, head ExecutionHead) (ExecutionSnapshot, error) {
	return loadExecution(ctx, l.db, l.route, &head)
}

func contextRecord(row executiondb.ReconstructExecutionContextsRow) ModelContext {
	return ModelContext{
		ID:                     row.ID,
		AgentID:                row.AgentID,
		TurnID:                 row.TurnID,
		Operation:              Operation(row.OperationKind),
		Attempt:                row.AttemptNumber,
		InputEventSequence:     row.InputEventSequence,
		SourceEventSequenceEnd: valueOrZero(row.SourceEventSequenceEnd),
		State:                  ContextState(row.State),
		Recovery:               RecoveryKind(valueOrZero(row.RecoveryKind)),
		RetryAt:                row.RetryAt,
		Opening: Opening{
			InputIDs:      row.OpeningInputIds,
			EventSequence: row.OpeningEventSequence,
		},
	}
}

func (l *executionLoader) turn(ctx context.Context, head ExecutionHead) (*Turn, error) {
	row, err := l.q.LoadExecutionTurn(
		ctx,
		l.db,
		executiondb.LoadExecutionTurnParams{AgentID: head.AgentID, ID: head.CurrentTurnID},
	)
	if err != nil {
		return nil, err
	}
	turn := &Turn{
		ID:                   row.ID,
		FirstOpeningSequence: row.FirstOpeningSequence,
		LastOpeningSequence:  row.LastOpeningSequence,
		FirstContentSequence: row.FirstContentSequence,
		LatestSemantic:       EventBoundary{Sequence: row.SemanticSequence, Time: row.SemanticTime},
	}
	if head.MaxContextInputSequence >= turn.FirstOpeningSequence ||
		head.StopSequence > turn.FirstOpeningSequence {
		return turn, nil
	}
	opening, err := l.q.CaptureExecutionOpening(ctx, l.db, executiondb.CaptureExecutionOpeningParams{
		ProjectID: l.route.ProjectID, AgentID: head.AgentID, TurnID: turn.ID,
		Watermark: turn.LastOpeningSequence, StopSequence: head.StopSequence})
	if err != nil {
		return nil, err
	}
	for i, input := range opening {
		turn.InitialOpening.InputIDs = append(turn.InitialOpening.InputIDs, input.ID)
		if i == 0 {
			turn.InitialOpening.EventSequence = input.Sequence
			turn.InitialReadyAt = input.CreatedAt
		}
		if input.CreatedAt.Before(turn.InitialReadyAt) {
			turn.InitialReadyAt = input.CreatedAt
		}
	}
	return turn, nil
}

func (l *executionLoader) batch(ctx context.Context, output ModelOutput) (*ToolBatch, error) {
	row, err := l.q.LoadExecutionBatch(
		ctx,
		l.db,
		executiondb.LoadExecutionBatchParams{AgentID: l.route.AgentID, ModelOutputID: output.ID},
	)
	if err != nil || !row.HasTools {
		return nil, err
	}
	batch := &ToolBatch{Output: output, HasIncomplete: row.Incomplete, HasRunnable: row.Runnable}
	if row.Incomplete {
		return batch, nil
	}
	completion, err := l.q.LoadExecutionBatchCompletion(
		ctx,
		l.db,
		executiondb.LoadExecutionBatchCompletionParams{
			AgentID: l.route.AgentID, ModelOutputID: output.ID},
	)
	if err != nil {
		return nil, err
	}
	if completion.Calls == 0 || completion.Calls != completion.Results ||
		completion.Calls != completion.Events {
		return nil, fmt.Errorf("%w: output %s incomplete result authority", ErrInvalidState, output.ID)
	}
	batch.Completion = &BatchCompletion{
		LastResultSequence: completion.LastResultSequence,
		ReadyAt:            completion.ReadyAt,
	}
	return batch, nil
}
