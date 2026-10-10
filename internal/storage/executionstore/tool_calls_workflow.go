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
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

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
