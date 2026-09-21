package integrationstore

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

const appStateMaxDataBytes = 2 * 1024 * 1024

// encodeAppState is the document boundary shared by app workflows. SQL owns
// identity and revision checks; each workflow validates its typed value before
// encoding and composes the shared queries inside its own storage transaction.
func encodeAppState(value any) (json.RawMessage, error) {
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	// Preserve the existing event limit without expanding HTML in RawMessage.
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	raw := bytes.TrimSpace(data.Bytes())
	if len(raw) == 0 || raw[0] != '{' || len(raw) > appStateMaxDataBytes {
		return nil, inboxInvalid("app state must be a bounded JSON object")
	}
	return raw, nil
}

// CleanupAppStates applies chooser retention and reclaims all state for deleted
// owners. Other kinds keep their own deadline/retention policy; a deadline alone
// never authorizes deletion of an arbitrary workflow.
func (s *Store) CleanupAppStates(ctx context.Context, choiceRetention time.Duration, limit int) (int64, error) {
	if choiceRetention < AppProfileChoiceMinRetention {
		return 0, inboxInvalid("profile choices must be retained for at least seven days beyond expiry")
	}
	if err := validateInboxBatch(limit); err != nil {
		return 0, err
	}
	// Drain deleted owners first so a continuous stream of expired menus cannot
	// starve them. Both independent sweeps share one deletion budget.
	deleted, err := s.q.CleanupDeletedAppStates(ctx, dbsqlc.CleanupDeletedAppStatesParams{RowLimit: int32(limit)})
	if err != nil || deleted == int64(limit) {
		return deleted, err
	}
	expired, err := s.q.CleanupExpiredAppProfileChoices(ctx, dbsqlc.CleanupExpiredAppProfileChoicesParams{
		RetentionMilliseconds: choiceRetention.Milliseconds(), RowLimit: int32(int64(limit) - deleted),
	})
	return deleted + expired, err
}
