package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/processaction"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

const machineFileProcessTimeoutSeconds = 30

var errMachineFileMachineUnavailable = errors.New(
	"paths outside /memory and /artifacts are machine paths, and no executable machine is available",
)

type machineFileCall struct {
	machineID uuid.UUID
	kind      processcmd.ExecutionKind
	operation processcmd.FileOperation
	cursor    string
}

type machineFileResolver func(json.RawMessage) (machineFileCall, bool, error)

func isOmnaraFilePath(path string) bool {
	for _, root := range []string{memorystore.Root, toolcatalog.ArtifactVFSRoot} {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

func onMachine(path string, machineID uuid.UUID) bool {
	return machineID != uuid.Nil || !isOmnaraFilePath(path)
}

func validateMachinePath(path string) error {
	if path == "" {
		return errors.New("path is required")
	}
	if strings.Contains(path, "\x00") {
		return errors.New("path cannot contain NUL")
	}
	return nil
}

func (c *machineFileCall) prepare(turn Turn, binding executionstore.AgentMachineBindingRecord) error {
	if binding.DaemonVersion != "" && !processcmd.SupportsFileOperations(binding.DaemonVersion) {
		return errors.New(daemonprotocol.UnsupportedFileOperationsMessage(binding.DaemonVersion))
	}
	if c.kind != processcmd.KindFileList {
		return nil
	}
	c.operation.CursorScope = listing.FileCursorScope(
		turn.ProjectID.String(), turn.AgentID.String(), binding.MachineID.String(), c.operation.Path,
	)
	if c.cursor == "" {
		return nil
	}
	cursor, ok := listing.DecodeFileCursor(c.cursor)
	if !ok || cursor.Scope != c.operation.CursorScope || cursor.Key == "" {
		return errInvalidFileListCursor
	}
	c.operation.After = cursor.Key
	return nil
}

// machineFileTool registers a file tool that runs Omnara paths async and machine paths as processes.
func machineFileTool(name string, async asyncToolHandler, resolve machineFileResolver) toolRegistration {
	return toolRegistration{
		name: name,
		semanticInputValidator: func(raw json.RawMessage) error {
			_, _, err := resolve(raw)
			return err
		},
		handler: toolHandler{
			Transactional: runMachineFileTool(resolve),
			Async:         async,
			Background:    wakeMachineFileTool,
		},
		permissionModes: commonPermissionModeHandlers(machineFilePermissionChallenge(resolve)),
	}
}

func runMachineFileTool(resolve machineFileResolver) transactionalToolHandler {
	return func(ctx context.Context, call transactionalToolContext) (transactionalPhaseResult, error) {
		request, machine, err := resolve(call.Call.Input)
		if err != nil {
			return failInTransaction(toolResultContent{}, err), nil
		}
		if !machine {
			return continueAsync(), nil
		}
		binding, err := resolveMachineExecutionTargetForToolCall(ctx, call.Reader, request.machineID)
		if errors.Is(err, ErrNoActiveAgentMachineBinding) && request.machineID == uuid.Nil {
			return failInTransaction(toolResultContent{}, errMachineFileMachineUnavailable), nil
		}
		if err != nil {
			return processToolMachineResolutionError(err)
		}
		if err := request.prepare(call.Turn, binding); err != nil {
			return failInTransaction(toolResultContent{}, err), nil
		}
		authorization, err := fileTransferAuthorizationInput(binding.ID, call.Call.Input)
		if err != nil {
			return nil, err
		}
		return startProcessTool(ctx, call, binding, authorization, executionstore.CreateProcessInput{
			ExecutionSpec:  processcmd.ForFileOperation(request.kind, request.operation),
			InitialWaitMS:  processaction.MaxWaitMilliseconds,
			TimeoutSeconds: machineFileProcessTimeoutSeconds,
		})
	}
}

func wakeMachineFileTool(ctx context.Context, call backgroundToolContext) error {
	if _, started := call.CommandResult.(executionstore.ProcessRecord); !started {
		return nil
	}
	return wakeProcessTool(ctx, call)
}

func machineFilePermissionChallenge(resolve machineFileResolver) permissionChallengeBuilder {
	return func(
		ctx context.Context,
		executor Executor,
		turn Turn,
		call model.ToolCall,
		mode permissionModeContext,
	) (toolpermission.Request, error) {
		request, machine, err := resolve(call.Input)
		if err != nil {
			return toolpermission.Request{}, err
		}
		if !machine {
			return genericPermissionChallenge(ctx, executor, turn, call, mode)
		}
		binding, err := executor.ResolveMachineExecutionTarget(ctx, turn, request.machineID)
		if errors.Is(err, ErrNoActiveAgentMachineBinding) && request.machineID == uuid.Nil {
			return toolpermission.Request{}, machineFilePreparationError(errMachineFileMachineUnavailable)
		}
		if err != nil {
			return toolpermission.Request{}, executor.machinePreparationError(err)
		}
		if err := request.prepare(turn, binding); err != nil {
			return toolpermission.Request{}, machineFilePreparationError(err)
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
			interactionform.ContextItem{Label: "Path", Value: request.operation.Path},
			interactionform.ContextItem{Label: "Machine", Value: machineID},
		)
	}
}

func machineFilePreparationError(cause error) error {
	content, err := structuredToolResultContent(map[string]any{"error": cause.Error()})
	if err != nil {
		return err
	}
	return newToolCallPreparationError(content, cause)
}
