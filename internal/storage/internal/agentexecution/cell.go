package agentexecution

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

type AgentCell struct {
	pool         *storeutil.Pool
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
