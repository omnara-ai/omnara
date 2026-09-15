package integrationstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

// MaxIntegrationEventAttempts counts all claims, including claims whose consumer
// crashed. Claiming enforces this ceiling independently of maintenance timing.
const MaxIntegrationEventAttempts = 32

// FailUnprocessableIntegrationEvents examines at most limit nonterminal receipts
// in a durable indexed sweep, terminalizing exhausted or unavailable-owner work.
// Healthy and locked receipts consume the page budget; later cycles revisit
// them. Unexpired claims retain their opportunity to complete. Zero failed rows
// can mean the cursor advanced over healthy work, not that the sweep is finished.
// No payloads are returned or dispatched by maintenance.
func (s *Store) FailUnprocessableIntegrationEvents(ctx context.Context, limit int) (int64, error) {
	if err := validateRowLimit(limit); err != nil {
		return 0, err
	}
	count, err := s.q.FailUnprocessableIntegrationEvents(ctx, dbsqlc.FailUnprocessableIntegrationEventsParams{
		MaxAttempts: MaxIntegrationEventAttempts, RowLimit: int32(limit),
	})
	if err != nil {
		return 0, fmt.Errorf("fail unprocessable integration events: %w", err)
	}
	return count, nil
}

type DeleteRetainedIntegrationEventsInput struct {
	Retention time.Duration
	Limit     int
}

// DeleteRetainedIntegrationEvents removes only terminal receipts older than the
// completion-based retention window. After deletion, receipt-level deduplication
// for that event identity ends; previously admitted agent inputs are unchanged.
func (s *Store) DeleteRetainedIntegrationEvents(
	ctx context.Context,
	input DeleteRetainedIntegrationEventsInput,
) (int64, error) {
	if input.Retention.Microseconds() <= 0 {
		return 0, errors.New("positive event retention of at least one microsecond is required")
	}
	if err := validateRowLimit(input.Limit); err != nil {
		return 0, err
	}
	count, err := s.q.DeleteRetainedIntegrationEvents(ctx, dbsqlc.DeleteRetainedIntegrationEventsParams{
		RetentionMicroseconds: input.Retention.Microseconds(), RowLimit: int32(input.Limit),
	})
	if err != nil {
		return 0, fmt.Errorf("delete retained integration events: %w", err)
	}
	return count, nil
}
