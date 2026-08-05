package executionstore

import (
	"encoding/json"
	"fmt"

	"github.com/omnara-ai/omnara/internal/jsoncanonical"
)

func normalizedJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	return value
}

func normalizedJSONArray(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`[]`)
	}
	return value
}

func normalizedJSONObject(value json.RawMessage, fieldName string) (json.RawMessage, error) {
	value = normalizedJSON(value)
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil {
		return nil, fmt.Errorf("parse %s: %w", fieldName, err)
	}
	if object == nil {
		return nil, fmt.Errorf("%s must be a JSON object", fieldName)
	}
	return value, nil
}

func marshalJSON(value any) (json.RawMessage, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func sameJSON(a, b json.RawMessage) bool {
	return jsoncanonical.Equal(a, b)
}
