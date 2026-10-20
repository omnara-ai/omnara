package processresult

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/processaction"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
)

type Process struct {
	ID                  uuid.UUID
	State               string
	DefaultOutputCursor int64
	SourceStartedAt     *time.Time
	SourceEndedAt       *time.Time
	ExitCode            *int
	ExitSignal          string
	StateReasonCode     string
	StateReasonMessage  string
	ExecutionSpec       processcmd.ExecutionSpec
}
type Action struct {
	ID      uuid.UUID
	Payload json.RawMessage
}

// observation is process output from a daemon, or the same fields in a result
// built from one. Output runs from Cursor; when TailCursor is set, Output ends
// at OmittedCursor and OutputTail runs from TailCursor to NextCursor.
type observation struct {
	ProcessID     *string
	Output        *string
	Cursor        int64
	NextCursor    *int64
	Truncated     bool
	OutputTail    string
	OmittedCursor int64
	TailCursor    int64
	Error         string
}

// publicOutputFields maps the daemon's output cursor fields to the byte
// offsets that process tool results expose.
var publicOutputFields = map[string]string{
	"cursor":         "offset_byte",
	"next_cursor":    "next_offset_byte",
	"omitted_cursor": "omitted_offset_byte",
	"tail_cursor":    "tail_offset_byte",
}

func (o observation) split() bool { return o.TailCursor > 0 }

func (o observation) complete() bool { return o.Output != nil && o.NextCursor != nil }

// validateRange checks that the output fits its byte range and maxBytes.
// Replacement symbols for NUL and invalid UTF-8 count as one raw byte.
func (o observation) validateRange(maxBytes int) error {
	cursor, next := o.Cursor, *o.NextCursor
	head := processaction.MinOutputRawBytes(*o.Output)
	if cursor < 0 || next < cursor {
		return errors.New("observation cursor range is invalid")
	}
	if !o.split() {
		span := next - cursor
		if o.OutputTail != "" || o.OmittedCursor != 0 || span > int64(maxBytes) || head > maxBytes ||
			int64(head) > span || (span > 0 && *o.Output == "") {
			return errors.New("observation exceeds its byte range")
		}
		return nil
	}
	tail := processaction.MinOutputRawBytes(o.OutputTail)
	if o.OmittedCursor <= cursor || o.TailCursor <= o.OmittedCursor || o.TailCursor > next ||
		*o.Output == "" || o.OutputTail == "" || head+tail > maxBytes ||
		int64(head) > o.OmittedCursor-cursor || int64(tail) > next-o.TailCursor {
		return errors.New("observation head and tail exceed their byte ranges")
	}
	return nil
}

func (o observation) safeText() error {
	if err := dbsafe.Text(*o.Output); err != nil {
		return fmt.Errorf("output %w", err)
	}
	if err := dbsafe.Text(o.OutputTail); err != nil {
		return fmt.Errorf("output_tail %w", err)
	}
	return nil
}

// addTo copies the output fields, leaving out a zero cursor and false truncated.
func (o observation) addTo(result map[string]any) {
	result["output"] = *o.Output
	if o.Cursor != 0 {
		result["offset_byte"] = o.Cursor
	}
	result["next_offset_byte"] = *o.NextCursor
	if o.Truncated {
		result["truncated"] = true
	}
	if o.split() {
		result["omitted_offset_byte"] = o.OmittedCursor
		result["tail_offset_byte"] = o.TailCursor
		result["output_tail"] = o.OutputTail
	}
}

func (o observation) matches(other observation) bool {
	return *o.ProcessID == *other.ProcessID &&
		*o.Output == *other.Output &&
		o.Cursor == other.Cursor &&
		*o.NextCursor == *other.NextCursor &&
		o.Truncated == other.Truncated &&
		o.OutputTail == other.OutputTail &&
		o.OmittedCursor == other.OmittedCursor &&
		o.TailCursor == other.TailCursor
}

// decodeObservation accepts the daemon's cursor fields and the published byte
// offsets, because process action completion re-reads canonical results.
func decodeObservation(raw json.RawMessage) (observation, error) {
	var body struct {
		ProcessID         *string `json:"process_id"`
		Output            *string `json:"output"`
		Cursor            *int64  `json:"cursor"`
		OffsetByte        *int64  `json:"offset_byte"`
		NextCursor        *int64  `json:"next_cursor"`
		NextOffsetByte    *int64  `json:"next_offset_byte"`
		Truncated         bool    `json:"truncated"`
		OutputTail        string  `json:"output_tail"`
		OmittedCursor     *int64  `json:"omitted_cursor"`
		OmittedOffsetByte *int64  `json:"omitted_offset_byte"`
		TailCursor        *int64  `json:"tail_cursor"`
		TailOffsetByte    *int64  `json:"tail_offset_byte"`
		Error             string  `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return observation{}, err
	}
	return observation{
		ProcessID:     body.ProcessID,
		Output:        body.Output,
		Cursor:        valueOrZero(cmp.Or(body.Cursor, body.OffsetByte)),
		NextCursor:    cmp.Or(body.NextCursor, body.NextOffsetByte),
		Truncated:     body.Truncated,
		OutputTail:    body.OutputTail,
		OmittedCursor: valueOrZero(cmp.Or(body.OmittedCursor, body.OmittedOffsetByte)),
		TailCursor:    valueOrZero(cmp.Or(body.TailCursor, body.TailOffsetByte)),
		Error:         body.Error,
	}, nil
}

func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

// PublicOutputRange renames the daemon's output cursor fields to the byte
// offsets that process tool results expose.
func PublicOutputRange(body map[string]any) {
	for wire, public := range publicOutputFields {
		if value, ok := body[wire]; ok {
			delete(body, wire)
			body[public] = value
		}
	}
}

func TerminalState(state string) bool {
	switch state {
	case "exited", "failed", "killed", "unknown":
		return true
	default:
		return false
	}
}
func publicID(kind publicid.Kind, id uuid.UUID) string { s, _ := publicid.Encode(kind, id); return s }

// addTerminalFacts adds how a terminal process ended. An exited process's
// reason message only restates its exit code, so it is left out.
func addTerminalFacts(result map[string]any, process Process) {
	if !TerminalState(process.State) {
		return
	}
	if process.ExitCode != nil {
		result["exit_code"] = *process.ExitCode
	}
	if process.ExitSignal != "" {
		result["exit_signal"] = process.ExitSignal
	}
	if process.StateReasonCode != "" {
		result["state_reason_code"] = process.StateReasonCode
	}
	if _, ok := result["error"]; ok || process.State == "exited" || process.StateReasonMessage == "" {
		return
	}
	result["error"] = process.StateReasonMessage
}

func Read(
	process Process,
	action Action,
	raw json.RawMessage,
) (json.RawMessage, *int64, error) {
	payload, err := processaction.DecodeReadPayload(action.Payload)
	if err != nil {
		return nil, nil, fmt.Errorf("decode read request: %w", err)
	}
	observed, err := decodeObservation(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("decode read observation: %w", err)
	}
	if observed.ProcessID == nil || !observed.complete() {
		return nil, nil, errors.New("read observation is incomplete")
	}
	if err := observed.safeText(); err != nil {
		return nil, nil, fmt.Errorf("read observation %w", err)
	}
	publicProcessID := publicID(publicid.KindProcess, process.ID)
	if *observed.ProcessID != publicProcessID {
		return nil, nil, errors.New("read observation names a different process")
	}
	minimumCursor := process.DefaultOutputCursor
	if payload.Cursor != nil {
		minimumCursor = *payload.Cursor
	}
	if observed.Cursor < minimumCursor || (observed.Cursor > minimumCursor && !observed.Truncated) {
		return nil, nil, errors.New("read observation cursor range is invalid")
	}
	if err := observed.validateRange(payload.ObservationBytes()); err != nil {
		return nil, nil, fmt.Errorf("read %w", err)
	}

	result := map[string]any{"process_id": publicProcessID, "state": process.State}
	observed.addTo(result)
	addTerminalFacts(result, process)
	canonical, err := json.Marshal(result)
	if err != nil {
		return nil, nil, err
	}
	if payload.Cursor != nil {
		return canonical, nil, nil
	}
	next := *observed.NextCursor
	return canonical, &next, nil
}

func ReadFailure(
	process Process,
	_ Action,
	code string,
	message string,
) (json.RawMessage, error) {
	if message == "" {
		message = code
	}
	result := map[string]any{
		"process_id": publicID(publicid.KindProcess, process.ID),
		"state":      process.State,
		"error_code": code,
		"error":      message,
		"retryable":  code == "machine_unreachable",
	}
	addTerminalFacts(result, process)
	return json.Marshal(result)
}

// Terminal is the result for a process that ended without output to report,
// such as a file transfer before its own result is added.
func Terminal(process Process) (string, json.RawMessage, error) {
	if !TerminalState(process.State) {
		return "", nil, fmt.Errorf("process %s is not terminal", process.ID)
	}
	exitCode := any(nil)
	if process.ExitCode != nil {
		exitCode = *process.ExitCode
	}
	errText := process.StateReasonMessage
	if errText == "" {
		errText = process.StateReasonCode
	}
	if process.State == "exited" && !succeeded(process) && errText == "" && process.ExitCode != nil {
		errText = fmt.Sprintf("exit code %d", *process.ExitCode)
	}
	result, err := json.Marshal(map[string]any{
		"process_id":        publicID(publicid.KindProcess, process.ID),
		"exit_code":         exitCode,
		"state":             process.State,
		"state_reason_code": process.StateReasonCode,
		"error":             errText,
	})
	if err != nil {
		return "", nil, fmt.Errorf("marshal process tool result: %w", err)
	}
	return terminalOutcome(process), result, nil
}

// TerminalOutput is the run_command result for a process that ended before
// the call returned, with the output the daemon observed if any.
func TerminalOutput(process Process, observed json.RawMessage) (string, json.RawMessage, error) {
	if !TerminalState(process.State) {
		return "", nil, fmt.Errorf("process %s is not terminal", process.ID)
	}
	result := map[string]any{
		"process_id": publicID(publicid.KindProcess, process.ID),
		"state":      process.State,
	}
	if observed = NormalizeObservation(observed); len(observed) != 0 {
		output, err := decodeObservation(observed)
		if err != nil {
			return "", nil, fmt.Errorf("decode process terminal observation: %w", err)
		}
		if output.complete() {
			output.addTo(result)
		}
	}
	addTerminalFacts(result, process)
	body, err := json.Marshal(result)
	if err != nil {
		return "", nil, fmt.Errorf("marshal process tool result: %w", err)
	}
	return terminalOutcome(process), body, nil
}

func succeeded(process Process) bool {
	return process.State == "exited" && process.ExitCode != nil && *process.ExitCode == 0
}

func terminalOutcome(process Process) string {
	if succeeded(process) {
		return "succeeded"
	}
	return "failed"
}

// Started is the run_command result for a process still running when the call
// returns. The agent is told separately when it ends.
func Started(process Process, observed json.RawMessage) (json.RawMessage, error) {
	result := map[string]any{
		"process_id": publicID(publicid.KindProcess, process.ID),
		"state":      process.State,
	}
	if observed = NormalizeObservation(observed); len(observed) != 0 {
		output, err := decodeObservation(observed)
		if err != nil {
			return nil, fmt.Errorf("decode process started result: %w", err)
		}
		if output.complete() {
			output.addTo(result)
		}
		if output.Error != "" {
			result["error"] = output.Error
		}
	}
	return json.Marshal(result)
}

func ActionResult(
	processID uuid.UUID,
	_ uuid.UUID,
	state string,
	reasonCode string,
	errText string,
) (json.RawMessage, error) {
	result := map[string]any{
		"process_id": publicID(publicid.KindProcess, processID),
		"state":      state,
	}
	if reasonCode != "" {
		result["state_reason_code"] = reasonCode
	}
	if errText != "" {
		result["error"] = errText
	}
	body, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal process action tool result: %w", err)
	}
	return body, nil
}

func ReadMatches(
	raw json.RawMessage,
	result json.RawMessage,
) (bool, error) {
	observed, err := decodeObservation(raw)
	if err != nil {
		return false, nil //nolint:nilerr // An undecodable observation differs from the committed result.
	}
	committed, err := decodeObservation(result)
	if err != nil {
		return false, fmt.Errorf("decode committed read result: %w", err)
	}
	if observed.ProcessID == nil || committed.ProcessID == nil ||
		!observed.complete() || !committed.complete() {
		return false, nil
	}
	return observed.matches(committed), nil
}

func ObservationNextCursor(
	raw json.RawMessage,
) (int64, bool, error) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return 0, false, nil
	}
	observed, err := decodeObservation(raw)
	if err != nil {
		return 0, false, err
	}
	if observed.NextCursor == nil {
		if observed.Output != nil {
			return 0, false, errors.New(
				"process observation cursor range is incomplete",
			)
		}
		return 0, false, nil
	}
	if observed.Output == nil {
		return 0, false, errors.New("process observation cursor range is invalid")
	}
	if err := observed.validateRange(processaction.MaxObservationBytes); err != nil {
		return 0, false, fmt.Errorf("process %w", err)
	}
	return *observed.NextCursor, true, nil
}

func NormalizeObservation(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	return raw
}
