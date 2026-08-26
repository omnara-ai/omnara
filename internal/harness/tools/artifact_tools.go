package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/processaction"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const uploadArtifactProcessTimeoutSeconds = 300

type uploadArtifactRequest struct {
	Path       string          `json:"path"`
	MachineRef json.RawMessage `json:"machine_ref,omitempty"`
}

type resolvedUploadArtifactRequest struct {
	Path       string
	MachineRef string
}

type uploadArtifactAuthorization struct {
	AgentMachineBindingID string `json:"agent_machine_binding_id"`
	Path                  string `json:"path"`
}

type showArtifactRequest struct {
	ArtifactID string `json:"artifact_id"`
}

type artifactToolResult struct {
	ArtifactID  string `json:"artifact_id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

func validateUploadArtifactInput(input json.RawMessage) error {
	_, err := resolveUploadArtifactRequest(input)
	return err
}

func resolveUploadArtifactRequest(raw json.RawMessage) (resolvedUploadArtifactRequest, error) {
	var input uploadArtifactRequest
	if err := decodeSingleStrictJSON(raw, &input, "upload_artifact request"); err != nil {
		return resolvedUploadArtifactRequest{}, fmt.Errorf("parse upload_artifact request: %w", err)
	}
	if input.Path == "" {
		return resolvedUploadArtifactRequest{}, errors.New("path is required")
	}
	if strings.Contains(input.Path, "\x00") {
		return resolvedUploadArtifactRequest{}, errors.New("path cannot contain NUL")
	}
	machineRef := ""
	if len(input.MachineRef) > 0 {
		var rawMachineRef *string
		if err := json.Unmarshal(input.MachineRef, &rawMachineRef); err != nil {
			return resolvedUploadArtifactRequest{}, fmt.Errorf("parse machine_ref: %w", err)
		}
		if rawMachineRef == nil {
			return resolvedUploadArtifactRequest{}, errors.New("machine_ref cannot be null")
		}
		machineRef = strings.TrimSpace(*rawMachineRef)
	}
	return resolvedUploadArtifactRequest{Path: input.Path, MachineRef: machineRef}, nil
}

func uploadArtifactAuthorizationInput(
	bindingID storage.ID,
	path string,
) (json.RawMessage, error) {
	input, err := marshalJSON(uploadArtifactAuthorization{
		AgentMachineBindingID: bindingID.String(),
		Path:                  path,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal upload_artifact authorization: %w", err)
	}
	return input, nil
}

func runUploadArtifact(
	ctx context.Context,
	call transactionalToolContext,
) (transactionalPhaseResult, error) {
	resolved, err := resolveUploadArtifactRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	binding, err := resolveMachineExecutionTargetForToolCall(ctx, call.Reader, resolved.MachineRef)
	if err != nil {
		return processToolMachineResolutionError(err)
	}
	toolCallID, err := publicid.Encode(publicid.KindToolCall, call.ToolCallID)
	if err != nil {
		return nil, fmt.Errorf("encode tool call id: %w", err)
	}
	authorizationInput, err := uploadArtifactAuthorizationInput(binding.ID, resolved.Path)
	if err != nil {
		return nil, err
	}
	return startProcessTool(
		ctx,
		call,
		binding,
		authorizationInput,
		uploadArtifactProcessInput(toolCallID, resolved.Path),
	)
}

func uploadArtifactProcessInput(
	toolCallID string,
	path string,
) executionstore.CreateProcessInput {
	encodedPath := base64.RawURLEncoding.EncodeToString([]byte(path))
	command := fmt.Sprintf(
		`"$OMNARA_HOME/bin/omnarad" __omnara_upload_artifact %s %s`,
		toolCallID,
		encodedPath,
	)
	return executionstore.CreateProcessInput{
		IOMode:         processcmd.IOModePipe,
		Command:        command,
		ShellSelector:  processcmd.ShellDefault,
		InitialWaitMS:  processaction.MaxWaitMilliseconds,
		TimeoutSeconds: uploadArtifactProcessTimeoutSeconds,
	}
}

func validateShowArtifactInput(input json.RawMessage) error {
	_, err := resolveShowArtifactRequest(input)
	return err
}

func resolveShowArtifactRequest(raw json.RawMessage) (storage.ID, error) {
	var input showArtifactRequest
	if err := decodeSingleStrictJSON(raw, &input, "show_artifact request"); err != nil {
		return storage.NilID, fmt.Errorf("parse show_artifact request: %w", err)
	}
	artifactID, err := publicid.Decode(publicid.KindArtifact, input.ArtifactID)
	if err != nil {
		return storage.NilID, errors.New("artifact_id is invalid")
	}
	return artifactID, nil
}

func runShowArtifact(ctx context.Context, call asyncToolContext) (asyncPhaseResult, error) {
	artifactID, err := resolveShowArtifactRequest(call.Call.Input)
	if err != nil {
		return failArtifactTool("malformed", err.Error(), err)
	}
	artifact, err := call.Executor.Store.Artifacts().GetArtifact(
		ctx,
		call.Turn.ProjectID,
		call.Turn.AgentID,
		artifactID,
	)
	if errors.Is(err, storeerr.ErrNotFound) {
		return failArtifactTool("artifact_not_found", "artifact does not exist", err)
	}
	if err != nil {
		return nil, err
	}
	content, err := artifactToolResultContent(artifact)
	if err != nil {
		return nil, err
	}
	return completeAsynchronously(content), nil
}

func artifactToolResultContent(artifact artifactstore.ArtifactRecord) (toolResultContent, error) {
	artifactID, err := publicid.Encode(publicid.KindArtifact, artifact.ID)
	if err != nil {
		return toolResultContent{}, err
	}
	sizeBytes := int64(0)
	if artifact.SizeBytes != nil {
		sizeBytes = *artifact.SizeBytes
	}
	filename := artifact.Filename
	if strings.TrimSpace(filename) == "" {
		filename = "artifact"
	}
	return structuredToolResultContent(artifactToolResult{
		ArtifactID:  artifactID,
		Filename:    filename,
		ContentType: artifact.ContentType,
		SizeBytes:   sizeBytes,
	})
}

func failArtifactTool(code, message string, cause error) (asyncPhaseResult, error) {
	content, err := structuredToolResultContent(map[string]any{
		"error_code": code,
		"error":      message,
	})
	if err != nil {
		return nil, err
	}
	return failAsynchronously(content, cause), nil
}
