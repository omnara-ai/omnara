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

// machineFileCall is a read_file, write_file, list_files, or search_files
// call that omnarad runs on an attached machine as a file operation process.
type machineFileCall struct {
	machineID uuid.UUID
	kind      processcmd.ExecutionKind
	operation processcmd.FileOperation
	// cursor is the list_files next_cursor to continue from.
	cursor string
}

// machineFileResolver validates a file tool input and reports false when the
// call is served from Omnara storage.
type machineFileResolver func(json.RawMessage) (machineFileCall, bool, error)

// isOmnaraFilePath reports whether path is under /memory or /artifacts.
func isOmnaraFilePath(path string) bool {
	for _, root := range []string{memorystore.Root, toolcatalog.ArtifactVFSRoot} {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

// onMachine routes Omnara paths to storage unless machine_id is supplied.
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

// prepare rejects daemons that cannot run file operations and binds list
// cursors to the selected machine.
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

// machineFileToolHandler serves Omnara calls with async and machine calls with
// file operation processes.
func machineFileToolHandler(async asyncToolHandler, resolve machineFileResolver) toolHandler {
	return toolHandler{
		Transactional: runMachineFileTool(resolve),
		Async:         async,
		Background:    wakeMachineFileTool(resolve),
	}
}

// runMachineFileTool leaves Omnara calls to the async phase and starts a file
// operation process for machine calls.
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

func wakeMachineFileTool(resolve machineFileResolver) backgroundToolHandler {
	return func(ctx context.Context, call backgroundToolContext) error {
		_, machine, err := resolve(call.Call.Input)
		if err != nil || !machine {
			return err
		}
		return wakeProcessTool(ctx, call)
	}
}

// machineFilePermissionChallenge shows the path and machine for machine calls.
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

// machineFilePreparationError fails a machine call that cannot be approved
// with the same result the transactional phase would report.
func machineFilePreparationError(cause error) error {
	content, err := structuredToolResultContent(map[string]any{"error": cause.Error()})
	if err != nil {
		return err
	}
	return newToolCallPreparationError(content, cause)
}
