package agentexecution

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
)

func (h *Handle) ReconstructExecution(ctx context.Context) (ExecutionSnapshot, error) {
	l, err := h.executionLoader(ctx)
	if err != nil {
		return ExecutionSnapshot{}, err
	}
	valid, err := l.q.ValidateExecutionLineage(
		ctx,
		l.db,
		executiondb.ValidateExecutionLineageParams{AgentID: l.route.AgentID},
	)
	if err != nil {
		return ExecutionSnapshot{}, err
	}
	if !valid {
		return ExecutionSnapshot{}, fmt.Errorf("%w: agent %s immutable lineage", ErrInvalidState, l.route.AgentID)
	}
	boundaries, err := l.q.LoadExecutionBoundaries(ctx, l.db, executiondb.LoadExecutionBoundariesParams{
		ProjectID: l.route.ProjectID, AgentID: l.route.AgentID})
	if err != nil {
		return ExecutionSnapshot{}, err
	}
	head := ExecutionHead{
		AgentID:                 l.route.AgentID,
		CurrentTurnID:           boundaries.TurnID,
		StopSequence:            boundaries.StopSequence,
		AnsweredThroughSequence: max(boundaries.StopSequence, boundaries.OutputSequence),
	}
	contexts, err := l.q.ReconstructExecutionContexts(
		ctx,
		l.db,
		executiondb.ReconstructExecutionContextsParams{AgentID: head.AgentID},
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
		executiondb.ReconstructExecutionConfigParams{
			AgentID: head.AgentID, TurnID: head.CurrentTurnID, NormalWatermark: head.MaxNormalInputSequence,
			StopSequence: head.StopSequence, FirstContentSequence: turn.FirstContentSequence},
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ExecutionSnapshot{}, err
	}
	head.PendingCheckpointID, err = l.q.ReconstructExecutionCheckpoint(
		ctx,
		l.db,
		executiondb.ReconstructExecutionCheckpointParams{
			AgentID: head.AgentID, TurnID: head.CurrentTurnID,
			NormalWatermark: head.MaxNormalInputSequence, StopSequence: head.StopSequence},
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ExecutionSnapshot{}, err
	}
	outputs, err := l.q.ReconstructExecutionOutputs(ctx, l.db, executiondb.ReconstructExecutionOutputsParams{
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
