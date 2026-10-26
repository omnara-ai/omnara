package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

type ExecutionHeadSample struct {
	AgentID     uuid.UUID
	ProjectID   uuid.UUID
	Differences []agentexecution.HeadDifference
	Err         error
}

type ExecutionHeadSampleReport struct {
	Samples []ExecutionHeadSample
	Cursor  uuid.UUID
}

func (s *Store) SampleExecutionHeads(ctx context.Context, after uuid.UUID, n int32) (ExecutionHeadSampleReport, error) {
	routes, err := readExecutionSnapshot(ctx, s.pool, "sample execution head",
		func(tx pgx.Tx) ([]agentexecution.AgentRoute, error) {
			return agentexecution.SelectExecutionSamples(ctx, tx, after, n)
		})
	if err != nil {
		return ExecutionHeadSampleReport{Cursor: after}, err
	}
	report := ExecutionHeadSampleReport{Cursor: after}
	for _, route := range routes {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		differences, err := readExecutionSnapshot(ctx, s.pool, "sample execution head",
			func(tx pgx.Tx) ([]agentexecution.HeadDifference, error) {
				return agentexecution.CompareExecutionHead(ctx, tx, route)
			})
		report.Cursor = route.AgentID
		if errors.Is(err, agentexecution.ErrSampleIneligible) {
			continue
		}
		report.Samples = append(report.Samples, ExecutionHeadSample{
			AgentID: route.AgentID, ProjectID: route.ProjectID, Differences: differences, Err: err})
	}
	return report, ctx.Err()
}

func readExecutionSnapshot[T any](
	ctx context.Context, pool *storeutil.Pool, operation string, read func(pgx.Tx) (T, error),
) (T, error) {
	return storeutil.RetryTransaction(ctx, operation, func() (T, error) {
		var zero T
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			return zero, err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err = tx.Exec(ctx, "SET LOCAL statement_timeout = '2s'; SET LOCAL transaction_timeout = '5s'"); err != nil {
			return zero, err
		}
		result, err := read(tx)
		if err != nil {
			return zero, fmt.Errorf("%s: %w", operation, err)
		}
		return result, tx.Commit(ctx)
	})
}
