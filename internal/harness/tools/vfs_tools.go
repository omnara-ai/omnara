package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type uploadFileRequest struct {
	Path      string          `json:"path"`
	Source    string          `json:"source"`
	MachineID json.RawMessage `json:"machine_id,omitempty"`
}

type resolvedUploadFileRequest struct {
	Source    string
	MachineID uuid.UUID
}

type downloadFileRequest struct {
	Path        string          `json:"path"`
	Destination string          `json:"destination"`
	MachineID   json.RawMessage `json:"machine_id,omitempty"`
}

type resolvedDownloadFileRequest struct {
	ArtifactID  string
	Destination string
	MachineID   uuid.UUID
}

func validateUploadFileInput(input json.RawMessage) error {
	_, err := resolveUploadFileRequest(input)
	return err
}

func resolveUploadFileRequest(raw json.RawMessage) (resolvedUploadFileRequest, error) {
	var input uploadFileRequest
	if err := decodeSingleStrictJSON(raw, &input, "upload_file request"); err != nil {
		return resolvedUploadFileRequest{}, fmt.Errorf("parse upload_file request: %w", err)
	}
	if input.Path != toolcatalog.ArtifactVFSRoot {
		return resolvedUploadFileRequest{}, errors.New("upload path must be /artifacts")
	}
	if input.Source == "" {
		return resolvedUploadFileRequest{}, errors.New("source is required")
	}
	if strings.Contains(input.Source, "\x00") {
		return resolvedUploadFileRequest{}, errors.New("source cannot contain NUL")
	}
	machineID, err := resolveOptionalMachineID(input.MachineID)
	if err != nil {
		return resolvedUploadFileRequest{}, err
	}
	return resolvedUploadFileRequest{Source: input.Source, MachineID: machineID}, nil
}

func validateDownloadFileInput(input json.RawMessage) error {
	_, err := resolveDownloadFileRequest(input)
	return err
}

func resolveDownloadFileRequest(raw json.RawMessage) (resolvedDownloadFileRequest, error) {
	var input downloadFileRequest
	if err := decodeSingleStrictJSON(raw, &input, "download_file request"); err != nil {
		return resolvedDownloadFileRequest{}, fmt.Errorf("parse download_file request: %w", err)
	}
	if _, err := resolveArtifactPath(input.Path); err != nil {
		return resolvedDownloadFileRequest{}, errors.New("path must be /artifacts/<artifact_id>")
	}
	artifactID := strings.TrimPrefix(input.Path, toolcatalog.ArtifactVFSRoot+"/")
	if input.Destination == "" {
		return resolvedDownloadFileRequest{}, errors.New("destination is required")
	}
	if strings.Contains(input.Destination, "\x00") {
		return resolvedDownloadFileRequest{}, errors.New("destination cannot contain NUL")
	}
	machineID, err := resolveOptionalMachineID(input.MachineID)
	if err != nil {
		return resolvedDownloadFileRequest{}, err
	}
	return resolvedDownloadFileRequest{
		ArtifactID: artifactID, Destination: input.Destination, MachineID: machineID,
	}, nil
}

func runUploadFile(
	ctx context.Context,
	call transactionalToolContext,
) (transactionalPhaseResult, error) {
	resolved, err := resolveUploadFileRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	binding, err := resolveMachineExecutionTargetForToolCall(ctx, call.Reader, resolved.MachineID)
	if err != nil {
		return processToolMachineResolutionError(err)
	}
	toolCallID, err := publicid.Encode(publicid.KindToolCall, call.ToolCallID)
	if err != nil {
		return nil, fmt.Errorf("encode tool call id: %w", err)
	}
	authorizationInput, err := uploadArtifactAuthorizationInput(binding.ID, resolved.Source)
	if err != nil {
		return nil, err
	}
	return startProcessTool(
		ctx,
		call,
		binding,
		authorizationInput,
		uploadArtifactProcessInput(toolCallID, resolved.Source),
	)
}

func runDownloadFile(
	ctx context.Context,
	call transactionalToolContext,
) (transactionalPhaseResult, error) {
	resolved, err := resolveDownloadFileRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	binding, err := resolveMachineExecutionTargetForToolCall(ctx, call.Reader, resolved.MachineID)
	if err != nil {
		return processToolMachineResolutionError(err)
	}
	toolCallID, err := publicid.Encode(publicid.KindToolCall, call.ToolCallID)
	if err != nil {
		return nil, fmt.Errorf("encode tool call id: %w", err)
	}
	authorizationInput, err := downloadArtifactAuthorizationInput(
		binding.ID,
		resolved.ArtifactID,
		resolved.Destination,
	)
	if err != nil {
		return nil, err
	}
	return startProcessTool(
		ctx,
		call,
		binding,
		authorizationInput,
		downloadArtifactProcessInput(toolCallID, resolved.ArtifactID, resolved.Destination),
	)
}
