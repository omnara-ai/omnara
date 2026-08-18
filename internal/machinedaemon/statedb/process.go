package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb/internal/dbsqlc"
)

func (s *Store) ReserveProcess(
	ctx context.Context,
	process Process,
) error {
	if process.ProcessID == "" || process.SupervisorInstanceID == "" ||
		process.SupervisorToken == "" {
		return errors.New("process, supervisor instance ID, and supervisor token are required")
	}

	tx, err := beginWrite(ctx, s.db)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.q.WithTx(tx)

	identity, err := qtx.GetProcessSupervisorIdentity(
		ctx,
		dbsqlc.GetProcessSupervisorIdentityParams{ProcessID: process.ProcessID},
	)
	switch {
	case err == nil:
		if identity.SupervisorInstanceID == process.SupervisorInstanceID &&
			identity.SupervisorToken == process.SupervisorToken {
			return commitWrite(tx)
		}
		return fmt.Errorf("%w: %s", ErrProcessExists, process.ProcessID)
	case !errors.Is(err, sql.ErrNoRows):
		return dbError("inspect existing process state", err)
	}

	if err := qtx.InsertProcess(
		ctx,
		dbsqlc.InsertProcessParams{
			ProcessID:            process.ProcessID,
			SupervisorInstanceID: process.SupervisorInstanceID,
			SupervisorToken:      process.SupervisorToken,
		},
	); err != nil {
		return dbError("reserve process state", err)
	}
	return commitWrite(tx)
}

func (s *Store) MarkPrepared(
	ctx context.Context,
	processID, supervisorInstanceID string,
) error {
	return s.updateProcess(
		ctx,
		"mark process prepared",
		processID,
		supervisorInstanceID,
		func(q *dbsqlc.Queries) (int64, error) {
			return q.MarkProcessPrepared(
				ctx,
				dbsqlc.MarkProcessPreparedParams{
					ProcessID:            processID,
					SupervisorInstanceID: supervisorInstanceID,
				},
			)
		},
	)
}

func (s *Store) MarkAccepted(
	ctx context.Context,
	processID, supervisorInstanceID string,
) error {
	return s.updateProcess(
		ctx,
		"mark process accepted",
		processID,
		supervisorInstanceID,
		func(q *dbsqlc.Queries) (int64, error) {
			return q.MarkProcessAccepted(
				ctx,
				dbsqlc.MarkProcessAcceptedParams{
					ProcessID:            processID,
					SupervisorInstanceID: supervisorInstanceID,
				},
			)
		},
	)
}

func (s *Store) DeleteRejectedPreparationAfterArtifacts(
	ctx context.Context,
	processID, supervisorInstanceID string,
) error {
	return s.deleteProcessAfterArtifacts(ctx, processID, supervisorInstanceID, false)
}

func (s *Store) DeleteStorageExhaustedAfterArtifacts(
	ctx context.Context,
	processID, supervisorInstanceID string,
) error {
	return s.deleteProcessAfterArtifacts(ctx, processID, supervisorInstanceID, true)
}

func (s *Store) deleteProcessAfterArtifacts(
	ctx context.Context,
	processID, supervisorInstanceID string,
	accepted bool,
) error {
	tx, err := beginWrite(ctx, s.db)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.q.WithTx(tx)

	process, found, err := processTx(ctx, qtx, processID)
	if err != nil {
		return err
	}
	if !found {
		return commitWrite(tx)
	}
	if process.SupervisorInstanceID != supervisorInstanceID {
		return fmt.Errorf(
			"%w: %s",
			ErrSupervisorIdentityMismatch,
			processID,
		)
	}
	validPhase := process.Phase == ProcessPreparing || process.Phase == ProcessPrepared
	if accepted {
		validPhase = process.Phase == ProcessAccepted || process.Phase == ProcessTerminal
	}
	if !validPhase {
		return fmt.Errorf(
			"%w: process %s cannot be deleted in %s",
			ErrStateConflict,
			processID,
			process.Phase,
		)
	}
	if !accepted && process.ExecCommitted {
		return fmt.Errorf(
			"%w: ungranted process %s crossed the execution boundary",
			ErrStateConflict,
			processID,
		)
	}
	if err := qtx.DeleteProcess(
		ctx,
		dbsqlc.DeleteProcessParams{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
		},
	); err != nil {
		return dbError("delete process state", err)
	}
	return commitWrite(tx)
}

func (s *Supervisor) AuthorizeSpawnOnce(ctx context.Context) (bool, error) {
	tx, err := beginWrite(ctx, s.store.db)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.store.q.WithTx(tx)

	count, err := qtx.AuthorizeProcessSpawn(
		ctx,
		dbsqlc.AuthorizeProcessSpawnParams{
			ProcessID:            s.processID,
			SupervisorInstanceID: s.supervisorInstanceID,
		},
	)
	if err != nil {
		return false, dbError("commit process execution boundary", err)
	}
	if count == 1 {
		return true, commitWrite(tx)
	}

	process, found, err := processTx(ctx, qtx, s.processID)
	if err != nil {
		return false, err
	}
	if !found || process.SupervisorInstanceID != s.supervisorInstanceID {
		return false, fmt.Errorf("%w: %s", ErrSupervisorIdentityMismatch, s.processID)
	}
	if process.ExecCommitted &&
		(process.Phase == ProcessAccepted || process.Phase == ProcessTerminal) {
		return false, commitWrite(tx)
	}
	return false, fmt.Errorf(
		"%w: process %s cannot cross the execution boundary in %s",
		ErrStateConflict,
		s.processID,
		process.Phase,
	)
}

func (s *Supervisor) RecordSpawned(
	ctx context.Context,
	containmentKind, containmentID string,
) error {
	if containmentKind == "" || containmentID == "" {
		return errors.New("spawned process containment identity is required")
	}
	return s.store.updateProcess(
		ctx,
		"record spawned process",
		s.processID,
		s.supervisorInstanceID,
		func(q *dbsqlc.Queries) (int64, error) {
			return q.RecordProcessSpawned(
				ctx,
				dbsqlc.RecordProcessSpawnedParams{
					ContainmentKind:      containmentKind,
					ContainmentID:        containmentID,
					ProcessID:            s.processID,
					SupervisorInstanceID: s.supervisorInstanceID,
				},
			)
		},
	)
}

func (s *Supervisor) CloseActionAdmission(ctx context.Context) error {
	return s.store.closeActionAdmission(ctx, s.processID, s.supervisorInstanceID)
}

func (s *Store) CloseActionAdmission(
	ctx context.Context,
	processID, supervisorInstanceID string,
) error {
	return s.closeActionAdmission(ctx, processID, supervisorInstanceID)
}

func (s *Store) closeActionAdmission(
	ctx context.Context,
	processID, supervisorInstanceID string,
) error {
	return s.updateProcess(
		ctx,
		"close process action admission",
		processID,
		supervisorInstanceID,
		func(q *dbsqlc.Queries) (int64, error) {
			return q.CloseProcessActionAdmission(
				ctx,
				dbsqlc.CloseProcessActionAdmissionParams{
					ProcessID:            processID,
					SupervisorInstanceID: supervisorInstanceID,
				},
			)
		},
	)
}

func (s *Supervisor) MarkContainmentEmpty(ctx context.Context) error {
	return s.store.markContainmentEmpty(ctx, s.processID, s.supervisorInstanceID)
}

func (s *Store) MarkContainmentEmpty(
	ctx context.Context,
	processID, supervisorInstanceID string,
) error {
	return s.markContainmentEmpty(ctx, processID, supervisorInstanceID)
}

func (s *Store) markContainmentEmpty(
	ctx context.Context,
	processID, supervisorInstanceID string,
) error {
	return s.updateProcess(
		ctx,
		"mark process containment empty",
		processID,
		supervisorInstanceID,
		func(q *dbsqlc.Queries) (int64, error) {
			return q.MarkProcessContainmentEmpty(
				ctx,
				dbsqlc.MarkProcessContainmentEmptyParams{
					ProcessID:            processID,
					SupervisorInstanceID: supervisorInstanceID,
				},
			)
		},
	)
}

func (s *Supervisor) MarkLocalClosed(ctx context.Context) error {
	return s.store.markLocalClosed(ctx, s.processID, s.supervisorInstanceID)
}

func (s *Supervisor) Process(ctx context.Context) (Process, error) {
	process, found, err := s.store.Process(ctx, s.processID)
	if err != nil {
		return Process{}, err
	}
	if !found || process.SupervisorInstanceID != s.supervisorInstanceID {
		return Process{}, fmt.Errorf("%w: %s", ErrSupervisorIdentityMismatch, s.processID)
	}
	return process, nil
}

// MarkRecoveredLocalClosed requires ownership of the process lifetime lock.
func (s *Store) MarkRecoveredLocalClosed(
	ctx context.Context,
	processID, supervisorInstanceID string,
) error {
	return s.markLocalClosed(ctx, processID, supervisorInstanceID)
}

func (s *Store) markLocalClosed(
	ctx context.Context,
	processID, supervisorInstanceID string,
) error {
	tx, err := beginWrite(ctx, s.db)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.q.WithTx(tx)

	process, found, err := processTx(ctx, qtx, processID)
	if err != nil {
		return err
	}
	if !found || process.SupervisorInstanceID != supervisorInstanceID {
		return fmt.Errorf("%w: %s", ErrSupervisorIdentityMismatch, processID)
	}
	if process.LocalClosed {
		return commitWrite(tx)
	}
	if process.Phase != ProcessTerminal ||
		!process.ActionAdmissionClosed ||
		!process.ContainmentEmpty {
		return fmt.Errorf(
			"%w: process %s phase=%s admission_closed=%t containment_empty=%t",
			ErrClosureBlocked,
			processID,
			process.Phase,
			process.ActionAdmissionClosed,
			process.ContainmentEmpty,
		)
	}

	terminalReports, err := qtx.CountTerminalProcessReports(
		ctx,
		dbsqlc.CountTerminalProcessReportsParams{ProcessID: processID},
	)
	if err != nil {
		return dbError("read terminal process evidence", err)
	}
	if terminalReports != 1 {
		return fmt.Errorf(
			"%w: process %s has no frozen terminal evidence",
			ErrClosureBlocked,
			processID,
		)
	}

	unresolvedActions, err := qtx.CountUnresolvedProcessActions(
		ctx,
		dbsqlc.CountUnresolvedProcessActionsParams{ProcessID: processID},
	)
	if err != nil {
		return dbError("read unresolved local actions", err)
	}
	if unresolvedActions != 0 {
		return fmt.Errorf(
			"%w: process %s has %d actions without frozen results",
			ErrClosureBlocked,
			processID,
			unresolvedActions,
		)
	}

	if err := qtx.MarkProcessLocalClosed(
		ctx,
		dbsqlc.MarkProcessLocalClosedParams{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
		},
	); err != nil {
		return dbError("close local process state", err)
	}
	return commitWrite(tx)
}

func (s *Store) MarkServerReleased(
	ctx context.Context,
	processID, supervisorInstanceID string,
) error {
	return s.updateProcess(
		ctx,
		"mark process server released",
		processID,
		supervisorInstanceID,
		func(q *dbsqlc.Queries) (int64, error) {
			return q.MarkProcessServerReleased(
				ctx,
				dbsqlc.MarkProcessServerReleasedParams{
					ProcessID:            processID,
					SupervisorInstanceID: supervisorInstanceID,
				},
			)
		},
	)
}

func (s *Store) DeleteClosedAfterArtifacts(
	ctx context.Context,
	processID, supervisorInstanceID string,
) error {
	tx, err := beginWrite(ctx, s.db)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.q.WithTx(tx)

	process, found, err := processTx(ctx, qtx, processID)
	if err != nil {
		return err
	}
	if !found {
		return commitWrite(tx)
	}
	if process.SupervisorInstanceID != supervisorInstanceID {
		return fmt.Errorf("%w: %s", ErrSupervisorIdentityMismatch, processID)
	}
	if !process.LocalClosed || !process.ServerReleased {
		return fmt.Errorf(
			"%w: process %s local_closed=%t server_released=%t",
			ErrClosureBlocked,
			processID,
			process.LocalClosed,
			process.ServerReleased,
		)
	}
	actions, err := qtx.CountProcessActions(
		ctx,
		dbsqlc.CountProcessActionsParams{ProcessID: processID},
	)
	if err != nil {
		return dbError("count process actions before cleanup", err)
	}
	if actions != 0 {
		return fmt.Errorf(
			"%w: process %s retains %d action rows",
			ErrClosureBlocked,
			processID,
			actions,
		)
	}
	unsettledReports, err := qtx.CountUnsettledProcessReports(
		ctx,
		dbsqlc.CountUnsettledProcessReportsParams{ProcessID: processID},
	)
	if err != nil {
		return dbError("count unsettled process reports before cleanup", err)
	}
	if unsettledReports != 0 {
		return fmt.Errorf(
			"%w: process %s retains %d unsettled reports",
			ErrClosureBlocked,
			processID,
			unsettledReports,
		)
	}
	if err := qtx.DeleteProcess(
		ctx,
		dbsqlc.DeleteProcessParams{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
		},
	); err != nil {
		return dbError("delete closed process state", err)
	}
	return commitWrite(tx)
}

func (s *Store) Process(
	ctx context.Context,
	processID string,
) (Process, bool, error) {
	row, err := s.q.GetProcess(
		ctx,
		dbsqlc.GetProcessParams{ProcessID: processID},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Process{}, false, nil
	}
	if err != nil {
		return Process{}, false, dbError("read process state", err)
	}
	return processFromSQLC(row), true, nil
}

func (s *Store) Processes(ctx context.Context) ([]Process, error) {
	rows, err := s.q.ListProcesses(ctx)
	if err != nil {
		return nil, dbError("list process state", err)
	}
	var processes []Process
	for _, row := range rows {
		processes = append(processes, processFromSQLC(row))
	}
	return processes, nil
}

func processTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	processID string,
) (Process, bool, error) {
	row, err := q.GetProcess(
		ctx,
		dbsqlc.GetProcessParams{ProcessID: processID},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Process{}, false, nil
	}
	if err != nil {
		return Process{}, false, dbError("read process state", err)
	}
	return processFromSQLC(row), true, nil
}

func processFromSQLC(row dbsqlc.Process) Process {
	return Process{
		ProcessID:             row.ProcessID,
		SupervisorInstanceID:  row.SupervisorInstanceID,
		SupervisorToken:       row.SupervisorToken,
		Phase:                 ProcessPhase(row.Phase),
		ResolvedActionSeq:     row.ResolvedActionSeq,
		ExecCommitted:         row.ExecCommitted != 0,
		ContainmentKind:       row.ContainmentKind,
		ContainmentID:         row.ContainmentID,
		ContainmentEmpty:      row.ContainmentEmpty != 0,
		ActionAdmissionClosed: row.ActionAdmissionClosed != 0,
		LocalClosed:           row.LocalClosed != 0,
		ServerReleased:        row.ServerReleased != 0,
	}
}

func (s *Store) updateProcess(
	ctx context.Context,
	operation string,
	processID, supervisorInstanceID string,
	update func(*dbsqlc.Queries) (int64, error),
) error {
	if operation == "" {
		return errors.New("process state update requires an operation label")
	}
	tx, err := beginWrite(ctx, s.db)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	count, err := update(s.q.WithTx(tx))
	if err != nil {
		return dbError(operation, err)
	}
	if count != 1 {
		return fmt.Errorf(
			"%w: %s affected %d process rows for %s/%s",
			ErrStateConflict,
			operation,
			count,
			processID,
			supervisorInstanceID,
		)
	}
	return commitWrite(tx)
}
