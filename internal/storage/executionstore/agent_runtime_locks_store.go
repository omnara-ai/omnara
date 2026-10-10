package executionstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	AgentRuntimeLockLeaseDuration        = 90 * time.Second
	MinimumAgentRuntimeLockLeaseDuration = 15 * time.Second
	MaximumAgentRuntimeLockLeaseDuration = 30 * time.Minute
)

type AgentRuntimeLockRecord struct {
	ID                uuid.UUID  `json:"id"`
	AgentID           uuid.UUID  `json:"agent_id"`
	WorkerProcessID   uuid.UUID  `json:"worker_process_id"`
	StartedAt         time.Time  `json:"started_at"`
	RenewedAt         time.Time  `json:"renewed_at"`
	LeaseExpiresAt    time.Time  `json:"lease_expires_at"`
	CancelRequestedAt *time.Time `json:"cancel_requested_at,omitempty"`
}

type AgentRuntimeLockRenewal struct {
	RuntimeLock               AgentRuntimeLockRecord
	LocalLeaseBudgetStartedAt time.Time
}

func validateAgentRuntimeLockLeaseDuration(leaseDuration time.Duration) error {
	if leaseDuration < MinimumAgentRuntimeLockLeaseDuration ||
		leaseDuration > MaximumAgentRuntimeLockLeaseDuration {
		return fmt.Errorf(
			"agent runtime lock lease duration must be between %s and %s",
			MinimumAgentRuntimeLockLeaseDuration,
			MaximumAgentRuntimeLockLeaseDuration,
		)
	}
	return nil
}

func (s *Store) EnsureRuntimeLockActive(
	ctx context.Context, projectID, agentID, runtimeID uuid.UUID,
) error {
	if projectID == uuid.Nil || agentID == uuid.Nil || runtimeID == uuid.Nil {
		return errors.New("project, agent, and runtime lock ids are required")
	}
	unit, h, err := beginExecution(ctx, s, projectID, agentID)
	if errors.Is(err, storeerr.ErrNotFound) {
		return storeerr.ErrRuntimeLockInactive
	}
	if err != nil {
		return err
	}
	defer func() { _ = unit.Rollback(ctx) }()
	if err := h.FenceRuntime(ctx, runtimeID); err != nil {
		return err
	}
	return unit.Commit(ctx, "runtime lock check")
}

const RuntimeToolInterruptedMessage = "Tool call was interrupted; its external outcome is unknown."

func (s *Store) RenewAgentRuntimeLock(
	ctx context.Context,
	projectID, agentID, runtimeLockID uuid.UUID,
	leaseDuration time.Duration,
) (AgentRuntimeLockRenewal, error) {
	unit, err := s.cell.Begin(ctx)
	if err != nil {
		return AgentRuntimeLockRenewal{}, err
	}
	defer func() { _ = unit.Rollback(ctx) }()
	renewal, err := unit.RenewRuntime(
		ctx,
		agentexecution.AgentRoute{CellID: unit.CellID(), ProjectID: projectID, AgentID: agentID},
		runtimeLockID,
		leaseDuration,
	)
	if err != nil {
		return AgentRuntimeLockRenewal{}, err
	}
	row, err := dbsqlc.New(unit.DB()).
		GetAgentRuntimeLockForRelease(ctx,
			dbsqlc.GetAgentRuntimeLockForReleaseParams{ProjectID: projectID,
				AgentID: agentID,
				ID:      runtimeLockID})
	if err != nil {
		return AgentRuntimeLockRenewal{}, err
	}
	if err = unit.Commit(ctx, "renew runtime"); err != nil {
		return AgentRuntimeLockRenewal{}, err
	}
	return AgentRuntimeLockRenewal{
		RuntimeLock:               agentRuntimeLockRecordFromSQLC(row),
		LocalLeaseBudgetStartedAt: renewal.LocalStartedAt,
	}, nil
}

func (s *Store) ReleaseAgentRuntimeLock(
	ctx context.Context,
	projectID, agentID, runtimeLockID uuid.UUID,
) error {
	unit, h, err := beginExecution(ctx, s, projectID, agentID)
	if errors.Is(err, storeerr.ErrNotFound) {
		return storeerr.ErrRuntimeLockInactive
	}
	if err != nil {
		return err
	}
	defer func() { _ = unit.Rollback(ctx) }()
	if err = h.Release(ctx, runtimeLockID); err != nil {
		return err
	}
	return unit.Commit(ctx, "release runtime")
}
