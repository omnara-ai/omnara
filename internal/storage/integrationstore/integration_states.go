package integrationstore

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

const integrationStateMaxDataBytes = 2 * 1024 * 1024

func encodeIntegrationState(value any) (json.RawMessage, error) {
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	// HTML escaping can push an otherwise valid event over the byte limit.
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	raw := bytes.TrimSpace(data.Bytes())
	if len(raw) == 0 || raw[0] != '{' || len(raw) > integrationStateMaxDataBytes {
		return nil, inboxInvalid("integration state must be a bounded JSON object")
	}
	return raw, nil
}

func (s *Store) CleanupIntegrationStates(ctx context.Context, choiceRetention time.Duration, limit int) (int64, error) {
	if choiceRetention < IntegrationProfileChoiceMinRetention {
		return 0, inboxInvalid("profile choices must be retained for at least seven days beyond expiry")
	}
	if err := validateInboxBatch(limit); err != nil {
		return 0, err
	}
	// Prioritize deleted owners so expired-menu traffic cannot starve teardown.
	deleted, err := s.q.CleanupDeletedIntegrationStates(ctx, dbsqlc.CleanupDeletedIntegrationStatesParams{
		RowLimit: int32(limit),
	})
	if err != nil || deleted == int64(limit) {
		return deleted, err
	}
	expired, err := s.q.CleanupExpiredIntegrationProfileChoices(ctx, dbsqlc.CleanupExpiredIntegrationProfileChoicesParams{
		RetentionMilliseconds: choiceRetention.Milliseconds(), RowLimit: int32(int64(limit) - deleted),
	})
	return deleted + expired, err
}
