package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	AgentRuntimeLockLeaseDuration        = 90 * time.Second
	MinimumAgentRuntimeLockLeaseDuration = 15 * time.Second
	MaximumAgentRuntimeLockLeaseDuration = 30 * time.Minute
)

type AgentRuntimeLockRecord struct {
	ID                ID         `json:"id"`
	AgentID           ID         `json:"agent_id"`
	WorkerProcessID   ID         `json:"worker_process_id"`
	StartedAt         time.Time  `json:"started_at"`
	RenewedAt         time.Time  `json:"renewed_at"`
	LeaseExpiresAt    time.Time  `json:"lease_expires_at"`
	CancelRequestedAt *time.Time `json:"cancel_requested_at,omitempty"`
}

type AgentRuntimeLockRenewal struct {
	RuntimeLock               AgentRuntimeLockRecord
	LocalLeaseBudgetStartedAt time.Time
}

func acquireAgentRuntimeLockTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID, workerProcessID ID,
	leaseDuration time.Duration,
) (AgentRuntimeLockRecord, ID, error) {
	agent, err := qtx.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: projectID, ID: agentID},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AgentRuntimeLockRecord{}, NilID, storeerr.ErrAgentNotAdvanceable
		}
		return AgentRuntimeLockRecord{}, NilID, fmt.Errorf("lock agent for runtime acquisition: %w", err)
	}
	row, err := qtx.AcquireAgentRuntimeLock(
		ctx,
		dbsqlc.AcquireAgentRuntimeLockParams{
			ProjectID:                 projectID,
			AgentID:                   agentID,
			WorkerProcessID:           workerProcessID,
			LeaseDurationMicroseconds: leaseDuration.Microseconds(),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentRuntimeLockRecord{}, NilID, storeerr.ErrAgentNotAdvanceable
	}
	if err != nil {
		return AgentRuntimeLockRecord{}, NilID, fmt.Errorf("acquire agent runtime lock: %w", err)
	}
	return agentRuntimeLockRecordFromSQLC(row), agent.OrgID, nil
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
	ctx context.Context,
	projectID, agentID, runtimeID ID,
) error {
	if isNilID(projectID) || isNilID(agentID) || isNilID(runtimeID) {
		return errors.New("project, agent, and runtime lock ids are required")
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin runtime lock check: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := ensureRuntimeLockActiveTx(ctx, tx, projectID, agentID, runtimeID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit runtime lock check: %w", err)
	}
	return nil
}

func lockAgentRuntimeForOwnedMutationTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID, runtimeID ID,
) error {
	if _, err := qtx.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: projectID, ID: agentID},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrRuntimeLockInactive
		}
		return fmt.Errorf("lock agent for runtime-owned mutation: %w", err)
	}
	if _, err := qtx.LockAgentRuntimeLockForOwnedMutation(
		ctx,
		dbsqlc.LockAgentRuntimeLockForOwnedMutationParams{
			ProjectID: projectID,
			AgentID:   agentID,
			ID:        runtimeID,
		},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrRuntimeLockInactive
		}
		return fmt.Errorf("lock runtime for owned mutation: %w", err)
	}
	return nil
}

func (s *Store) beginAgentRuntimeOwnedMutation(
	ctx context.Context,
	projectID, agentID, runtimeID ID,
) (pgx.Tx, *dbsqlc.Queries, error) {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("begin runtime-owned mutation: %w", err)
	}
	qtx := dbsqlc.New(tx)
	if err := lockAgentRuntimeForOwnedMutationTx(ctx, qtx, projectID, agentID, runtimeID); err != nil {
		_ = tx.Rollback(ctx)
		return nil, nil, err
	}
	return tx, qtx, nil
}

func agentRuntimeLockActiveTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID, runtimeID ID,
) error {
	active, err := qtx.AgentRuntimeLockIsActive(
		ctx,
		dbsqlc.AgentRuntimeLockIsActiveParams{
			ProjectID: projectID,
			AgentID:   agentID,
			ID:        runtimeID,
		},
	)
	if err != nil {
		return fmt.Errorf("check agent runtime lock: %w", err)
	}
	if !active {
		return storeerr.ErrRuntimeLockInactive
	}
	return nil
}

func ensureRuntimeLockActiveTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID, runtimeID ID,
) error {
	if isNilID(projectID) || isNilID(agentID) || isNilID(runtimeID) {
		return errors.New("project, agent, and runtime lock ids are required")
	}
	qtx := dbsqlc.New(tx)
	if err := lockAgentRuntimeForOwnedMutationTx(ctx, qtx, projectID, agentID, runtimeID); err != nil {
		return err
	}
	return agentRuntimeLockActiveTx(ctx, qtx, projectID, agentID, runtimeID)
}

func failQueuedRuntimeWorkTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	projectID, agentID, runtimeID ID,
	reason string,
) error {
	if isNilID(runtimeID) {
		return nil
	}
	params := dbsqlc.FailQueuedProcessesForRuntimeEndParams{
		ProjectID:       projectID,
		AgentID:         agentID,
		RuntimeLockID:   runtimeID,
		StateReasonCode: sqlcTextFromEmpty(reason),
	}
	processRows, err := qtx.FailQueuedProcessesForRuntimeEnd(ctx, params)
	if err != nil {
		return fmt.Errorf("fail queued processes for ended runtime: %w", err)
	}
	for _, row := range processRows {
		record := processRecordFromSQLC(row)
		if err := completeProcessToolCallFromRecordTx(
			ctx,
			txNotifications,
			tx,
			qtx,
			record,
			nil,
			reason,
		); err != nil {
			return err
		}
	}
	actionRows, err := qtx.FailQueuedProcessActionsForRuntimeEnd(
		ctx,
		dbsqlc.FailQueuedProcessActionsForRuntimeEndParams(params),
	)
	if err != nil {
		return fmt.Errorf("fail queued process actions for ended runtime: %w", err)
	}
	for _, row := range actionRows {
		record := processActionRecordFromSQLC(row)
		if err := completeTerminalProcessActionToolCallTx(
			ctx,
			txNotifications,
			tx,
			qtx,
			record,
			ProcessActionStateFailed,
			reason,
		); err != nil {
			return err
		}
	}
	return nil
}

const RuntimeToolInterruptedMessage = "Tool call was interrupted; its external outcome is unknown."

func failRuntimeToolCallsTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	projectID, agentID, runtimeID ID,
	reason string,
) error {
	result, err := marshalJSON(map[string]any{
		"code":    reason,
		"message": RuntimeToolInterruptedMessage,
	})
	if err != nil {
		return fmt.Errorf("marshal interrupted runtime tool result: %w", err)
	}
	contentParts, err := ToolResultContentParts(result)
	if err != nil {
		return err
	}
	qtx := dbsqlc.New(tx)
	rows, err := qtx.FailRuntimeToolCalls(
		ctx,
		dbsqlc.FailRuntimeToolCallsParams{
			ProjectID:     projectID,
			AgentID:       agentID,
			RuntimeLockID: runtimeID,
		},
	)
	if err != nil {
		return fmt.Errorf("fail interrupted runtime tool calls: %w", err)
	}
	for _, row := range rows {
		record := toolCallRecordFromFailRuntimeSQLC(row)
		record.ResultContentParts = contentParts
		if _, err := appendToolResultEventTx(ctx, txNotifications, tx, record); err != nil {
			return err
		}
	}
	if len(rows) == 0 {
		return nil
	}
	metadata, err := marshalJSON(map[string]any{
		"reason":          reason,
		"runtime_lock_id": runtimeID,
	})
	if err != nil {
		return err
	}
	if err := qtx.MarkAgentWakeup(
		ctx,
		dbsqlc.MarkAgentWakeupParams{
			ProjectID: projectID,
			AgentID:   agentID,
			Metadata:  metadata,
		},
	); err != nil {
		return fmt.Errorf("mark interrupted runtime tool wakeup: %w", err)
	}
	return nil
}

func (s *Store) RenewAgentRuntimeLock(
	ctx context.Context,
	projectID, agentID, runtimeLockID ID,
	leaseDuration time.Duration,
) (AgentRuntimeLockRenewal, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(runtimeLockID) {
		return AgentRuntimeLockRenewal{}, errors.New("project, agent, and runtime lock ids are required")
	}
	if err := validateAgentRuntimeLockLeaseDuration(leaseDuration); err != nil {
		return AgentRuntimeLockRenewal{}, err
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AgentRuntimeLockRenewal{}, fmt.Errorf("begin renew agent runtime lock: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	record, err := renewAgentRuntimeLockTx(
		ctx,
		dbsqlc.New(tx),
		projectID,
		agentID,
		runtimeLockID,
		leaseDuration,
	)
	if err != nil {
		return AgentRuntimeLockRenewal{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AgentRuntimeLockRenewal{}, fmt.Errorf("commit renew agent runtime lock: %w", err)
	}
	return record, nil
}

func renewAgentRuntimeLockTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID, runtimeLockID ID,
	leaseDuration time.Duration,
) (AgentRuntimeLockRenewal, error) {
	if _, err := qtx.LockAgentRuntimeLockForRenewal(
		ctx,
		dbsqlc.LockAgentRuntimeLockForRenewalParams{
			ProjectID: projectID,
			AgentID:   agentID,
			ID:        runtimeLockID,
		},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AgentRuntimeLockRenewal{}, storeerr.ErrRuntimeLockInactive
		}
		return AgentRuntimeLockRenewal{}, fmt.Errorf("lock agent runtime renewal: %w", err)
	}
	localLeaseBudgetStartedAt := time.Now() //nolint:omnaralint // Local monotonic scheduling budget; never persisted.
	row, err := qtx.RenewAgentRuntimeLock(
		ctx,
		dbsqlc.RenewAgentRuntimeLockParams{
			ProjectID:                 projectID,
			AgentID:                   agentID,
			ID:                        runtimeLockID,
			LeaseDurationMicroseconds: leaseDuration.Microseconds(),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentRuntimeLockRenewal{}, storeerr.ErrRuntimeLockInactive
	}
	if err != nil {
		return AgentRuntimeLockRenewal{}, fmt.Errorf("renew agent runtime lock: %w", err)
	}
	return AgentRuntimeLockRenewal{
		RuntimeLock:               agentRuntimeLockRecordFromSQLC(row),
		LocalLeaseBudgetStartedAt: localLeaseBudgetStartedAt,
	}, nil
}

func (s *Store) ReleaseAgentRuntimeLock(
	ctx context.Context,
	projectID, agentID, runtimeLockID ID,
) error {
	txNotifications := s.newTxNotifications()
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin release agent runtime lock: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	lockParams := dbsqlc.LockAgentInProjectParams{
		ProjectID: projectID,
		ID:        agentID,
	}
	if _, err := qtx.LockAgentInProject(ctx, lockParams); err != nil {
		return fmt.Errorf("lock agent for runtime lock release: %w", err)
	}
	releaseParams := dbsqlc.GetAgentRuntimeLockForReleaseParams{
		ProjectID: projectID,
		AgentID:   agentID,
		ID:        runtimeLockID,
	}
	if _, err := qtx.GetAgentRuntimeLockForRelease(ctx, releaseParams); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrRuntimeLockInactive
		}
		return fmt.Errorf("get agent runtime lock for release: %w", err)
	}
	if err := recoverRuntimeModelCallContextTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		projectID,
		agentID,
		runtimeLockID,
		runtimeModelCallRecoveryEvidence{
			Code:    "runtime_released_before_model_result_acceptance",
			Message: "runtime released before the model result was durably accepted",
		},
		s.modelCallRetryDelay,
	); err != nil {
		return err
	}
	if err := failRuntimeToolCallsTx(
		ctx,
		txNotifications,
		tx,
		projectID,
		agentID,
		runtimeLockID,
		"runtime_lock_released",
	); err != nil {
		return err
	}
	changed, err := qtx.ReleaseAgentRuntimeLock(
		ctx,
		dbsqlc.ReleaseAgentRuntimeLockParams{
			ProjectID: projectID,
			AgentID:   agentID,
			ID:        runtimeLockID,
		},
	)
	if err != nil {
		return fmt.Errorf("release agent runtime lock: %w", err)
	}
	if changed != 1 {
		return storeerr.ErrRuntimeLockInactive
	}
	if err := qtx.ReconcileAgentWakeup(ctx, dbsqlc.ReconcileAgentWakeupParams{
		ProjectID: projectID,
		AgentID:   agentID,
		Metadata:  json.RawMessage(`{"reason":"runtime_release"}`),
	}); err != nil {
		return fmt.Errorf("reconcile agent wakeup after runtime release: %w", err)
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "release agent runtime lock"); err != nil {
		return err
	}
	return nil
}
