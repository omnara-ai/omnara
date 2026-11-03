package agentexecution

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
)

var ErrInvalidState = errors.New("invalid agent execution state")

func SelectNext(view ExecutionView, now time.Time) (Selection, error) {
	if err := validateView(view); err != nil {
		return Selection{}, err
	}
	if view.State == AgentArchived {
		return Selection{Wait: WaitArchived}, nil
	}
	selection := Selection{Wait: WaitIdle}
	governing := continuableContext(view)
	selection.TurnContinuable = governing != nil
	if batch := view.ToolBatch; batch != nil && view.StopSequence <= batch.Output.Event.Sequence {
		selection.IncompleteTools = batch.HasIncomplete
		if batch.HasIncomplete {
			selection.TurnContinuable = true
			selection.Wait = WaitToolBatch
			if batch.HasRunnable {
				selection.Work = WorkTool
				selection.Wait = WaitNone
				selection.LogicalReadyAt = &now
				selection.Tool = &ToolDecision{
					TurnID: batch.Output.Context.TurnID, SourceContextID: batch.Output.Context.ID,
					OutputID: batch.Output.ID, SourceEventID: batch.Output.EventID,
				}
			}
			return selection, nil
		}
	}
	selection.Model = selectModel(view, governing)
	selection.TurnContinuable = selection.TurnContinuable || selection.Model != nil
	switch {
	case view.Inputs.Steering:
		selection.Work, selection.Admission = WorkInput, AdmitAllSteering
		selection.LogicalReadyAt = &now
	case selection.Model != nil:
		selection.LogicalReadyAt = &selection.Model.ReadyAt
		if selection.Model.ReadyAt.After(now) {
			selection.Wait = WaitModelDeadline
			return selection, nil
		}
		selection.Work = WorkModel
	case view.Inputs.Queued:
		selection.Work, selection.Admission = WorkInput, AdmitOneQueued
		selection.LogicalReadyAt = &now
	default:
		return selection, nil
	}
	selection.Wait = WaitNone
	return selection, nil
}

func continuableContext(view ExecutionView) *ModelContext {
	governing := view.NormalContext
	if view.CompactionContext != nil {
		governing = view.CompactionContext
	}
	if governing == nil || view.StopSequence > governing.InputEventSequence {
		return nil
	}
	if governing.State == ContextStarted || (governing.State == ContextFailed && governing.Recovery != RecoveryNone) {
		return governing
	}
	return nil
}

func selectModel(view ExecutionView, governing *ModelContext) *ModelDecision {
	turn := view.Turn
	if turn == nil {
		return nil
	}
	candidates := make([]ModelDecision, 0, 7)
	if view.MaxContextInputSequence < turn.FirstOpeningSequence && view.StopSequence <= turn.FirstOpeningSequence {
		candidates = append(candidates, ModelDecision{
			Kind: ModelStart, Origin: OriginInitial, TurnID: turn.ID,
			Opening: turn.InitialOpening, ReadyAt: turn.InitialReadyAt,
		})
	}
	if governing != nil {
		laterSemantic := turn.LatestSemantic.Sequence > governing.InputEventSequence
		if !laterSemantic && governing.State == ContextFailed && governing.Recovery == RecoveryRetry {
			candidates = append(candidates, ModelDecision{
				Kind: ModelResume, Origin: OriginRetry, TurnID: turn.ID, SourceContextID: governing.ID,
				Opening: governing.Opening, ReadyAt: *governing.RetryAt,
			})
		}
		if laterSemantic {
			candidates = append(candidates, ModelDecision{
				Kind: ModelStart, Origin: OriginSemantic, TurnID: turn.ID, SourceContextID: governing.ID,
				Opening: governing.Opening, ReadyAt: turn.LatestSemantic.Time,
			})
		}
	}
	if config := view.Config; eligibleBoundary(view, config) &&
		turn.FirstContentSequence > 0 && config.Event.Sequence >= turn.FirstContentSequence {
		candidates = append(candidates, ModelDecision{
			Kind: ModelStart, Origin: OriginConfig, TurnID: turn.ID, SourceInputID: config.ID,
			Opening: config.Opening, ReadyAt: config.Event.Time,
		})
	}
	if batch := view.ToolBatch; batch != nil && view.StopSequence <= batch.Output.Event.Sequence {
		candidates = append(candidates, ModelDecision{
			Kind: ModelContinue, Origin: OriginTools, TurnID: turn.ID,
			SourceContextID: batch.Output.Context.ID, SourceOutputID: batch.Output.ID,
			Opening: batch.Output.Context.Opening, ReadyAt: batch.Completion.ReadyAt,
		})
	}
	if output := view.OutputLimit; output != nil && view.StopSequence <= output.Event.Sequence &&
		output.Event.Sequence > view.MaxNormalInputSequence {
		candidates = append(candidates, ModelDecision{
			Kind: ModelContinue, Origin: OriginOutputLimit, TurnID: turn.ID,
			SourceContextID: output.Context.ID, SourceOutputID: output.ID,
			Opening: output.Context.Opening, ReadyAt: output.Event.Time,
		})
	}
	if checkpoint := view.Checkpoint; eligibleBoundary(view, checkpoint) {
		candidates = append(candidates, ModelDecision{
			Kind: ModelStart, Origin: OriginCheckpoint, TurnID: turn.ID, SourceCheckpointID: checkpoint.ID,
			Opening: checkpoint.Opening, ReadyAt: checkpoint.Event.Time,
		})
	}
	for _, candidate := range candidates {
		if len(candidate.Opening.InputIDs) != 0 {
			candidate.Opening.InputIDs = slices.Clone(candidate.Opening.InputIDs)
			return &candidate
		}
	}
	return nil
}

func eligibleBoundary(view ExecutionView, work *BoundaryWork) bool {
	return work != nil && work.Event.Sequence > view.MaxNormalInputSequence &&
		view.StopSequence <= work.Event.Sequence
}

func validateView(view ExecutionView) error {
	invalid := func(reason string) error { return fmt.Errorf("%w: %s", ErrInvalidState, reason) }
	if view.AgentID == uuid.Nil || (view.State != AgentActive && view.State != AgentArchived) {
		return invalid("agent identity or lifecycle")
	}
	if view.StopSequence < 0 || view.MaxNormalInputSequence < 0 ||
		view.MaxContextInputSequence < view.MaxNormalInputSequence {
		return invalid("event watermarks")
	}
	if view.Turn == nil {
		if view.NormalContext != nil || view.CompactionContext != nil || view.ToolBatch != nil ||
			view.OutputLimit != nil || view.Config != nil || view.Checkpoint != nil {
			return invalid("work without a current turn")
		}
		return nil
	}
	turn := view.Turn
	if turn.ID == uuid.Nil || turn.FirstOpeningSequence <= 0 ||
		turn.LastOpeningSequence < turn.FirstOpeningSequence {
		return invalid("turn opening boundary")
	}
	if err := validateOpening(turn.InitialOpening); err != nil {
		return err
	}
	for _, governing := range []*ModelContext{view.NormalContext, view.CompactionContext} {
		if governing == nil {
			continue
		}
		if err := validateContext(view, *governing); err != nil {
			return err
		}
	}
	if governing := view.NormalContext; governing != nil &&
		(governing.Operation != OperationNormal || governing.InputEventSequence != view.MaxNormalInputSequence) {
		return invalid("normal context head")
	}
	if governing := view.CompactionContext; governing != nil &&
		(governing.Operation != OperationCompaction || view.NormalContext == nil ||
			governing.InputEventSequence != view.NormalContext.InputEventSequence) {
		return invalid("compaction dependency head")
	}
	if view.ToolBatch != nil && view.OutputLimit != nil {
		return invalid("multiple pending model outputs")
	}
	if batch := view.ToolBatch; batch != nil {
		if err := validateOutput(view, batch.Output); err != nil {
			return err
		}
		if (batch.HasRunnable && !batch.HasIncomplete) || (batch.HasIncomplete == (batch.Completion != nil)) {
			return invalid("tool batch completion facts")
		}
		if batch.Completion != nil && batch.Completion.LastResultSequence <= batch.Output.Event.Sequence {
			return invalid("tool result frontier")
		}
	}
	if output := view.OutputLimit; output != nil {
		if err := validateOutput(view, *output); err != nil {
			return err
		}
	}
	for _, work := range []*BoundaryWork{view.Config, view.Checkpoint} {
		if work == nil {
			continue
		}
		if work.ID == uuid.Nil || work.AgentID != view.AgentID || work.TurnID != turn.ID || work.Event.Sequence <= 0 {
			return invalid("config or checkpoint scope")
		}
		if err := validateOpening(work.Opening); err != nil {
			return err
		}
	}
	return nil
}

func validateContext(view ExecutionView, governing ModelContext) error {
	if governing.ID == uuid.Nil || governing.AgentID != view.AgentID || governing.TurnID != view.Turn.ID ||
		governing.Attempt < 1 || governing.InputEventSequence < view.Turn.FirstOpeningSequence ||
		governing.InputEventSequence > view.MaxContextInputSequence {
		return fmt.Errorf("%w: context scope or identity", ErrInvalidState)
	}
	if (governing.Operation == OperationNormal && governing.SourceEventSequenceEnd != 0) ||
		(governing.Operation == OperationCompaction && (governing.SourceEventSequenceEnd <= 0 ||
			governing.SourceEventSequenceEnd > governing.InputEventSequence)) ||
		(governing.Operation != OperationNormal && governing.Operation != OperationCompaction) {
		return fmt.Errorf("%w: context operation", ErrInvalidState)
	}
	switch governing.State {
	case ContextStarted, ContextSucceeded, ContextFailed, ContextCanceled:
	default:
		return fmt.Errorf("%w: context state", ErrInvalidState)
	}
	switch governing.Recovery {
	case RecoveryNone, RecoveryRetry:
	case RecoveryCompact:
		if governing.Operation != OperationNormal {
			return fmt.Errorf("%w: normal compaction dependency", ErrInvalidState)
		}
	case RecoveryReduce:
		if governing.Operation != OperationCompaction {
			return fmt.Errorf("%w: compaction source reduction", ErrInvalidState)
		}
	default:
		return fmt.Errorf("%w: context recovery", ErrInvalidState)
	}
	if governing.Opening.EventSequence > governing.InputEventSequence {
		return fmt.Errorf("%w: opening after context watermark", ErrInvalidState)
	}
	if governing.Recovery != RecoveryNone && governing.State != ContextFailed {
		return fmt.Errorf("%w: recovery on nonfailed context", ErrInvalidState)
	}
	if (governing.Recovery == RecoveryRetry) != (governing.RetryAt != nil) {
		return fmt.Errorf("%w: retry deadline", ErrInvalidState)
	}
	return validateOpening(governing.Opening)
}

func validateOutput(view ExecutionView, output ModelOutput) error {
	if output.ID == uuid.Nil || output.EventID == uuid.Nil || output.Context.Operation != OperationNormal ||
		output.Context.State != ContextSucceeded || output.Event.Sequence <= output.Context.InputEventSequence {
		return fmt.Errorf("%w: pending model output", ErrInvalidState)
	}
	return validateContext(view, output.Context)
}

func validateOpening(opening Opening) error {
	if (len(opening.InputIDs) == 0) != (opening.EventSequence == 0) || opening.EventSequence < 0 {
		return fmt.Errorf("%w: opening boundary", ErrInvalidState)
	}
	seen := make(map[uuid.UUID]struct{}, len(opening.InputIDs))
	for _, id := range opening.InputIDs {
		if _, duplicate := seen[id]; duplicate || id == uuid.Nil {
			return fmt.Errorf("%w: duplicate or empty opening identity", ErrInvalidState)
		}
		seen[id] = struct{}{}
	}
	return nil
}
