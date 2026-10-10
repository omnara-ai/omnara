package agentexecution

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
)

func nullableID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func loadExecution(
	ctx context.Context,
	db executiondb.DBTX,
	route AgentRoute,
	override *ExecutionHead,
) (ExecutionSnapshot, error) {
	q := executiondb.New()
	if override != nil {
		return loadExecutionFacts(ctx, db, route, *override)
	}
	base, err := q.LoadExecutionBase(ctx, db, executiondb.LoadExecutionBaseParams{
		ProjectID: route.ProjectID, AgentID: route.AgentID,
	})
	if err != nil {
		return ExecutionSnapshot{}, fmt.Errorf("load execution base: %w", err)
	}
	if !base.HeadExists {
		return ExecutionSnapshot{}, fmt.Errorf("load execution head: %w", pgx.ErrNoRows)
	}
	return loadExecutionFacts(ctx, db, route, executionHead(base))
}

func executionFactsParams(route AgentRoute, head ExecutionHead) executiondb.LoadExecutionFactsParams {
	return executiondb.LoadExecutionFactsParams{AgentID: route.AgentID, ProjectID: route.ProjectID,
		TurnID: nullableID(
			head.CurrentTurnID,
		), StopSequence: head.StopSequence, MaxContextInputSequence: head.MaxContextInputSequence,
		NormalContextID: nullableID(
			head.NormalContextID,
		), CompactionContextID: nullableID(head.CompactionContextID),
		ToolOutputID: nullableID(
			head.PendingToolOutputID,
		), LimitOutputID: nullableID(head.PendingOutputLimitID),
		ConfigID: nullableID(head.PendingConfigInputID), CheckpointID: nullableID(head.PendingCheckpointID)}
}

func loadExecutionFacts(
	ctx context.Context,
	db executiondb.DBTX,
	route AgentRoute,
	head ExecutionHead,
) (ExecutionSnapshot, error) {
	batch := executiondb.New().
		LoadExecutionFacts(ctx, db, []executiondb.LoadExecutionFactsParams{executionFactsParams(route, head)})
	var snapshot ExecutionSnapshot
	var err error
	batch.Query(func(_ int, rows []executiondb.LoadExecutionFactsRow, queryErr error) {
		if queryErr != nil {
			err = queryErr
			return
		}
		snapshot, err = executionSnapshot(route, head, rows)
	})
	return snapshot, errors.Join(err, batch.Close())
}

func executionSnapshot(
	route AgentRoute,
	head ExecutionHead,
	rows []executiondb.LoadExecutionFactsRow,
) (ExecutionSnapshot, error) {
	view := ExecutionView{AgentID: route.AgentID, StopSequence: head.StopSequence,
		MaxNormalInputSequence: head.MaxNormalInputSequence, MaxContextInputSequence: head.MaxContextInputSequence}
	var now time.Time
	for _, row := range rows {
		if row.Kind != "scope" {
			continue
		}
		if row.ID != route.AgentID || row.RootAgentID != route.RootAgentID ||
			row.TurnID != head.CurrentTurnID {
			return ExecutionSnapshot{}, fmt.Errorf("%w: execution scope", ErrInvalidState)
		}
		view.State = AgentState(row.State)
		now = row.DatabaseNow
		view.Inputs = InputAvailability{Steering: row.Steering, Queued: row.Queued}
		if row.TurnID != uuid.Nil {
			view.Turn = &Turn{ID: row.TurnID, FirstOpeningSequence: row.FirstOpeningSequence,
				LastOpeningSequence: row.LastOpeningSequence, FirstContentSequence: row.FirstContentSequence,
				LatestSemantic: EventBoundary{Sequence: row.Sequence, Time: row.EventTime}}
		}
	}
	if now.IsZero() {
		return ExecutionSnapshot{}, fmt.Errorf("%w: missing agent", ErrInvalidState)
	}
	contexts := make(map[uuid.UUID]*ModelContext)
	var batch *ToolBatch
	for _, row := range rows {
		switch row.Kind {
		case "context":
			contexts[row.ID] = &ModelContext{ID: row.ID, AgentID: route.AgentID, TurnID: row.TurnID,
				Operation: Operation(row.Operation), Attempt: row.Attempt, InputEventSequence: row.Watermark,
				SourceEventSequenceEnd: row.SourceEnd, State: ContextState(row.State), Recovery: RecoveryKind(row.Recovery),
				RetryAt: row.RetryAt, Opening: Opening{InputIDs: row.OpeningIds, EventSequence: row.OpeningSequence}}
		case "batch":
			if !row.HasTools {
				return ExecutionSnapshot{}, fmt.Errorf("%w: pending output has no tools", ErrInvalidState)
			}
			batch = &ToolBatch{HasIncomplete: row.Incomplete, HasRunnable: row.Runnable}
			if !row.Incomplete {
				if row.Calls == 0 || row.Calls != row.Results || row.Calls != row.Events {
					return ExecutionSnapshot{}, fmt.Errorf("%w: incomplete result authority", ErrInvalidState)
				}
				batch.Completion = &BatchCompletion{LastResultSequence: row.Sequence, ReadyAt: row.EventTime}
			}
		case "config", "checkpoint":
			work := &BoundaryWork{ID: row.ID, AgentID: route.AgentID, TurnID: row.TurnID,
				Event:   EventBoundary{Sequence: row.Sequence, Time: row.EventTime},
				Opening: Opening{InputIDs: row.OpeningIds, EventSequence: row.OpeningSequence}}
			if row.Kind == "config" {
				view.Config = work
			} else {
				view.Checkpoint = work
			}
		case "opening":
			if view.Turn == nil || head.MaxContextInputSequence >= view.Turn.FirstOpeningSequence ||
				head.StopSequence > view.Turn.FirstOpeningSequence {
				return ExecutionSnapshot{}, ErrInvalidState
			}
			if len(view.Turn.InitialOpening.InputIDs) == 0 {
				view.Turn.InitialOpening.EventSequence = row.Sequence
				view.Turn.InitialReadyAt = row.EventTime
			}
			view.Turn.InitialOpening.InputIDs = append(view.Turn.InitialOpening.InputIDs, row.ID)
			if row.EventTime.Before(view.Turn.InitialReadyAt) {
				view.Turn.InitialReadyAt = row.EventTime
			}
		}
	}
	view.NormalContext = contexts[head.NormalContextID]
	view.CompactionContext = contexts[head.CompactionContextID]
	for _, row := range rows {
		if row.Kind != "output" {
			continue
		}
		source := contexts[row.ContextID]
		if source == nil {
			return ExecutionSnapshot{}, fmt.Errorf("%w: missing output context", ErrInvalidState)
		}
		output := ModelOutput{
			ID:      row.ID,
			EventID: row.EventID,
			Event:   EventBoundary{Sequence: row.Sequence, Time: row.EventTime},
			Context: *source,
		}
		if row.ID == head.PendingToolOutputID && batch != nil {
			batch.Output = output
			view.ToolBatch = batch
		} else if row.ID == head.PendingOutputLimitID && row.StopReason == "max_tokens" {
			view.OutputLimit = &output
		} else {
			return ExecutionSnapshot{}, fmt.Errorf("%w: pending output", ErrInvalidState)
		}
	}
	if head.NormalContextID != uuid.Nil && view.NormalContext == nil ||
		head.CompactionContextID != uuid.Nil && view.CompactionContext == nil ||
		head.PendingToolOutputID != uuid.Nil && view.ToolBatch == nil ||
		head.PendingOutputLimitID != uuid.Nil && view.OutputLimit == nil ||
		head.PendingConfigInputID != uuid.Nil && view.Config == nil ||
		head.PendingCheckpointID != uuid.Nil && view.Checkpoint == nil {
		return ExecutionSnapshot{}, fmt.Errorf("%w: missing execution pointer", ErrInvalidState)
	}
	selected, err := SelectNext(view, now)
	if err != nil {
		return ExecutionSnapshot{}, err
	}
	head.TurnContinuable = selected.TurnContinuable
	head.IncompleteTools = selected.IncompleteTools
	head.LogicalReadyAt = selected.LogicalReadyAt
	return ExecutionSnapshot{Head: head, View: view, Selection: selected}, nil
}
