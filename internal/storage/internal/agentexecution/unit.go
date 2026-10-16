package agentexecution

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/resourceguard"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

var ErrUnitClosed = errors.New("agent execution unit is closed")
var ErrUnitAborted = errors.New("agent execution unit requires rollback")
var ErrLockPlan = errors.New("agent execution lock plan must be restarted")

type Unit struct {
	creations         []resourceguard.Creation
	cell              *AgentCell
	expanded          retryPlan
	claimedAgent      uuid.UUID
	claimTiming       ClaimTiming
	tx                pgx.Tx
	notifications     *notifications.TxNotifications
	handles           map[uuid.UUID]*Handle
	exclusiveProjects map[uuid.UUID]struct{}
	runtimeLocked     bool
	closed            bool
	aborted           bool
	finalizing        bool
}

func newUnit(cell *AgentCell, tx pgx.Tx) *Unit {
	return &Unit{
		cell: cell, tx: tx, notifications: notifications.NewTxNotifications(),
		handles:           make(map[uuid.UUID]*Handle),
		exclusiveProjects: make(map[uuid.UUID]struct{}),
	}
}

func (u *Unit) DB() unitDB { return unitDB{unit: u} }

func (u *Unit) TrackCreation(creation resourceguard.Creation) {
	u.creations = append(u.creations, creation)
}

func (u *Unit) Notifications() *notifications.TxNotifications { return u.notifications }

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
	if u.finalizing {
		return errors.New("cannot commit during unit finalization")
	}
	u.finalizing = true
	defer func() {
		if err != nil {
			u.aborted = true
		}
	}()
	if err := u.publishExecution(ctx); err != nil {
		return err
	}
	if err := u.enqueueWebhooks(ctx); err != nil {
		return err
	}
	if err := resourceguard.CheckCreations(ctx, u.DB(), u.creations); err != nil {
		return err
	}
	if err := u.tx.Commit(ctx); err != nil {
		u.closed = true
		var postgres *pgconn.PgError
		knownAbort := errors.As(err, &postgres) &&
			(postgres.SeverityUnlocalized == "ERROR" || postgres.Severity == "ERROR") &&
			postgres.Code != "40003" && !strings.HasPrefix(postgres.Code, "08")
		if !knownAbort && !errors.Is(err, pgx.ErrTxCommitRollback) && !pgconn.SafeToRetry(err) {
			return fmt.Errorf("commit %s: %w", operation, errors.Join(storeerr.ErrCommitOutcomeUnknown, err))
		}
		return fmt.Errorf("commit %s: %w", operation, err)
	}
	u.closed = true
	u.notifications.Flush(context.WithoutCancel(ctx), u.cell.publisher)
	return nil
}

func (u *Unit) Rollback(ctx context.Context) error {
	if u.closed {
		return pgx.ErrTxClosed
	}
	u.closed = true
	return u.tx.Rollback(ctx)
}

func (u *Unit) orderedHandles() []*Handle {
	handles := slices.Collect(maps.Values(u.handles))
	slices.SortFunc(handles, func(a, b *Handle) int { return compareRoutes(a.route, b.route) })
	return handles
}
