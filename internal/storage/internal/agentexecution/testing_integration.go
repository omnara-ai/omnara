//go:build integration

package agentexecution

import (
	"context"
	"github.com/jackc/pgx/v5"
	"io"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
)

func BeginTestUnit(ctx context.Context, cell *AgentCell) (*Unit, error) {
	return ReplanTest(ctx, cell.Begin)
}

func ReplanTest[T any](ctx context.Context, run func(context.Context) (T, error)) (T, error) {
	if _, nested := ctx.Value(lockPlanKey{}).(retryPlan); nested {
		return run(ctx)
	}
	return replan(ctx, run)
}

func LockTestAgent(
	ctx context.Context, unit *Unit, projectID, agentID uuid.UUID, authority *RuntimeAuthority,
) (*Handle, error) {
	refs := []lifecyclelock.AgentRef{{ProjectID: projectID, AgentID: agentID}}
	if err := unit.LockAgentRefs(ctx, refs, authority); err != nil {
		return nil, err
	}
	return unit.Handle(projectID, agentID)
}

func CapacityExemptForTest(input ReceiveContentInput) ReceiveContentInput {
	input.capacityExempt = true
	return input
}

func LoseTestCommitAcknowledgement(unit *Unit, committed bool) {
	unit.tx = lostTestCommit{Tx: unit.tx, committed: committed}
}

type lostTestCommit struct {
	pgx.Tx
	committed bool
}

func (tx lostTestCommit) Commit(ctx context.Context) error {
	if tx.committed {
		if err := tx.Tx.Commit(ctx); err != nil {
			return err
		}
	} else {
		if err := tx.Tx.Rollback(ctx); err != nil {
			return err
		}
	}
	return io.ErrUnexpectedEOF
}

func (c *AgentCell) WrapTransactionsForTest(wrap func(pgx.Tx) pgx.Tx) {
	c.pool = wrappedTestTransactions{transactionBeginner: c.pool, wrap: wrap}
}

type wrappedTestTransactions struct {
	transactionBeginner
	wrap func(pgx.Tx) pgx.Tx
}

func (p wrappedTestTransactions) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	tx, err := p.transactionBeginner.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return p.wrap(tx), nil
}
