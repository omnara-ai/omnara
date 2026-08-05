package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/processaction"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type daemonProcessReadObservation struct {
	ProcessID  string  `json:"process_id"`
	Output     *string `json:"output"`
	Cursor     *int64  `json:"cursor"`
	NextCursor *int64  `json:"next_cursor"`
	Truncated  *bool   `json:"truncated"`
}

func canonicalProcessReadResult(
	process ProcessRecord,
	action ProcessActionRecord,
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
	publicProcessID := publicResourceID(publicid.KindProcess, process.ID)
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
		"done":        isProcessTerminal(process.State),
	}
	if process.SourceStartedAt != nil {
		result["started_at"] = *process.SourceStartedAt
	}
	if isProcessTerminal(process.State) {
		result["ended_at"] = process.SourceEndedAt
		result["exit_code"] = process.ExitCode
		result["exit_signal"] = process.ExitSignal
		result["state_reason_code"] = process.StateReasonCode
		result["state_reason_message"] = process.StateReasonMessage
	}
	canonical, err := marshalJSON(result)
	if err != nil {
		return nil, nil, err
	}
	if payload.Cursor != nil {
		return canonical, nil, nil
	}
	next := *observed.NextCursor
	return canonical, &next, nil
}

func canonicalProcessReadFailureResult(
	process ProcessRecord,
	action ProcessActionRecord,
	code string,
	message string,
) (json.RawMessage, error) {
	result := map[string]any{
		"process_id":        publicResourceID(publicid.KindProcess, process.ID),
		"process_action_id": publicResourceID(publicid.KindProcessAction, action.ID),
		"state":             process.State,
		"error_code":        code,
		"message":           message,
		"error":             message,
		"retryable":         code == ProcessToolReasonMachineUnreachable,
		"done":              isProcessTerminal(process.State),
	}
	if process.SourceStartedAt != nil {
		result["started_at"] = *process.SourceStartedAt
	}
	if isProcessTerminal(process.State) {
		result["ended_at"] = process.SourceEndedAt
		result["exit_code"] = process.ExitCode
		result["exit_signal"] = process.ExitSignal
		result["state_reason_code"] = process.StateReasonCode
		result["state_reason_message"] = process.StateReasonMessage
	}
	return marshalJSON(result)
}

func processReadObservationMatchesResult(
	raw json.RawMessage,
	result json.RawMessage,
) (bool, error) {
	observed, ok := decodeProcessReadObservation(raw)
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

func decodeProcessReadObservation(
	raw json.RawMessage,
) (daemonProcessReadObservation, bool) {
	var observed daemonProcessReadObservation
	return observed, json.Unmarshal(raw, &observed) == nil
}

func inspectPublishedProcessReadObservationTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID ID,
	agentID ID,
	toolCallID ID,
	raw json.RawMessage,
) (toolCallResultPublication, error) {
	result, err := qtx.GetToolCallResultByToolCall(
		ctx,
		dbsqlc.GetToolCallResultByToolCallParams{
			ProjectID:  projectID,
			AgentID:    agentID,
			ToolCallID: toolCallID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return toolCallResultPublication{}, nil
	}
	if err != nil {
		return toolCallResultPublication{}, fmt.Errorf(
			"load committed read result: %w",
			err,
		)
	}
	contentParts, err := toolCallResultContentBlocksTx(
		ctx,
		qtx,
		projectID,
		agentID,
		result.ID,
	)
	if err != nil {
		return toolCallResultPublication{}, err
	}
	publication, err := inspectPublishedToolCallResultTx(
		ctx,
		qtx,
		projectID,
		agentID,
		toolCallID,
		ToolResultOutcomeSucceeded,
		contentParts,
	)
	if err != nil || !publication.Matches {
		return publication, err
	}
	committed, ok := structuredToolResultValue(contentParts)
	if !ok {
		publication.Matches = false
		return publication, nil
	}
	publication.Matches, err = processReadObservationMatchesResult(
		raw,
		committed,
	)
	return publication, err
}

func structuredToolResultValue(parts json.RawMessage) (json.RawMessage, bool) {
	var decoded []struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
	}
	if json.Unmarshal(parts, &decoded) != nil {
		return nil, false
	}
	for _, part := range decoded {
		if part.Type == "structured_data" && len(part.Value) != 0 {
			return part.Value, true
		}
	}
	return nil, false
}

func processObservationNextCursor(
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

func advanceProcessDefaultOutputCursorFromResult(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	process *ProcessRecord,
	result json.RawMessage,
) error {
	nextCursor, found, err := processObservationNextCursor(result)
	if err != nil {
		return fmt.Errorf("validate process output cursor: %w", err)
	}
	if !found {
		return nil
	}
	updated, err := qtx.AdvanceProcessDefaultOutputCursor(
		ctx,
		dbsqlc.AdvanceProcessDefaultOutputCursorParams{
			ProjectID:  process.ProjectID,
			AgentID:    process.AgentID,
			ID:         process.ID,
			NextCursor: nextCursor,
		},
	)
	if err != nil {
		return err
	}
	if updated != 1 {
		return errors.New("process disappeared while advancing its output cursor")
	}
	process.DefaultOutputCursor = max(
		process.DefaultOutputCursor,
		nextCursor,
	)
	return nil
}
