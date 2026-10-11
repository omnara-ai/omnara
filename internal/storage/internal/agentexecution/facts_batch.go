package agentexecution

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
)

type factsBatch struct {
	executiondb.DBTX
	queued  pgx.Batch
	results pgx.BatchResults
	readers []func()
	err     error
}

type factsResults struct{ batch *factsBatch }

func (b *factsBatch) SendBatch(_ context.Context, queries *pgx.Batch) pgx.BatchResults {
	b.queued.QueuedQueries = append(b.queued.QueuedQueries, queries.QueuedQueries...)
	return factsResults{batch: b}
}
func (r factsResults) Query() (pgx.Rows, error) {
	rows, err := r.batch.results.Query()
	if err != nil {
		return nil, err
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	return rows, nil
}
func (r factsResults) QueryRow() pgx.Row                { return r.batch.results.QueryRow() }
func (r factsResults) Exec() (pgconn.CommandTag, error) { return r.batch.results.Exec() }
func (r factsResults) Close() error                     { return nil }

func (b *factsBatch) read(ctx context.Context) error {
	if b.queued.Len() == 0 {
		return nil
	}
	b.results = b.DBTX.SendBatch(ctx, &b.queued)
	defer func() { _ = b.results.Close() }()
	for _, read := range b.readers {
		read()
	}
	return errors.Join(b.err, b.results.Close())
}

type executionFacts struct {
	scope       executiondb.LoadExecutionFactsRow
	contexts    []executiondb.LoadExecutionContextsRow
	configs     []executiondb.LoadExecutionPendingConfigRow
	checkpoints []executiondb.LoadExecutionPendingCheckpointRow
}

func (b *factsBatch) queue(ctx context.Context, route AgentRoute, head ExecutionHead) *executionFacts {
	facts := &executionFacts{}
	q := executiondb.New()
	scope := q.LoadExecutionFacts(ctx,
		b,
		[]executiondb.LoadExecutionFactsParams{{AgentID: route.AgentID,
			ProjectID:               route.ProjectID,
			TurnID:                  nullableID(head.CurrentTurnID),
			StopSequence:            head.StopSequence,
			MaxContextInputSequence: head.MaxContextInputSequence,
			ToolOutputID:            nullableID(head.PendingToolOutputID)}})
	b.readers = append(b.readers, func() {
		scope.QueryRow(func(_ int, row executiondb.LoadExecutionFactsRow, err error) {
			facts.scope = row
			b.err = errors.Join(b.err, err)
		})
		b.err = errors.Join(b.err, scope.Close())
	})
	if head.NormalContextID != uuid.Nil ||
		head.CompactionContextID != uuid.Nil ||
		head.PendingToolOutputID != uuid.Nil ||
		head.PendingOutputLimitID != uuid.Nil {
		rows := q.LoadExecutionContexts(ctx, b, []executiondb.LoadExecutionContextsParams{{AgentID: route.AgentID,
			NormalContextID: nullableID(head.NormalContextID), CompactionContextID: nullableID(head.CompactionContextID),
			ToolOutputID: nullableID(head.PendingToolOutputID), LimitOutputID: nullableID(head.PendingOutputLimitID)}})
		b.readers = append(b.readers, func() {
			rows.Query(func(_ int, rows []executiondb.LoadExecutionContextsRow, err error) {
				facts.contexts = rows
				b.err = errors.Join(b.err, err)
			})
			b.err = errors.Join(b.err, rows.Close())
		})
	}
	if head.PendingConfigInputID != uuid.Nil {
		rows := q.LoadExecutionPendingConfig(ctx,
			b,
			[]executiondb.LoadExecutionPendingConfigParams{{AgentID: route.AgentID,
				ID: head.PendingConfigInputID}})
		b.readers = append(b.readers, func() {
			rows.Query(func(_ int, rows []executiondb.LoadExecutionPendingConfigRow, err error) {
				facts.configs = rows
				b.err = errors.Join(b.err, err)
			})
			b.err = errors.Join(b.err, rows.Close())
		})
	}
	if head.PendingCheckpointID != uuid.Nil {
		rows := q.LoadExecutionPendingCheckpoint(ctx,
			b,
			[]executiondb.LoadExecutionPendingCheckpointParams{{AgentID: route.AgentID,
				ID: head.PendingCheckpointID}})
		b.readers = append(b.readers, func() {
			rows.Query(func(_ int, rows []executiondb.LoadExecutionPendingCheckpointRow, err error) {
				facts.checkpoints = rows
				b.err = errors.Join(b.err, err)
			})
			b.err = errors.Join(b.err, rows.Close())
		})
	}
	return facts
}
