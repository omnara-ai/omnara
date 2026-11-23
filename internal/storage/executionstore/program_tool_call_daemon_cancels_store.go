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

// CancelDaemonProgramToolCall ends a call a daemon's command stopped waiting for, even one a server is
// still running: that server loses its claim and stops. A custom call waiting for its result stays
// pending. It reports true once nothing of the call is left to end.
func (s *Store) CancelDaemonProgramToolCall(
	ctx context.Context, authority DaemonRuntimeAuthority, ref ProgramToolCallRef,
) (bool, error) {
	if err := ref.validate(); err != nil {
		return false, err
	}
	done, found, err := daemonProgramUnit(ctx, s, "cancel_daemon_program_tool_call", authority, ref.ProcessID,
		func(h *agentexecution.Handle) (bool, error) {
			return h.CancelProgramTool(ctx, ref.ref())
		})
	switch {
	case errors.Is(err, storeerr.ErrProcessTerminal), errors.Is(err, storeerr.ErrNotFound):
		return true, nil
	case err != nil:
		return false, err
	}
	return done || !found, nil
}

// CancelDaemonProgramToolCallByKey ends the call a key of a daemon's command names, for a cancel of
// an offer made on an earlier connection. A call a later offer of the key has made or picked up since
// is not that offer's to end. It succeeds only once nothing of the offer's call is left to end.
// The call may still be open where no new one could start, such as after code mode is turned off,
// so the process need only belong to the daemon's machine.
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

// daemonProgramUnit runs a daemon's change to a call of one of its machine's processes. The daemon's
// runtime must still be able to report: a revoked token is refused with ErrDaemonRuntimeUnregistered,
// while a runtime whose lease expired can still clean up. found is false, with nothing run, when the
// process is not one of the machine's.
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
