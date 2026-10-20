package agentexecution

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/executionpolicy"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

type transactionBeginner interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

type AgentCell struct {
	pool         transactionBeginner
	publisher    notifications.PostCommitPublisher
	retryBackoff func(int, string) time.Duration
}

func NewCell(
	pool *pgxpool.Pool,
	publisher notifications.PostCommitPublisher,
	retryBackoff func(int, string) time.Duration,
) *AgentCell {
	if retryBackoff == nil {
		retryBackoff = modelprotocol.RetryBackoff
	}
	return &AgentCell{
		pool:         storeutil.WrapPool(pool),
		publisher:    publisher,
		retryBackoff: retryBackoff,
	}
}

func (c *AgentCell) Begin(ctx context.Context) (*Unit, error) {
	plan, ok := ctx.Value(lockPlanKey{}).(retryPlan)
	if !ok {
		return nil, errors.New("agent execution unit requires a retry boundary")
	}
	tx, err := c.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin execution: %w", err)
	}
	u := newUnit(c, tx)
	u.expanded = plan
	return u, nil
}

func (c *AgentCell) DeferClaim(ctx context.Context, agentID uuid.UUID) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tx, err := c.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := executiondb.New().DeferExecutionClaim(ctx, tx, executiondb.DeferExecutionClaimParams{
		AgentID: agentID, DelaySeconds: int64(executionpolicy.ClaimFailureDelay / time.Second),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
