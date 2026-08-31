package executionstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const (
	toolCallResultEventIDPrefix        = "tool-call-result-authority:"
	toolCallCompletedInteractionReason = "tool_call_completed"
)

func appendToolResultEventTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	record ToolCallRecord,
) (events.Event, error) {
	admitted, err := appendToolResultRecordTx(
		ctx,
		txNotifications,
		tx,
		record,
	)
	if err != nil {
		return events.Event{}, err
	}
	return admitted.Event.Event, nil
}

func appendToolResultRecordTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	record ToolCallRecord,
) (admittedToolCallResult, error) {
	if _, err := dbsqlc.New(tx).CancelOpenAgentInteractionsForToolCall(
		ctx,
		dbsqlc.CancelOpenAgentInteractionsForToolCallParams{
			ProjectID:  record.ProjectID,
			AgentID:    record.AgentID,
			ToolCallID: record.ID,
			Reason:     toolCallCompletedInteractionReason,
		},
	); err != nil {
		return admittedToolCallResult{}, fmt.Errorf(
			"close interactions for completed tool call: %w",
			err,
		)
	}
	admitted, err := admitToolCallResultTx(
		ctx,
		txNotifications,
		tx,
		CreateToolCallResultAuthorityInput{
			ProjectID:          record.ProjectID,
			AgentID:            record.AgentID,
			TurnID:             record.TurnID,
			ToolCallID:         record.ID,
			Outcome:            record.Outcome,
			ResultContentParts: record.ResultContentParts,
			IdempotencyKey:     toolCallResultEventIDPrefix + record.ID.String(),
		},
	)
	if err != nil {
		return admittedToolCallResult{}, err
	}
	if admitted.Event.ToolCallResultID != admitted.Result.ID {
		return admittedToolCallResult{}, storeerr.ErrIdempotencyConflict
	}
	if admitted.Inserted {
		if err := updateAgentTurnLatestEventTx(
			ctx,
			tx,
			record.ProjectID,
			record.AgentID,
			record.TurnID,
			admitted.Event.Event.ID,
			NilID,
		); err != nil {
			return admittedToolCallResult{}, err
		}
		txNotifications.AddToolCallUpdate(
			record.AgentID,
			record.ID,
			string(ToolCallStateCompleted),
		)
	}
	return admitted, nil
}

func applyAdmittedToolResult(record *ToolCallRecord, admitted admittedToolCallResult) {
	record.ResultContentParts = admitted.ContentBlocks
	record.CompletedAt = timePtr(admitted.Result.CompletedAt)
}

func completedToolCallMatchesTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID, toolCallID ID,
	outcome ToolResultOutcome,
	contentParts json.RawMessage,
) (bool, error) {
	blocks, err := parseToolResultContentBlocks(contentParts)
	if err != nil {
		return false, err
	}
	contentParts, err = marshalToolResultContentBlocks(blocks)
	if err != nil {
		return false, err
	}
	existing, err := qtx.GetToolCall(
		ctx,
		dbsqlc.GetToolCallParams{ProjectID: projectID, AgentID: agentID, ID: toolCallID},
	)
	if err != nil {
		return false, fmt.Errorf("load linked tool call for replay validation: %w", err)
	}
	if existing.State != "completed" || existing.CompletedAt == nil {
		return false, nil
	}
	result, err := qtx.GetToolCallResultByToolCall(
		ctx,
		dbsqlc.GetToolCallResultByToolCallParams{
			ProjectID:  projectID,
			AgentID:    agentID,
			ToolCallID: toolCallID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load linked tool result for replay validation: %w", err)
	}
	storedParts, err := toolCallResultContentBlocksTx(
		ctx,
		qtx,
		projectID,
		agentID,
		result.ID,
	)
	if err != nil {
		return false, err
	}
	return result.Outcome == string(outcome) &&
		sameJSON(storedParts, normalizedJSON(contentParts)), nil
}

func processToolResult(process ProcessRecord) (ToolResultOutcome, json.RawMessage, error) {
	if !isProcessTerminal(process.State) {
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
	succeeded := process.State == ProcessStateExited && process.ExitCode != nil && *process.ExitCode == 0
	if process.State == ProcessStateExited && !succeeded && errText == "" && process.ExitCode != nil {
		errText = fmt.Sprintf("exit code %d", *process.ExitCode)
	}
	result, err := marshalJSON(map[string]any{
		"process_id":        publicResourceID(publicid.KindProcess, process.ID),
		"exit_code":         exitCode,
		"state":             process.State,
		"state_reason_code": process.StateReasonCode,
		"error":             errText,
	})
	if err != nil {
		return "", nil, fmt.Errorf("marshal process tool result: %w", err)
	}
	outcome := ToolResultOutcomeSucceeded
	if !succeeded {
		outcome = ToolResultOutcomeFailed
	}
	return outcome, result, nil
}

func startedProcessToolResult(process ProcessRecord, observed json.RawMessage) (json.RawMessage, error) {
	commandLabel := processcmd.CommandLabel(process.Command)
	result := map[string]any{
		"process_id":  publicResourceID(publicid.KindProcess, process.ID),
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
		result["process_id"] = publicResourceID(publicid.KindProcess, process.ID)
		result["state"] = process.State
		result["command"] = commandLabel
		result["next_action"] = "use write_process for stdin or read_process for retained output"
	}
	return marshalJSON(result)
}

func commandTerminalToolResult(
	processID ID,
	result json.RawMessage,
) (json.RawMessage, error) {
	if len(result) == 0 || string(result) == "null" {
		return result, nil
	}
	var object map[string]any
	if err := json.Unmarshal(result, &object); err != nil {
		return nil, fmt.Errorf("decode terminal command result: %w", err)
	}
	if object == nil {
		return nil, errors.New("terminal command result must be an object")
	}
	object["process_id"] = publicResourceID(publicid.KindProcess, processID)
	return marshalJSON(object)
}

func isUploadArtifactToolCall(call ToolCallRecord) bool {
	return call.Type == toolcatalog.ToolTypeBuiltIn &&
		call.Name == toolcatalog.ToolNameUploadArtifact
}

func UploadArtifactIdempotencyKey(toolCallID ID) string {
	return "upload-artifact:" + toolCallID.String()
}

func uploadArtifactProcessToolResultContentParts(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	process ProcessRecord,
) (ToolResultOutcome, json.RawMessage, error) {
	artifact, err := qtx.GetArtifactByIdempotencyKey(ctx, dbsqlc.GetArtifactByIdempotencyKeyParams{
		ProjectID:      process.ProjectID,
		AgentID:        process.AgentID,
		IdempotencyKey: UploadArtifactIdempotencyKey(process.ToolCallID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return failedUploadArtifactProcessToolResultContentParts(
			process,
			"upload completed without an artifact",
		)
	}
	if err != nil {
		return "", nil, fmt.Errorf("load uploaded artifact: %w", err)
	}
	contentParts, err := marshalJSON([]map[string]any{
		{
			"type": "structured_data",
			"value": map[string]any{
				"artifact_id": publicResourceID(publicid.KindArtifact, artifact.ID),
			},
		},
		{
			"type":                       "media_ref",
			"artifact_id":                artifact.ID.String(),
			"exclude_from_model_context": true,
		},
	})
	return ToolResultOutcomeSucceeded, contentParts, err
}

func failedUploadArtifactProcessToolResultContentParts(
	process ProcessRecord,
	message string,
) (ToolResultOutcome, json.RawMessage, error) {
	result, err := marshalJSON(map[string]any{
		"process_id": publicResourceID(publicid.KindProcess, process.ID),
		"state":      process.State,
		"error":      message,
	})
	if err != nil {
		return "", nil, err
	}
	contentParts, err := ToolResultContentParts(result)
	return ToolResultOutcomeFailed, contentParts, err
}

func ToolResultContentParts(result json.RawMessage) (json.RawMessage, error) {
	result = bytes.TrimSpace(result)
	if len(result) == 0 || bytes.Equal(result, []byte("null")) {
		return json.RawMessage(`[]`), nil
	}
	return marshalJSON([]map[string]any{{"type": "structured_data", "value": result}})
}

func canceledToolResultContentParts() (json.RawMessage, error) {
	return ToolResultContentParts(
		json.RawMessage(`{"reason":"Agent canceled before this tool call completed."}`),
	)
}
