package agentexecution

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

func loadExecution(
	ctx context.Context,
	db executiondb.DBTX,
	route AgentRoute,
	override *ExecutionHead,
	known ...executiondb.LoadExecutionContextsRow,
) (ExecutionSnapshot, error) {
	q := executiondb.New()
	if override != nil {
		return loadExecutionFacts(ctx, db, route, *override, known...)
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

func loadExecutionFacts(ctx context.Context,
	db executiondb.DBTX,
	route AgentRoute,
	head ExecutionHead, known ...executiondb.LoadExecutionContextsRow) (ExecutionSnapshot,
	error) {
	batch := factsBatch{DBTX: db}
	facts := batch.queue(ctx, route, head, known...)
	if err := batch.read(ctx); err != nil {
		return ExecutionSnapshot{}, err
	}
	return executionSnapshot(route, head, *facts)
}

func executionSnapshot(route AgentRoute, head ExecutionHead, facts executionFacts) (ExecutionSnapshot, error) {
	scope := facts.scope
	if scope.AgentID != route.AgentID ||
		scope.RootAgentID != route.RootAgentID ||
		storeutil.ValueOrZero(scope.TurnID) != head.CurrentTurnID {
		return ExecutionSnapshot{}, fmt.Errorf("%w: execution scope", ErrInvalidState)
	}
	now := scope.DatabaseNow
	view := ExecutionView{AgentID: route.AgentID, State: AgentState(scope.State), StopSequence: head.StopSequence,
		MaxNormalInputSequence: head.MaxNormalInputSequence, MaxContextInputSequence: head.MaxContextInputSequence,
		Inputs: InputAvailability{Steering: scope.Steering, Queued: scope.Queued}}
	if scope.TurnID != nil {
		view.Turn = &Turn{ID: *scope.TurnID,
			FirstOpeningSequence: scope.FirstOpeningSequence,
			LastOpeningSequence:  scope.LastOpeningSequence,
			FirstContentSequence: scope.FirstContentSequence,
			LatestSemantic: EventBoundary{Sequence: scope.SemanticSequence,
				Time: scope.SemanticTime}}
		if len(scope.OpeningIds) > 0 {
			view.Turn.InitialOpening = Opening{InputIDs: scope.OpeningIds, EventSequence: scope.OpeningSequence}
			view.Turn.InitialReadyAt = scope.OpeningReadyAt
		}
	}
	contexts := make(map[uuid.UUID]*ModelContext)
	for _, row := range facts.contexts {
		contexts[row.ID] = &ModelContext{ID: row.ID,
			AgentID:                row.AgentID,
			TurnID:                 row.TurnID,
			Operation:              Operation(row.OperationKind),
			Attempt:                row.AttemptNumber,
			InputEventSequence:     row.InputEventSequence,
			SourceEventSequenceEnd: storeutil.ValueOrZero(row.SourceEventSequenceEnd),
			State:                  ContextState(row.State),
			Recovery:               RecoveryKind(storeutil.ValueOrZero(row.RecoveryKind)),
			RetryAt:                row.RetryAt,
			Opening:                Opening{InputIDs: row.OpeningInputIds, EventSequence: row.OpeningEventSequence}}
	}
	view.NormalContext = contexts[head.NormalContextID]
	view.CompactionContext = contexts[head.CompactionContextID]
	for _, row := range facts.configs {
		view.Config = &BoundaryWork{ID: row.ID, AgentID: route.AgentID, TurnID: row.TurnID,
			Event: EventBoundary{Sequence: row.Sequence,
				Time: row.CreatedAt},
			Opening: Opening{InputIDs: row.OpeningInputIds,
				EventSequence: row.OpeningEventSequence}}
	}
	for _, row := range facts.checkpoints {
		view.Checkpoint = &BoundaryWork{ID: row.ID, AgentID: route.AgentID, TurnID: row.TurnID,
			Event: EventBoundary{Sequence: row.Sequence,
				Time: row.CreatedAt},
			Opening: Opening{InputIDs: row.OpeningInputIds,
				EventSequence: row.OpeningEventSequence}}
	}
	for _, row := range facts.contexts {
		if row.OutputID == nil {
			continue
		}
		source := contexts[row.ID]
		if source == nil {
			return ExecutionSnapshot{}, fmt.Errorf("%w: missing output context", ErrInvalidState)
		}
		output := ModelOutput{ID: *row.OutputID,
			EventID: storeutil.ValueOrZero(row.OutputEventID),
			Context: *source,
			Event: EventBoundary{Sequence: storeutil.ValueOrZero(row.OutputSequence),
				Time: storeutil.ValueOrZero(row.OutputCreatedAt)}}
		if *row.OutputID == head.PendingToolOutputID {
			b := facts.scope
			if !b.BatchHasTools {
				return ExecutionSnapshot{}, fmt.Errorf("%w: pending output has no tools", ErrInvalidState)
			}
			view.ToolBatch = &ToolBatch{Output: output, HasIncomplete: b.BatchIncomplete, HasRunnable: b.BatchRunnable}
			if !b.BatchIncomplete {
				if b.BatchCalls == 0 || b.BatchCalls != b.BatchResults || b.BatchCalls != b.BatchEvents {
					return ExecutionSnapshot{}, fmt.Errorf("%w: incomplete result authority", ErrInvalidState)
				}
				view.ToolBatch.Completion = &BatchCompletion{LastResultSequence: b.BatchLastSequence, ReadyAt: b.BatchReadyAt}
			}
		} else if *row.OutputID == head.PendingOutputLimitID && storeutil.ValueOrZero(row.OutputStopReason) == "max_tokens" {
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
	head.IncompleteTools = selected.IncompleteTools
	head.LogicalReadyAt = selected.LogicalReadyAt
	return ExecutionSnapshot{Head: head, View: view, Selection: selected, databaseNow: now, contexts: facts.contexts}, nil
}
