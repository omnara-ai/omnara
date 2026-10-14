//go:build integration

package agentexecution

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

func (h *Handle) ReconstructExecution(ctx context.Context) (ExecutionSnapshot, error) {
	l, err := h.executionLoader(ctx)
	if err != nil {
		return ExecutionSnapshot{}, err
	}
	valid, err := l.q.ValidateExecutionLineage(
		ctx,
		l.db,
		ValidateExecutionLineageParams{AgentID: l.route.AgentID},
	)
	if err != nil {
		return ExecutionSnapshot{}, err
	}
	if !valid {
		return ExecutionSnapshot{}, fmt.Errorf("%w: agent %s immutable lineage", ErrInvalidState, l.route.AgentID)
	}
	boundaries, err := l.q.LoadExecutionBoundaries(ctx, l.db, LoadExecutionBoundariesParams{
		ProjectID: l.route.ProjectID, AgentID: l.route.AgentID})
	if err != nil {
		return ExecutionSnapshot{}, err
	}
	head := ExecutionHead{
		AgentID:       l.route.AgentID,
		CurrentTurnID: boundaries.TurnID,
		StopSequence:  boundaries.StopSequence,
	}
	contexts, err := l.q.ReconstructExecutionContexts(
		ctx,
		l.db,
		ReconstructExecutionContextsParams{AgentID: head.AgentID},
	)
	if err != nil {
		return ExecutionSnapshot{}, err
	}
	records := make(map[uuid.UUID]ModelContext, len(contexts))
	var normal, compaction *ModelContext
	for _, row := range contexts {
		record := contextRecord(row)
		records[record.ID] = record
		head.MaxContextInputSequence = max(head.MaxContextInputSequence, record.InputEventSequence)
		if record.Operation != OperationNormal {
			continue
		}
		head.MaxNormalInputSequence = max(head.MaxNormalInputSequence, record.InputEventSequence)
		if record.TurnID == head.CurrentTurnID &&
			(normal == nil || record.InputEventSequence > normal.InputEventSequence ||
				record.InputEventSequence == normal.InputEventSequence && record.Attempt > normal.Attempt) {
			normal = &record
		}
	}
	if err := validateGoverningContexts(head, records); err != nil {
		return ExecutionSnapshot{}, err
	}
	if normal != nil {
		head.NormalContextID = normal.ID
		for _, record := range records {
			if record.TurnID != head.CurrentTurnID || record.Operation != OperationCompaction ||
				record.InputEventSequence != normal.InputEventSequence {
				continue
			}
			if compaction == nil || record.SourceEventSequenceEnd < compaction.SourceEventSequenceEnd ||
				record.SourceEventSequenceEnd == compaction.SourceEventSequenceEnd &&
					record.Attempt > compaction.Attempt {
				record := record
				compaction = &record
			}
		}
	}
	if compaction != nil {
		head.CompactionContextID = compaction.ID
	}
	if head.CurrentTurnID == uuid.Nil {
		return l.load(ctx, head)
	}
	turn, err := l.turn(ctx, head)
	if err != nil {
		return ExecutionSnapshot{}, err
	}
	head.PendingConfigInputID, err = l.q.ReconstructExecutionConfig(
		ctx,
		l.db,
		ReconstructExecutionConfigParams{
			AgentID: head.AgentID, TurnID: head.CurrentTurnID, NormalWatermark: head.MaxNormalInputSequence,
			StopSequence: head.StopSequence, FirstContentSequence: turn.FirstContentSequence},
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ExecutionSnapshot{}, err
	}
	head.PendingCheckpointID, err = l.q.ReconstructExecutionCheckpoint(
		ctx,
		l.db,
		ReconstructExecutionCheckpointParams{
			AgentID: head.AgentID, TurnID: head.CurrentTurnID,
			NormalWatermark: head.MaxNormalInputSequence, StopSequence: head.StopSequence},
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ExecutionSnapshot{}, err
	}
	outputs, err := l.q.ReconstructExecutionOutputs(ctx, l.db, ReconstructExecutionOutputsParams{
		AgentID: head.AgentID, TurnID: head.CurrentTurnID, StopSequence: head.StopSequence})
	if err != nil {
		return ExecutionSnapshot{}, err
	}
	var acceptedWatermark int64
	for _, row := range outputs {
		source, ok := records[row.ModelCallContextID]
		if !ok || source.TurnID != head.CurrentTurnID || row.Sequence <= source.InputEventSequence {
			return ExecutionSnapshot{}, fmt.Errorf("%w: output %s boundary", ErrInvalidState, row.ID)
		}
		acceptedWatermark = max(acceptedWatermark, source.InputEventSequence)
	}
	for _, row := range outputs {
		source, ok := records[row.ModelCallContextID]
		if !ok {
			return ExecutionSnapshot{}, fmt.Errorf("%w: output %s missing context", ErrInvalidState, row.ID)
		}
		output := ModelOutput{
			ID:      row.ID,
			EventID: row.EventID,
			Event:   EventBoundary{Sequence: row.Sequence, Time: row.CreatedAt},
			Context: source,
		}
		batch, loadErr := l.batch(ctx, output)
		if loadErr != nil {
			return ExecutionSnapshot{}, loadErr
		}
		if batch != nil {
			if batch.Completion != nil && acceptedWatermark >= batch.Completion.LastResultSequence {
				continue
			}
			if head.PendingToolOutputID != uuid.Nil {
				return ExecutionSnapshot{}, fmt.Errorf(
					"%w: multiple tool outputs %s and %s",
					ErrInvalidState,
					head.PendingToolOutputID,
					row.ID,
				)
			}
			head.PendingToolOutputID = row.ID
		} else if row.StopReason == "max_tokens" && source.Operation == OperationNormal && source.State == ContextSucceeded &&
			row.Sequence > head.MaxNormalInputSequence {
			if head.PendingOutputLimitID != uuid.Nil {
				return ExecutionSnapshot{}, fmt.Errorf("%w: multiple output limits %s and %s",
					ErrInvalidState, head.PendingOutputLimitID, row.ID)
			}
			head.PendingOutputLimitID = row.ID
		}
	}
	return l.load(ctx, head)
}

func validateGoverningContexts(head ExecutionHead, records map[uuid.UUID]ModelContext) error {
	type identity struct {
		operation Operation
		watermark int64
		end       int64
	}
	attempts := make(map[identity]int32)
	ends := make(map[int64]int64)
	var normalWatermark int64
	for _, record := range records {
		if record.TurnID != head.CurrentTurnID {
			continue
		}
		key := identity{record.Operation, record.InputEventSequence, record.SourceEventSequenceEnd}
		attempts[key] = max(attempts[key], record.Attempt)
		if record.Operation == OperationNormal {
			normalWatermark = max(normalWatermark, record.InputEventSequence)
		} else {
			end, ok := ends[record.InputEventSequence]
			if !ok || record.SourceEventSequenceEnd < end {
				ends[record.InputEventSequence] = record.SourceEventSequenceEnd
			}
		}
	}
	var governing uuid.UUID
	for _, record := range records {
		if record.TurnID != head.CurrentTurnID || record.InputEventSequence < normalWatermark ||
			record.InputEventSequence < head.StopSequence {
			continue
		}
		key := identity{record.Operation, record.InputEventSequence, record.SourceEventSequenceEnd}
		if record.Attempt != attempts[key] {
			continue
		}
		end, dependency := ends[record.InputEventSequence]
		if record.Operation == OperationNormal && dependency ||
			record.Operation == OperationCompaction && record.SourceEventSequenceEnd != end {
			continue
		}
		if record.State != ContextStarted && (record.State != ContextFailed || record.Recovery == RecoveryNone) {
			continue
		}
		if governing != uuid.Nil {
			return fmt.Errorf("%w: governing contexts %s and %s", ErrInvalidState, governing, record.ID)
		}
		governing = record.ID
		if record.Operation == OperationCompaction && record.InputEventSequence != normalWatermark {
			return fmt.Errorf("%w: compaction %s without normal parent", ErrInvalidState, record.ID)
		}
	}
	return nil
}

func (h *Handle) Repair(ctx context.Context) (ExecutionSnapshot, error) {
	snapshot, err := h.ReconstructExecution(ctx)
	if err != nil {
		h.unit.aborted = true
		return ExecutionSnapshot{}, err
	}
	h.mutation = &executionMutation{head: snapshot.Head, loaded: &snapshot, dirty: true, insertHead: true}
	return snapshot, nil
}

type executionLoader struct {
	q     *reconstructionQueries
	db    executiondb.DBTX
	route AgentRoute
}

func (h *Handle) executionLoader(ctx context.Context) (*executionLoader, error) {
	route, err := h.Route()
	if err != nil {
		return nil, err
	}
	l := &executionLoader{q: &reconstructionQueries{Queries: executiondb.New()}, db: h.unit.DB(), route: route}
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

func (l *executionLoader) load(ctx context.Context, head ExecutionHead) (ExecutionSnapshot, error) {
	return loadExecution(ctx, l.db, l.route, &head)
}

func contextRecord(row ReconstructExecutionContextsRow) ModelContext {
	return ModelContext{
		ID:                     row.ID,
		AgentID:                row.AgentID,
		TurnID:                 row.TurnID,
		Operation:              Operation(row.OperationKind),
		Attempt:                row.AttemptNumber,
		InputEventSequence:     row.InputEventSequence,
		SourceEventSequenceEnd: storeutil.ValueOrZero(row.SourceEventSequenceEnd),
		State:                  ContextState(row.State),
		Recovery:               RecoveryKind(storeutil.ValueOrZero(row.RecoveryKind)),
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
		LoadExecutionTurnParams{AgentID: head.AgentID, ID: head.CurrentTurnID},
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
		LoadExecutionBatchParams{AgentID: l.route.AgentID, ModelOutputID: output.ID},
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
		LoadExecutionBatchCompletionParams{
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
