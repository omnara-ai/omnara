package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

type uploadFileRequest struct {
	Path       string          `json:"path"`
	Source     string          `json:"source"`
	MachineRef json.RawMessage `json:"machine_ref,omitempty"`
}

type resolvedUploadFileRequest struct {
	Source     string
	MachineRef string
}

type downloadFileRequest struct {
	Path        string          `json:"path"`
	Destination string          `json:"destination"`
	MachineRef  json.RawMessage `json:"machine_ref,omitempty"`
}

type resolvedDownloadFileRequest struct {
	ArtifactID  string
	Destination string
	MachineRef  string
}

func resolveOptionalMachineRef(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("parse machine_ref: %w", err)
	}
	if value == nil {
		return "", errors.New("machine_ref cannot be null")
	}
	return strings.TrimSpace(*value), nil
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
	machineRef, err := resolveOptionalMachineRef(input.MachineRef)
	if err != nil {
		return resolvedUploadFileRequest{}, err
	}
	return resolvedUploadFileRequest{Source: input.Source, MachineRef: machineRef}, nil
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
	artifactID, ok := strings.CutPrefix(input.Path, toolcatalog.ArtifactVFSRoot+"/")
	if !ok || artifactID == "" || strings.Contains(artifactID, "/") {
		return resolvedDownloadFileRequest{}, errors.New("download path must be /artifacts/<artifact_id>")
	}
	if _, err := publicid.Decode(publicid.KindArtifact, artifactID); err != nil {
		return resolvedDownloadFileRequest{}, errors.New("artifact path must contain a valid artifact ID")
	}
	if input.Destination == "" {
		return resolvedDownloadFileRequest{}, errors.New("destination is required")
	}
	if strings.Contains(input.Destination, "\x00") {
		return resolvedDownloadFileRequest{}, errors.New("destination cannot contain NUL")
	}
	machineRef, err := resolveOptionalMachineRef(input.MachineRef)
	if err != nil {
		return resolvedDownloadFileRequest{}, err
	}
	return resolvedDownloadFileRequest{
		ArtifactID: artifactID, Destination: input.Destination, MachineRef: machineRef,
	}, nil
}

func artifactUploadCall(call model.ToolCall, resolved resolvedUploadFileRequest) (model.ToolCall, error) {
	input := uploadArtifactRequest{Path: resolved.Source}
	if resolved.MachineRef != "" {
		raw, err := marshalJSON(resolved.MachineRef)
		if err != nil {
			return model.ToolCall{}, err
		}
		input.MachineRef = raw
	}
	raw, err := marshalJSON(input)
	if err != nil {
		return model.ToolCall{}, fmt.Errorf("marshal artifact upload input: %w", err)
	}
	call.Input = raw
	return call, nil
}

func artifactDownloadCall(call model.ToolCall, resolved resolvedDownloadFileRequest) (model.ToolCall, error) {
	input := downloadArtifactRequest{
		ArtifactID: resolved.ArtifactID,
		Path:       resolved.Destination,
	}
	if resolved.MachineRef != "" {
		raw, err := marshalJSON(resolved.MachineRef)
		if err != nil {
			return model.ToolCall{}, err
		}
		input.MachineRef = raw
	}
	raw, err := marshalJSON(input)
	if err != nil {
		return model.ToolCall{}, fmt.Errorf("marshal artifact download input: %w", err)
	}
	call.Input = raw
	return call, nil
}

func runUploadFile(
	ctx context.Context,
	call transactionalToolContext,
) (transactionalPhaseResult, error) {
	resolved, err := resolveUploadFileRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	artifactCall, err := artifactUploadCall(call.Call, resolved)
	if err != nil {
		return nil, err
	}
	call.Call = artifactCall
	return runUploadArtifact(ctx, call)
}

func runDownloadFile(
	ctx context.Context,
	call transactionalToolContext,
) (transactionalPhaseResult, error) {
	resolved, err := resolveDownloadFileRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	artifactCall, err := artifactDownloadCall(call.Call, resolved)
	if err != nil {
		return nil, err
	}
	call.Call = artifactCall
	return runDownloadArtifact(ctx, call)
}

func uploadFilePermissionChallenge(
	ctx context.Context,
	executor Executor,
	turn Turn,
	call model.ToolCall,
	mode permissionModeContext,
) (toolpermission.Request, error) {
	resolved, err := resolveUploadFileRequest(call.Input)
	if err != nil {
		return toolpermission.Request{}, err
	}
	artifactCall, err := artifactUploadCall(call, resolved)
	if err != nil {
		return toolpermission.Request{}, err
	}
	return uploadArtifactPermissionChallenge(ctx, executor, turn, artifactCall, mode)
}

func downloadFilePermissionChallenge(
	ctx context.Context,
	executor Executor,
	turn Turn,
	call model.ToolCall,
	mode permissionModeContext,
) (toolpermission.Request, error) {
	resolved, err := resolveDownloadFileRequest(call.Input)
	if err != nil {
		return toolpermission.Request{}, err
	}
	artifactCall, err := artifactDownloadCall(call, resolved)
	if err != nil {
		return toolpermission.Request{}, err
	}
	return downloadArtifactPermissionChallenge(ctx, executor, turn, artifactCall, mode)
}
