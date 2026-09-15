package channelconnector

import (
	"encoding/json"
	"errors"
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
	if len(raw) > MaxMetadataBytes {
		return nil, fmt.Errorf("JSON object exceeds the %d-byte limit", MaxMetadataBytes)
	}
	normalized, err := jsoncanonical.Normalize(raw)
	if err != nil {
		return nil, err
	}
	if len(normalized) > MaxMetadataBytes {
		return nil, fmt.Errorf("JSON object exceeds the %d-byte limit", MaxMetadataBytes)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(normalized, &object); err != nil || object == nil {
		if err == nil {
			err = errors.New("must be a JSON object")
		}
		return nil, err
	}
	if err := dbsafe.JSONB(normalized, MaxMetadataBytes); err != nil {
		return nil, fmt.Errorf("PostgreSQL-safe JSON: %w", err)
	}
	return normalized, nil
}
