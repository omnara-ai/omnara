package executionstore

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) CancelDaemonProgramToolCall(
	ctx context.Context, authority DaemonRuntimeAuthority, ref ProgramToolCallRef,
) (bool, bool, error) {
	if err := ref.validate(); err != nil {
		return false, false, err
	}
	ended, found, err := daemonProgramUnit(ctx, s, "cancel_daemon_program_tool_call", authority, ref.ProcessID,
		func(h *agentexecution.Handle) (bool, error) {
			return h.CancelProgramTool(ctx, ref.ref())
		})
	switch {
	case errors.Is(err, storeerr.ErrProcessTerminal), errors.Is(err, storeerr.ErrNotFound):
		return true, true, nil
	case err != nil:
		return false, false, err
	}
	return true, ended || !found, nil
}

func (s *Store) CancelDaemonProgramToolCallByKey(
	ctx context.Context, authority DaemonRuntimeAuthority, processID uuid.UUID, key, offerID string,
) error {
	if processID == uuid.Nil || key == "" {
		return errors.New("process and key are required")
	}
	_, _, err := daemonProgramUnit(ctx, s, "cancel_daemon_program_tool_call_by_key", authority, processID,
		func(h *agentexecution.Handle) (struct{}, error) {
			return struct{}{}, h.CancelProgramToolByKey(ctx, processID, key, offerID)
		})
	if errors.Is(err, storeerr.ErrProcessTerminal) || errors.Is(err, storeerr.ErrNotFound) {
		return nil
	}
	return err
}

func daemonProgramUnit[T any](
	ctx context.Context, s *Store, operation string, authority DaemonRuntimeAuthority, processID uuid.UUID,
	run func(*agentexecution.Handle) (T, error),
) (T, bool, error) {
	type result struct {
		value T
		found bool
	}
	done, err := agentexecution.Retry(ctx, operation, func(ctx context.Context) (result, error) {
		unit, err := s.cell.Begin(ctx)
		if err != nil {
			return result{}, err
		}
		defer func() { _ = unit.Rollback(ctx) }()
		q := dbsqlc.New(unit.DB())
		if err := requireReportableDaemonRuntimeAuthorityTx(ctx, q, authority); err != nil {
			return result{}, err
		}
		process, err := q.GetProcessByMachine(ctx, dbsqlc.GetProcessByMachineParams{
			OrgID: authority.OrgID, MachineID: authority.MachineID, ID: processID})
		if errors.Is(err, pgx.ErrNoRows) {
			return result{}, nil
		}
		if err != nil {
			return result{}, err
		}
		h, err := lockExecution(ctx, unit, process.ProjectID, process.AgentID, false, uuid.Nil)
		if err != nil {
			return result{}, err
		}
		value, err := run(h)
		if err != nil {
			return result{}, err
		}
		return result{value: value, found: true}, unit.Commit(ctx, operation)
	})
	return done.value, done.found, err
}
