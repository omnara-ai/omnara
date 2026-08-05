package processaction

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	MaxWaitMilliseconds     = 10 * 1000
	MaxObservationBytes     = 64 * 1024
	DefaultObservationBytes = 8 * 1024
	MaxWriteBytes           = 128 * 1024
)

type Kind string

const (
	KindWrite     Kind = "write"
	KindRead      Kind = "read"
	KindInterrupt Kind = "interrupt"
	KindTerminate Kind = "terminate"
)

func (kind Kind) Valid() bool {
	switch kind {
	case KindWrite, KindRead, KindInterrupt, KindTerminate:
		return true
	default:
		return false
	}
}

func (kind Kind) Mutating() bool {
	switch kind {
	case KindWrite, KindInterrupt, KindTerminate:
		return true
	default:
		return false
	}
}

type WritePayload struct {
	CloseStdin bool   `json:"close_stdin,omitempty"`
	Data       string `json:"data,omitempty"`
}

func DecodeWritePayload(raw json.RawMessage) (WritePayload, error) {
	var payload WritePayload
	if err := decodeObject(raw, &payload); err != nil {
		return WritePayload{}, err
	}
	if payload.Data == "" && !payload.CloseStdin {
		return WritePayload{}, errors.New(
			"write payload requires non-empty data or close_stdin=true",
		)
	}
	if len(payload.Data) > MaxWriteBytes {
		return WritePayload{}, fmt.Errorf(
			"write data cannot exceed %d bytes",
			MaxWriteBytes,
		)
	}
	return payload, nil
}

type ReadPayload struct {
	Cursor   *int64 `json:"cursor,omitempty"`
	MaxBytes int    `json:"max_bytes,omitempty"`
	WaitMS   int    `json:"wait_ms,omitempty"`
}

type ReadObservation struct {
	ProcessID  string `json:"process_id"`
	Output     string `json:"output"`
	Cursor     int64  `json:"cursor"`
	NextCursor int64  `json:"next_cursor"`
	Truncated  bool   `json:"truncated"`
}

func DecodeReadPayload(raw json.RawMessage) (ReadPayload, error) {
	var payload ReadPayload
	if err := decodeObject(raw, &payload); err != nil {
		return ReadPayload{}, err
	}
	if payload.Cursor != nil && *payload.Cursor < 0 {
		return ReadPayload{}, errors.New("cursor cannot be negative")
	}
	if payload.MaxBytes < 0 || payload.MaxBytes > MaxObservationBytes {
		return ReadPayload{}, fmt.Errorf(
			"max_bytes must be between 0 and %d",
			MaxObservationBytes,
		)
	}
	if payload.WaitMS < 0 || payload.WaitMS > MaxWaitMilliseconds {
		return ReadPayload{}, fmt.Errorf(
			"wait_ms must be between 0 and %d",
			MaxWaitMilliseconds,
		)
	}
	return payload, nil
}

func (payload ReadPayload) ObservationBytes() int {
	if payload.MaxBytes == 0 {
		return DefaultObservationBytes
	}
	return payload.MaxBytes
}

func DecodeEmptyPayload(raw json.RawMessage) error {
	return decodeObject(raw, &struct{}{})
}

func decodeObject(raw json.RawMessage, dst any) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if fields == nil {
		return errors.New("payload must be a JSON object")
	}
	for name, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("payload field %s cannot be null", name)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil || !errors.Is(err, io.EOF) {
		return errors.New("payload must contain one JSON object")
	}
	return nil
}
