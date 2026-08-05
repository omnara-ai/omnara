package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb/internal/dbsqlc"
)

type ApplyDecision uint8

const (
	_ ApplyDecision = iota
	ApplyExecute
	ApplyAlreadyReported
	ApplyOutcomeUnknown
	ApplyAlreadyResolved
	ApplyBlocked
	ApplyAdmissionClosed
)

type NoEffectDecision uint8

const (
	_ NoEffectDecision = iota
	NoEffectRecorded
	NoEffectAlreadyReported
	NoEffectLostToApply
	NoEffectAlreadyResolved
	NoEffectBlocked
	NoEffectAdmissionClosed
)

func (s *Supervisor) ApplyOnce(
	ctx context.Context,
	action Action,
) (ApplyDecision, Report, error) {
	if err := validateAction(action, s.processID); err != nil {
		return ApplyAdmissionClosed, Report{}, err
	}

	tx, err := beginWrite(ctx, s.store.db)
	if err != nil {
		return ApplyAdmissionClosed, Report{}, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.store.q.WithTx(tx)

	process, existing, found, err := loadActionTransitionTx(
		ctx,
		qtx,
		s.processID,
		s.supervisorInstanceID,
		action,
	)
	if err != nil {
		return ApplyAdmissionClosed, Report{}, err
	}
	if found {
		report, reported, err := reportByActionTx(ctx, qtx, action.ID)
		if err != nil {
			return ApplyAdmissionClosed, Report{}, err
		}
		if reported {
			return ApplyAlreadyReported, report, commitWrite(tx)
		}
		if existing.EffectCommitted {
			return ApplyOutcomeUnknown, Report{}, commitWrite(tx)
		}
		return ApplyAdmissionClosed, Report{}, fmt.Errorf(
			"%w: action %s has no effect or terminal evidence",
			ErrStateConflict,
			action.ID,
		)
	}

	if action.Seq <= process.ResolvedActionSeq {
		return ApplyAlreadyResolved, Report{}, commitWrite(tx)
	}
	if err := compactMissingActionPrefixTx(
		ctx,
		qtx,
		&process,
		action.Seq,
	); err != nil {
		if errors.Is(err, ErrActionBlocked) {
			return ApplyBlocked, Report{}, commitWrite(tx)
		}
		return ApplyAdmissionClosed, Report{}, err
	}
	if action.Seq != process.ResolvedActionSeq+1 {
		return ApplyBlocked, Report{}, commitWrite(tx)
	}
	if process.LocalClosed || process.ActionAdmissionClosed ||
		process.Phase != ProcessAccepted ||
		!process.ExecCommitted ||
		process.ContainmentKind == "" {
		return ApplyAdmissionClosed, Report{}, commitWrite(tx)
	}
	if err := ensureActionSequenceUnused(ctx, qtx, action); err != nil {
		return ApplyAdmissionClosed, Report{}, err
	}
	if err := qtx.InsertProcessAction(
		ctx,
		dbsqlc.InsertProcessActionParams{
			ActionID:        action.ID,
			ProcessID:       action.ProcessID,
			ActionKind:      string(action.Kind),
			Seq:             action.Seq,
			EffectCommitted: 1,
		},
	); err != nil {
		return ApplyAdmissionClosed, Report{}, dbError(
			"commit process action effect boundary",
			err,
		)
	}
	return ApplyExecute, Report{}, commitWrite(tx)
}

func (s *Supervisor) RecordActionWithoutEffect(
	ctx context.Context,
	action Action,
	report Report,
) (NoEffectDecision, Report, error) {
	if err := validateAction(action, s.processID); err != nil {
		return NoEffectAdmissionClosed, Report{}, err
	}
	if err := validateActionReportInput(
		report,
		s.processID,
		action.ID,
	); err != nil {
		return NoEffectAdmissionClosed, Report{}, err
	}

	tx, err := beginWrite(ctx, s.store.db)
	if err != nil {
		return NoEffectAdmissionClosed, Report{}, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.store.q.WithTx(tx)

	process, existing, found, err := loadActionTransitionTx(
		ctx,
		qtx,
		s.processID,
		s.supervisorInstanceID,
		action,
	)
	if err != nil {
		return NoEffectAdmissionClosed, Report{}, err
	}
	if found {
		existingReport, reported, err := reportByActionTx(
			ctx,
			qtx,
			action.ID,
		)
		if err != nil {
			return NoEffectAdmissionClosed, Report{}, err
		}
		if reported {
			return NoEffectAlreadyReported, existingReport, commitWrite(tx)
		}
		if existing.EffectCommitted {
			return NoEffectLostToApply, Report{}, commitWrite(tx)
		}
		return NoEffectAdmissionClosed, Report{}, fmt.Errorf(
			"%w: action %s has no effect or terminal evidence",
			ErrStateConflict,
			action.ID,
		)
	}

	if action.Seq <= process.ResolvedActionSeq {
		return NoEffectAlreadyResolved, Report{}, commitWrite(tx)
	}
	if err := compactMissingActionPrefixTx(
		ctx,
		qtx,
		&process,
		action.Seq,
	); err != nil {
		if errors.Is(err, ErrActionBlocked) {
			return NoEffectBlocked, Report{}, commitWrite(tx)
		}
		return NoEffectAdmissionClosed, Report{}, err
	}
	if action.Seq != process.ResolvedActionSeq+1 {
		return NoEffectBlocked, Report{}, commitWrite(tx)
	}
	if process.LocalClosed ||
		(process.Phase != ProcessAccepted && process.Phase != ProcessTerminal) {
		return NoEffectAdmissionClosed, Report{}, commitWrite(tx)
	}
	if err := ensureActionSequenceUnused(ctx, qtx, action); err != nil {
		return NoEffectAdmissionClosed, Report{}, err
	}
	if err := qtx.InsertProcessAction(
		ctx,
		dbsqlc.InsertProcessActionParams{
			ActionID:        action.ID,
			ProcessID:       action.ProcessID,
			ActionKind:      string(action.Kind),
			Seq:             action.Seq,
			EffectCommitted: 0,
		},
	); err != nil {
		return NoEffectAdmissionClosed, Report{}, dbError(
			"record process action without effect",
			err,
		)
	}
	frozen, err := freezeReportTx(ctx, qtx, report)
	if err != nil {
		return NoEffectAdmissionClosed, Report{}, err
	}
	if err := commitWrite(tx); err != nil {
		return NoEffectAdmissionClosed, Report{}, err
	}
	return NoEffectRecorded, frozen, nil
}

func (s *Supervisor) FreezeActionReport(
	ctx context.Context,
	actionID string,
	report Report,
) (Report, error) {
	return s.store.freezeActionReport(
		ctx,
		s.processID,
		s.supervisorInstanceID,
		actionID,
		report,
	)
}

// FreezeRecoveredActionReport requires the process lifetime lock.
func (s *Store) FreezeRecoveredActionReport(
	ctx context.Context,
	processID, supervisorInstanceID, actionID string,
	report Report,
) (Report, error) {
	return s.freezeActionReport(
		ctx,
		processID,
		supervisorInstanceID,
		actionID,
		report,
	)
}

func (s *Store) freezeActionReport(
	ctx context.Context,
	processID, supervisorInstanceID, actionID string,
	report Report,
) (Report, error) {
	if err := validateActionReportInput(report, processID, actionID); err != nil {
		return Report{}, err
	}
	tx, err := beginWrite(ctx, s.db)
	if err != nil {
		return Report{}, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.q.WithTx(tx)

	process, found, err := processTx(ctx, qtx, processID)
	if err != nil {
		return Report{}, err
	}
	if !found || process.SupervisorInstanceID != supervisorInstanceID {
		return Report{}, fmt.Errorf("%w: %s", ErrSupervisorIdentityMismatch, processID)
	}
	action, found, err := actionTx(ctx, qtx, actionID)
	if err != nil {
		return Report{}, err
	}
	if !found || action.ProcessID != processID {
		return Report{}, fmt.Errorf("%w: action %s", ErrSupervisorIdentityMismatch, actionID)
	}
	if !action.EffectCommitted {
		existing, reported, err := reportByActionTx(ctx, qtx, actionID)
		if err != nil {
			return Report{}, err
		}
		if reported && sameReportContent(existing, report) {
			return existing, commitWrite(tx)
		}
		return Report{}, fmt.Errorf(
			"%w: action %s was resolved without an effect",
			ErrStateConflict,
			actionID,
		)
	}
	frozen, err := freezeReportTx(ctx, qtx, report)
	if err != nil {
		return Report{}, err
	}
	if err := commitWrite(tx); err != nil {
		return Report{}, err
	}
	return frozen, nil
}

func (s *Store) Action(
	ctx context.Context,
	actionID string,
) (Action, bool, error) {
	row, err := s.q.GetProcessAction(
		ctx,
		dbsqlc.GetProcessActionParams{ActionID: actionID},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Action{}, false, nil
	}
	if err != nil {
		return Action{}, false, dbError("read local process action", err)
	}
	return actionFromSQLC(row), true, nil
}

func (s *Store) Actions(
	ctx context.Context,
	processID string,
) ([]Action, error) {
	rows, err := s.q.ListProcessActions(
		ctx,
		dbsqlc.ListProcessActionsParams{ProcessID: processID},
	)
	if err != nil {
		return nil, dbError("list process actions", err)
	}
	var actions []Action
	for _, row := range rows {
		actions = append(actions, actionFromSQLC(row))
	}
	return actions, nil
}

func actionTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	actionID string,
) (Action, bool, error) {
	row, err := q.GetProcessAction(
		ctx,
		dbsqlc.GetProcessActionParams{ActionID: actionID},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Action{}, false, nil
	}
	if err != nil {
		return Action{}, false, dbError("read local process action", err)
	}
	return actionFromSQLC(row), true, nil
}

func actionFromSQLC(row dbsqlc.ProcessAction) Action {
	return Action{
		ID:              row.ActionID,
		ProcessID:       row.ProcessID,
		Kind:            daemonprotocol.ProcessActionKind(row.ActionKind),
		Seq:             row.Seq,
		EffectCommitted: row.EffectCommitted != 0,
	}
}

func validateAction(action Action, processID string) error {
	if action.ID == "" || action.ProcessID != processID || action.Seq <= 0 {
		return errors.New("complete action identity is required")
	}
	switch action.Kind {
	case daemonprotocol.ProcessActionWrite,
		daemonprotocol.ProcessActionInterrupt,
		daemonprotocol.ProcessActionTerminate:
		return nil
	default:
		return fmt.Errorf("unsupported process action kind %q", action.Kind)
	}
}

func validateActionReportInput(
	report Report,
	processID, actionID string,
) error {
	if report.ProcessID != processID || report.ActionID != actionID ||
		report.Kind != ReportActionTerminal {
		return errors.New("invalid action-terminal report")
	}
	return nil
}

func sameActionIdentity(left, right Action) bool {
	return left.ID == right.ID &&
		left.ProcessID == right.ProcessID &&
		left.Kind == right.Kind &&
		left.Seq == right.Seq
}

func loadActionTransitionTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	processID, supervisorInstanceID string,
	action Action,
) (Process, Action, bool, error) {
	process, found, err := processTx(ctx, q, processID)
	if err != nil {
		return Process{}, Action{}, false, err
	}
	if !found || process.SupervisorInstanceID != supervisorInstanceID {
		return Process{}, Action{}, false, fmt.Errorf(
			"%w: %s",
			ErrSupervisorIdentityMismatch,
			processID,
		)
	}
	existing, found, err := actionTx(ctx, q, action.ID)
	if err != nil {
		return Process{}, Action{}, false, err
	}
	if found && !sameActionIdentity(existing, action) {
		return Process{}, Action{}, false, fmt.Errorf(
			"%w: action %s identity changed",
			ErrStateConflict,
			action.ID,
		)
	}
	return process, existing, found, nil
}

func ensureActionSequenceUnused(
	ctx context.Context,
	q *dbsqlc.Queries,
	action Action,
) error {
	existingID, err := q.GetProcessActionIDBySequence(
		ctx,
		dbsqlc.GetProcessActionIDBySequenceParams{
			ProcessID: action.ProcessID,
			Seq:       action.Seq,
		},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return dbError("inspect process action sequence", err)
	}
	return fmt.Errorf(
		"%w: process %s sequence %d already belongs to action %s",
		ErrStateConflict,
		action.ProcessID,
		action.Seq,
		existingID,
	)
}

// Reaching sequence N proves any absent prefix was resolved before local
// admission: offers, acceptance, reconciliation, and execution are ordered per
// process. Existing local rows still block compaction until evidence settles.
func compactMissingActionPrefixTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	process *Process,
	nextSeq int64,
) error {
	if nextSeq <= process.ResolvedActionSeq+1 {
		return nil
	}
	localRows, err := q.CountProcessActionsBetween(
		ctx,
		dbsqlc.CountProcessActionsBetweenParams{
			ProcessID: process.ProcessID,
			AfterSeq:  process.ResolvedActionSeq,
			BeforeSeq: nextSeq,
		},
	)
	if err != nil {
		return dbError("inspect missing process action prefix", err)
	}
	if localRows != 0 {
		return ErrActionBlocked
	}
	if err := q.CompactMissingActionPrefix(
		ctx,
		dbsqlc.CompactMissingActionPrefixParams{
			NextResolvedActionSeq:     nextSeq - 1,
			ProcessID:                 process.ProcessID,
			SupervisorInstanceID:      process.SupervisorInstanceID,
			PreviousResolvedActionSeq: process.ResolvedActionSeq,
		},
	); err != nil {
		return dbError("compact missing process action prefix", err)
	}
	process.ResolvedActionSeq = nextSeq - 1
	return nil
}
