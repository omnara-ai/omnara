package statedb

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/processaction"
)

const maxFrozenReportBytes = daemonprotocol.MaxMessageBytes

type frozenObservation struct {
	Output     *string `json:"output"`
	NextCursor *int64  `json:"next_cursor"`
}

func (s *Supervisor) FreezeStartedReport(
	ctx context.Context,
	report Report,
) (Report, error) {
	if report.Kind != ReportProcessStarted ||
		report.ProcessID != s.processID || report.ActionID != "" {
		return Report{}, errors.New("invalid process-started report")
	}
	tx, err := beginWrite(ctx, s.store.db)
	if err != nil {
		return Report{}, err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.store.q.WithTx(tx)

	process, found, err := processTx(ctx, qtx, s.processID)
	if err != nil {
		return Report{}, err
	}
	if !found || process.SupervisorInstanceID != s.supervisorInstanceID {
		return Report{}, fmt.Errorf("%w: %s", ErrSupervisorIdentityMismatch, s.processID)
	}
	if (process.Phase != ProcessAccepted && process.Phase != ProcessTerminal) ||
		!process.ExecCommitted || process.ContainmentKind == "" {
		return Report{}, fmt.Errorf(
			"%w: process %s cannot freeze started evidence",
			ErrStateConflict,
			s.processID,
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

func (s *Supervisor) FreezeTerminalReport(
	ctx context.Context,
	report Report,
) (Report, error) {
	return s.store.freezeTerminalReport(
		ctx,
		s.processID,
		s.supervisorInstanceID,
		report,
	)
}

// FreezeRecoveredTerminalReport requires the process lifetime lock.
func (s *Store) FreezeRecoveredTerminalReport(
	ctx context.Context,
	processID, supervisorInstanceID string,
	report Report,
) (Report, error) {
	return s.freezeTerminalReport(ctx, processID, supervisorInstanceID, report)
}

func (s *Store) freezeTerminalReport(
	ctx context.Context,
	processID, supervisorInstanceID string,
	report Report,
) (Report, error) {
	if report.Kind != ReportProcessTerminal ||
		report.ProcessID != processID || report.ActionID != "" {
		return Report{}, errors.New("invalid process-terminal report")
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
	if process.Phase != ProcessAccepted && process.Phase != ProcessTerminal {
		return Report{}, fmt.Errorf(
			"%w: process %s cannot freeze terminal evidence in %s",
			ErrStateConflict,
			processID,
			process.Phase,
		)
	}
	frozen, err := freezeReportTx(ctx, qtx, report)
	if err != nil {
		return Report{}, err
	}
	if err := qtx.SetProcessTerminal(
		ctx,
		dbsqlc.SetProcessTerminalParams{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
		},
	); err != nil {
		return Report{}, dbError("record terminal process state", err)
	}
	if err := commitWrite(tx); err != nil {
		return Report{}, err
	}
	return frozen, nil
}

func freezeReportTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	report Report,
) (Report, error) {
	if err := validateReport(report); err != nil {
		return Report{}, err
	}
	existing, found, err := reportBySlotTx(
		ctx,
		q,
		report.Kind,
		report.ProcessID,
		report.ActionID,
	)
	if err != nil {
		return Report{}, err
	}
	if found {
		if sameReportContent(existing, report) {
			return existing, nil
		}
		return Report{}, fmt.Errorf(
			"%w: report slot %s/%s/%s already contains different evidence",
			ErrStateConflict,
			report.ProcessID,
			report.ActionID,
			report.Kind,
		)
	}
	if report.ID != "" || report.State != "" ||
		report.ErrorCode != "" || report.Error != "" {
		return Report{}, errors.New(
			"a new frozen report cannot supply local persistence state",
		)
	}

	report.ID = newReportID()
	report.State = ReportPending
	if err := validateReportWireEnvelope(report); err != nil {
		return Report{}, fmt.Errorf(
			"%w: frozen report is not deliverable: %w",
			ErrStateConflict,
			err,
		)
	}
	var actionID sql.NullString
	if report.ActionID != "" {
		actionID = sql.NullString{String: report.ActionID, Valid: true}
	}
	if err := q.InsertFrozenReport(
		ctx,
		dbsqlc.InsertFrozenReportParams{
			ReportID:   report.ID,
			ProcessID:  report.ProcessID,
			ActionID:   actionID,
			ReportKind: string(report.Kind),
			Body:       report.Body,
		},
	); err != nil {
		return Report{}, dbError("freeze canonical daemon report", err)
	}
	return report, nil
}

func validateReportWireEnvelope(report Report) error {
	var event daemonprotocol.ReportedEvent
	if err := json.Unmarshal(report.Body, &event); err != nil {
		return fmt.Errorf("decode frozen report for wire validation: %w", err)
	}
	body, err := json.Marshal(daemonprotocol.Message{
		Type:     daemonprotocol.MessageReport,
		ReportID: report.ID,
		Event:    &event,
	})
	if err != nil {
		return fmt.Errorf("encode frozen report for wire validation: %w", err)
	}
	if len(body) > daemonprotocol.MaxMessageBytes {
		return fmt.Errorf(
			"frozen report envelope is %d bytes; maximum is %d",
			len(body),
			daemonprotocol.MaxMessageBytes,
		)
	}
	return nil
}

func validateReport(report Report) error {
	if report.ProcessID == "" || report.Kind == "" || len(report.Body) == 0 {
		return errors.New("report process, kind, and body are required")
	}
	if len(report.Body) > maxFrozenReportBytes {
		return fmt.Errorf(
			"frozen report is %d bytes; maximum is %d",
			len(report.Body),
			maxFrozenReportBytes,
		)
	}
	if report.Kind == ReportActionTerminal {
		if report.ActionID == "" {
			return errors.New("action-terminal report requires an action")
		}
	} else if report.ActionID != "" {
		return errors.New("process report cannot name an action")
	}

	var event daemonprotocol.ReportedEvent
	if err := json.Unmarshal(report.Body, &event); err != nil {
		return fmt.Errorf("decode frozen report body: %w", err)
	}
	if event.ProcessID != report.ProcessID ||
		event.ProcessActionID != report.ActionID {
		return errors.New("frozen report body identity does not match its slot")
	}
	switch report.Kind {
	case ReportProcessStarted:
		if event.Type != daemonprotocol.EventProcessStarted ||
			event.StartedAt.IsZero() ||
			event.ObservedAt.IsZero() ||
			event.StartedAt.After(event.ObservedAt) {
			return errors.New(
				"process-started report requires ordered started_at and observed_at",
			)
		}
	case ReportProcessTerminal:
		if event.Type != daemonprotocol.EventProcessFinished ||
			!event.ObservedAt.IsZero() ||
			!daemonprotocol.ValidProcessTerminalSourceTimes(
				event.State,
				event.StartedAt,
				event.EndedAt,
			) {
			return errors.New(
				"process-terminal report has invalid terminal source evidence",
			)
		}
		if (event.State == daemonprotocol.ProcessStateFailed ||
			event.State == daemonprotocol.ProcessStateUnknown) &&
			event.StateReasonCode == "" {
			return errors.New(
				"failed or unknown process report requires a reason",
			)
		}
	case ReportActionTerminal:
		if !validTerminalActionEvent(event.Type) ||
			!event.ObservedAt.IsZero() {
			return errors.New(
				"action-terminal report requires a terminal event",
			)
		}
		if event.Type != daemonprotocol.EventProcessActionApplied &&
			event.StateReasonCode == "" {
			return errors.New(
				"non-applied action report requires a reason",
			)
		}
	default:
		return fmt.Errorf("unsupported frozen report kind %q", report.Kind)
	}
	if len(event.Result) == 0 || bytes.Equal(event.Result, []byte("null")) {
		return nil
	}

	var observation frozenObservation
	if err := json.Unmarshal(event.Result, &observation); err != nil {
		return fmt.Errorf("decode frozen report result: %w", err)
	}
	if observation.Output != nil &&
		len([]byte(*observation.Output)) > processaction.MaxObservationBytes {
		return fmt.Errorf(
			"frozen process output is %d bytes; maximum is %d",
			len([]byte(*observation.Output)),
			processaction.MaxObservationBytes,
		)
	}
	return nil
}

func validTerminalActionEvent(eventType daemonprotocol.ReportedEventType) bool {
	switch eventType {
	case daemonprotocol.EventProcessActionApplied,
		daemonprotocol.EventProcessActionFailed,
		daemonprotocol.EventProcessActionUnknown:
		return true
	default:
		return false
	}
}

func sameReportContent(left, right Report) bool {
	return left.ProcessID == right.ProcessID &&
		left.ActionID == right.ActionID &&
		left.Kind == right.Kind &&
		bytes.Equal(left.Body, right.Body)
}

func (s *Store) ReportBySlot(
	ctx context.Context,
	kind ReportKind,
	processID, actionID string,
) (Report, bool, error) {
	if actionID == "" {
		row, err := s.q.GetFrozenProcessReportBySlot(
			ctx,
			dbsqlc.GetFrozenProcessReportBySlotParams{
				ProcessID:  processID,
				ReportKind: string(kind),
			},
		)
		if errors.Is(err, sql.ErrNoRows) {
			return Report{}, false, nil
		}
		if err != nil {
			return Report{}, false, dbError("read frozen process report", err)
		}
		return reportFromSQLC(row), true, nil
	}
	row, err := s.q.GetFrozenActionReportBySlot(
		ctx,
		dbsqlc.GetFrozenActionReportBySlotParams{
			ProcessID:  processID,
			ActionID:   sql.NullString{String: actionID, Valid: true},
			ReportKind: string(kind),
		},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Report{}, false, nil
	}
	if err != nil {
		return Report{}, false, dbError("read frozen action report", err)
	}
	return reportFromSQLC(row), true, nil
}

func (s *Store) DeliveryCandidates(ctx context.Context) ([]Report, error) {
	rows, err := s.q.ListFrozenReportDeliveryCandidates(ctx)
	if err != nil {
		return nil, dbError("list daemon report delivery candidates", err)
	}
	var reports []Report
	for _, row := range rows {
		reports = append(reports, reportFromSQLC(row))
	}
	return reports, nil
}

func (s *Store) ReportsForProcess(
	ctx context.Context,
	processID string,
) ([]Report, error) {
	rows, err := s.q.ListFrozenReportsForProcess(
		ctx,
		dbsqlc.ListFrozenReportsForProcessParams{ProcessID: processID},
	)
	if err != nil {
		return nil, dbError("list process reports", err)
	}
	var reports []Report
	for _, row := range rows {
		reports = append(reports, reportFromSQLC(row))
	}
	return reports, nil
}

func (s *Store) AcknowledgeReport(
	ctx context.Context,
	reportID string,
) error {
	tx, err := beginWrite(ctx, s.db)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.q.WithTx(tx)

	row, err := qtx.GetFrozenReportByID(
		ctx,
		dbsqlc.GetFrozenReportByIDParams{ReportID: reportID},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: report %s", ErrStateConflict, reportID)
	}
	if err != nil {
		return dbError("read acknowledged daemon report", err)
	}
	report := reportFromSQLC(row)
	switch report.Kind {
	case ReportActionTerminal:
		if report.State != ReportPending && report.State != ReportRejected {
			return fmt.Errorf(
				"%w: action report %s is %s",
				ErrStateConflict,
				reportID,
				report.State,
			)
		}
		action, found, err := actionTx(ctx, qtx, report.ActionID)
		if err != nil {
			return err
		}
		if !found || action.ProcessID != report.ProcessID {
			return fmt.Errorf(
				"%w: action report %s has no action",
				ErrStateConflict,
				reportID,
			)
		}
		process, found, err := processTx(ctx, qtx, report.ProcessID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf(
				"%w: action report %s has no supervisor instance ID",
				ErrStateConflict,
				reportID,
			)
		}
		if action.Seq != process.ResolvedActionSeq+1 {
			return fmt.Errorf(
				"%w: action %s sequence %d follows local frontier %d",
				ErrActionBlocked,
				action.ID,
				action.Seq,
				process.ResolvedActionSeq,
			)
		}
		if err := qtx.AdvanceResolvedActionSequence(
			ctx,
			dbsqlc.AdvanceResolvedActionSequenceParams{
				ResolvedActionSeq: action.Seq,
				ProcessID:         action.ProcessID,
			},
		); err != nil {
			return dbError("advance resolved process action sequence", err)
		}
		if err := qtx.DeleteProcessAction(
			ctx,
			dbsqlc.DeleteProcessActionParams{
				ActionID:  action.ID,
				ProcessID: action.ProcessID,
			},
		); err != nil {
			return dbError("compact acknowledged process action", err)
		}
	case ReportProcessStarted, ReportProcessTerminal:
		if report.State == ReportPending || report.State == ReportRejected {
			if err := qtx.AcknowledgeProcessReport(
				ctx,
				dbsqlc.AcknowledgeProcessReportParams{ReportID: reportID},
			); err != nil {
				return dbError("acknowledge process report", err)
			}
		} else if report.State != ReportAcknowledged {
			return fmt.Errorf(
				"%w: process report %s is %s",
				ErrStateConflict,
				reportID,
				report.State,
			)
		}
		if report.Kind == ReportProcessTerminal {
			if err := qtx.MarkTerminalProcessServerReleased(
				ctx,
				dbsqlc.MarkTerminalProcessServerReleasedParams{
					ProcessID: report.ProcessID,
				},
			); err != nil {
				return dbError("mark terminal process server released", err)
			}
		}
	default:
		return fmt.Errorf("unsupported report kind %q", report.Kind)
	}
	return commitWrite(tx)
}

func (s *Store) ReleaseAction(
	ctx context.Context,
	processID, supervisorInstanceID, actionID string,
	seq int64,
) error {
	return s.releaseAction(
		ctx,
		processID,
		supervisorInstanceID,
		actionID,
		seq,
		false,
	)
}

func (s *Store) ResolveAbsentAction(
	ctx context.Context,
	processID, supervisorInstanceID, actionID string,
	seq int64,
) error {
	return s.releaseAction(
		ctx,
		processID,
		supervisorInstanceID,
		actionID,
		seq,
		true,
	)
}

func (s *Store) releaseAction(
	ctx context.Context,
	processID, supervisorInstanceID, actionID string,
	seq int64,
	requireAbsent bool,
) error {
	if processID == "" || supervisorInstanceID == "" || actionID == "" || seq <= 0 {
		return errors.New("complete process action release identity is required")
	}
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
	action, actionFound, err := actionTx(ctx, qtx, actionID)
	if err != nil {
		return err
	}
	if actionFound && (action.ProcessID != processID || action.Seq != seq) {
		return fmt.Errorf(
			"%w: action %s release identity changed",
			ErrStateConflict,
			actionID,
		)
	}
	if requireAbsent && actionFound {
		return fmt.Errorf(
			"%w: action %s unexpectedly remains recorded locally",
			ErrStateConflict,
			actionID,
		)
	}
	if !actionFound {
		if err := ensureActionSequenceUnused(
			ctx,
			qtx,
			Action{
				ID:        actionID,
				ProcessID: processID,
				Seq:       seq,
			},
		); err != nil {
			return err
		}
	}
	if seq <= process.ResolvedActionSeq {
		if actionFound {
			return fmt.Errorf(
				"%w: resolved action %s remains recorded locally",
				ErrStateConflict,
				actionID,
			)
		}
		return commitWrite(tx)
	}
	if seq > process.ResolvedActionSeq+1 {
		if err := compactMissingActionPrefixTx(
			ctx,
			qtx,
			&process,
			seq,
		); err != nil {
			if errors.Is(err, ErrActionBlocked) && !actionFound {
				return commitWrite(tx)
			}
			return err
		}
	}
	if seq != process.ResolvedActionSeq+1 {
		return fmt.Errorf(
			"%w: action %s sequence %d follows local frontier %d",
			ErrActionBlocked,
			actionID,
			seq,
			process.ResolvedActionSeq,
		)
	}
	if actionFound {
		if _, reported, err := reportByActionTx(
			ctx,
			qtx,
			actionID,
		); err != nil {
			return err
		} else if reported {
			return fmt.Errorf(
				"%w: action %s has frozen evidence awaiting acknowledgement",
				ErrStateConflict,
				actionID,
			)
		}
		if err := qtx.DeleteProcessAction(
			ctx,
			dbsqlc.DeleteProcessActionParams{
				ActionID:  actionID,
				ProcessID: processID,
			},
		); err != nil {
			return dbError("release resolved process action", err)
		}
	}
	if err := qtx.AdvanceReleasedActionSequence(
		ctx,
		dbsqlc.AdvanceReleasedActionSequenceParams{
			ResolvedActionSeq:    seq,
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
		},
	); err != nil {
		return dbError("advance released process action sequence", err)
	}
	return commitWrite(tx)
}

func (s *Store) RejectReport(
	ctx context.Context,
	reportID, code, message string,
) error {
	if reportID == "" || code == "" {
		return errors.New("report and rejection code are required")
	}
	tx, err := beginWrite(ctx, s.db)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.q.WithTx(tx)

	row, err := qtx.GetFrozenReportByID(
		ctx,
		dbsqlc.GetFrozenReportByIDParams{ReportID: reportID},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: report %s", ErrStateConflict, reportID)
	}
	if err != nil {
		return dbError("read rejected daemon report", err)
	}
	report := reportFromSQLC(row)
	if report.State == ReportRejected {
		if report.ErrorCode == code && report.Error == message {
			return commitWrite(tx)
		}
		return fmt.Errorf(
			"%w: report %s rejection changed",
			ErrStateConflict,
			reportID,
		)
	}
	if report.State != ReportPending {
		return fmt.Errorf(
			"%w: acknowledged report %s cannot be rejected",
			ErrStateConflict,
			reportID,
		)
	}
	if err := qtx.RejectFrozenReport(
		ctx,
		dbsqlc.RejectFrozenReportParams{
			ErrorCode: code,
			Error:     message,
			ReportID:  reportID,
		},
	); err != nil {
		return dbError("retain rejected daemon report", err)
	}
	return commitWrite(tx)
}

func reportBySlotTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	kind ReportKind,
	processID, actionID string,
) (Report, bool, error) {
	if actionID == "" {
		row, err := q.GetFrozenProcessReportBySlot(
			ctx,
			dbsqlc.GetFrozenProcessReportBySlotParams{
				ProcessID:  processID,
				ReportKind: string(kind),
			},
		)
		if errors.Is(err, sql.ErrNoRows) {
			return Report{}, false, nil
		}
		if err != nil {
			return Report{}, false, dbError("read frozen process report", err)
		}
		return reportFromSQLC(row), true, nil
	}
	row, err := q.GetFrozenActionReportBySlot(
		ctx,
		dbsqlc.GetFrozenActionReportBySlotParams{
			ProcessID:  processID,
			ActionID:   sql.NullString{String: actionID, Valid: true},
			ReportKind: string(kind),
		},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Report{}, false, nil
	}
	if err != nil {
		return Report{}, false, dbError("read frozen action report", err)
	}
	return reportFromSQLC(row), true, nil
}

func reportByActionTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	actionID string,
) (Report, bool, error) {
	row, err := q.GetFrozenReportByAction(
		ctx,
		dbsqlc.GetFrozenReportByActionParams{
			ActionID: sql.NullString{String: actionID, Valid: true},
		},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Report{}, false, nil
	}
	if err != nil {
		return Report{}, false, dbError("read frozen action report", err)
	}
	return reportFromSQLC(row), true, nil
}

func reportFromSQLC(row dbsqlc.FrozenReport) Report {
	report := Report{
		ID:        row.ReportID,
		ProcessID: row.ProcessID,
		Kind:      ReportKind(row.ReportKind),
		Body:      row.Body,
		State:     ReportState(row.State),
		ErrorCode: row.ErrorCode,
		Error:     row.Error,
	}
	if row.ActionID.Valid {
		report.ActionID = row.ActionID.String
	}
	return report
}
