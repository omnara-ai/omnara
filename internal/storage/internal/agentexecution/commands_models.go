package agentexecution

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type PreparedModel struct {
	Context       ModelContext
	ConfigID      uuid.UUID
	RevisionID    uuid.UUID
	RuntimeLockID uuid.UUID
	Created       bool
	Claimed       bool
}

type PrepareModelInput struct {
	RuntimeLockID uuid.UUID
	Selected      ModelDecision
}

func (h *Handle) FenceRuntime(ctx context.Context, id uuid.UUID) error {
	if id == uuid.Nil {
		return storeerr.ErrRuntimeLockInactive
	}
	if err := h.unit.LockAgentRefs(ctx,
		[]lifecyclelock.AgentRef{{ProjectID: h.route.ProjectID,
			AgentID: h.route.AgentID}},
		RuntimeAuthority{AgentID: h.route.AgentID, RuntimeLockID: id}); err != nil {
		return err
	}
	_, err := executiondb.New().FenceExecutionRuntime(ctx, h.unit.DB(), executiondb.FenceExecutionRuntimeParams{
		ProjectID: h.route.ProjectID, AgentID: h.route.AgentID, RuntimeID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrRuntimeLockInactive
	}

	return err
}

func attemptRecord(row executiondb.ReadExecutionAttemptRow) PreparedModel {
	return PreparedModel{Context: ModelContext{ID: row.ID, AgentID: row.AgentID, TurnID: row.TurnID,
		Operation:              Operation(row.OperationKind),
		Attempt:                row.AttemptNumber,
		InputEventSequence:     row.InputEventSequence,
		SourceEventSequenceEnd: valueOrZero(row.SourceEventSequenceEnd), State: ContextState(row.State),
		Recovery: RecoveryKind(valueOrZero(row.RecoveryKind)), RetryAt: row.RetryAt,
		Opening: Opening{InputIDs: row.OpeningInputIds, EventSequence: row.OpeningEventSequence}},
		ConfigID: row.AgentConfigID, RevisionID: row.ConfiguredModelRevisionID, RuntimeLockID: row.RuntimeLockID}
}

func sameDecision(a, b ModelDecision) bool {
	return a.Kind == b.Kind &&
		a.Origin == b.Origin &&
		a.TurnID == b.TurnID &&
		a.SourceContextID == b.SourceContextID &&
		a.SourceOutputID == b.SourceOutputID &&
		a.SourceInputID == b.SourceInputID &&
		a.SourceCheckpointID == b.SourceCheckpointID &&
		a.Opening.EventSequence == b.Opening.EventSequence && slices.Equal(a.Opening.InputIDs, b.Opening.InputIDs)
}

func (h *Handle) PrepareModel(ctx context.Context, input PrepareModelInput) (PreparedModel, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (PreparedModel, error) {
		if err := h.FenceRuntime(ctx, input.RuntimeLockID); err != nil {
			return PreparedModel{}, err
		}
		q := executiondb.New()
		boundary, err := q.ExecutionPreparationBoundary(
			ctx,
			h.unit.DB(),
			executiondb.ExecutionPreparationBoundaryParams{AgentID: h.route.AgentID},
		)
		if err != nil {
			return PreparedModel{}, err
		}
		desired := PreparedModel{ConfigID: boundary.CurrentConfigID, RuntimeLockID: input.RuntimeLockID,
			Context: ModelContext{
				ID:                 uuid.New(),
				AgentID:            h.route.AgentID,
				TurnID:             input.Selected.TurnID,
				Operation:          OperationNormal,
				Attempt:            1,
				InputEventSequence: boundary.Watermark,
				State:              ContextStarted,
				Opening:            input.Selected.Opening,
			}}
		if input.Selected.Kind == ModelResume {
			source, err := q.ReadExecutionAttempt(
				ctx,
				h.unit.DB(),
				executiondb.ReadExecutionAttemptParams{AgentID: h.route.AgentID, ID: input.Selected.SourceContextID},
			)
			if err != nil {
				return PreparedModel{}, err
			}
			desired = attemptRecord(source)
			desired.Context.ID = uuid.New()
			desired.Context.Attempt++
			desired.Context.State = ContextStarted
			desired.Context.Recovery = RecoveryNone
			desired.Context.RetryAt = nil
			desired.RuntimeLockID = input.RuntimeLockID
		}
		if desired.Context.InputEventSequence <= m.head.MaxContextInputSequence {
			id, err := q.LatestExecutionAttempt(
				ctx,
				h.unit.DB(),
				executiondb.LatestExecutionAttemptParams{AgentID: h.route.AgentID,
					Operation: string(desired.Context.Operation),
					Watermark: desired.Context.InputEventSequence,
					SourceEnd: optionalSequence(desired.Context.SourceEventSequenceEnd)},
			)
			if err == nil {
				existing, err := q.ReadExecutionAttempt(
					ctx,
					h.unit.DB(),
					executiondb.ReadExecutionAttemptParams{AgentID: h.route.AgentID, ID: id},
				)
				if err != nil {
					return PreparedModel{}, err
				}
				record := attemptRecord(existing)
				if record.Context.Attempt >= desired.Context.Attempt {
					if record.Context.TurnID != input.Selected.TurnID || record.ConfigID != desired.ConfigID ||
						!slices.Equal(record.Context.Opening.InputIDs, input.Selected.Opening.InputIDs) ||
						record.Context.Opening.EventSequence != input.Selected.Opening.EventSequence {
						return PreparedModel{}, storeerr.ErrIdempotencyConflict
					}
					return record, nil
				}
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return PreparedModel{}, err
			}
		}
		snapshot, err := h.LoadExecution(ctx)
		if err != nil {
			return PreparedModel{}, err
		}
		candidate := snapshot.Selection.Model
		if candidate == nil || !sameDecision(*candidate, input.Selected) || boundary.Started ||
			(input.Selected.Kind == ModelResume && (candidate.ReadyAt.After(boundary.DatabaseNow) ||
				desired.Context.Attempt > 9)) {
			return PreparedModel{}, storeerr.ErrAgentNotAdvanceable
		}
		return h.createAttempt(ctx, m, desired, snapshot)
	})
}

func (h *Handle) createAttempt(
	ctx context.Context,
	m *executionMutation,
	desired PreparedModel,
	snapshot ExecutionSnapshot,
) (PreparedModel, error) {
	q := executiondb.New()
	revision, err := q.ExecutionModelRevision(
		ctx,
		h.unit.DB(),
		executiondb.ExecutionModelRevisionParams{ProjectID: h.route.ProjectID, ConfigID: desired.ConfigID},
	)
	if err != nil {
		return PreparedModel{}, err
	}
	c := cloneModel(desired.Context)
	_, err = q.CreateExecutionAttempt(
		ctx,
		h.unit.DB(),
		executiondb.CreateExecutionAttemptParams{ID: c.ID, AgentID: h.route.AgentID,
			ProjectID:       h.route.ProjectID,
			TurnID:          c.TurnID,
			Operation:       string(c.Operation),
			Attempt:         c.Attempt,
			ConfigID:        desired.ConfigID,
			RevisionID:      revision.CurrentRevisionID,
			Watermark:       c.InputEventSequence,
			SourceEnd:       optionalSequence(c.SourceEventSequenceEnd),
			RuntimeID:       desired.RuntimeLockID,
			OpeningIds:      c.Opening.InputIDs,
			OpeningSequence: c.Opening.EventSequence},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PreparedModel{}, storeerr.ErrRuntimeLockInactive
	}
	if err != nil {
		return PreparedModel{}, err
	}
	m.head.MaxContextInputSequence = max(m.head.MaxContextInputSequence, c.InputEventSequence)
	if c.Operation == OperationNormal {
		m.head.MaxNormalInputSequence = max(m.head.MaxNormalInputSequence, c.InputEventSequence)
		m.head.NormalContextID = c.ID
		m.head.CompactionContextID = uuid.Nil
		if snapshot.View.Config != nil && snapshot.View.Config.Event.Sequence <= c.InputEventSequence {
			m.head.PendingConfigInputID = uuid.Nil
		}
		if snapshot.View.Checkpoint != nil && snapshot.View.Checkpoint.Event.Sequence <= c.InputEventSequence {
			m.head.PendingCheckpointID = uuid.Nil
		}
		if snapshot.View.OutputLimit != nil && snapshot.View.OutputLimit.Event.Sequence <= c.InputEventSequence {
			m.head.PendingOutputLimitID = uuid.Nil
		}
	} else {
		m.head.CompactionContextID = c.ID
	}
	if c.Operation == OperationNormal {
		snapshot.View.NormalContext = &c
		snapshot.View.CompactionContext = nil
		if m.head.PendingConfigInputID == uuid.Nil {
			snapshot.View.Config = nil
		}
		if m.head.PendingCheckpointID == uuid.Nil {
			snapshot.View.Checkpoint = nil
		}
		if m.head.PendingOutputLimitID == uuid.Nil {
			snapshot.View.OutputLimit = nil
		}
	} else {
		snapshot.View.CompactionContext = &c
	}
	if snapshot.View.Turn != nil && m.head.MaxContextInputSequence >= snapshot.View.Turn.FirstOpeningSequence {
		snapshot.View.Turn.InitialOpening = Opening{}
		snapshot.View.Turn.InitialReadyAt = time.Time{}
	}
	if err := m.updated(snapshot); err != nil {
		return PreparedModel{}, err
	}
	desired.RevisionID = revision.CurrentRevisionID
	desired.Created = true
	desired.Claimed = revision.Allowed
	if !revision.Allowed {
		_, err := h.terminalFailure(ctx, m, ModelFailure{ContextID: c.ID, RuntimeLockID: desired.RuntimeLockID,
			ErrorKind:    "runtime",
			ErrorCode:    storeerr.ManagedWorkAdmissionDeniedCode,
			ErrorMessage: storeerr.InsufficientOmnaraCreditsMessage})
		if err != nil {
			return PreparedModel{}, err
		}
		desired.Context.State = ContextFailed
	}
	return desired, nil
}

type PrepareNormalInput struct {
	RuntimeLockID   uuid.UUID
	OpeningInputIDs []uuid.UUID
	SourceContextID uuid.UUID
	SourceOutputID  uuid.UUID
}

func (h *Handle) PrepareNormal(ctx context.Context, input PrepareNormalInput) (PreparedModel, error) {
	return executeCommand(ctx, h, func(_ *executionMutation) (PreparedModel, error) {
		if err := h.FenceRuntime(ctx, input.RuntimeLockID); err != nil {
			return PreparedModel{}, err
		}
		snapshot, err := h.LoadExecution(ctx)
		if err != nil {
			return PreparedModel{}, err
		}
		selected := snapshot.Selection.Model
		if selected == nil {
			governing := snapshot.View.NormalContext
			if governing == nil {
				return PreparedModel{}, storeerr.ErrAgentNotAdvanceable
			}
			selected = &ModelDecision{
				Kind:    ModelStart,
				Origin:  OriginInitial,
				TurnID:  governing.TurnID,
				Opening: governing.Opening,
			}
		}
		if !slices.Equal(selected.Opening.InputIDs, input.OpeningInputIDs) ||
			selected.Kind == ModelResume ||
			(selected.Kind == ModelContinue &&
				(selected.SourceContextID != input.SourceContextID ||
					selected.SourceOutputID != input.SourceOutputID)) ||
			(selected.Kind != ModelContinue &&
				(input.SourceContextID != uuid.Nil ||
					input.SourceOutputID != uuid.Nil)) {
			return PreparedModel{}, storeerr.ErrAgentNotAdvanceable
		}
		prepared, err := h.PrepareModel(
			ctx,
			PrepareModelInput{RuntimeLockID: input.RuntimeLockID, Selected: *selected},
		)
		if err != nil {
			return PreparedModel{}, err
		}
		return prepared, nil
	})
}

func (h *Handle) ResumeModel(ctx context.Context, runtimeID, predecessorID uuid.UUID) (PreparedModel, error) {
	return executeCommand(ctx, h, func(_ *executionMutation) (PreparedModel, error) {
		if err := h.FenceRuntime(ctx, runtimeID); err != nil {
			return PreparedModel{}, err
		}
		q := executiondb.New()
		predecessor, err := q.ReadExecutionAttempt(ctx, h.unit.DB(), executiondb.ReadExecutionAttemptParams{
			AgentID: h.route.AgentID, ID: predecessorID,
		})
		if err != nil {
			return PreparedModel{}, err
		}
		id, err := q.LatestExecutionAttempt(ctx, h.unit.DB(), executiondb.LatestExecutionAttemptParams{
			AgentID: h.route.AgentID, Operation: predecessor.OperationKind,
			Watermark: predecessor.InputEventSequence, SourceEnd: predecessor.SourceEventSequenceEnd,
		})
		if err != nil {
			return PreparedModel{}, err
		}
		latest, err := q.ReadExecutionAttempt(ctx, h.unit.DB(), executiondb.ReadExecutionAttemptParams{
			AgentID: h.route.AgentID, ID: id,
		})
		if err != nil {
			return PreparedModel{}, err
		}
		snapshot, err := h.LoadExecution(ctx)
		if err != nil {
			return PreparedModel{}, err
		}
		selected := snapshot.Selection.Model
		if selected == nil || selected.Kind != ModelResume || selected.SourceContextID != predecessor.ID {
			return attemptRecord(latest), nil
		}
		boundary, err := q.ExecutionPreparationBoundary(ctx, h.unit.DB(), executiondb.ExecutionPreparationBoundaryParams{
			AgentID: h.route.AgentID,
		})
		if err != nil {
			return PreparedModel{}, err
		}
		if boundary.Started || selected.ReadyAt.After(boundary.DatabaseNow) || predecessor.AttemptNumber >= 9 {
			return attemptRecord(latest), nil
		}
		return h.PrepareModel(ctx, PrepareModelInput{RuntimeLockID: runtimeID, Selected: *selected})
	})
}
