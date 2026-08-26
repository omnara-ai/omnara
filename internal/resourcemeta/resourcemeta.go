package resourcemeta

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
		if err := ValidateEntry(key, value); err != nil {
			return err
		}
	}
	return nil
}

func (m Metadata) ValidateWithReservedKey(reserved string) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if len(m) >= MaxEntries {
		return fmt.Errorf(
			"metadata cannot have more than %d entries; the %s key is reserved",
			MaxEntries-1, reserved,
		)
	}
	if _, ok := m[reserved]; ok {
		return fmt.Errorf("metadata key %q is reserved", reserved)
	}
	return nil
}

func ValidateEntry(key, value string) error {
	if err := validateMetadataString(key); err != nil {
		return fmt.Errorf("metadata key %w", err)
	}
	if err := validateMetadataString(value); err != nil {
		return fmt.Errorf("metadata value %w", err)
	}
	if key == "" || utf8.RuneCountInString(key) > MaxKeyLength {
		return fmt.Errorf("metadata keys must be 1-%d characters", MaxKeyLength)
	}
	if utf8.RuneCountInString(value) > MaxValueLength {
		return fmt.Errorf("metadata values must be at most %d characters", MaxValueLength)
	}
	return nil
}

func validateMetadataString(value string) error {
	if !utf8.ValidString(value) {
		return errors.New("contains invalid UTF-8")
	}
	if strings.IndexByte(value, 0) >= 0 {
		return errors.New("contains U+0000")
	}
	return nil
}

func (m Metadata) JSON() (json.RawMessage, error) {
	if m == nil {
		m = Metadata{}
	}
	if err := m.Validate(); err != nil {
		return nil, err
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
	if err := metadata.Validate(); err != nil {
		return nil, err
	}
	return metadata, nil
}
