package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/processaction"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

const fileTransferProcessTimeoutSeconds = 30

type uploadFileRequest struct {
	ExpectedDigest json.RawMessage `json:"expected_digest,omitempty"`
	Path           string          `json:"path"`
	Source         string          `json:"source"`
	MachineID      json.RawMessage `json:"machine_id,omitempty"`
}

type resolvedUploadFileRequest struct {
	Path      string
	Source    string
	MachineID uuid.UUID
}

type downloadFileRequest struct {
	Path        string          `json:"path"`
	Destination string          `json:"destination"`
	MachineID   json.RawMessage `json:"machine_id,omitempty"`
}

type resolvedDownloadFileRequest struct {
	Path        string
	Memory      bool
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
	isMemory := strings.HasPrefix(input.Path, memorystore.Root+"/")
	if isMemory {
		if _, _, err := memorystore.ParsePath(input.Path); err != nil {
			return resolvedUploadFileRequest{}, err
		}
		if len(input.ExpectedDigest) != 0 {
			var digest string
			if err := json.Unmarshal(input.ExpectedDigest, &digest); err != nil {
				return resolvedUploadFileRequest{}, err
			}
			if err := daemonprotocol.ValidateFileDigest(digest); err != nil {
				return resolvedUploadFileRequest{}, err
			}
		}
	} else if len(input.ExpectedDigest) != 0 {
		return resolvedUploadFileRequest{}, errors.New("expected_digest only applies to memory")
	}
	if !isMemory && input.Path != toolcatalog.ArtifactVFSRoot {
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
	return resolvedUploadFileRequest{Path: input.Path, Source: input.Source, MachineID: machineID}, nil
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
	isMemory := strings.HasPrefix(input.Path, memorystore.Root+"/")
	if isMemory {
		if _, _, err := memorystore.ParsePath(input.Path); err != nil {
			return resolvedDownloadFileRequest{}, err
		}
	} else {
		if _, err := resolveArtifactPath(input.Path); err != nil {
			return resolvedDownloadFileRequest{}, errors.New("path must be /artifacts/<artifact_id>")
		}
	}
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
		Path: input.Path, Memory: isMemory,
		Destination: input.Destination, MachineID: machineID,
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
	authorization, err := fileTransferAuthorizationInput(binding.ID, call.Call.Input)
	if err != nil {
		return nil, err
	}
	return startProcessTool(ctx, call, binding, authorization,
		fileTransferProcessInput("upload", toolCallID, resolved.Source, resolved.Path, fileTransferProcessTimeoutSeconds))
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
	authorization, err := fileTransferAuthorizationInput(binding.ID, call.Call.Input)
	if err != nil {
		return nil, err
	}
	timeout := 0
	if resolved.Memory {
		timeout = fileTransferProcessTimeoutSeconds
	}
	return startProcessTool(ctx, call, binding, authorization,
		fileTransferProcessInput("download", toolCallID, resolved.Destination, resolved.Path, timeout))
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
	binding, err := executor.ResolveMachineExecutionTarget(ctx, turn, resolved.MachineID)
	if err != nil {
		return toolpermission.Request{}, executor.machinePreparationError(err)
	}
	authorization, err := fileTransferAuthorizationInput(binding.ID, call.Input)
	if err != nil {
		return toolpermission.Request{}, err
	}
	machineID, err := publicid.Encode(publicid.KindMachine, binding.MachineID)
	if err != nil {
		return toolpermission.Request{}, err
	}
	return permissionChallenge(call, mode, authorization,
		interactionform.ContextItem{Label: "Source", Value: resolved.Source},
		interactionform.ContextItem{Label: "Destination", Value: resolved.Path},
		interactionform.ContextItem{Label: "Machine", Value: machineID},
	)
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
	binding, err := executor.ResolveMachineExecutionTarget(ctx, turn, resolved.MachineID)
	if err != nil {
		return toolpermission.Request{}, executor.machinePreparationError(err)
	}
	authorization, err := fileTransferAuthorizationInput(binding.ID, call.Input)
	if err != nil {
		return toolpermission.Request{}, err
	}
	machineID, err := publicid.Encode(publicid.KindMachine, binding.MachineID)
	if err != nil {
		return toolpermission.Request{}, err
	}
	return permissionChallenge(call, mode, authorization,
		interactionform.ContextItem{Label: "Source", Value: resolved.Path},
		interactionform.ContextItem{Label: "Destination", Value: resolved.Destination},
		interactionform.ContextItem{Label: "Machine", Value: machineID},
	)
}

func fileTransferAuthorizationInput(bindingID uuid.UUID, input json.RawMessage) (json.RawMessage, error) {
	return marshalJSON(struct {
		BindingID string          `json:"agent_machine_binding_id"`
		Input     json.RawMessage `json:"input"`
	}{BindingID: bindingID.String(), Input: input})
}

func fileTransferProcessInput(
	direction, toolCallID, localPath, remotePath string,
	timeoutSeconds int,
) executionstore.CreateProcessInput {
	encodedPath := base64.RawURLEncoding.EncodeToString([]byte(localPath))
	var command string
	switch {
	case strings.HasPrefix(remotePath, memorystore.Root+"/"):
		command = fmt.Sprintf(
			`"$OMNARA_HOME/bin/omnarad" __omnara_file_transfer %s %s %s`,
			direction, toolCallID, encodedPath,
		)
	case direction == "upload":
		command = fmt.Sprintf(`"$OMNARA_HOME/bin/omnarad" __omnara_upload_artifact %s %s`, toolCallID, encodedPath)
	default:
		command = fmt.Sprintf(
			`"$OMNARA_HOME/bin/omnarad" __omnara_download_artifact %s %s %s`,
			toolCallID, strings.TrimPrefix(remotePath, toolcatalog.ArtifactVFSRoot+"/"), encodedPath,
		)
	}
	return executionstore.CreateProcessInput{
		IOMode: processcmd.IOModePipe, Command: command, ShellSelector: processcmd.ShellDefault,
		InitialWaitMS: processaction.MaxWaitMilliseconds, TimeoutSeconds: timeoutSeconds,
	}
}
