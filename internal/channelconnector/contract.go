package channelconnector

import (
	"encoding/json"
	"fmt"

	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
)

const MaxMetadataBytes = 256 * 1024

// NormalizeOpaqueObject applies the shared connector wire contract before an
// opaque provider object can reach PostgreSQL or be nested in persisted data.
func NormalizeOpaqueObject(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	object, err := jsoncanonical.ParseObject(raw, MaxMetadataBytes)
	if err != nil {
		return nil, err
	}
	normalized, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	if err := dbsafe.JSONB(normalized, MaxMetadataBytes); err != nil {
		return nil, fmt.Errorf("PostgreSQL-safe JSON: %w", err)
	}
	return normalized, nil
}
