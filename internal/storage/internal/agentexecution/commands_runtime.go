package agentexecution

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type OwnedWork struct {
	RuntimeID uuid.UUID
	Selection Selection
	Model     PreparedModel
	Admission InputAdmission
	Released  bool
}

func (h *Handle) Claim(
	ctx context.Context,
	workerID uuid.UUID,
	leaseDuration time.Duration,
) (OwnedWork, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (OwnedWork, error) {
		if workerID == uuid.Nil || leaseDuration < 15*time.Second || leaseDuration > 30*time.Minute {
			return OwnedWork{}, errors.New("worker identity is required")
		}
		q := executiondb.New()
		_, err := q.ReadExecutionRuntime(
			ctx,
			h.unit.DB(),
			executiondb.ReadExecutionRuntimeParams{AgentID: h.route.AgentID},
		)
		if err == nil {
			return OwnedWork{}, storeerr.ErrAgentNotAdvanceable
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return OwnedWork{}, err
		}
		snapshot, err := h.LoadExecution(ctx)
		if err != nil {
			return OwnedWork{}, err
		}
		if snapshot.Selection.Work == WorkNone {
			m.changed()
			return OwnedWork{Selection: snapshot.Selection}, nil
		}
		result := OwnedWork{}
		if snapshot.Selection.Work == WorkInput {
			result.Admission, err = h.admitInputs(ctx, m, snapshot.Selection.Admission)
			if err != nil {
				return OwnedWork{}, err
			}
			snapshot, err = h.LoadExecution(ctx)
			if err != nil {
				return OwnedWork{}, err
			}
		}
		result.Selection = snapshot.Selection
		if result.Selection.Work == WorkNone {
			return result, nil
		}
		result.RuntimeID, err = q.AcquireExecutionRuntime(
			ctx,
			h.unit.DB(),
			executiondb.AcquireExecutionRuntimeParams{
				AgentID:  h.route.AgentID,
				ID:       uuid.New(),
				WorkerID: workerID, LeaseMicroseconds: leaseDuration.Microseconds(),
			},
		)
		if err != nil {
			return OwnedWork{}, err
		}
		h.unit.runtimeLocked = true
		m.changed()
		if err = q.ConsumeExecutionWakeup(ctx,
			h.unit.DB(),
			executiondb.ConsumeExecutionWakeupParams{AgentID: h.route.AgentID}); err != nil {
			return OwnedWork{}, err
		}
		return result, nil
	})
}

func (h *Handle) Advance(
	ctx context.Context,
	runtimeID uuid.UUID,
	allowModelWork, prepareModel bool,
) (OwnedWork, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (OwnedWork, error) {
		runtime, err := h.lockRuntime(ctx, runtimeID, false)
		if err != nil {
			return OwnedWork{}, err
		}
		if runtime.CancelRequestedAt != nil || !runtime.LeaseExpiresAt.After(runtime.DatabaseNow) {
			return OwnedWork{}, storeerr.ErrRuntimeLockInactive
		}
		retained, err := executiondb.New().
			ExecutionRetainedRuntimeWork(ctx,
				h.unit.DB(),
				executiondb.ExecutionRetainedRuntimeWorkParams{AgentID: h.route.AgentID,
					RuntimeID: &runtimeID})
		if err != nil {
			return OwnedWork{}, err
		}
		if valueOrZero(retained) {
			if err = h.releaseRuntime(ctx, m, runtime, false); err != nil {
				return OwnedWork{}, err
			}
			return OwnedWork{Released: true}, nil
		}
		snapshot, err := h.LoadExecution(ctx)
		if err != nil {
			return OwnedWork{}, err
		}
		result := OwnedWork{RuntimeID: runtimeID}
		if !allowModelWork && (snapshot.Selection.Work == WorkModel || snapshot.Selection.Work == WorkInput) {
			if err = h.releaseRuntime(ctx, m, runtime, false); err != nil {
				return OwnedWork{}, err
			}
			return OwnedWork{Released: true}, nil
		}
		if snapshot.Selection.Work == WorkInput {
			result.Admission, err = h.admitInputs(ctx, m, snapshot.Selection.Admission)
			if err != nil {
				return OwnedWork{}, err
			}
			snapshot, err = h.LoadExecution(ctx)
			if err != nil {
				return OwnedWork{}, err
			}
		}
		result.Selection = snapshot.Selection
		if snapshot.Selection.Work == WorkNone {
			if err = h.releaseRuntime(ctx, m, runtime, false); err != nil {
				return OwnedWork{}, err
			}
			result.RuntimeID = uuid.Nil
			result.Released = true
			return result, nil
		}
		if prepareModel && snapshot.Selection.Work == WorkModel &&
			snapshot.Selection.Model.Kind != ModelResume {
			result.Model, err = h.PrepareModel(
				ctx,
				PrepareModelInput{RuntimeLockID: runtimeID, Selected: *snapshot.Selection.Model},
			)
			if err != nil {
				return OwnedWork{}, err
			}
		}
		m.changed()
		return result, nil
	})
}

func (h *Handle) AcceptAndAdvance(
	ctx context.Context,
	input AcceptOutputInput,
	allowModelWork, prepareModel bool,
) (AcceptedOutput, OwnedWork, error) {
	output, err := h.AcceptOutput(ctx, input)
	if err != nil {
		return AcceptedOutput{}, OwnedWork{}, err
	}
	if !output.Created {
		return output, OwnedWork{Released: true}, h.Release(ctx, input.RuntimeLockID)
	}
	work, err := h.Advance(ctx, input.RuntimeLockID, allowModelWork, prepareModel)
	return output, work, err
}

func (h *Handle) lockRuntime(
	ctx context.Context,
	id uuid.UUID,
	skip bool,
) (executiondb.ReadExecutionRuntimeRow, error) {
	q := executiondb.New()
	var err error
	if skip {
		_, err = q.TryLockExecutionRuntime(
			ctx,
			h.unit.DB(),
			executiondb.TryLockExecutionRuntimeParams{AgentID: h.route.AgentID, ID: id},
		)
	} else {
		_,
			err = q.LockExecutionRuntime(ctx,
			h.unit.DB(),
			executiondb.LockExecutionRuntimeParams{AgentID: h.route.AgentID,
				ID: id})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return executiondb.ReadExecutionRuntimeRow{}, storeerr.ErrRuntimeLockInactive
	}
	if err != nil {
		return executiondb.ReadExecutionRuntimeRow{}, err
	}
	h.unit.runtimeLocked = true
	return q.ReadExecutionRuntime(
		ctx,
		h.unit.DB(),
		executiondb.ReadExecutionRuntimeParams{AgentID: h.route.AgentID, ID: &id},
	)
}

func (h *Handle) Release(ctx context.Context, id uuid.UUID) error {
	_, err := executeCommand(ctx, h, func(m *executionMutation) (bool, error) {
		runtime, err := h.lockRuntime(ctx, id, false)
		if err != nil {
			return false, err
		}
		return true, h.releaseRuntime(ctx, m, runtime, false)
	})
	return err
}

func (h *Handle) Reap(ctx context.Context, id uuid.UUID) (bool, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (bool, error) {
		runtime, err := h.lockRuntime(ctx, id, true)
		if errors.Is(err, storeerr.ErrRuntimeLockInactive) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if runtime.LeaseExpiresAt.After(runtime.DatabaseNow) {
			return false, nil
		}
		return true, h.releaseRuntime(ctx, m, runtime, true)
	})
}

type RuntimeRenewal struct {
	LeaseExpiresAt time.Time
	LocalStartedAt time.Time
}

func (u *Unit) RenewRuntime(
	ctx context.Context, route AgentRoute, id uuid.UUID, leaseDuration time.Duration,
) (RuntimeRenewal, error) {
	if route.CellID != u.cell.id || route.ProjectID == uuid.Nil || route.AgentID == uuid.Nil ||
		id == uuid.Nil {
		return RuntimeRenewal{}, storeerr.ErrRuntimeLockInactive
	}
	if leaseDuration < 15*time.Second || leaseDuration > 30*time.Minute {
		return RuntimeRenewal{}, errors.New("runtime lease must be between 15 seconds and 30 minutes")
	}
	q := executiondb.New()
	_, err := q.LockExecutionRuntime(
		ctx,
		u.DB(),
		executiondb.LockExecutionRuntimeParams{AgentID: route.AgentID, ID: id},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return RuntimeRenewal{}, storeerr.ErrRuntimeLockInactive
	}
	if err != nil {
		return RuntimeRenewal{}, err
	}
	u.runtimeLocked = true
	started := time.Now() //nolint:omnaralint // Monotonic lease budget.
	expires, err := q.RenewExecutionRuntime(ctx, u.DB(), executiondb.RenewExecutionRuntimeParams{
		ProjectID: route.ProjectID, AgentID: route.AgentID, ID: id,
		LeaseMicroseconds: leaseDuration.Microseconds(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return RuntimeRenewal{}, storeerr.ErrRuntimeLockInactive
	}
	return RuntimeRenewal{LeaseExpiresAt: expires, LocalStartedAt: started}, err
}

func (h *Handle) releaseRuntime(
	ctx context.Context,
	m *executionMutation,
	runtime executiondb.ReadExecutionRuntimeRow,
	expired bool,
) error {
	code := "runtime_released_before_model_result_acceptance"
	message := "runtime released before the model result was durably accepted"
	reason := "runtime_lock_released"
	if expired {
		code = "runtime_lease_expired_before_model_result_acceptance"
		message = "runtime lease expired before the model result was durably accepted"
		reason = "runtime_lock_stale"
	}
	q := executiondb.New()
	live, err := q.ListExecutionLiveContexts(
		ctx,
		h.unit.DB(),
		executiondb.ListExecutionLiveContextsParams{AgentID: h.route.AgentID, RuntimeID: &runtime.ID},
	)
	if err != nil {
		return err
	}
	for _, c := range live {
		row, err := q.ReadExecutionAttempt(
			ctx,
			h.unit.DB(),
			executiondb.ReadExecutionAttemptParams{AgentID: h.route.AgentID, ID: c.ID},
		)
		if err != nil {
			return err
		}
		details, err := json.Marshal(
			map[string]any{
				"attempt_number":    row.AttemptNumber,
				"code":              code,
				"message":           message,
				"outcome_ambiguous": true,
				"runtime_lock_id":   runtime.ID,
			},
		)
		if err != nil {
			return err
		}
		args := executiondb.TeardownExecutionContextParams{
			AgentID:      h.route.AgentID,
			ID:           c.ID,
			RuntimeID:    runtime.ID,
			State:        "failed",
			ErrorKind:    "runtime",
			ErrorCode:    code,
			ErrorMessage: message,
			ErrorDetails: details,
		}
		if row.AttemptNumber <= 8 {
			delay := max(0, h.unit.cell.retryBackoff(int(row.AttemptNumber), c.ID.String()).Microseconds())
			args.RetryMicroseconds = &delay
			args.Recovery = optionalText("retry")
		}
		snapshot, err := h.LoadExecution(ctx)
		if err != nil {
			return err
		}
		n, err := q.TeardownExecutionContext(ctx, h.unit.DB(), args)
		if err != nil {
			return err
		}
		if n != 1 {
			return storeerr.ErrStateTransitionConflict
		}
		m.changed()
		if args.Recovery == nil {
			_, err = h.publishTerminalFailure(
				ctx,
				m,
				ModelFailure{
					ContextID:     c.ID,
					RuntimeLockID: runtime.ID,
					ErrorKind:     "runtime",
					ErrorCode:     code,
					ErrorMessage:  message,
					ErrorDetails:  details,
				},
				row,
				snapshot,
				true,
			)
			if err != nil {
				return err
			}
		}
	}
	calls, err := q.ListExecutionUnfinishedTools(
		ctx,
		h.unit.DB(),
		executiondb.ListExecutionUnfinishedToolsParams{AgentID: h.route.AgentID, RuntimeID: &runtime.ID},
	)
	if err != nil {
		return err
	}
	body, err := json.Marshal(
		map[string]string{
			"error_code": reason,
			"message":    "Tool call was interrupted; its external outcome is unknown.",
		},
	)
	if err != nil {
		return err
	}
	for _, id := range calls {
		call, err := h.tool(ctx, id)
		if err != nil {
			return err
		}
		if _,
			err = h.completeTool(ctx,
			m,
			call,
			"failed",
			[]Content{{Kind: "structured_data",
				Data: body}},
			uuid.Nil, nil); err != nil {
			return err
		}
	}
	if expired {
		if err = h.failQueuedRuntime(ctx, runtime.ID, reason); err != nil {
			return err
		}
	}
	n, err := q.DeleteExecutionRuntime(
		ctx,
		h.unit.DB(),
		executiondb.DeleteExecutionRuntimeParams{AgentID: h.route.AgentID, ID: runtime.ID},
	)
	if err != nil {
		return err
	}
	if n != 1 {
		return storeerr.ErrRuntimeLockInactive
	}
	m.changed()
	return nil
}
