package agentexecution

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/executionpolicy"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ClaimedWork struct {
	RuntimeID uuid.UUID
	Runtime   RuntimeRecord
	Selection Selection
	Admission InputAdmission
}

func (h *Handle) Claim(
	ctx context.Context,
	workerID uuid.UUID,
	leaseDuration time.Duration,
) (ClaimedWork, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (ClaimedWork, error) {
		if workerID == uuid.Nil {
			return ClaimedWork{}, errors.New("worker identity is required")
		}
		if err := executionpolicy.ValidateLeaseDuration(leaseDuration); err != nil {
			return ClaimedWork{}, err
		}
		q := executiondb.New()
		_, err := q.ReadExecutionRuntime(
			ctx,
			h.unit.DB(),
			executiondb.ReadExecutionRuntimeParams{AgentID: h.route.AgentID},
		)
		if err == nil {
			return ClaimedWork{}, storeerr.ErrAgentNotAdvanceable
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return ClaimedWork{}, err
		}
		snapshot, err := h.LoadExecution(ctx)
		if err != nil {
			return ClaimedWork{}, err
		}
		if snapshot.Selection.Work == WorkNone {
			m.dirty = true
			return ClaimedWork{Selection: snapshot.Selection}, nil
		}
		result := ClaimedWork{}
		if snapshot.Selection.Work == WorkInput {
			result.Admission, err = h.admitInputs(ctx, m, snapshot.Selection.Admission)
			if err != nil {
				return ClaimedWork{}, err
			}
			snapshot, err = h.LoadExecution(ctx)
			if err != nil {
				return ClaimedWork{}, err
			}
		}
		result.Selection = snapshot.Selection
		if result.Selection.Work == WorkNone {
			return result, nil
		}
		runtime, err := q.AcquireExecutionRuntime(
			ctx,
			h.unit.DB(),
			executiondb.AcquireExecutionRuntimeParams{
				AgentID:  h.route.AgentID,
				ID:       uuid.New(),
				WorkerID: workerID, LeaseMicroseconds: leaseDuration.Microseconds(),
			},
		)
		if err != nil {
			return ClaimedWork{}, err
		}
		result.RuntimeID = runtime.ID
		result.Runtime = RuntimeRecord(runtime)
		h.unit.runtimeLocked = true
		h.lockedRuntimeID = result.RuntimeID
		m.dirty = true
		if err = q.ConsumeExecutionWakeup(ctx,
			h.unit.DB(),
			executiondb.ConsumeExecutionWakeupParams{AgentID: h.route.AgentID}); err != nil {
			return ClaimedWork{}, err
		}
		return result, nil
	})
}

func (h *Handle) lockRuntime(
	ctx context.Context,
	id uuid.UUID,
	skip bool,
) (executiondb.ReadExecutionRuntimeRow, error) {
	if id == uuid.Nil {
		return executiondb.ReadExecutionRuntimeRow{}, storeerr.ErrRuntimeLockInactive
	}
	q := executiondb.New()
	var err error
	if h.lockedRuntimeID != id {
		if skip {
			_, err = q.TryLockExecutionRuntime(
				ctx,
				h.unit.DB(),
				executiondb.TryLockExecutionRuntimeParams{AgentID: h.route.AgentID, ID: id},
			)
		} else {
			_, err = q.LockExecutionRuntime(ctx,
				h.unit.DB(),
				executiondb.LockExecutionRuntimeParams{AgentID: h.route.AgentID,
					ID: id})
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return executiondb.ReadExecutionRuntimeRow{}, storeerr.ErrRuntimeLockInactive
	}
	if err != nil {
		return executiondb.ReadExecutionRuntimeRow{}, err
	}
	h.unit.runtimeLocked = true
	h.lockedRuntimeID = id
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

type RuntimeRecord struct {
	ID                uuid.UUID
	AgentID           uuid.UUID
	WorkerProcessID   uuid.UUID
	StartedAt         time.Time
	RenewedAt         time.Time
	LeaseExpiresAt    time.Time
	CancelRequestedAt *time.Time
}

type RuntimeRenewal struct {
	Runtime        RuntimeRecord
	LeaseExpiresAt time.Time
	LocalStartedAt time.Time
}

func (u *Unit) RenewRuntime(
	ctx context.Context, route AgentRoute, id uuid.UUID, leaseDuration time.Duration,
) (RuntimeRenewal, error) {
	if route.ProjectID == uuid.Nil || route.AgentID == uuid.Nil ||
		id == uuid.Nil {
		return RuntimeRenewal{}, storeerr.ErrRuntimeLockInactive
	}
	if err := executionpolicy.ValidateLeaseDuration(leaseDuration); err != nil {
		return RuntimeRenewal{}, err
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
	return RuntimeRenewal{Runtime: RuntimeRecord(expires), LeaseExpiresAt: expires.LeaseExpiresAt, LocalStartedAt: started}, err
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
		retryCount, err := dbsqlc.New(h.unit.DB()).CountModelCallRetries(ctx, dbsqlc.CountModelCallRetriesParams{
			ProjectID: h.route.ProjectID, AgentID: h.route.AgentID, ModelCallContextID: row.ID,
		})
		if err != nil {
			return err
		}
		if retryCount < executionpolicy.MaxModelCallRetriesPerOperation {
			delay := max(0, h.unit.cell.retryBackoff(int(retryCount)+1, c.ID.String()).Microseconds())
			args.RetryMicroseconds = &delay
			args.Recovery = storeutil.TextFromEmpty("retry")
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
			"message":    executionpolicy.InterruptedToolMessage,
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
		if _, err = h.completeTool(ctx,
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
	h.lockedRuntimeID = uuid.Nil
	if m.loaded == nil {
		m.changed()
		return nil
	}
	m.loaded.databaseNow = runtime.DatabaseNow
	m.dirty = true
	return m.selectLoaded()
}
