package agentexecution

import (
	"context"
	"errors"
	"time"

	"github.com/omnara-ai/omnara/internal/modelprotocol"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

type AgentCell struct {
	id           CellID
	pool         *storeutil.Pool
	publisher    notifications.PostCommitPublisher
	retryBackoff func(int, string) time.Duration
}

func NewCell(
	id CellID,
	pool *pgxpool.Pool,
	publisher notifications.PostCommitPublisher,
	retryBackoff func(int, string) time.Duration,
) *AgentCell {
	if retryBackoff == nil {
		retryBackoff = modelprotocol.RetryBackoff
	}
	return &AgentCell{
		id:           id,
		pool:         storeutil.WrapPool(pool),
		publisher:    publisher,
		retryBackoff: retryBackoff,
	}
}

func (c *AgentCell) ID() CellID { return c.id }

func (c *AgentCell) Begin(ctx context.Context) (*Unit, error) {
	if c.id == "" {
		return nil, errors.New("agent cell identity is required")
	}
	tx, err := c.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	return newUnit(c, tx), nil
}

func (c *AgentCell) Transact(ctx context.Context, run func(*Unit) error) error {
	u, err := c.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = u.Rollback(context.WithoutCancel(ctx)) }()
	if err := run(u); err != nil {
		return err
	}
	return u.Commit(ctx, "agent execution")
}
