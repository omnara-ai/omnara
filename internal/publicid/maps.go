package publicid

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

func DecodeMapJSON(kind Kind, raw json.RawMessage) (json.RawMessage, error) {
	return mapJSON(raw, func(value string) (string, error) {
		id, err := Decode(kind, value)
		return id.String(), err
	})
}

func EncodeMapJSON(kind Kind, raw json.RawMessage) (json.RawMessage, error) {
	return mapJSON(raw, func(value string) (string, error) {
		id, err := uuid.Parse(value)
		if err != nil {
			return "", err
		}
		return Encode(kind, id)
	})
}

func mapJSON(raw json.RawMessage, convert func(string) (string, error)) (json.RawMessage, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	var values map[string]*string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	for key, value := range values {
		if value == nil {
			continue
		}
		converted, err := convert(*value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		values[key] = &converted
	}
	return json.Marshal(values)
}
