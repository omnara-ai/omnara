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
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const uploadArtifactProcessTimeoutSeconds = 30

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
