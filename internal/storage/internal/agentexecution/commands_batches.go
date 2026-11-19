package agentexecution

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/executionpolicy"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// BatchRequest is a model call waiting on a provider batch. Its runtime lock
// stays held while parked, so no other work can start for the agent, but no
// worker owns the lock until the batch is resumed.
type BatchRequest struct {
	ID                   uuid.UUID
	ProjectID            uuid.UUID
	AgentID              uuid.UUID
	RuntimeID            uuid.UUID
	ContextID            uuid.UUID
	TurnID               uuid.UUID
	OpeningInputIDs      []uuid.UUID
	OpeningEventSequence int64
	CustomID             string
	InputFingerprint     string
	ProviderBatchID      string
	DeadlineAt           *time.Time
	// CreatedAt is set when the request is read back, not when it is claimed.
	CreatedAt time.Time
}

type CreateBatchRequestInput struct {
	ID                   uuid.UUID
	RuntimeID            uuid.UUID
	ContextID            uuid.UUID
	TurnID               uuid.UUID
	OpeningInputIDs      []uuid.UUID
	OpeningEventSequence int64
	CallbackTokenHash    []byte
	CustomID             string
	InputFingerprint     string
}

type ParkBatchInput struct {
	ID              uuid.UUID
	RuntimeID       uuid.UUID
	ProviderBatchID string
	// HoldFor bounds how long the parked lock is held without a worker. It is
	// fixed by the first park; later parks keep the original deadline.
	HoldFor time.Duration
	// PollDelay is the fallback resume delay when no callback arrives.
	PollDelay time.Duration
}

func (u *Unit) CreateBatchRequest(ctx context.Context, route AgentRoute, input CreateBatchRequestInput) error {
	if route.ProjectID == uuid.Nil || route.AgentID == uuid.Nil || input.RuntimeID == uuid.Nil {
		return storeerr.ErrRuntimeLockInactive
	}
	q := executiondb.New()
	if _, err := q.LockExecutionRuntime(ctx, u.DB(), executiondb.LockExecutionRuntimeParams{
		AgentID: route.AgentID, ID: input.RuntimeID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrRuntimeLockInactive
		}
		return err
	}
	u.runtimeLocked = true
	n, err := q.InsertExecutionBatchRequest(ctx, u.DB(), executiondb.InsertExecutionBatchRequestParams{
		ID:                   input.ID,
		ContextID:            input.ContextID,
		TurnID:               input.TurnID,
		OpeningInputIds:      input.OpeningInputIDs,
		OpeningEventSequence: input.OpeningEventSequence,
		CallbackTokenHash:    input.CallbackTokenHash,
		CustomID:             input.CustomID,
		InputFingerprint:     input.InputFingerprint,
		RuntimeID:            input.RuntimeID,
		AgentID:              route.AgentID,
		ProjectID:            route.ProjectID,
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return storeerr.ErrRuntimeLockInactive
	}
	return nil
}

func (u *Unit) BatchRequest(ctx context.Context, route AgentRoute, runtimeID, id uuid.UUID) (BatchRequest, error) {
	row, err := executiondb.New().ReadExecutionBatchRequest(ctx, u.DB(), executiondb.ReadExecutionBatchRequestParams{
		ID: id, AgentID: route.AgentID, RuntimeID: runtimeID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return BatchRequest{}, storeerr.ErrRuntimeLockInactive
	}
	if err != nil {
		return BatchRequest{}, err
	}
	return BatchRequest{
		ID: row.ID, ProjectID: row.ProjectID, AgentID: row.AgentID, RuntimeID: row.RuntimeLockID,
		ContextID: row.ModelCallContextID, TurnID: row.TurnID, OpeningInputIDs: row.OpeningInputIds,
		OpeningEventSequence: row.OpeningEventSequence, CustomID: row.CustomID,
		InputFingerprint: row.InputFingerprint, ProviderBatchID: row.ProviderBatchID, DeadlineAt: row.DeadlineAt,
		CreatedAt: row.CreatedAt,
	}, nil
}

// ParkRuntimeForBatch keeps the runtime lock held until the batch deadline and
// hands the batch request to the resume queue. It returns when a worker will
// next look at the batch absent a callback.
func (u *Unit) ParkRuntimeForBatch(ctx context.Context, route AgentRoute, input ParkBatchInput) (time.Time, error) {
	if route.AgentID == uuid.Nil || input.RuntimeID == uuid.Nil || input.ID == uuid.Nil {
		return time.Time{}, storeerr.ErrRuntimeLockInactive
	}
	if input.PollDelay <= 0 || input.HoldFor <= 0 {
		return time.Time{}, errors.New("batch poll delay and hold duration must be positive")
	}
	q := executiondb.New()
	if _, err := q.LockExecutionRuntime(ctx, u.DB(), executiondb.LockExecutionRuntimeParams{
		AgentID: route.AgentID, ID: input.RuntimeID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, storeerr.ErrRuntimeLockInactive
		}
		return time.Time{}, err
	}
	u.runtimeLocked = true
	resumeAt, err := q.ParkExecutionBatchRequest(ctx, u.DB(), executiondb.ParkExecutionBatchRequestParams{
		ProviderBatchID:  input.ProviderBatchID,
		HoldMicroseconds: input.HoldFor.Microseconds(),
		PollMicroseconds: input.PollDelay.Microseconds(),
		ID:               input.ID,
		AgentID:          route.AgentID,
		RuntimeID:        input.RuntimeID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, storeerr.ErrStateTransitionConflict
	}
	if err != nil {
		return time.Time{}, err
	}
	if resumeAt == nil {
		return time.Time{}, storeerr.ErrStateTransitionConflict
	}
	n, err := q.ParkExecutionRuntime(ctx, u.DB(), executiondb.ParkExecutionRuntimeParams{
		AgentID: route.AgentID, ID: input.RuntimeID, BatchRequestID: input.ID,
	})
	if err != nil {
		return time.Time{}, err
	}
	if n != 1 {
		return time.Time{}, storeerr.ErrRuntimeLockInactive
	}
	return *resumeAt, nil
}

// ClaimParkedBatch transfers one resumable parked runtime to workerID with a
// normal lease. It returns pgx.ErrNoRows when nothing is ready.
func (u *Unit) ClaimParkedBatch(
	ctx context.Context, workerID uuid.UUID, leaseDuration time.Duration,
) (BatchRequest, RuntimeRenewal, error) {
	if workerID == uuid.Nil {
		return BatchRequest{}, RuntimeRenewal{}, errors.New("worker identity is required")
	}
	if err := executionpolicy.ValidateLeaseDuration(leaseDuration); err != nil {
		return BatchRequest{}, RuntimeRenewal{}, err
	}
	q := executiondb.New()
	candidate, err := q.LockExecutionParkedBatchRuntime(ctx, u.DB())
	if err != nil {
		return BatchRequest{}, RuntimeRenewal{}, err
	}
	u.runtimeLocked = true
	row, err := q.ResumeExecutionBatchRequest(ctx, u.DB(), executiondb.ResumeExecutionBatchRequestParams{
		ID: candidate.ID, RuntimeID: candidate.RuntimeLockID,
	})
	if err != nil {
		return BatchRequest{}, RuntimeRenewal{}, err
	}
	started := time.Now() //nolint:omnaralint // Monotonic lease budget.
	expires, err := q.UnparkExecutionRuntime(ctx, u.DB(), executiondb.UnparkExecutionRuntimeParams{
		WorkerID: workerID, LeaseMicroseconds: leaseDuration.Microseconds(), ID: candidate.RuntimeLockID,
	})
	if err != nil {
		return BatchRequest{}, RuntimeRenewal{}, err
	}
	return BatchRequest{
		ID: row.ID, ProjectID: row.ProjectID, AgentID: row.AgentID, RuntimeID: row.RuntimeLockID,
		ContextID: row.ModelCallContextID, TurnID: row.TurnID, OpeningInputIDs: row.OpeningInputIds,
		OpeningEventSequence: row.OpeningEventSequence, CustomID: row.CustomID,
		InputFingerprint: row.InputFingerprint, ProviderBatchID: row.ProviderBatchID, DeadlineAt: row.DeadlineAt,
	}, RuntimeRenewal{LeaseExpiresAt: expires, LocalStartedAt: started}, nil
}

// WakeBatchRequest makes a parked batch resumable now. It reports whether the
// request exists and the callback token matched.
func (u *Unit) WakeBatchRequest(ctx context.Context, id uuid.UUID, callbackTokenHash []byte) (bool, error) {
	n, err := executiondb.New().WakeExecutionBatchRequest(ctx, u.DB(), executiondb.WakeExecutionBatchRequestParams{
		ID: id, CallbackTokenHash: callbackTokenHash,
	})
	return n == 1, err
}

func (u *Unit) DeleteBatchRequest(ctx context.Context, runtimeID, id uuid.UUID) error {
	return executiondb.New().DeleteExecutionBatchRequest(ctx, u.DB(), executiondb.DeleteExecutionBatchRequestParams{
		ID: id, RuntimeID: runtimeID,
	})
}

// PurgeOrphanBatchRequests removes batch requests whose runtime lock no longer
// exists. A request is only ever inserted under its live lock, so a missing
// lock means the model call was already torn down.
func (u *Unit) PurgeOrphanBatchRequests(ctx context.Context) (int64, error) {
	return executiondb.New().DeleteExecutionOrphanBatchRequests(ctx, u.DB())
}
