package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

const (
	fileUploadProcessTimeoutSeconds   = 30
	fileDownloadProcessTimeoutSeconds = 120
	fileTransferInitialWaitMS         = 10 * 1000
)

type uploadFileRequest struct {
	Path      string          `json:"path"`
	Source    string          `json:"source"`
	MachineID json.RawMessage `json:"machine_id,omitempty"`
	Force     bool            `json:"force,omitempty"`
	View      bool            `json:"view,omitempty"`
}

type resolvedUploadFileRequest struct {
	Path      string
	Source    string
	MachineID uuid.UUID
	Force     bool
	View      bool
}

type downloadFileRequest struct {
	Path        string          `json:"path"`
	Destination string          `json:"destination"`
	MachineID   json.RawMessage `json:"machine_id,omitempty"`
}

type resolvedDownloadFileRequest struct {
	Path        string
	Destination string
	MachineID   uuid.UUID
}

func validateUploadFileInput(input json.RawMessage) error {
	_, err := resolveUploadFileRequest(input)
	return err
}

func resolveUploadFileRequest(raw json.RawMessage) (resolvedUploadFileRequest, error) {
	var input uploadFileRequest
	if err := decodeSingleStrictJSON(raw, &input, "storage_upload request"); err != nil {
		return resolvedUploadFileRequest{}, fmt.Errorf("parse storage_upload request: %w", err)
	}
	isMemory := strings.HasPrefix(input.Path, memorystore.Root+"/")
	if isMemory {
		if _, _, err := memorystore.ParsePath(input.Path); err != nil {
			return resolvedUploadFileRequest{}, err
		}
		if input.View {
			return resolvedUploadFileRequest{}, errors.New("view only applies to /artifacts uploads")
		}
	} else if input.Force {
		return resolvedUploadFileRequest{}, errors.New("force only applies to memory uploads")
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
	return resolvedUploadFileRequest{
		Path: input.Path, Source: input.Source, MachineID: machineID, Force: input.Force, View: input.View,
	}, nil
}

func validateDownloadFileInput(input json.RawMessage) error {
	_, err := resolveDownloadFileRequest(input)
	return err
}

func resolveDownloadFileRequest(raw json.RawMessage) (resolvedDownloadFileRequest, error) {
	var input downloadFileRequest
	if err := decodeSingleStrictJSON(raw, &input, "storage_download request"); err != nil {
		return resolvedDownloadFileRequest{}, fmt.Errorf("parse storage_download request: %w", err)
	}
	if err := validateFilePath(input.Path); err != nil {
		return resolvedDownloadFileRequest{}, err
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
		Path: input.Path, Destination: input.Destination, MachineID: machineID,
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
	authorization, err := fileTransferAuthorizationInput(binding.ID, call.Call.Input)
	if err != nil {
		return nil, err
	}
	processInput, err := fileTransferProcessInput(
		ctx, call.Reader, processcmd.FileTransferUpload, resolved.Source, resolved.Path, resolved.Force, resolved.View,
	)
	if errors.Is(err, storeerr.ErrNotFound) {
		return failInTransaction(toolResultContent{}, err), nil
	}
	if err != nil {
		return nil, err
	}
	return startProcessTool(ctx, call, binding, authorization, processInput)
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
	authorization, err := fileTransferAuthorizationInput(binding.ID, call.Call.Input)
	if err != nil {
		return nil, err
	}
	processInput, err := fileTransferProcessInput(
		ctx, call.Reader, processcmd.FileTransferDownload, resolved.Destination, resolved.Path, false, false,
	)
	if errors.Is(err, storeerr.ErrNotFound) {
		return failInTransaction(toolResultContent{}, err), nil
	}
	if err != nil {
		return nil, err
	}
	return startProcessTool(ctx, call, binding, authorization, processInput)
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
	ctx context.Context, reader *executionstore.ToolCallReader,
	direction processcmd.FileTransferDirection, localPath, remotePath string, force, view bool,
) (executionstore.CreateProcessInput, error) {
	transfer := processcmd.FileTransfer{Direction: direction, LocalPath: localPath}
	if strings.HasPrefix(remotePath, memorystore.Root+"/") {
		name, path, err := memorystore.ParsePath(remotePath)
		if err != nil {
			return executionstore.CreateProcessInput{}, err
		}
		id, err := reader.ResolveMemoryTransferStore(ctx, name)
		if err != nil {
			return executionstore.CreateProcessInput{}, err
		}
		transfer.Target.Memory = &processcmd.MemoryTarget{StoreID: id, Path: path, Force: force}
	} else {
		target := &processcmd.ArtifactTarget{View: view}
		if direction == processcmd.FileTransferDownload {
			id, err := resolveArtifactPath(remotePath)
			if err != nil {
				return executionstore.CreateProcessInput{}, err
			}
			target.ID = id
		}
		transfer.Target.Artifact = target
	}
	timeoutSeconds := fileUploadProcessTimeoutSeconds
	if direction == processcmd.FileTransferDownload {
		timeoutSeconds = fileDownloadProcessTimeoutSeconds
	}
	return executionstore.CreateProcessInput{
		ExecutionSpec:  processcmd.ForFileTransfer(transfer),
		InitialWaitMS:  fileTransferInitialWaitMS,
		TimeoutSeconds: timeoutSeconds,
	}, nil
}
