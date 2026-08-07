package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/daemonversion"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	DaemonRuntimeLeaseDuration   = 2 * time.Minute
	MinDaemonRuntimeLeaseTimeout = 3 * time.Second
)

const (
	daemonRuntimeReplacedReason                   = "daemon_runtime_replaced"
	DaemonRuntimeReleasedReason                   = "daemon_runtime_released"
	daemonRuntimeLeaseExpiredReason               = "daemon_lease_expired"
	LocalProcessMissingAfterDaemonReconnectReason = "local_process_missing_after_daemon_reconnect"
	processExecutionNotStartedReason              = "execution_not_started"
	processActionNotDeliveredReason               = "process_action_not_delivered"
	ProcessActionOutcomeUnrecoverableReason       = "local_action_outcome_unrecoverable"
	daemonRuntimeAsleepReason                     = "machine_asleep"
)

func (s *Store) RegisterDaemonRuntimeWithReconciliation(
	ctx context.Context,
	input RegisterDaemonRuntimeInput,
) (DaemonRuntimeRegistrationRecord, error) {
	if isNilID(input.OrgID) || isNilID(input.MachineID) || isNilID(input.DaemonTokenID) {
		return DaemonRuntimeRegistrationRecord{}, errors.New("org, machine, and daemon token are required")
	}
	if isNilID(input.DaemonInstanceID) {
		return DaemonRuntimeRegistrationRecord{}, errors.New("daemon instance id is required")
	}
	if err := daemonversion.Validate(input.DaemonVersion); err != nil {
		return DaemonRuntimeRegistrationRecord{}, fmt.Errorf("daemon version: %w", err)
	}
	if input.LeaseTimeout <= 0 {
		input.LeaseTimeout = DaemonRuntimeLeaseDuration
	}
	if input.LeaseTimeout < MinDaemonRuntimeLeaseTimeout {
		return DaemonRuntimeRegistrationRecord{}, errors.New("daemon runtime lease timeout is too short")
	}
	input.Capacity = normalizedJSON(input.Capacity)
	input.ObservedPlatform = normalizedJSON(input.ObservedPlatform)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return DaemonRuntimeRegistrationRecord{}, fmt.Errorf("begin register daemon runtime: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	txNotifications := s.newTxNotifications()
	qtx := dbsqlc.New(tx)
	machine, err := qtx.LockMachineForRuntimeRegistration(
		ctx,
		dbsqlc.LockMachineForRuntimeRegistrationParams{
			OrgID: input.OrgID,
			ID:    input.MachineID,
		},
	)
	if err != nil {
		return DaemonRuntimeRegistrationRecord{}, fmt.Errorf("lock machine for runtime registration: %w", err)
	}
	instanceRow, err := qtx.GetDaemonRuntimeInstanceForUpdate(
		ctx,
		dbsqlc.GetDaemonRuntimeInstanceForUpdateParams{
			OrgID:            input.OrgID,
			MachineID:        input.MachineID,
			DaemonInstanceID: input.DaemonInstanceID,
		},
	)
	instanceFound := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return DaemonRuntimeRegistrationRecord{}, fmt.Errorf("get daemon runtime instance: %w", err)
	}
	if instanceFound && (idFromSQLCPtr(machine.CurrentDaemonRuntimeID) != instanceRow.ID ||
		instanceRow.DaemonVersion != input.DaemonVersion) {
		return DaemonRuntimeRegistrationRecord{}, storeerr.ErrDaemonInstanceSuperseded
	}
	if err := qtx.ClearMachineSleep(
		ctx,
		dbsqlc.ClearMachineSleepParams{OrgID: input.OrgID, ID: input.MachineID},
	); err != nil {
		return DaemonRuntimeRegistrationRecord{}, fmt.Errorf("clear machine sleep on registration: %w", err)
	}
	priorRows, err := qtx.ListActiveDaemonRuntimesForUpdate(
		ctx,
		dbsqlc.ListActiveDaemonRuntimesForUpdateParams{OrgID: input.OrgID, MachineID: input.MachineID},
	)
	if err != nil {
		return DaemonRuntimeRegistrationRecord{}, fmt.Errorf("list registered daemon runtimes: %w", err)
	}
	var runtimeID ID
	var replacedRows []dbsqlc.ListActiveDaemonRuntimesForUpdateRow
	if instanceFound {
		if DaemonRuntimeState(instanceRow.State) != DaemonRuntimeStateActive {
			if (instanceRow.StateReasonCode != daemonRuntimeLeaseExpiredReason &&
				instanceRow.StateReasonCode != daemonRuntimeAsleepReason) || len(priorRows) != 0 {
				return DaemonRuntimeRegistrationRecord{}, storeerr.ErrDaemonInstanceSuperseded
			}
		}
		runtimeID, err = qtx.RefreshDaemonRuntimeRegistration(
			ctx,
			dbsqlc.RefreshDaemonRuntimeRegistrationParams{
				OrgID:                    input.OrgID,
				MachineID:                input.MachineID,
				DaemonTokenID:            input.DaemonTokenID,
				DaemonInstanceID:         input.DaemonInstanceID,
				Capacity:                 input.Capacity,
				Metadata:                 json.RawMessage(`{}`),
				LeaseTimeoutMilliseconds: input.LeaseTimeout.Milliseconds(),
			},
		)
		if err != nil {
			return DaemonRuntimeRegistrationRecord{}, fmt.Errorf("refresh daemon runtime registration: %w", err)
		}
	} else {
		replacedRows = priorRows
		for _, prior := range replacedRows {
			if _, err := qtx.ForceEndDaemonRuntime(
				ctx,
				dbsqlc.ForceEndDaemonRuntimeParams{
					OrgID:     prior.OrgID,
					MachineID: prior.MachineID,
					ID:        prior.ID,
					Reason: sqlcTextFromEmpty(
						daemonRuntimeReplacedReason,
					),
					Message: "",
				},
			); err != nil {
				return DaemonRuntimeRegistrationRecord{}, fmt.Errorf("end replaced daemon runtime: %w", err)
			}
			txNotifications.AddDaemonRuntimeEnded(
				prior.ID,
				prior.MachineID,
				notifications.DaemonRuntimeEndReconnect,
			)
		}
		runtimeID, err = qtx.InsertDaemonRuntime(
			ctx,
			dbsqlc.InsertDaemonRuntimeParams{
				OrgID:                    input.OrgID,
				MachineID:                input.MachineID,
				DaemonTokenID:            input.DaemonTokenID,
				DaemonInstanceID:         input.DaemonInstanceID,
				DaemonVersion:            input.DaemonVersion,
				Capacity:                 input.Capacity,
				Metadata:                 json.RawMessage(`{}`),
				LeaseTimeoutMilliseconds: input.LeaseTimeout.Milliseconds(),
			},
		)
		if err != nil {
			return DaemonRuntimeRegistrationRecord{}, fmt.Errorf("insert daemon runtime: %w", err)
		}
	}
	if _, err := qtx.SetMachineCurrentDaemonRuntime(
		ctx,
		dbsqlc.SetMachineCurrentDaemonRuntimeParams{
			OrgID:           input.OrgID,
			MachineID:       input.MachineID,
			DaemonRuntimeID: runtimeID,
		},
	); err != nil {
		return DaemonRuntimeRegistrationRecord{}, fmt.Errorf("set current daemon runtime: %w", err)
	}
	if err := qtx.RevokeSiblingSystemMachineDaemonTokens(
		ctx,
		dbsqlc.RevokeSiblingSystemMachineDaemonTokensParams{
			OrgID:         input.OrgID,
			MachineID:     input.MachineID,
			ActiveTokenID: input.DaemonTokenID,
		},
	); err != nil {
		return DaemonRuntimeRegistrationRecord{}, fmt.Errorf("revoke sibling system bootstrap tokens: %w", err)
	}
	if _, err := qtx.ClearMachineUpdateFailureReport(
		ctx,
		dbsqlc.ClearMachineUpdateFailureReportParams{
			OrgID:         input.OrgID,
			MachineID:     input.MachineID,
			DaemonVersion: input.DaemonVersion,
		},
	); err != nil {
		return DaemonRuntimeRegistrationRecord{}, fmt.Errorf("clear stale daemon update failure report: %w", err)
	}
	reconciliation, err := reconcileRegisteredRuntimeTx(ctx, txNotifications, tx, qtx, input)
	if err != nil {
		return DaemonRuntimeRegistrationRecord{}, err
	}
	if err := qtx.UpdateMachineObservation(
		ctx,
		dbsqlc.UpdateMachineObservationParams{
			OrgID:            input.OrgID,
			ID:               input.MachineID,
			ObservedPlatform: input.ObservedPlatform,
		},
	); err != nil {
		return DaemonRuntimeRegistrationRecord{}, fmt.Errorf("update machine observation: %w", err)
	}
	finalRow, err := qtx.HeartbeatDaemonRuntime(
		ctx,
		dbsqlc.HeartbeatDaemonRuntimeParams{
			ID:                       runtimeID,
			OrgID:                    input.OrgID,
			MachineID:                input.MachineID,
			DaemonTokenID:            input.DaemonTokenID,
			DaemonInstanceID:         input.DaemonInstanceID,
			Capacity:                 input.Capacity,
			Metadata:                 json.RawMessage(`{}`),
			LeaseTimeoutMilliseconds: input.LeaseTimeout.Milliseconds(),
		},
	)
	if err != nil {
		return DaemonRuntimeRegistrationRecord{}, fmt.Errorf("finalize daemon runtime registration lease: %w", err)
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "register daemon runtime"); err != nil {
		return DaemonRuntimeRegistrationRecord{}, err
	}
	return DaemonRuntimeRegistrationRecord{
		Runtime:        daemonRuntimeFromHeartbeat(finalRow),
		Reconciliation: reconciliation,
	}, nil
}

func (s *Store) HeartbeatDaemonRuntime(
	ctx context.Context,
	input DaemonRuntimeLeaseInput,
) (DaemonRuntimeRecord, error) {
	if err := validateDaemonRuntimeAuthority(input.Authority); err != nil {
		return DaemonRuntimeRecord{}, err
	}
	if isNilID(input.DaemonInstanceID) {
		return DaemonRuntimeRecord{}, errors.New("daemon instance id is required")
	}
	if input.LeaseTimeout <= 0 {
		input.LeaseTimeout = DaemonRuntimeLeaseDuration
	}
	if input.LeaseTimeout < MinDaemonRuntimeLeaseTimeout {
		return DaemonRuntimeRecord{}, errors.New("daemon runtime lease timeout is too short")
	}
	input.ObservedPlatform = normalizedJSON(input.ObservedPlatform)
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return DaemonRuntimeRecord{}, fmt.Errorf("begin heartbeat daemon runtime: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if _, err := qtx.LockMachineForRuntimeRegistration(
		ctx,
		dbsqlc.LockMachineForRuntimeRegistrationParams{
			OrgID: input.Authority.OrgID,
			ID:    input.Authority.MachineID,
		},
	); err != nil {
		return DaemonRuntimeRecord{}, fmt.Errorf("lock machine for daemon runtime heartbeat: %w", err)
	}
	row, err := qtx.HeartbeatDaemonRuntime(
		ctx,
		dbsqlc.HeartbeatDaemonRuntimeParams{
			ID:                       input.Authority.DaemonRuntimeID,
			OrgID:                    input.Authority.OrgID,
			MachineID:                input.Authority.MachineID,
			DaemonTokenID:            input.Authority.DaemonTokenID,
			DaemonInstanceID:         input.DaemonInstanceID,
			Capacity:                 normalizedJSON(input.Capacity),
			Metadata:                 json.RawMessage(`{}`),
			LeaseTimeoutMilliseconds: input.LeaseTimeout.Milliseconds(),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DaemonRuntimeRecord{}, storeerr.ErrDaemonRuntimeUnregistered
	}
	if err != nil {
		return DaemonRuntimeRecord{}, fmt.Errorf("heartbeat daemon runtime: %w", err)
	}
	if err := qtx.UpdateMachineObservation(
		ctx,
		dbsqlc.UpdateMachineObservationParams{
			OrgID:            input.Authority.OrgID,
			ID:               input.Authority.MachineID,
			ObservedPlatform: input.ObservedPlatform,
		},
	); err != nil {
		return DaemonRuntimeRecord{}, fmt.Errorf("update machine heartbeat observation: %w", err)
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "heartbeat daemon runtime"); err != nil {
		return DaemonRuntimeRecord{}, err
	}
	return daemonRuntimeFromHeartbeat(row), nil
}

func (s *Store) EndDaemonRuntime(
	ctx context.Context,
	authority DaemonRuntimeAuthority,
) (DaemonRuntimeRecord, error) {
	if err := validateDaemonRuntimeAuthority(authority); err != nil {
		return DaemonRuntimeRecord{}, err
	}
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return DaemonRuntimeRecord{}, fmt.Errorf("begin end daemon runtime: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if _, err := qtx.LockMachineForRuntimeRegistration(
		ctx,
		dbsqlc.LockMachineForRuntimeRegistrationParams{
			OrgID: authority.OrgID,
			ID:    authority.MachineID,
		},
	); err != nil {
		return DaemonRuntimeRecord{}, fmt.Errorf("lock machine for daemon runtime end: %w", err)
	}
	row, err := qtx.EndDaemonRuntime(
		ctx,
		dbsqlc.EndDaemonRuntimeParams{
			ID:            authority.DaemonRuntimeID,
			OrgID:         authority.OrgID,
			MachineID:     authority.MachineID,
			DaemonTokenID: authority.DaemonTokenID,
			Reason:        sqlcTextFromEmpty(DaemonRuntimeReleasedReason),
			Message:       "",
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DaemonRuntimeRecord{}, storeerr.ErrDaemonRuntimeUnregistered
	}
	if err != nil {
		return DaemonRuntimeRecord{}, fmt.Errorf("end daemon runtime: %w", err)
	}
	txNotifications.AddDaemonRuntimeEnded(
		authority.DaemonRuntimeID,
		authority.MachineID,
		notifications.DaemonRuntimeEndReconnect,
	)
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "end daemon runtime"); err != nil {
		return DaemonRuntimeRecord{}, err
	}
	return daemonRuntimeFromEnd(row), nil
}

func (s *Store) SleepDaemonRuntime(
	ctx context.Context,
	authority DaemonRuntimeAuthority,
) (DaemonRuntimeRecord, error) {
	if err := validateDaemonRuntimeAuthority(authority); err != nil {
		return DaemonRuntimeRecord{}, err
	}
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return DaemonRuntimeRecord{}, fmt.Errorf("begin sleep daemon runtime: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if _, err := qtx.LockMachineForRuntimeRegistration(
		ctx,
		dbsqlc.LockMachineForRuntimeRegistrationParams{
			OrgID: authority.OrgID,
			ID:    authority.MachineID,
		},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DaemonRuntimeRecord{}, storeerr.ErrDaemonRuntimeUnregistered
		}
		return DaemonRuntimeRecord{}, fmt.Errorf("lock machine for daemon runtime sleep: %w", err)
	}
	unfinished, err := qtx.MachineHasUnfinishedDaemonWork(
		ctx,
		dbsqlc.MachineHasUnfinishedDaemonWorkParams{
			OrgID:     authority.OrgID,
			MachineID: authority.MachineID,
		},
	)
	if err != nil {
		return DaemonRuntimeRecord{}, fmt.Errorf("check unfinished daemon work for sleep: %w", err)
	}
	if unfinished != nil && *unfinished {
		return DaemonRuntimeRecord{}, storeerr.ErrMachineSleepPendingWork
	}
	if _, err := qtx.MarkMachineAsleep(
		ctx,
		dbsqlc.MarkMachineAsleepParams{OrgID: authority.OrgID, ID: authority.MachineID},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DaemonRuntimeRecord{}, storeerr.ErrMachineNotWakeCapable
		}
		return DaemonRuntimeRecord{}, fmt.Errorf("mark machine asleep: %w", err)
	}
	row, err := qtx.EndDaemonRuntime(
		ctx,
		dbsqlc.EndDaemonRuntimeParams{
			ID:            authority.DaemonRuntimeID,
			OrgID:         authority.OrgID,
			MachineID:     authority.MachineID,
			DaemonTokenID: authority.DaemonTokenID,
			Reason:        sqlcTextFromEmpty(daemonRuntimeAsleepReason),
			Message:       "",
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DaemonRuntimeRecord{}, storeerr.ErrDaemonRuntimeUnregistered
	}
	if err != nil {
		return DaemonRuntimeRecord{}, fmt.Errorf("end daemon runtime for sleep: %w", err)
	}
	txNotifications.AddDaemonRuntimeEnded(
		authority.DaemonRuntimeID,
		authority.MachineID,
		notifications.DaemonRuntimeEndReconnect,
	)
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "sleep daemon runtime"); err != nil {
		return DaemonRuntimeRecord{}, err
	}
	return daemonRuntimeFromEnd(row), nil
}

func (s *Store) RegisteredDaemonRuntimeExists(ctx context.Context, authority DaemonRuntimeAuthority) (bool, error) {
	_, exists, err := s.RegisteredDaemonRuntimeVersion(ctx, authority)
	return exists, err
}

func (s *Store) RegisteredDaemonRuntimeVersion(
	ctx context.Context,
	authority DaemonRuntimeAuthority,
) (string, bool, error) {
	if err := validateDaemonRuntimeAuthority(authority); err != nil {
		return "", false, err
	}
	version, err := s.q.RegisteredDaemonRuntimeVersion(
		ctx,
		dbsqlc.RegisteredDaemonRuntimeVersionParams{
			OrgID:         authority.OrgID,
			MachineID:     authority.MachineID,
			ID:            authority.DaemonRuntimeID,
			DaemonTokenID: authority.DaemonTokenID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get registered daemon runtime version: %w", err)
	}
	return version, true, nil
}

func (s *Store) OnlineDaemonRuntimeExists(ctx context.Context, authority DaemonRuntimeAuthority) (bool, error) {
	if err := validateDaemonRuntimeAuthority(authority); err != nil {
		return false, err
	}
	exists, err := s.q.OnlineDaemonRuntimeExists(
		ctx,
		dbsqlc.OnlineDaemonRuntimeExistsParams{
			OrgID:         authority.OrgID,
			MachineID:     authority.MachineID,
			ID:            authority.DaemonRuntimeID,
			DaemonTokenID: authority.DaemonTokenID,
		},
	)
	if err != nil {
		return false, fmt.Errorf("check online daemon runtime: %w", err)
	}
	return exists, nil
}

func requireReportableDaemonRuntimeAuthorityTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	authority DaemonRuntimeAuthority,
) error {
	if err := validateDaemonRuntimeAuthority(authority); err != nil {
		return err
	}
	if _, err := qtx.LockMachineForRuntimeRegistration(
		ctx,
		dbsqlc.LockMachineForRuntimeRegistrationParams{
			OrgID: authority.OrgID,
			ID:    authority.MachineID,
		},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrDaemonRuntimeUnregistered
		}
		return fmt.Errorf("lock machine for daemon runtime authority: %w", err)
	}
	return checkReportableDaemonRuntimeAuthority(ctx, qtx, authority)
}

func checkReportableDaemonRuntimeAuthority(
	ctx context.Context,
	queries *dbsqlc.Queries,
	authority DaemonRuntimeAuthority,
) error {
	if err := validateDaemonRuntimeAuthority(authority); err != nil {
		return err
	}
	exists, err := queries.ReportableDaemonRuntimeExists(
		ctx,
		dbsqlc.ReportableDaemonRuntimeExistsParams{
			OrgID:         authority.OrgID,
			MachineID:     authority.MachineID,
			ID:            authority.DaemonRuntimeID,
			DaemonTokenID: authority.DaemonTokenID,
		},
	)
	if err != nil {
		return fmt.Errorf("verify daemon report authority: %w", err)
	}
	if !exists {
		return storeerr.ErrDaemonRuntimeUnregistered
	}
	return nil
}
