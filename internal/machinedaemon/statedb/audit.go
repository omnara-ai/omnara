package statedb

import (
	"context"
	"fmt"
)

func (s *Store) Audit(ctx context.Context) error {
	if err := verifyPragmas(ctx, s.db); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return dbError("run state database integrity check", err)
	}
	defer func() { _ = rows.Close() }()
	var integrityProblems []string
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return dbError("read state database integrity check", err)
		}
		if result != "ok" {
			integrityProblems = append(integrityProblems, result)
		}
	}
	if err := rows.Close(); err != nil {
		return dbError("close state database integrity check", err)
	}
	if err := rows.Err(); err != nil {
		return dbError("iterate state database integrity check", err)
	}
	if len(integrityProblems) != 0 {
		return fmt.Errorf(
			"state database integrity check failed: %v",
			integrityProblems,
		)
	}

	var foreignKeyViolations int
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT count(*) FROM pragma_foreign_key_check`,
	).Scan(&foreignKeyViolations); err != nil {
		return dbError("run state database foreign-key check", err)
	}
	if foreignKeyViolations != 0 {
		return fmt.Errorf(
			"state database has %d foreign-key violations",
			foreignKeyViolations,
		)
	}

	violations, err := s.q.GetInvariantViolations(ctx)
	if err != nil {
		return dbError("audit state database semantics", err)
	}
	var problem string
	switch {
	case violations.TerminalProcessMissingReport:
		problem = "terminal process has no terminal report"
	case violations.TerminalReportOnNonterminalProcess:
		problem = "terminal report belongs to a non-terminal process"
	case violations.LocallyClosedProcessNotTerminal:
		problem = "locally closed process is not terminal"
	case violations.LocallyClosedProcessActionMissingReport:
		problem = "locally closed process has an action without a report"
	case violations.NoEffectActionMissingReport:
		problem = "action without an effect has no terminal report"
	case violations.ActionBehindResolvedFrontier:
		problem = "uncompacted action is behind the resolved frontier"
	}
	if problem != "" {
		return fmt.Errorf("state database semantic audit failed: %s", problem)
	}

	reportRows, err := s.q.ListFrozenReports(ctx)
	if err != nil {
		return dbError("list reports for state audit", err)
	}
	for _, row := range reportRows {
		report := reportFromSQLC(row)
		if err := validateReport(report); err != nil {
			return fmt.Errorf("audit frozen report %s: %w", report.ID, err)
		}
	}
	return nil
}
