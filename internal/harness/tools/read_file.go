package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/fileread"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type readFileRequest struct {
	Path           string          `json:"path"`
	MachineID      json.RawMessage `json:"machine_id,omitempty"`
	ExpectedDigest *string         `json:"expected_digest,omitempty"`
	fileread.Paging

	onMachine bool
	machineID uuid.UUID
}

func validateReadFileInput(raw json.RawMessage) error {
	_, err := resolveReadFileRequest(raw)
	return err
}

// resolveReadFileRequest routes /memory and /artifacts paths to Omnara storage
// unless machine_id is supplied; every other path is read from a machine.
func resolveReadFileRequest(raw json.RawMessage) (readFileRequest, error) {
	var input readFileRequest
	if err := decodeSingleStrictJSON(raw, &input, "read_file request"); err != nil {
		return readFileRequest{}, fmt.Errorf("parse read_file request: %w", err)
	}
	machineID, err := resolveOptionalMachineID(input.MachineID)
	if err != nil {
		return readFileRequest{}, err
	}
	input.machineID = machineID
	input.onMachine = onMachine(input.Path, machineID)
	if input.onMachine {
		if err := validateMachinePath(input.Path); err != nil {
			return readFileRequest{}, err
		}
		if input.ExpectedDigest != nil {
			return readFileRequest{}, errExpectedDigestMemoryOnly
		}
	} else if err := validateFilePath(input.Path); err != nil {
		return readFileRequest{}, err
	}
	if input.ExpectedDigest != nil {
		if err := daemonprotocol.ValidateFileDigest(*input.ExpectedDigest); err != nil {
			return readFileRequest{}, err
		}
	}
	input.Paging, err = input.Paging.Normalize()
	if err != nil {
		return readFileRequest{}, err
	}
	return input, nil
}

func machineReadFile(raw json.RawMessage) (machineFileCall, bool, error) {
	input, err := resolveReadFileRequest(raw)
	if err != nil || !input.onMachine {
		return machineFileCall{}, false, err
	}
	return machineFileCall{machineID: input.machineID, kind: processcmd.KindFileRead, operation: processcmd.FileOperation{
		Path: input.Path, Paging: input.Paging,
	}}, true, nil
}

func runReadFileAsync(
	ctx context.Context,
	call asyncToolContext,
) (asyncPhaseResult, error) {
	input, err := resolveReadFileRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	if input.onMachine {
		return nil, errors.New("machine reads run as file operation processes")
	}
	var content []byte
	var digest, contentType string
	var artifactID uuid.UUID
	if strings.HasPrefix(input.Path, memorystore.Root+"/") {
		digest, content, err = call.Executor.readMemoryFile(ctx, call.Turn, input.Path)
		if err != nil {
			return nil, err
		}
		contentType = http.DetectContentType(content)
	} else {
		artifactID, err = resolveArtifactPath(input.Path)
		if err != nil {
			return nil, err
		}
		var record artifactstore.ArtifactRecord
		content, record, err = loadArtifactContent(ctx, call, artifactID)
		if err != nil {
			return nil, err
		}
		digest, contentType = record.Digest, record.ContentType
	}
	if input.ExpectedDigest != nil && *input.ExpectedDigest != digest {
		return nil, storeerr.Tag(&storeerr.FileContentConflictError{CurrentDigest: digest},
			errors.New("file digest mismatch; restart the read"))
	}
	if fileread.IsViewableImage(contentType, content) {
		if artifactID == uuid.Nil {
			artifact, err := call.Executor.Store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
				ProjectID:      call.Turn.ProjectID,
				AgentID:        call.Turn.AgentID,
				ContentType:    contentType,
				Filename:       path.Base(input.Path),
				Content:        content,
				MaxBytes:       daemonprotocol.MaxFileTransferBytes,
				IdempotencyKey: "memory-image:" + blobstore.ContentDigest([]byte(input.Path+"\x00"+digest)),
			})
			if err != nil {
				return nil, fmt.Errorf("save memory image: %w", err)
			}
			artifactID = artifact.ID
		}
		return completeImageRead(input.Path, contentType, digest, len(content), artifactID)
	}
	result, err := input.Page(content, input.Path)
	if err != nil {
		return nil, err
	}
	result["content_type"] = contentType
	result["size_bytes"] = len(content)
	result["digest"] = digest
	return completeFileTool(result)
}

func (e Executor) readMemoryFile(ctx context.Context, turn Turn, filePath string) (string, []byte, error) {
	name, relativePath, err := memorystore.ParsePath(filePath)
	if err != nil {
		return "", nil, err
	}
	store, err := e.Store.Memories().Resolve(ctx, turn.ProjectID, name)
	if err != nil {
		return "", nil, err
	}
	return e.Store.Memories().Read(ctx, memorystore.Scope{
		OrgID: turn.OrgID, ProjectID: turn.ProjectID, AgentID: turn.AgentID,
	}, store.ID, relativePath)
}

func loadArtifactContent(
	ctx context.Context,
	call asyncToolContext,
	artifactID uuid.UUID,
) ([]byte, artifactstore.ArtifactRecord, error) {
	if call.Executor.Store == nil || call.Executor.Store.Artifacts() == nil {
		return nil, artifactstore.ArtifactRecord{}, errors.New("artifact storage is not configured")
	}
	record, err := call.Executor.Store.Artifacts().GetArtifact(
		ctx,
		call.Turn.ProjectID,
		call.Turn.AgentID,
		artifactID,
	)
	if err != nil {
		return nil, artifactstore.ArtifactRecord{}, fmt.Errorf("load artifact: %w", err)
	}
	if record.SizeBytes != nil && *record.SizeBytes > toolcatalog.MaxReadableArtifactBytes {
		return nil, artifactstore.ArtifactRecord{}, fmt.Errorf(
			"artifact exceeds the %d-byte readable limit",
			toolcatalog.MaxReadableArtifactBytes,
		)
	}
	content, record, err := call.Executor.Store.Artifacts().GetArtifactBlob(
		ctx,
		call.Turn.ProjectID,
		call.Turn.AgentID,
		artifactID,
	)
	if err != nil {
		return nil, artifactstore.ArtifactRecord{}, fmt.Errorf("read artifact: %w", err)
	}
	if len(content) > toolcatalog.MaxReadableArtifactBytes {
		return nil, artifactstore.ArtifactRecord{}, errors.New("artifact exceeds readable limit")
	}
	return content, record, nil
}

func completeImageRead(
	path string,
	contentType string,
	digest string,
	sizeBytes int,
	artifactID uuid.UUID,
) (asyncPhaseResult, error) {
	metadata, err := structuredToolResultPart(map[string]any{
		"path":         path,
		"content_type": contentType,
		"size_bytes":   sizeBytes,
		"digest":       digest,
	})
	if err != nil {
		return nil, err
	}
	media, err := mediaToolResultPart(artifactID)
	if err != nil {
		return nil, err
	}
	return completeAsynchronously(newToolResultContent(metadata, media)), nil
}

func completeFileTool(value any) (asyncPhaseResult, error) {
	content, err := structuredToolResultContent(value)
	if err != nil {
		return nil, err
	}
	return completeAsynchronously(content), nil
}

func validateFilePath(path string) error {
	if strings.HasPrefix(path, memorystore.Root+"/") {
		_, _, err := memorystore.ParsePath(path)
		return err
	}
	if strings.HasPrefix(path, toolcatalog.ArtifactVFSRoot+"/") {
		_, err := resolveArtifactPath(path)
		return err
	}
	return errors.New("path must be /artifacts/<artifact_id> or /memory/<store>/<file>")
}

func resolveArtifactPath(path string) (uuid.UUID, error) {
	id, ok := strings.CutPrefix(path, toolcatalog.ArtifactVFSRoot+"/")
	if ok && !strings.Contains(id, "/") {
		if artifactID, err := publicid.Decode(publicid.KindArtifact, id); err == nil {
			return artifactID, nil
		}
	}
	return uuid.Nil, errors.New(
		"artifacts are read by ID; use list_files with /artifacts/<filename or glob>, then use a returned /artifacts/<artifact_id> path",
	)
}
