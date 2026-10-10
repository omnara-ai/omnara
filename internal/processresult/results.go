package processresult

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
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
type daemonProcessReadObservation struct {
	ProcessID  string  `json:"process_id"`
	Output     *string `json:"output"`
	Cursor     *int64  `json:"cursor"`
	NextCursor *int64  `json:"next_cursor"`
	Truncated  *bool   `json:"truncated"`
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
func Read(
	process Process,
	action Action,
	raw json.RawMessage,
) (json.RawMessage, *int64, error) {
	payload, err := processaction.DecodeReadPayload(action.Payload)
	if err != nil {
		return nil, nil, fmt.Errorf("decode read request: %w", err)
	}

	var observed daemonProcessReadObservation
	if err := json.Unmarshal(raw, &observed); err != nil {
		return nil, nil, fmt.Errorf("decode read observation: %w", err)
	}
	if observed.Output == nil ||
		observed.Cursor == nil ||
		observed.NextCursor == nil ||
		observed.Truncated == nil {
		return nil, nil, errors.New("read observation is incomplete")
	}
	publicProcessID := publicID(publicid.KindProcess, process.ID)
	if observed.ProcessID != publicProcessID {
		return nil, nil, errors.New("read observation names a different process")
	}
	minimumCursor := process.DefaultOutputCursor
	if payload.Cursor != nil {
		minimumCursor = *payload.Cursor
	}
	if *observed.Cursor < 0 ||
		*observed.Cursor < minimumCursor ||
		(*observed.Cursor > minimumCursor && !*observed.Truncated) ||
		*observed.NextCursor < *observed.Cursor {
		return nil, nil, errors.New("read observation cursor range is invalid")
	}
	span := *observed.NextCursor - *observed.Cursor
	maxBytes := payload.ObservationBytes()
	if span > int64(maxBytes) ||
		len([]byte(*observed.Output)) > maxBytes ||
		int64(len([]byte(*observed.Output))) > span ||
		(span > 0 && *observed.Output == "") {
		return nil, nil, errors.New("read observation exceeds its requested range")
	}

	result := map[string]any{
		"process_id":  publicProcessID,
		"state":       process.State,
		"output":      *observed.Output,
		"cursor":      *observed.Cursor,
		"next_cursor": *observed.NextCursor,
		"truncated":   *observed.Truncated,
		"done":        TerminalState(process.State),
	}
	if process.SourceStartedAt != nil {
		result["started_at"] = *process.SourceStartedAt
	}
	if TerminalState(process.State) {
		result["ended_at"] = process.SourceEndedAt
		result["exit_code"] = process.ExitCode
		result["exit_signal"] = process.ExitSignal
		result["state_reason_code"] = process.StateReasonCode
		result["state_reason_message"] = process.StateReasonMessage
	}
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
	action Action,
	code string,
	message string,
) (json.RawMessage, error) {
	result := map[string]any{
		"process_id":        publicID(publicid.KindProcess, process.ID),
		"process_action_id": publicID(publicid.KindProcessAction, action.ID),
		"state":             process.State,
		"error_code":        code,
		"message":           message,
		"error":             message,
		"retryable":         code == "machine_unreachable",
		"done":              TerminalState(process.State),
	}
	if process.SourceStartedAt != nil {
		result["started_at"] = *process.SourceStartedAt
	}
	if TerminalState(process.State) {
		result["ended_at"] = process.SourceEndedAt
		result["exit_code"] = process.ExitCode
		result["exit_signal"] = process.ExitSignal
		result["state_reason_code"] = process.StateReasonCode
		result["state_reason_message"] = process.StateReasonMessage
	}
	return json.Marshal(result)
}

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
	succeeded := process.State == "exited" && process.ExitCode != nil && *process.ExitCode == 0
	if process.State == "exited" && !succeeded && errText == "" && process.ExitCode != nil {
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
	outcome := "succeeded"
	if !succeeded {
		outcome = "failed"
	}
	return outcome, result, nil
}

func Started(process Process, observed json.RawMessage) (json.RawMessage, error) {
	commandLabel := process.ExecutionSpec.Label()
	result := map[string]any{
		"process_id":  publicID(publicid.KindProcess, process.ID),
		"state":       process.State,
		"command":     commandLabel,
		"next_action": "use write_process for stdin or read_process for retained output",
	}
	if len(observed) != 0 && string(observed) != "null" && string(observed) != "{}" {
		var observedObject map[string]any
		if err := json.Unmarshal(observed, &observedObject); err != nil {
			return nil, fmt.Errorf("decode process started result: %w", err)
		}
		for key, value := range observedObject {
			result[key] = value
		}
		result["process_id"] = publicID(publicid.KindProcess, process.ID)
		result["state"] = process.State
		result["command"] = commandLabel
		result["next_action"] = "use write_process for stdin or read_process for retained output"
	}
	return json.Marshal(result)
}

func ActionResult(
	processID uuid.UUID,
	actionID uuid.UUID,
	state string,
	reasonCode string,
	errText string,
) (json.RawMessage, error) {
	body, err := json.Marshal(struct {
		ProcessID       string `json:"process_id"`
		ProcessActionID string `json:"process_action_id"`
		State           string `json:"state"`
		StateReasonCode string `json:"state_reason_code"`
		Error           string `json:"error"`
	}{
		ProcessID:       publicID(publicid.KindProcess, processID),
		ProcessActionID: publicID(publicid.KindProcessAction, actionID),
		State:           state,
		StateReasonCode: reasonCode,
		Error:           errText,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal process action tool result: %w", err)
	}
	return body, nil
}

func ReadMatches(
	raw json.RawMessage,
	result json.RawMessage,
) (bool, error) {
	var observed daemonProcessReadObservation
	ok := json.Unmarshal(raw, &observed) == nil
	if !ok {
		return false, nil
	}
	var committed daemonProcessReadObservation
	if err := json.Unmarshal(result, &committed); err != nil {
		return false, fmt.Errorf("decode committed read result: %w", err)
	}
	if observed.Output == nil || committed.Output == nil ||
		observed.Cursor == nil || committed.Cursor == nil ||
		observed.NextCursor == nil || committed.NextCursor == nil ||
		observed.Truncated == nil || committed.Truncated == nil {
		return false, nil
	}
	return observed.ProcessID == committed.ProcessID &&
		*observed.Output == *committed.Output &&
		*observed.Cursor == *committed.Cursor &&
		*observed.NextCursor == *committed.NextCursor &&
		*observed.Truncated == *committed.Truncated, nil
}

func ObservationNextCursor(
	raw json.RawMessage,
) (int64, bool, error) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return 0, false, nil
	}
	var observed struct {
		Output     *string `json:"output"`
		Cursor     *int64  `json:"cursor"`
		NextCursor *int64  `json:"next_cursor"`
	}
	if err := json.Unmarshal(raw, &observed); err != nil {
		return 0, false, err
	}
	if observed.NextCursor == nil {
		if observed.Cursor != nil || observed.Output != nil {
			return 0, false, errors.New(
				"process observation cursor range is incomplete",
			)
		}
		return 0, false, nil
	}
	if observed.Cursor == nil || observed.Output == nil ||
		*observed.Cursor < 0 ||
		*observed.NextCursor < *observed.Cursor {
		return 0, false, errors.New(
			"process observation cursor range is invalid",
		)
	}
	span := *observed.NextCursor - *observed.Cursor
	outputBytes := len([]byte(*observed.Output))
	if span > int64(processaction.MaxObservationBytes) ||
		outputBytes > processaction.MaxObservationBytes ||
		int64(outputBytes) > span ||
		(span > 0 && *observed.Output == "") {
		return 0, false, errors.New(
			"process observation exceeds its allowed range",
		)
	}
	return *observed.NextCursor, true, nil
}
