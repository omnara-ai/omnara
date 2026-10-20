package processaction

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	MaxWaitMilliseconds        = 5 * 60 * 1000
	MaxPayloadWaitMilliseconds = 24 * 60 * 60 * 1000
	LegacyMaxWaitMilliseconds  = 10 * 1000
	LongWaitDaemonVersion      = "0.2.6"

	MaxObservationBytes     = 64 * 1024
	DefaultObservationBytes = 8 * 1024
	MaxWriteBytes           = 128 * 1024

	// Agent-facing process reads wait for exit instead of the first output
	// and return at most AgentOutputBytes, keeping the first AgentHeadBytes
	// and the end of longer output. Daemons older than OutputWindowDaemonVersion
	// ignore these settings.
	AgentOutputBytes          = 16 * 1024
	AgentHeadBytes            = 2 * 1024
	CommandWaitMilliseconds   = 10 * 1000
	PTYQuietMilliseconds      = 1000
	OutputWindowDaemonVersion = "0.2.9"
)

// Process output replaces NUL bytes and invalid UTF-8, which Postgres cannot
// store, with these symbols.
const (
	NULSymbol         = "\u2400"
	InvalidUTF8Symbol = "\uFFFD"
)

func MinOutputRawBytes(output string) int {
	return len(output) -
		(len(NULSymbol)-1)*strings.Count(output, NULSymbol) -
		(len(InvalidUTF8Symbol)-1)*strings.Count(output, InvalidUTF8Symbol)
}

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
	OutputWait
}

// OutputWait changes when a process read returns and which output it keeps.
// The zero value is the original behavior: return on the first output and
// keep the first bytes.
type OutputWait struct {
	// WaitForExit keeps waiting through output until the process ends or the
	// wait expires.
	WaitForExit bool `json:"wait_for_exit,omitempty"`
	// QuietMS, with WaitForExit, also returns once output has arrived and then
	// stopped for this long, as an interactive program does at a prompt.
	QuietMS int `json:"quiet_ms,omitempty"`
	// HeadBytes, when more output is available than fits, keeps this many
	// bytes from the start and fills the rest from the end.
	HeadBytes int `json:"head_bytes,omitempty"`
}

func (wait OutputWait) Validate(maxBytes int) error {
	if wait.QuietMS < 0 || wait.QuietMS > MaxWaitMilliseconds {
		return fmt.Errorf("quiet_ms must be between 0 and %d", MaxWaitMilliseconds)
	}
	if wait.QuietMS > 0 && !wait.WaitForExit {
		return errors.New("quiet_ms requires wait_for_exit")
	}
	if wait.HeadBytes < 0 || (wait.HeadBytes > 0 && wait.HeadBytes >= maxBytes) {
		return fmt.Errorf("head_bytes must be between 0 and %d", maxBytes-1)
	}
	return nil
}

func (wait OutputWait) Zero() bool {
	return wait == OutputWait{}
}

type ReadObservation struct {
	ProcessID  string `json:"process_id"`
	Output     string `json:"output"`
	Cursor     int64  `json:"cursor"`
	NextCursor int64  `json:"next_cursor"`
	Truncated  bool   `json:"truncated"`
	OutputTail string `json:"output_tail,omitempty"`
	// OmittedCursor and TailCursor bound the bytes left out between Output,
	// the head of the range, and OutputTail. Both are zero when nothing is.
	OmittedCursor int64 `json:"omitted_cursor,omitempty"`
	TailCursor    int64 `json:"tail_cursor,omitempty"`
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
	if payload.WaitMS < 0 || payload.WaitMS > MaxPayloadWaitMilliseconds {
		return ReadPayload{}, fmt.Errorf(
			"wait_ms must be between 0 and %d",
			MaxPayloadWaitMilliseconds,
		)
	}
	if err := payload.OutputWait.Validate(payload.ObservationBytes()); err != nil {
		return ReadPayload{}, err
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
