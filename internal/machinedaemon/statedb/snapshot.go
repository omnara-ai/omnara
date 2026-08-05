package statedb

import (
	"context"
	"database/sql"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
)

type Snapshot struct {
	Processes []ProcessSnapshot
}

type ProcessSnapshot struct {
	Process           Process
	Actions           []ActionSnapshot
	RejectedReportIDs []string
}

type ActionSnapshot struct {
	Action   Action
	Reported bool
}

func (s *Store) SnapshotForReconciliation(
	ctx context.Context,
) (Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Snapshot{}, dbError("begin reconciliation snapshot", err)
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.q.WithTx(tx)

	processRows, err := qtx.ListProcesses(ctx)
	if err != nil {
		return Snapshot{}, dbError("list processes for reconciliation", err)
	}
	var snapshot Snapshot
	byProcess := make(map[string]int)
	for _, row := range processRows {
		process := processFromSQLC(row)
		byProcess[process.ProcessID] = len(snapshot.Processes)
		snapshot.Processes = append(
			snapshot.Processes,
			ProcessSnapshot{Process: process},
		)
	}

	actionRows, err := qtx.ListProcessActionSnapshots(ctx)
	if err != nil {
		return Snapshot{}, dbError("list actions for reconciliation", err)
	}
	for _, row := range actionRows {
		action := Action{
			ID:              row.ActionID,
			ProcessID:       row.ProcessID,
			Kind:            daemonprotocol.ProcessActionKind(row.ActionKind),
			Seq:             row.Seq,
			EffectCommitted: row.EffectCommitted != 0,
		}
		index, found := byProcess[action.ProcessID]
		if !found {
			return Snapshot{}, dbError(
				"join action reconciliation snapshot",
				sql.ErrNoRows,
			)
		}
		snapshot.Processes[index].Actions = append(
			snapshot.Processes[index].Actions,
			ActionSnapshot{Action: action, Reported: row.Reported},
		)
		if ReportState(row.ReportState) == ReportRejected {
			snapshot.Processes[index].RejectedReportIDs = append(
				snapshot.Processes[index].RejectedReportIDs,
				row.ReportID,
			)
		}
	}

	rejectedRows, err := qtx.ListRejectedProcessReportIDs(ctx)
	if err != nil {
		return Snapshot{}, dbError(
			"list rejected process reports for reconciliation",
			err,
		)
	}
	for _, row := range rejectedRows {
		index, found := byProcess[row.ProcessID]
		if !found {
			return Snapshot{}, dbError(
				"join rejected process report reconciliation snapshot",
				sql.ErrNoRows,
			)
		}
		snapshot.Processes[index].RejectedReportIDs = append(
			snapshot.Processes[index].RejectedReportIDs,
			row.ReportID,
		)
	}
	if err := tx.Commit(); err != nil {
		return Snapshot{}, dbError("commit reconciliation snapshot", err)
	}
	return snapshot, nil
}
