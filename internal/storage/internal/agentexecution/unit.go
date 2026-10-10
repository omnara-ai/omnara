package agentexecution

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/notifications"
)

var ErrUnitClosed = errors.New("agent execution unit is closed")
var ErrUnitAborted = errors.New("agent execution unit requires rollback")
var ErrLockPlan = errors.New("agent execution lock plan must be restarted")

type Unit struct {
	cell              *AgentCell
	tx                pgx.Tx
	notifications     *notifications.TxNotifications
	handles           map[uuid.UUID]*Handle
	webhooks          map[uuid.UUID]webhookTarget
	exclusiveProjects map[uuid.UUID]struct{}
	runtimeLocked     bool
	closed            bool
	aborted           bool
	finalizing        bool
	savepoints        int
}

func newUnit(cell *AgentCell, tx pgx.Tx) *Unit {
	return &Unit{
		cell: cell, tx: tx, notifications: notifications.NewTxNotifications(),
		handles: make(map[uuid.UUID]*Handle), webhooks: make(map[uuid.UUID]webhookTarget),
		exclusiveProjects: make(map[uuid.UUID]struct{}),
	}
}

func (u *Unit) DB() unitDB { return unitDB{unit: u} }

func (u *Unit) Notifications() *notifications.TxNotifications { return u.notifications }

func (u *Unit) CellID() CellID { return u.cell.id }

func (u *Unit) active() error {
	if u.closed {
		return ErrUnitClosed
	}
	if u.aborted {
		return ErrUnitAborted
	}
	return nil
}

func (u *Unit) Commit(ctx context.Context, operation string) (err error) {
	if err := u.active(); err != nil {
		return err
	}
	if u.savepoints != 0 || u.finalizing {
		return errors.New("cannot commit inside unit savepoint or finalization")
	}
	u.finalizing = true
	defer func() {
		if err != nil {
			u.aborted = true
			err = fmt.Errorf("commit %s: %w", operation, err)
		}
	}()
	if err := u.publishExecution(ctx); err != nil {
		return err
	}
	if err := u.enqueueWebhooks(ctx); err != nil {
		return err
	}
	if err := u.tx.Commit(ctx); err != nil {
		u.closed = true
		return err
	}
	u.closed = true
	u.notifications.Flush(context.WithoutCancel(ctx), u.cell.publisher)
	return nil
}

func (u *Unit) Rollback(ctx context.Context) error {
	if u.closed {
		return pgx.ErrTxClosed
	}
	if u.savepoints != 0 {
		return errors.New("use savepoint rollback inside a unit savepoint")
	}
	u.closed = true
	return u.tx.Rollback(ctx)
}

func (u *Unit) orderedHandles() []*Handle {
	handles := slices.Collect(maps.Values(u.handles))
	slices.SortFunc(handles, func(a, b *Handle) int { return compareRoutes(a.route, b.route) })
	return handles
}

func (u *Unit) Savepoint(ctx context.Context, run func(*Unit) error) (err error) {
	if err := u.active(); err != nil {
		return err
	}
	if u.finalizing {
		return errors.New("cannot create savepoint during finalization")
	}
	parent := u.tx
	nested, err := parent.Begin(ctx)
	if err != nil {
		return err
	}
	snapshot := u.snapshot()
	u.tx = nested
	u.savepoints++
	released := false
	defer func() {
		u.savepoints--
		u.tx = parent
		if !released {
			rollbackErr := nested.Rollback(context.WithoutCancel(ctx))
			u.restore(snapshot)
			if rollbackErr != nil {
				u.aborted = true
				err = errors.Join(err, fmt.Errorf("rollback agent execution savepoint: %w", rollbackErr))
			}
		}
	}()
	if err := run(u); err != nil {
		return err
	}
	if err := u.active(); err != nil {
		return err
	}
	if err := nested.Commit(ctx); err != nil {
		return err
	}
	released = true
	return nil
}

type unitSnapshot struct {
	handles           map[uuid.UUID]*Handle
	states            map[uuid.UUID]Handle
	notifications     *notifications.TxNotifications
	webhooks          map[uuid.UUID]webhookTarget
	exclusiveProjects map[uuid.UUID]struct{}
	runtimeLocked     bool
}

func (u *Unit) snapshot() unitSnapshot {
	s := unitSnapshot{
		handles: maps.Clone(u.handles), states: make(map[uuid.UUID]Handle),
		notifications: u.notifications.Clone(), webhooks: maps.Clone(u.webhooks),
		exclusiveProjects: maps.Clone(u.exclusiveProjects),
		runtimeLocked:     u.runtimeLocked,
	}
	for id, h := range u.handles {
		state := *h
		if h.mutation != nil {
			state.mutation = h.mutation.clone()
		}
		s.states[id] = state
	}
	for id, target := range s.webhooks {
		target.events = slices.Clone(target.events)
		s.webhooks[id] = target
	}
	return s
}

func (u *Unit) restore(snapshot unitSnapshot) {
	for id, h := range u.handles {
		if snapshot.handles[id] != h {
			h.valid = false
		}
	}
	u.handles = snapshot.handles
	for id, h := range u.handles {
		*h = snapshot.states[id]
	}
	u.notifications.Restore(snapshot.notifications)
	u.webhooks = snapshot.webhooks
	u.exclusiveProjects = snapshot.exclusiveProjects
	u.runtimeLocked = snapshot.runtimeLocked
	u.aborted = false
}
