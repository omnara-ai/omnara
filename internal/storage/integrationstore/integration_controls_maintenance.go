package integrationstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

// OldestPendingIntegrationControl returns the oldest unapplied work for internal
// maintenance observability. It includes leased/retrying receipts, with no data
// or child authority projection. Nil means no pending or processing receipts.
func (s *Store) OldestPendingIntegrationControl(ctx context.Context) (*time.Time, error) {
	createdAt, err := s.q.OldestPendingIntegrationControl(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil //nolint:nilnil // No pending work is a successful, empty observation.
	}
	if err != nil {
		return nil, fmt.Errorf("read oldest pending integration control: %w", err)
	}
	return &createdAt, nil
}

// FailUnprocessableIntegrationControls scans at most limit nonterminal rows,
// including healthy or locked candidates, with a durable bounded sweep cursor.
// Only unavailable owners fail; transient failures have no claim/age ceiling.
// Unexpired claims retain their opportunity to finish.
func (s *Store) FailUnprocessableIntegrationControls(ctx context.Context, limit int) (int64, error) {
	if err := validateRowLimit(limit); err != nil {
		return 0, err
	}
	count, err := s.q.FailUnprocessableIntegrationControls(ctx, dbsqlc.FailUnprocessableIntegrationControlsParams{
		RowLimit: int32(limit),
	})
	if err != nil {
		return 0, fmt.Errorf("fail unprocessable integration controls: %w", err)
	}
	return count, nil
}

// DeleteRetainedIntegrationControls expires only terminal receipts. Deduplication
// ends with retention; pending work remains eligible for eventual recovery.
func (s *Store) DeleteRetainedIntegrationControls(
	ctx context.Context, input DeleteRetainedIntegrationControlsInput,
) (int64, error) {
	if input.Retention.Microseconds() <= 0 {
		return 0, errors.New("positive control retention of at least one microsecond is required")
	}
	if err := validateRowLimit(input.Limit); err != nil {
		return 0, err
	}
	count, err := s.q.DeleteRetainedIntegrationControls(ctx, dbsqlc.DeleteRetainedIntegrationControlsParams{
		RetentionMicroseconds: input.Retention.Microseconds(), RowLimit: int32(input.Limit),
	})
	if err != nil {
		return 0, fmt.Errorf("delete retained integration controls: %w", err)
	}
	return count, nil
}
