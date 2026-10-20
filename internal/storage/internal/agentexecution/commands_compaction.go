package agentexecution

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type BeginCompactionInput struct {
	Failure   ModelFailure
	SourceEnd int64
}
type CompactionStart struct {
	Model     PreparedModel
	Preempted bool
	Admission InputAdmission
}

func (h *Handle) BeginCompaction(ctx context.Context, input BeginCompactionInput) (CompactionStart, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (CompactionStart, error) {
		if err := h.FenceRuntime(ctx, input.Failure.RuntimeLockID); err != nil {
			return CompactionStart{}, err
		}
		q := executiondb.New()
		source, err := q.ReadExecutionAttempt(
			ctx,
			h.unit.DB(),
			executiondb.ReadExecutionAttemptParams{AgentID: h.route.AgentID, ID: input.Failure.ContextID},
		)
		if err != nil {
			return CompactionStart{}, err
		}
		if source.State == "started" && (input.Failure.ErrorKind == "" || input.Failure.ErrorMessage == "") {
			return CompactionStart{}, errors.New("compaction requires failure evidence")
		}
		if input.SourceEnd <= 0 || input.SourceEnd > source.InputEventSequence ||
			source.OperationKind == "compaction" &&
				!compactionSourceProjectionAdvances(source, input) {
			return CompactionStart{}, storeerr.ErrStateTransitionConflict
		}
		if source.OperationKind == "normal" && input.Failure.Recovery != RecoveryCompact ||
			source.OperationKind == "compaction" && input.Failure.Recovery != RecoveryReduce {
			return CompactionStart{}, storeerr.ErrStateTransitionConflict
		}
		if source.State != "started" {
			historical := input.Failure
			historical.RuntimeLockID = source.RuntimeLockID
			if _, err = h.finishAttempt(
				ctx, m, source, ContextFailed, historical, input.Failure.Evidence,
			); err != nil {
				return CompactionStart{}, err
			}
			id, err := q.LatestExecutionAttempt(ctx, h.unit.DB(), executiondb.LatestExecutionAttemptParams{
				AgentID: h.route.AgentID, Operation: "compaction", Watermark: source.InputEventSequence,
				SourceEnd: &input.SourceEnd})
			if err == nil && id != source.ID {
				existing, err := q.ReadExecutionAttempt(
					ctx,
					h.unit.DB(),
					executiondb.ReadExecutionAttemptParams{AgentID: h.route.AgentID, ID: id},
				)
				return CompactionStart{Model: attemptRecord(existing)}, err
			}
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return CompactionStart{}, err
			}
			snapshot, err := h.LoadExecution(ctx)
			if err != nil {
				return CompactionStart{}, err
			}
			if snapshot.Head.CurrentTurnID != source.TurnID ||
				snapshot.Head.StopSequence > source.InputEventSequence ||
				snapshot.View.Turn.LatestSemantic.Sequence > source.InputEventSequence {
				return CompactionStart{Preempted: true}, nil
			}
		}
		covered, err := q.ExecutionCompactionBoundary(
			ctx,
			h.unit.DB(),
			executiondb.ExecutionCompactionBoundaryParams{
				AgentID:   h.route.AgentID,
				Watermark: source.InputEventSequence,
			},
		)
		if err != nil {
			return CompactionStart{}, err
		}
		if input.SourceEnd <= covered {
			return CompactionStart{}, storeerr.ErrStateTransitionConflict
		}
		if source.State == "started" {
			if _, err = h.finishAttempt(
				ctx, m, source, ContextFailed, input.Failure, input.Failure.Evidence,
			); err != nil {
				return CompactionStart{}, err
			}
		}

		snapshot, err := h.LoadExecution(ctx)
		if err != nil {
			return CompactionStart{}, err
		}
		if snapshot.View.Inputs.Steering {
			admitted, err := h.admitInputs(ctx, m, AdmitAllSteering)
			return CompactionStart{Preempted: true, Admission: admitted}, err
		}
		if snapshot.Head.CurrentTurnID != source.TurnID ||
			snapshot.Head.StopSequence > source.InputEventSequence ||
			snapshot.View.Turn.LatestSemantic.Sequence > source.InputEventSequence {
			return CompactionStart{Preempted: true}, nil
		}
		id, err := q.LatestExecutionAttempt(ctx, h.unit.DB(), executiondb.LatestExecutionAttemptParams{
			AgentID:   h.route.AgentID,
			Operation: "compaction",
			Watermark: source.InputEventSequence,
			SourceEnd: &input.SourceEnd})
		if err == nil && id != source.ID {
			existing, err := q.ReadExecutionAttempt(
				ctx,
				h.unit.DB(),
				executiondb.ReadExecutionAttemptParams{AgentID: h.route.AgentID, ID: id},
			)
			return CompactionStart{Model: attemptRecord(existing)}, err
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return CompactionStart{}, err
		}
		if source.OperationKind == "normal" && m.head.CompactionContextID != uuid.Nil {
			return CompactionStart{}, storeerr.ErrStateTransitionConflict
		}
		governing := m.head.NormalContextID
		if source.OperationKind == "compaction" {
			governing = m.head.CompactionContextID
		}
		if governing != source.ID {
			return CompactionStart{}, storeerr.ErrAgentNotAdvanceable
		}
		desired := attemptRecord(source)
		desired.Context.ID = uuid.New()
		desired.Context.Operation = OperationCompaction
		desired.Context.Attempt = 1
		if source.OperationKind == "compaction" && input.SourceEnd == storeutil.ValueOrZero(source.SourceEventSequenceEnd) {
			desired.Context.Attempt = source.AttemptNumber + 1
		}
		desired.Context.SourceEventSequenceEnd = input.SourceEnd
		desired.Context.State = ContextStarted
		desired.Context.Recovery = RecoveryNone
		desired.Context.RetryAt = nil
		desired.RuntimeLockID = input.Failure.RuntimeLockID
		result, err := h.createAttempt(ctx, m, desired, snapshot)
		return CompactionStart{Model: result}, err
	})
}

type PublishCheckpointInput struct {
	RuntimeLockID uuid.UUID
	ContextID     uuid.UUID
	Summary       string
	Evidence      ModelEvidence
}

type PublishedCheckpoint struct {
	ID      uuid.UUID
	Event   ExecutionEvent
	Created bool
}

func (h *Handle) PublishCheckpoint(
	ctx context.Context,
	input PublishCheckpointInput,
) (PublishedCheckpoint, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (PublishedCheckpoint, error) {
		if input.Summary == "" {
			return PublishedCheckpoint{}, errors.New("checkpoint summary is required")
		}
		if err := h.FenceRuntime(ctx, input.RuntimeLockID); err != nil {
			return PublishedCheckpoint{}, err
		}
		q := executiondb.New()
		source, err := q.ReadExecutionAttempt(
			ctx,
			h.unit.DB(),
			executiondb.ReadExecutionAttemptParams{AgentID: h.route.AgentID, ID: input.ContextID},
		)
		if err != nil {
			return PublishedCheckpoint{}, err
		}
		if source.OperationKind != "compaction" || source.SourceEventSequenceEnd == nil {
			return PublishedCheckpoint{}, storeerr.ErrStateTransitionConflict
		}
		failure := ModelFailure{ContextID: input.ContextID, RuntimeLockID: input.RuntimeLockID}
		if source.State != "started" {
			if _, err = h.finishAttempt(ctx, m, source, ContextSucceeded, failure, input.Evidence); err != nil {
				return PublishedCheckpoint{}, err
			}
			row, err := q.FindExecutionCheckpoint(
				ctx,
				h.unit.DB(),
				executiondb.FindExecutionCheckpointParams{AgentID: h.route.AgentID, ContextID: source.ID},
			)
			if err != nil {
				return PublishedCheckpoint{}, err
			}
			return PublishedCheckpoint{ID: row.ID, Event: ExecutionEvent{ID: row.EventID, TurnID: row.TurnID,
				EventBoundary: EventBoundary{Sequence: row.Sequence, Time: row.CreatedAt}}}, nil
		}
		if source.RuntimeLockID != input.RuntimeLockID {
			return PublishedCheckpoint{}, storeerr.ErrRuntimeLockInactive
		}
		covered, err := q.ExecutionCompactionBoundary(
			ctx,
			h.unit.DB(),
			executiondb.ExecutionCompactionBoundaryParams{
				AgentID:   h.route.AgentID,
				Watermark: source.InputEventSequence,
			},
		)
		if err != nil {
			return PublishedCheckpoint{}, err
		}
		end := *source.SourceEventSequenceEnd
		if end <= covered {
			return PublishedCheckpoint{}, storeerr.ErrStateTransitionConflict
		}
		valid, err := q.ValidateExecutionCheckpoint(
			ctx,
			h.unit.DB(),
			executiondb.ValidateExecutionCheckpointParams{
				AgentID:       h.route.AgentID,
				StartSequence: covered + 1,
				EndSequence:   end,
			},
		)
		if err != nil {
			return PublishedCheckpoint{}, err
		}
		if valid.Events != end-covered || valid.OpenAuthorities {
			return PublishedCheckpoint{}, storeerr.ErrCheckpointBoundaryUnsafe
		}
		boundary, err := q.ExecutionPreparationBoundary(
			ctx,
			h.unit.DB(),
			executiondb.ExecutionPreparationBoundaryParams{AgentID: h.route.AgentID},
		)
		if err != nil {
			return PublishedCheckpoint{}, err
		}
		opening, err := h.captureOpening(ctx, source.TurnID, boundary.Watermark, m.head.StopSequence)
		if err != nil {
			return PublishedCheckpoint{}, err
		}
		row, err := q.CreateExecutionCheckpoint(
			ctx,
			h.unit.DB(),
			executiondb.CreateExecutionCheckpointParams{AgentID: h.route.AgentID,
				SourceEnd:       end,
				ContextID:       source.ID,
				Summary:         input.Summary,
				OpeningIds:      opening.InputIDs,
				OpeningSequence: optionalSequence(opening.EventSequence)},
		)
		if err != nil {
			return PublishedCheckpoint{}, err
		}
		event, err := h.appendEvent(
			ctx,
			source.TurnID,
			"context_checkpoint",
			uuid.Nil,
			uuid.Nil,
			row.ID,
			false, row.CreatedAt,
		)
		if err != nil {
			return PublishedCheckpoint{}, err
		}
		if err = h.advanceTurn(ctx, event, false); err != nil {
			return PublishedCheckpoint{}, err
		}
		if _, err = h.finishAttempt(ctx, m, source, ContextSucceeded, failure, input.Evidence); err != nil {
			return PublishedCheckpoint{}, err
		}
		if source.TurnID == m.head.CurrentTurnID && event.Sequence > m.head.MaxNormalInputSequence &&
			event.Sequence >= m.head.StopSequence {
			m.head.PendingCheckpointID = row.ID
		}
		m.changed()
		return PublishedCheckpoint{ID: row.ID, Event: event, Created: true}, nil
	})
}

func compactionSourceProjectionAdvances(source executiondb.ReadExecutionAttemptRow, input BeginCompactionInput) bool {
	if source.SourceEventSequenceEnd == nil {
		return false
	}
	if input.SourceEnd < *source.SourceEventSequenceEnd {
		return source.SourceExcerptReductionCount == 0
	}
	return input.SourceEnd == *source.SourceEventSequenceEnd && input.Failure.Evidence.APIFormat != "" &&
		input.Failure.Evidence.APIVariant != "" &&
		(input.Failure.ErrorKind == modelprotocol.ErrorKindContextWindow ||
			input.Failure.ErrorKind == modelprotocol.ErrorKindPayloadTooLarge)
}
