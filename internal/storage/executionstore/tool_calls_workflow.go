package executionstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
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
	interactionUpdate *notifications.AgentInteractionUpdate,
) (events.Event, error) {
	admitted, err := appendToolResultRecordTx(
		ctx,
		txNotifications,
		tx,
		record,
		interactionUpdate,
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
	interactionUpdate *notifications.AgentInteractionUpdate,
) (admittedToolCallResult, error) {
	interactions, err := dbsqlc.New(tx).CancelOpenAgentInteractionsForToolCall(
		ctx,
		dbsqlc.CancelOpenAgentInteractionsForToolCallParams{
			ProjectID:  record.ProjectID,
			AgentID:    record.AgentID,
			ToolCallID: record.ID,
			Reason:     toolCallCompletedInteractionReason,
		},
	)
	if err != nil {
		return admittedToolCallResult{}, fmt.Errorf(
			"close interactions for completed tool call: %w",
			err,
		)
	}
	for _, interaction := range interactions {
		interactionUpdate = &notifications.AgentInteractionUpdate{
			ID: interaction.ID, InteractionKind: interaction.InteractionKind, State: interaction.State,
		}
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
			uuid.Nil,
		); err != nil {
			return admittedToolCallResult{}, err
		}
		txNotifications.AddToolCallUpdate(
			record.AgentID,
			record.ID,
			string(ToolCallStateCompleted),
			interactionUpdate,
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
	projectID, agentID, toolCallID uuid.UUID,
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
	if result.Outcome != string(outcome) {
		return false, nil
	}
	return toolResultContentMatchesTx(ctx, qtx, projectID, agentID, toolCallID, storedParts, blocks, contentParts)
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
	commandLabel := process.ExecutionSpec.Label()
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
	processID uuid.UUID,
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

func fileTransferToolResultContentParts(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	process ProcessRecord,
	result json.RawMessage,
) (ToolResultOutcome, json.RawMessage, error) {
	transfer := process.ExecutionSpec.FileTransfer
	direction := transfer.Direction
	var observed struct {
		FileTransfer *daemonprotocol.FileTransferResult `json:"file_transfer"`
	}
	valid := json.Unmarshal(result, &observed) == nil && observed.FileTransfer != nil &&
		observed.FileTransfer.Validate(direction) == nil
	metadata := observed.FileTransfer
	if process.State != ProcessStateExited || process.ExitCode == nil || *process.ExitCode != 0 {
		if valid && metadata.Error != nil && process.ExitCode != nil && *process.ExitCode != 0 &&
			(process.State == ProcessStateExited ||
				(process.State == ProcessStateFailed && process.StateReasonCode == "nonzero_exit")) {
			return failedFileTransferToolResultContentParts(result, *metadata.Error)
		}
		contentParts, err := ToolResultContentParts(result)
		return ToolResultOutcomeFailed, contentParts, err
	}
	invalidMetadata := daemonprotocol.FileTransferError{
		Message: string(direction) + " completed without valid file metadata",
	}
	if !valid || metadata.Error != nil {
		return failedFileTransferToolResultContentParts(result, invalidMetadata)
	}
	parts := []map[string]any{{"type": "structured_data", "value": metadata}}
	if direction == processcmd.FileTransferUpload {
		if transfer.Target.Artifact != nil {
			artifact, err := qtx.GetArtifactByIdempotencyKey(ctx, dbsqlc.GetArtifactByIdempotencyKeyParams{
				ProjectID:      process.ProjectID,
				AgentID:        process.AgentID,
				IdempotencyKey: UploadArtifactIdempotencyKey(process.ToolCallID),
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return failedFileTransferToolResultContentParts(result,
					daemonprotocol.FileTransferError{Message: "upload completed without an artifact"})
			}
			if err != nil {
				return "", nil, fmt.Errorf("load uploaded artifact: %w", err)
			}
			path := toolcatalog.ArtifactVFSRoot + "/" + publicResourceID(publicid.KindArtifact, artifact.ID)
			if metadata.Path != path || metadata.Digest != artifact.Digest {
				return failedFileTransferToolResultContentParts(result, invalidMetadata)
			}
			parts = append(parts, map[string]any{
				"type": "media_ref", "artifact_id": artifact.ID.String(), "exclude_from_model_context": true,
			})
		} else {
			target := transfer.Target.Memory
			name, err := qtx.GetMemoryStoreName(ctx, dbsqlc.GetMemoryStoreNameParams{
				ProjectID: process.ProjectID, ID: target.StoreID,
			})
			if err != nil {
				return "", nil, fmt.Errorf("load memory transfer name: %w", err)
			}
			if metadata.Path != "/memory/"+name+"/"+target.Path {
				return failedFileTransferToolResultContentParts(result, invalidMetadata)
			}
		}
	}
	contentParts, err := marshalJSON(parts)
	return ToolResultOutcomeSucceeded, contentParts, err
}

func UploadArtifactIdempotencyKey(toolCallID uuid.UUID) string {
	return "upload-artifact:" + toolCallID.String()
}

func failedFileTransferToolResultContentParts(
	result json.RawMessage,
	failure daemonprotocol.FileTransferError,
) (ToolResultOutcome, json.RawMessage, error) {
	var value map[string]any
	if err := json.Unmarshal(result, &value); err != nil {
		return "", nil, fmt.Errorf("decode file transfer result: %w", err)
	}
	delete(value, "file_transfer")
	value["error"] = failure.Message
	if failure.Code != "" {
		value["error_code"] = failure.Code
	}
	if failure.CurrentDigest != "" {
		value["current_digest"] = failure.CurrentDigest
	}
	contentParts, err := marshalJSON([]map[string]any{{"type": "structured_data", "value": value}})
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
