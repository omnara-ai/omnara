package resourcemeta

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

const (
	MaxEntries     = 16
	MaxKeyLength   = 64
	MaxValueLength = 512
)

type Metadata map[string]string

func (m Metadata) Validate() error {
	if len(m) > MaxEntries {
		return fmt.Errorf("metadata cannot have more than %d entries", MaxEntries)
	}
	for key, value := range m {
		if key == "" || utf8.RuneCountInString(key) > MaxKeyLength {
			return fmt.Errorf("metadata keys must be 1-%d characters", MaxKeyLength)
		}
		if utf8.RuneCountInString(value) > MaxValueLength {
			return fmt.Errorf("metadata values must be at most %d characters", MaxValueLength)
		}
	}
	return nil
}

func (m Metadata) JSON() (json.RawMessage, error) {
	if m == nil {
		m = Metadata{}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode metadata: %w", err)
	}
	return raw, nil
}

func FromJSON(raw json.RawMessage) (Metadata, error) {
	metadata := Metadata{}
	if len(raw) == 0 || string(raw) == "null" {
		return metadata, nil
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil, fmt.Errorf("decode metadata: %w", err)
	}
	return metadata, nil
}
