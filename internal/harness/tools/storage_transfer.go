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

type storageUploadRequest struct {
	Path      string          `json:"path"`
	Source    string          `json:"source"`
	MachineID json.RawMessage `json:"machine_id,omitempty"`
	Force     bool            `json:"force,omitempty"`
	View      bool            `json:"view,omitempty"`
}

type resolvedStorageUploadRequest struct {
	Path      string
	Source    string
	MachineID uuid.UUID
	Force     bool
	View      bool
}

type storageDownloadRequest struct {
	Path        string          `json:"path"`
	Destination string          `json:"destination"`
	MachineID   json.RawMessage `json:"machine_id,omitempty"`
}

type resolvedStorageDownloadRequest struct {
	Path        string
	Destination string
	MachineID   uuid.UUID
}

func validateStorageUploadInput(input json.RawMessage) error {
	_, err := resolveStorageUploadRequest(input)
	return err
}

func resolveStorageUploadRequest(raw json.RawMessage) (resolvedStorageUploadRequest, error) {
	var input storageUploadRequest
	if err := decodeSingleStrictJSON(raw, &input, "storage_upload request"); err != nil {
		return resolvedStorageUploadRequest{}, fmt.Errorf("parse storage_upload request: %w", err)
	}
	isMemory := strings.HasPrefix(input.Path, memorystore.Root+"/")
	if isMemory {
		if _, _, err := memorystore.ParsePath(input.Path); err != nil {
			return resolvedStorageUploadRequest{}, err
		}
		if input.View {
			return resolvedStorageUploadRequest{}, errors.New("view only applies to /artifacts uploads")
		}
	} else if input.Force {
		return resolvedStorageUploadRequest{}, errors.New("force only applies to memory uploads")
	}
	if !isMemory && input.Path != toolcatalog.ArtifactVFSRoot {
		return resolvedStorageUploadRequest{}, errors.New("upload path must be /artifacts")
	}
	if input.Source == "" {
		return resolvedStorageUploadRequest{}, errors.New("source is required")
	}
	if strings.Contains(input.Source, "\x00") {
		return resolvedStorageUploadRequest{}, errors.New("source cannot contain NUL")
	}
	machineID, err := resolveOptionalMachineID(input.MachineID)
	if err != nil {
		return resolvedStorageUploadRequest{}, err
	}
	return resolvedStorageUploadRequest{
		Path: input.Path, Source: input.Source, MachineID: machineID, Force: input.Force, View: input.View,
	}, nil
}

func validateStorageDownloadInput(input json.RawMessage) error {
	_, err := resolveStorageDownloadRequest(input)
	return err
}

func resolveStorageDownloadRequest(raw json.RawMessage) (resolvedStorageDownloadRequest, error) {
	var input storageDownloadRequest
	if err := decodeSingleStrictJSON(raw, &input, "storage_download request"); err != nil {
		return resolvedStorageDownloadRequest{}, fmt.Errorf("parse storage_download request: %w", err)
	}
	if err := validateFilePath(input.Path); err != nil {
		return resolvedStorageDownloadRequest{}, err
	}
	if input.Destination == "" {
		return resolvedStorageDownloadRequest{}, errors.New("destination is required")
	}
	if strings.Contains(input.Destination, "\x00") {
		return resolvedStorageDownloadRequest{}, errors.New("destination cannot contain NUL")
	}
	machineID, err := resolveOptionalMachineID(input.MachineID)
	if err != nil {
		return resolvedStorageDownloadRequest{}, err
	}
	return resolvedStorageDownloadRequest{
		Path: input.Path, Destination: input.Destination, MachineID: machineID,
	}, nil
}

func runStorageUpload(
	ctx context.Context,
	call transactionalToolContext,
) (transactionalPhaseResult, error) {
	resolved, err := resolveStorageUploadRequest(call.Call.Input)
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

func runStorageDownload(
	ctx context.Context,
	call transactionalToolContext,
) (transactionalPhaseResult, error) {
	resolved, err := resolveStorageDownloadRequest(call.Call.Input)
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

func storageUploadPermissionChallenge(
	ctx context.Context,
	executor Executor,
	turn Turn,
	call model.ToolCall,
	mode permissionModeContext,
) (toolpermission.Request, error) {
	resolved, err := resolveStorageUploadRequest(call.Input)
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

func storageDownloadPermissionChallenge(
	ctx context.Context,
	executor Executor,
	turn Turn,
	call model.ToolCall,
	mode permissionModeContext,
) (toolpermission.Request, error) {
	resolved, err := resolveStorageDownloadRequest(call.Input)
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
