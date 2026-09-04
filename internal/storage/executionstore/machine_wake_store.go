package executionstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type MachineWakeDisposition uint8

const (
	MachineWakeUnavailable MachineWakeDisposition = iota
	MachineWakeReady
	MachineWakePending
	MachineWakeOnline
	MachineWakeUnresolved
)

func (s *Store) BeginMachineWake(
	ctx context.Context,
	orgID, machineID, machinePoolID ID,
	wakeTimeout time.Duration,
) (MachineWakeDisposition, error) {
	if orgID == NilID || machineID == NilID || machinePoolID == NilID {
		return MachineWakeUnavailable, errors.New("org, machine, and machine pool are required")
	}
	if wakeTimeout < time.Millisecond {
		return MachineWakeUnavailable, errors.New("machine wake timeout must be at least one millisecond")
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return MachineWakeUnavailable, fmt.Errorf("begin machine wake: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	if err := lifecyclelock.EnterActiveOrganization(ctx, tx, orgID); errors.Is(err, storeerr.ErrNotFound) {
		return MachineWakeUnavailable, nil
	} else if err != nil {
		return MachineWakeUnavailable, err
	}
	if err := lifecyclelock.Pools(
		ctx,
		tx,
		[]lifecyclelock.PoolRef{{OrgID: orgID, PoolID: machinePoolID}},
	); errors.Is(err, storeerr.ErrNotFound) {
		return MachineWakeUnavailable, nil
	} else if err != nil {
		return MachineWakeUnavailable, fmt.Errorf("lock machine pool for wake: %w", err)
	}
	if err := lifecyclelock.Machines(
		ctx,
		tx,
		[]lifecyclelock.MachineRef{{OrgID: orgID, MachineID: machineID}},
	); errors.Is(err, storeerr.ErrNotFound) {
		return MachineWakeUnavailable, nil
	} else if err != nil {
		return MachineWakeUnavailable, fmt.Errorf("lock machine for wake: %w", err)
	}

	state, err := qtx.GetMachineWakeState(ctx, dbsqlc.GetMachineWakeStateParams{
		OrgID:         orgID,
		MachineID:     machineID,
		MachinePoolID: machinePoolID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return MachineWakeUnavailable, nil
	}
	if err != nil {
		return MachineWakeUnavailable, fmt.Errorf("get machine wake state: %w", err)
	}
	if state.Online {
		return MachineWakeOnline, nil
	}
	if !state.Asleep {
		return MachineWakeUnavailable, nil
	}
	if state.WakeAttemptExpiresAt != nil {
		if state.WakeAttemptActive {
			return MachineWakePending, nil
		}
		if state.RuntimeProtectionEnabled {
			return MachineWakeUnresolved, nil
		}
	}

	if _, err := qtx.ClaimMachineWakeRequest(
		ctx,
		dbsqlc.ClaimMachineWakeRequestParams{
			WakeTimeoutMilliseconds: wakeTimeout.Milliseconds(),
			OrgID:                   orgID,
			MachineID:               machineID,
			MachinePoolID:           machinePoolID,
		},
	); errors.Is(err, pgx.ErrNoRows) {
		return MachineWakeUnavailable, nil
	} else if err != nil {
		return MachineWakeUnavailable, fmt.Errorf("start machine wake request: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return MachineWakeUnavailable, fmt.Errorf("commit machine wake request: %w", err)
	}
	return MachineWakeReady, nil
}
