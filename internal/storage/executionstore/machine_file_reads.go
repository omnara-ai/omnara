package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/fileread"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

// errMachineFileReadNotPrepared makes the daemon retry a read_file completion
// whose uploaded snapshot was not loaded before the completion transaction.
var errMachineFileReadNotPrepared = errors.New("machine file read snapshot is not prepared; retry the report")

// machineFileRead is the uploaded snapshot of a machine file requested by
// read_file. It is loaded outside the completion transaction so blob reads
// never hold the agent execution lock.
type machineFileRead struct {
	toolCallInput json.RawMessage
	artifactID    uuid.UUID
	contentType   string
	digest        string
	content       []byte
	// unavailable records a snapshot whose blob is permanently missing, so the
	// read fails instead of retrying the daemon report forever.
	unavailable bool
}

// loadMachineFileRead returns the snapshot for a successful read_file machine
// transfer. It reports false for every other process so callers keep the
// existing file transfer behavior.
func (s *Store) loadMachineFileRead(
	ctx context.Context,
	input CompleteDaemonProcessInput,
) (machineFileRead, bool, error) {
	if input.State != ProcessStateExited || input.ExitCode == nil || *input.ExitCode != 0 {
		return machineFileRead{}, false, nil
	}
	row, err := s.q.GetProcess(ctx, dbsqlc.GetProcessParams{
		ProjectID: input.ProjectID, AgentID: input.AgentID, ID: input.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return machineFileRead{}, false, nil
	}
	if err != nil {
		return machineFileRead{}, false, fmt.Errorf("load machine file read process: %w", err)
	}
	process := processRecordFromSQLC(row)
	if process.MachineID != input.Authority.MachineID || !isArtifactUpload(process) {
		return machineFileRead{}, false, nil
	}
	call, err := s.GetToolCall(ctx, input.ProjectID, input.AgentID, process.ToolCallID)
	if err != nil {
		return machineFileRead{}, false, fmt.Errorf("load machine file read tool call: %w", err)
	}
	if !isMachineFileReadCall(call.Type, call.Name) {
		return machineFileRead{}, false, nil
	}
	artifact, err := s.q.GetArtifactByIdempotencyKey(ctx, dbsqlc.GetArtifactByIdempotencyKeyParams{
		ProjectID:      input.ProjectID,
		AgentID:        input.AgentID,
		IdempotencyKey: UploadArtifactIdempotencyKey(process.ToolCallID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return machineFileRead{}, false, nil
	}
	if err != nil {
		return machineFileRead{}, false, fmt.Errorf("load machine file read artifact: %w", err)
	}
	if s.artifacts == nil {
		return machineFileRead{}, false, errors.New("artifact storage is required for machine file reads")
	}
	read := machineFileRead{toolCallInput: call.Input, artifactID: artifact.ID, digest: artifact.Digest}
	content, record, err := s.artifacts.GetArtifactBlob(ctx, input.ProjectID, input.AgentID, artifact.ID)
	if errors.Is(err, storeerr.ErrNotFound) {
		read.unavailable = true
		return read, true, nil
	}
	if err != nil {
		return machineFileRead{}, false, fmt.Errorf("read machine file read artifact: %w", err)
	}
	read.contentType, read.content = record.ContentType, content
	return read, true, nil
}

func isArtifactUpload(process ProcessRecord) bool {
	transfer := process.ExecutionSpec.FileTransfer
	return process.ToolCallID != uuid.Nil && transfer != nil &&
		transfer.Direction == processcmd.FileTransferUpload && transfer.Target.Artifact != nil
}

func isMachineFileReadCall(toolType, name string) bool {
	return toolType == toolcatalog.ToolTypeBuiltIn && name == toolcatalog.ToolNameReadFile
}

func isMachineFileReadTx(ctx context.Context, qtx *dbsqlc.Queries, process ProcessRecord) (bool, error) {
	call, err := qtx.GetToolCall(ctx, dbsqlc.GetToolCallParams{
		ProjectID: process.ProjectID, AgentID: process.AgentID, ID: process.ToolCallID,
	})
	if err != nil {
		return false, fmt.Errorf("load file transfer tool call: %w", err)
	}
	return isMachineFileReadCall(call.Type, call.Name), nil
}

// machineFileReadContentParts renders the read_file result for an uploaded
// machine file snapshot with the same paging and image rules as Omnara files.
func machineFileReadContentParts(
	process ProcessRecord,
	read machineFileRead,
) (ToolResultOutcome, json.RawMessage, error) {
	var request struct {
		Path           string  `json:"path"`
		ExpectedDigest *string `json:"expected_digest"`
		fileread.Paging
	}
	if err := json.Unmarshal(read.toolCallInput, &request); err != nil {
		return "", nil, fmt.Errorf("decode read_file input: %w", err)
	}
	machineID, err := publicid.Encode(publicid.KindMachine, process.MachineID)
	if err != nil {
		return "", nil, err
	}
	artifactID, err := publicid.Encode(publicid.KindArtifact, read.artifactID)
	if err != nil {
		return "", nil, err
	}
	metadata := map[string]any{
		"path":          request.Path,
		"machine_id":    machineID,
		"artifact_path": toolcatalog.ArtifactVFSRoot + "/" + artifactID,
		"content_type":  read.contentType,
		"size_bytes":    len(read.content),
		"digest":        read.digest,
	}
	failed := func(message, code string) (ToolResultOutcome, json.RawMessage, error) {
		metadata["error"] = message
		if code != "" {
			metadata["error_code"] = code
		}
		parts, err := marshalJSON([]map[string]any{{"type": "structured_data", "value": metadata}})
		return ToolResultOutcomeFailed, parts, err
	}
	if read.unavailable {
		delete(metadata, "content_type")
		delete(metadata, "size_bytes")
		return failed("the uploaded file snapshot is unavailable; read the file again", "")
	}
	if request.ExpectedDigest != nil && *request.ExpectedDigest != read.digest {
		metadata["current_digest"] = read.digest
		return failed("file digest mismatch; restart the read", "file_content_conflict")
	}
	if fileread.IsViewableImage(read.contentType, read.content) {
		parts, err := marshalJSON([]map[string]any{
			{"type": "structured_data", "value": metadata},
			{"type": "media_ref", "artifact_id": read.artifactID.String()},
		})
		return ToolResultOutcomeSucceeded, parts, err
	}
	paging, err := request.Paging.Normalize()
	if err != nil {
		return failed(err.Error(), "")
	}
	page, err := paging.Page(read.content, request.Path)
	if err != nil {
		return failed(err.Error(), "")
	}
	for key, value := range metadata {
		page[key] = value
	}
	parts, err := marshalJSON([]map[string]any{{"type": "structured_data", "value": page}})
	return ToolResultOutcomeSucceeded, parts, err
}
