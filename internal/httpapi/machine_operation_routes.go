package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

// Machine operations let an API caller run a command on, write a file to, or
// read a file from a connected machine without an agent. Each one is a single
// daemon process offered with the existing process protocol (the same
// ExecutionSpec run_command, upload_file, and download_file use), so released
// daemons run them unchanged. Their state lives in machine_operations rather
// than processes because they have no agent, tool call, or binding. The
// handler offers the process through the replica inbox, then polls the row
// until the daemon's terminal report lands.

const (
	machineOperationReportGrace     = time.Minute
	machineOperationAcceptTimeout   = 2 * time.Minute
	machineOperationWakeRetryMax    = 5 * time.Second
	machineOperationRepublishPeriod = 5 * time.Second
	machineOperationFileTimeout     = 300
	machineOperationCleanupTimeout  = 5 * time.Second
	machineOperationBlobKeyPrefix   = "machine-operations/"
	machineOperationBlobInput       = "input"
	machineOperationBlobOutput      = "output"
	machineOperationTimeoutReason   = "timeout"
	machineOperationOfflineMessage  = "machine is offline"
	machineOperationNoReportMessage = "the machine daemon did not report a result in time"
)

type machineOperationInbox interface {
	Publish(
		ctx context.Context,
		machineID uuid.UUID,
		kind daemonprotocol.MessageType,
		payload json.RawMessage,
		opts notifications.PublishOptions,
	) error
}

type machineOperationConfig struct {
	inbox machineOperationInbox
}

// WithMachineOperations enables the agent-free machine exec and file routes.
func WithMachineOperations(inbox machineOperationInbox) Option {
	return func(s *Server) {
		if inbox == nil {
			return
		}
		s.machineOperations = &machineOperationConfig{inbox: inbox}
	}
}

// machineOperationRequest is one process to offer for a machine operation.
type machineOperationRequest struct {
	spec           processcmd.ExecutionSpec
	cwd            string
	env            map[string]string
	timeoutSeconds int
	waitMS         int
}

// processTerminalResult is the result object the daemon attaches to a
// process_finished report.
type processTerminalResult struct {
	Output       string                             `json:"output"`
	Truncated    bool                               `json:"truncated"`
	Error        string                             `json:"error"`
	FileTransfer *daemonprotocol.FileTransferResult `json:"file_transfer"`
}

func (s strictOpenAPIServer) ExecMachineCommand(
	ctx context.Context,
	request openapi.ExecMachineCommandRequestObject,
) (openapi.ExecMachineCommandResponseObject, error) {
	machine, err := activeOperationMachine(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	operation, err := execMachineOperationRequest(machine, *request.Body)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	started := time.Now()
	record, err := s.runMachineOperation(ctx, machine, operation)
	if err != nil {
		return nil, err
	}
	return openapi.ExecMachineCommand200JSONResponse(execMachineCommandResponse(record, time.Since(started))), nil
}

func execMachineOperationRequest(
	machine executionstore.MachineRecord,
	body openapi.ExecMachineCommandRequest,
) (machineOperationRequest, error) {
	shell := processcmd.ShellDefault
	if body.Shell != nil {
		shell = processcmd.ShellSelector(*body.Shell)
	}
	spec, err := processcmd.NormalizeShellCommand(body.Command, shell)
	if err != nil {
		return machineOperationRequest{}, err
	}
	timeoutSeconds := 300
	if body.TimeoutSeconds != nil {
		timeoutSeconds = *body.TimeoutSeconds
	}
	if timeoutSeconds < 1 || timeoutSeconds > 3600 {
		return machineOperationRequest{}, errors.New("timeout_seconds must be between 1 and 3600")
	}
	requestedCwd := strings.TrimSpace(stringValue(body.Cwd))
	if strings.ContainsRune(requestedCwd, 0) {
		return machineOperationRequest{}, errors.New("cwd cannot contain NUL")
	}
	env := map[string]string{}
	if environment, err := executionstore.MachineEnvironmentFromColumns(machine.Env, nil); err == nil {
		for name, value := range environment.Env {
			env[name] = value
		}
	}
	if body.Env != nil {
		for name, value := range *body.Env {
			if name == "" || strings.ContainsAny(name, "=\x00") || strings.ContainsRune(value, 0) {
				return machineOperationRequest{}, fmt.Errorf("invalid env entry %q", name)
			}
			env[name] = value
		}
	}
	if len(env) == 0 {
		env = nil
	}
	cwd := resolveMachineOperationCwd(machine.Cwd, requestedCwd)
	if cwd == "" {
		cwd = "~"
	}
	return machineOperationRequest{
		spec:           processcmd.ForShell(spec.Command, spec.Shell, processcmd.IOModePipe),
		cwd:            cwd,
		env:            env,
		timeoutSeconds: timeoutSeconds,
	}, nil
}

func execMachineCommandResponse(
	record executionstore.MachineOperationRecord,
	duration time.Duration,
) openapi.ExecMachineCommandResponse {
	var result processTerminalResult
	_ = json.Unmarshal(record.Result, &result)
	timedOut := record.StateReasonCode == machineOperationTimeoutReason
	response := openapi.ExecMachineCommandResponse{
		DurationMs:      duration.Milliseconds(),
		Output:          strings.ToValidUTF8(result.Output, "�"),
		OutputTruncated: result.Truncated,
		TimedOut:        timedOut,
	}
	if record.ExitCode != nil && !timedOut {
		response.ExitCode.Set(*record.ExitCode)
	} else {
		response.ExitCode.SetNull()
	}
	if record.ExitSignal != "" {
		signal := record.ExitSignal
		response.ExitSignal = &signal
	}
	if response.ExitCode.IsNull() {
		message := record.StateReasonMessage
		if message == "" {
			message = result.Error
		}
		if message == "" {
			message = record.StateReasonCode
		}
		if message != "" {
			response.Error = &message
		}
	}
	return response
}

// resolveMachineOperationCwd mirrors how agent processes combine the
// machine's configured cwd with a requested one.
func resolveMachineOperationCwd(machineCwd, requested string) string {
	switch {
	case requested == "":
		return machineCwd
	case processcmd.IsHomeRelativePath(requested):
		return requested
	case path.IsAbs(requested) || machineCwd == "":
		return path.Clean(requested)
	case processcmd.IsHomeRelativePath(machineCwd):
		return strings.TrimSuffix(machineCwd, "/") + "/" + requested
	default:
		return path.Join(machineCwd, requested)
	}
}

func (s strictOpenAPIServer) UploadMachineFile(
	ctx context.Context,
	request openapi.UploadMachineFileRequestObject,
) (openapi.UploadMachineFileResponseObject, error) {
	machine, err := activeOperationMachine(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateMachineFilePath(request.Params.Path); err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	content, err := readMachineOperationBody(ctx, request.Body, daemonprotocol.MaxFileTransferBytes)
	if err != nil {
		return nil, err
	}
	digest := blobstore.ContentDigest(content)
	record, err := s.writeMachineFile(ctx, machine, request.Params.Path, content)
	if err == nil && machineFileParentMissing(record) && isPOSIXPath(request.Params.Path) {
		// The daemon writes through a temporary file beside the target, so
		// the parent must exist. Create it with a shell process and retry.
		var mkdir executionstore.MachineOperationRecord
		mkdir, err = s.runMachineOperation(ctx, machine, machineOperationRequest{
			spec: processcmd.ForShell(
				"mkdir -p -- "+shellQuotePOSIXPath(posixDir(request.Params.Path)),
				processcmd.ShellSH, processcmd.IOModePipe,
			),
			timeoutSeconds: 60,
		})
		if err == nil && mkdir.State == executionstore.MachineOperationStateExited &&
			mkdir.ExitCode != nil && *mkdir.ExitCode == 0 {
			record, err = s.writeMachineFile(ctx, machine, request.Params.Path, content)
		}
	}
	if err != nil {
		return nil, err
	}
	if err := machineFileTransferError(record); err != nil {
		return nil, err
	}
	return openapi.UploadMachineFile200JSONResponse{
		Path: request.Params.Path, SizeBytes: int64(len(content)), Digest: digest,
	}, nil
}

func (s strictOpenAPIServer) writeMachineFile(
	ctx context.Context,
	machine executionstore.MachineRecord,
	remotePath string,
	content []byte,
) (executionstore.MachineOperationRecord, error) {
	operationID, err := uuid.NewV7()
	if err != nil {
		return executionstore.MachineOperationRecord{}, err
	}
	if err := s.putMachineOperationBlob(
		ctx, machine.OrgID, machine.ID, operationID, machineOperationBlobInput, content,
	); err != nil {
		return executionstore.MachineOperationRecord{}, err
	}
	defer s.deleteMachineOperationBlob(ctx, machine, operationID, machineOperationBlobInput)
	return s.runMachineOperationWithID(ctx, machine, operationID, machineOperationRequest{
		spec: processcmd.ForFileTransfer(processcmd.FileTransfer{
			Direction: processcmd.FileTransferDownload,
			LocalPath: remotePath,
			Target:    processcmd.FileTarget{Artifact: &processcmd.ArtifactTarget{ID: operationID}},
		}),
		timeoutSeconds: machineOperationFileTimeout,
		waitMS:         0,
	})
}

func (s strictOpenAPIServer) DownloadMachineFile(
	ctx context.Context,
	request openapi.DownloadMachineFileRequestObject,
) (openapi.DownloadMachineFileResponseObject, error) {
	machine, err := activeOperationMachine(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateMachineFilePath(request.Params.Path); err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	operationID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	defer s.deleteMachineOperationBlob(ctx, machine, operationID, machineOperationBlobOutput)
	record, err := s.runMachineOperationWithID(ctx, machine, operationID, machineOperationRequest{
		spec: processcmd.ForFileTransfer(processcmd.FileTransfer{
			Direction: processcmd.FileTransferUpload,
			LocalPath: request.Params.Path,
			Target:    processcmd.FileTarget{Artifact: &processcmd.ArtifactTarget{}},
		}),
		timeoutSeconds: machineOperationFileTimeout,
	})
	if err != nil {
		return nil, err
	}
	if err := machineFileTransferError(record); err != nil {
		return nil, err
	}
	content, digest, err := s.getMachineOperationBlob(
		ctx, machine.OrgID, machine.ID, operationID, machineOperationBlobOutput,
	)
	if err != nil {
		return nil, apierror.FromCode(
			openapi.ErrorCodeServiceUnavailable, "the machine daemon reported a file that was not staged",
		).WithCause(err)
	}
	etag, cache, nosniff := `"`+digest+`"`, "no-store", "nosniff"
	disposition := contentDisposition(path.Base(strings.ReplaceAll(request.Params.Path, `\`, "/")))
	return openapi.DownloadMachineFile200ApplicationoctetStreamResponse{
		Body: bytes.NewReader(content), ContentLength: int64(len(content)),
		Headers: openapi.DownloadMachineFile200ResponseHeaders{
			ETag: &etag, XOmnaraFileDigest: &digest, ContentDisposition: &disposition,
			CacheControl: &cache, XContentTypeOptions: &nosniff,
		},
	}, nil
}

func validateMachineFilePath(remotePath string) error {
	if remotePath == "" {
		return errors.New("path is required")
	}
	if strings.ContainsRune(remotePath, 0) {
		return errors.New("path cannot contain NUL")
	}
	if processcmd.IsHomeRelativePath(remotePath) || strings.HasPrefix(remotePath, "/") {
		return nil
	}
	if len(remotePath) >= 3 && remotePath[1] == ':' && (remotePath[2] == '\\' || remotePath[2] == '/') {
		return nil
	}
	return errors.New("path must be absolute or start with ~/")
}

func isPOSIXPath(remotePath string) bool {
	return strings.HasPrefix(remotePath, "/") || strings.HasPrefix(remotePath, "~/")
}

func posixDir(remotePath string) string {
	if rest, ok := strings.CutPrefix(remotePath, "~/"); ok {
		return "~/" + path.Dir(rest)
	}
	return path.Dir(remotePath)
}

// shellQuotePOSIXPath single-quotes a path for /bin/sh, keeping a leading ~/
// outside the quotes so the shell still expands it.
func shellQuotePOSIXPath(value string) string {
	prefix := ""
	if rest, ok := strings.CutPrefix(value, "~/"); ok {
		prefix, value = `"$HOME"/`, rest
	}
	return prefix + "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func machineFileParentMissing(record executionstore.MachineOperationRecord) bool {
	transferErr := machineFileTransferMessage(record)
	return strings.Contains(transferErr, "create temporary file") &&
		strings.Contains(strings.ToLower(transferErr), "no such file or directory")
}

func machineFileTransferMessage(record executionstore.MachineOperationRecord) string {
	var result processTerminalResult
	_ = json.Unmarshal(record.Result, &result)
	if result.FileTransfer != nil && result.FileTransfer.Error != nil {
		return result.FileTransfer.Error.Message
	}
	if record.State == executionstore.MachineOperationStateExited &&
		record.ExitCode != nil && *record.ExitCode == 0 && result.FileTransfer != nil {
		return ""
	}
	for _, message := range []string{record.StateReasonMessage, result.Error, record.StateReasonCode} {
		if message != "" {
			return message
		}
	}
	return "file transfer did not complete"
}

// machineFileTransferError maps a finished transfer process onto an API
// error, or nil when the transfer succeeded.
func machineFileTransferError(record executionstore.MachineOperationRecord) error {
	message := machineFileTransferMessage(record)
	if message == "" {
		return nil
	}
	lower := strings.ToLower(message)
	switch {
	case record.StateReasonCode == machineOperationTimeoutReason:
		return apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "file transfer timed out")
	case strings.Contains(lower, "no such file or directory"),
		strings.Contains(lower, "cannot find the"):
		return apierror.FromCode(openapi.ErrorCodeNotFound, message)
	case strings.Contains(lower, "regular file"), strings.Contains(lower, "is a directory"):
		return apierror.FromCode(openapi.ErrorCodeNotAFile, message)
	case strings.Contains(lower, "size limit"):
		return apierror.FromCode(openapi.ErrorCodeRequestTooLarge, message)
	case strings.Contains(lower, "permission denied"):
		return apierror.FromCode(openapi.ErrorCodeForbidden, message)
	default:
		return apierror.FromCode(openapi.ErrorCodeUnprocessable, message)
	}
}

func activeOperationMachine(ctx context.Context) (executionstore.MachineRecord, error) {
	machine, err := machineScopeFromContext(ctx)
	if err != nil {
		return executionstore.MachineRecord{}, err
	}
	if machine.DeletedAt != nil || machine.LifecycleState != executionstore.MachineLifecycleStateActive {
		return executionstore.MachineRecord{}, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	return machine, nil
}

func (s strictOpenAPIServer) runMachineOperation(
	ctx context.Context,
	machine executionstore.MachineRecord,
	request machineOperationRequest,
) (executionstore.MachineOperationRecord, error) {
	operationID, err := uuid.NewV7()
	if err != nil {
		return executionstore.MachineOperationRecord{}, err
	}
	return s.runMachineOperationWithID(ctx, machine, operationID, request)
}

// runMachineOperationWithID records the operation, offers it to the
// machine's daemon until a runtime accepts it, and waits for the terminal
// report. Ending the request early terminates the process.
func (s strictOpenAPIServer) runMachineOperationWithID(
	ctx context.Context,
	machine executionstore.MachineRecord,
	operationID uuid.UUID,
	request machineOperationRequest,
) (executionstore.MachineOperationRecord, error) {
	if s.server.machineOperations == nil {
		return executionstore.MachineOperationRecord{}, apierror.FromCode(
			openapi.ErrorCodeServiceUnavailable, "machine operations are not configured",
		)
	}
	processID, err := publicID(publicid.KindProcess, operationID)
	if err != nil {
		return executionstore.MachineOperationRecord{}, err
	}
	payload, err := json.Marshal(daemonprotocol.Message{
		Type:      daemonprotocol.MessageProcessOffer,
		ProcessID: processID,
		ProcessOffer: &daemonprotocol.ProcessOffer{
			ExecutionSpec:  request.spec,
			ProcessID:      processID,
			Cwd:            request.cwd,
			Env:            request.env,
			WaitMs:         request.waitMS,
			TimeoutSeconds: request.timeoutSeconds,
		},
	})
	if err != nil {
		return executionstore.MachineOperationRecord{}, fmt.Errorf("encode machine operation offer: %w", err)
	}
	if len(payload) > daemonSocketReadLimitBytes {
		return executionstore.MachineOperationRecord{}, apierror.FromCode(
			openapi.ErrorCodeRequestTooLarge, "machine operation exceeds the daemon message size limit",
		)
	}
	if _, err := s.server.store.Execution().CreateMachineOperation(ctx, executionstore.CreateMachineOperationInput{
		ID: operationID, OrgID: machine.OrgID, MachineID: machine.ID,
		ExecutionSpec: request.spec, TimeoutSeconds: request.timeoutSeconds,
	}); err != nil {
		return executionstore.MachineOperationRecord{}, apierror.OrgScoped(err)
	}
	record, err := s.awaitMachineOperation(ctx, machine, operationID, processID, payload,
		time.Duration(request.timeoutSeconds)*time.Second+machineOperationReportGrace)
	if err != nil {
		s.abandonMachineOperation(ctx, machine, operationID, processID)
		return executionstore.MachineOperationRecord{}, err
	}
	return record, nil
}

func (s strictOpenAPIServer) awaitMachineOperation(
	ctx context.Context,
	machine executionstore.MachineRecord,
	operationID uuid.UUID,
	processID string,
	payload json.RawMessage,
	runTimeout time.Duration,
) (executionstore.MachineOperationRecord, error) {
	acceptDeadline := time.Now().Add(machineOperationAcceptTimeout)
	var runDeadline time.Time
	nextPublish := time.Time{}
	wakeDelay := time.Second
	poll := 100 * time.Millisecond
	for {
		record, found, err := s.server.store.Execution().GetMachineOperation(ctx, machine.OrgID, machine.ID, operationID)
		if err != nil {
			return executionstore.MachineOperationRecord{}, apierror.OrgScoped(err)
		}
		if !found {
			return executionstore.MachineOperationRecord{}, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
		}
		now := time.Now()
		switch {
		case record.State.Terminal():
			if record.State == executionstore.MachineOperationStateFailed &&
				record.StateReasonCode == executionstore.MachineOperationReasonNotAccepted {
				return executionstore.MachineOperationRecord{}, apierror.FromCode(
					openapi.ErrorCodeConflict, machineOperationOfflineMessage,
				)
			}
			return record, nil
		case record.State == executionstore.MachineOperationStateQueued:
			if now.After(acceptDeadline) {
				return executionstore.MachineOperationRecord{}, apierror.FromCode(
					openapi.ErrorCodeConflict, "machine did not accept the operation; it may be offline",
				)
			}
			if !now.Before(nextPublish) {
				delay, err := s.publishMachineOperation(ctx, machine, payload, wakeDelay)
				if err != nil {
					return executionstore.MachineOperationRecord{}, err
				}
				if delay > 0 {
					wakeDelay = min(wakeDelay*2, machineOperationWakeRetryMax)
					nextPublish = now.Add(delay)
				} else {
					nextPublish = now.Add(machineOperationRepublishPeriod)
				}
			}
		default:
			if runDeadline.IsZero() {
				runDeadline = now.Add(runTimeout)
			}
			if now.After(runDeadline) {
				return executionstore.MachineOperationRecord{}, apierror.FromCode(
					openapi.ErrorCodeServiceUnavailable, machineOperationNoReportMessage,
				)
			}
		}
		select {
		case <-ctx.Done():
			return executionstore.MachineOperationRecord{}, ctx.Err()
		case <-time.After(poll):
		}
		poll = min(poll*2, time.Second)
	}
}

// publishMachineOperation routes the offer to the replica holding the
// machine's socket. When the daemon is offline it asks the pool manager to
// wake the machine and returns how long to wait before publishing again.
func (s strictOpenAPIServer) publishMachineOperation(
	ctx context.Context,
	machine executionstore.MachineRecord,
	payload json.RawMessage,
	wakeDelay time.Duration,
) (time.Duration, error) {
	err := s.server.machineOperations.inbox.Publish(
		ctx, machine.ID, daemonprotocol.MessageProcessOffer, payload, notifications.PublishOptions{},
	)
	if err == nil {
		return 0, nil
	}
	if !errors.Is(err, notifications.ErrDaemonOffline) {
		return 0, apierror.FromCode(
			openapi.ErrorCodeServiceUnavailable, "machine operation could not be delivered",
		).WithCause(err)
	}
	if s.server.machinePoolManager == nil {
		return 0, apierror.FromCode(openapi.ErrorCodeConflict, machineOperationOfflineMessage)
	}
	shouldWait, wakeErr := s.server.machinePoolManager.WakeMachine(ctx, machine.OrgID, machine.ID)
	if wakeErr != nil && !errors.Is(wakeErr, storeerr.ErrMachineNotWakeCapable) {
		return 0, apierror.FromCode(openapi.ErrorCodeConflict, machineOperationOfflineMessage).WithCause(wakeErr)
	}
	if wakeErr != nil || !shouldWait {
		return 0, apierror.FromCode(openapi.ErrorCodeConflict, machineOperationOfflineMessage)
	}
	return wakeDelay, nil
}

// abandonMachineOperation stops an operation the caller is no longer waiting
// for: a queued one is failed so a late accept is refused, and a started one
// is terminated on the machine.
func (s strictOpenAPIServer) abandonMachineOperation(
	ctx context.Context,
	machine executionstore.MachineRecord,
	operationID uuid.UUID,
	processID string,
) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), machineOperationCleanupTimeout)
	defer cancel()
	record, found, err := s.server.store.Execution().GetMachineOperation(
		cleanupCtx, machine.OrgID, machine.ID, operationID,
	)
	if err != nil || !found || record.State.Terminal() {
		return
	}
	if record.State == executionstore.MachineOperationStateQueued {
		_, _ = s.server.store.Execution().CompleteMachineOperation(cleanupCtx, executionstore.CompleteMachineOperationInput{
			OrgID: machine.OrgID, MachineID: machine.ID, ID: operationID,
			State:           executionstore.MachineOperationStateFailed,
			StateReasonCode: executionstore.MachineOperationReasonNotAccepted,
		})
	}
	payload, err := json.Marshal(daemonprotocol.Message{
		Type: daemonprotocol.MessageProcessTerminate, ProcessID: processID,
	})
	if err != nil {
		return
	}
	_ = s.server.machineOperations.inbox.Publish(
		cleanupCtx, machine.ID, daemonprotocol.MessageProcessTerminate, payload, notifications.PublishOptions{},
	)
}

func readMachineOperationBody(ctx context.Context, body io.Reader, limit int64) ([]byte, error) {
	tooLarge := apierror.FromCode(
		openapi.ErrorCodeRequestTooLarge, fmt.Sprintf("file exceeds the %d MiB limit", limit/(1024*1024)),
	)
	if request, ok := openAPIHTTPRequest(ctx); ok && request.ContentLength > limit {
		return nil, tooLarge
	}
	if body == nil {
		return []byte{}, nil
	}
	content, err := io.ReadAll(io.LimitReader(body, limit+1))
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) || int64(len(content)) > limit {
		return nil, tooLarge
	}
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "cannot read file content").WithCause(err)
	}
	return content, nil
}

func machineOperationBlobKey(orgID, machineID, operationID uuid.UUID, name string) string {
	return machineOperationBlobKeyPrefix + orgID.String() + "/" + machineID.String() + "/" +
		operationID.String() + "/" + name
}

func (s strictOpenAPIServer) putMachineOperationBlob(
	ctx context.Context, orgID, machineID, operationID uuid.UUID, name string, content []byte,
) error {
	blobs := s.server.store.Blobs()
	if blobs == nil {
		return apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "blob storage is not configured")
	}
	if _, err := blobs.PutBlob(ctx, machineOperationBlobKey(orgID, machineID, operationID, name), content); err != nil {
		return apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "store machine file").WithCause(err)
	}
	return nil
}

func (s strictOpenAPIServer) getMachineOperationBlob(
	ctx context.Context, orgID, machineID, operationID uuid.UUID, name string,
) ([]byte, string, error) {
	blobs := s.server.store.Blobs()
	if blobs == nil {
		return nil, "", apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "blob storage is not configured")
	}
	content, metadata, err := blobs.GetBlob(ctx, machineOperationBlobKey(orgID, machineID, operationID, name))
	if errors.Is(err, blobstore.ErrNotFound) {
		return nil, "", apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if err != nil {
		return nil, "", apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "load machine file").WithCause(err)
	}
	digest := metadata.Digest
	if digest == "" {
		digest = blobstore.ContentDigest(content)
	}
	return content, digest, nil
}

func (s strictOpenAPIServer) deleteMachineOperationBlob(
	ctx context.Context, machine executionstore.MachineRecord, operationID uuid.UUID, name string,
) {
	blobs := s.server.store.Blobs()
	if blobs == nil {
		return
	}
	deleteCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), machineOperationCleanupTimeout)
	defer cancel()
	err := blobs.DeleteBlob(deleteCtx, machineOperationBlobKey(machine.OrgID, machine.ID, operationID, name))
	if err != nil && !errors.Is(err, blobstore.ErrNotFound) && s.server.log != nil {
		s.server.log.Warn("delete machine operation file failed", "operation_id", operationID, "error", err)
	}
}

// Daemon-facing handling. Machine operation processes arrive on the same
// socket messages and file routes as agent processes; these helpers claim
// the ones that belong to machine_operations.

func (s *daemonSocket) machineOperation(
	ctx context.Context,
	processID uuid.UUID,
) (executionstore.MachineOperationRecord, bool, error) {
	return s.server.store.Execution().GetMachineOperation(ctx, s.orgID, s.machineID, processID)
}

func (s *daemonSocket) acceptMachineOperation(
	ctx context.Context,
	processID uuid.UUID,
	processPublicID string,
) (bool, error) {
	if _, found, err := s.machineOperation(ctx, processID); err != nil || !found {
		return false, err
	}
	_, accepted, err := s.server.store.Execution().AcceptMachineOperation(ctx, s.authority(), processID)
	if err != nil {
		return true, err
	}
	if !accepted {
		s.recordSocketEvent("machine_operation_accept", "conflict", "not_queued")
		return true, errDaemonProcessOfferUnavailable
	}
	s.recordSocketEvent("machine_operation_accept", "accepted", "none")
	s.enqueueOrClose(daemonprotocol.Message{
		Type:      daemonprotocol.MessageProcessAcceptAck,
		ProcessID: processPublicID,
	})
	return true, nil
}

// applyMachineOperationReport records a report for a machine operation. It
// returns handled=false for processes that are not machine operations.
func (s *daemonSocket) applyMachineOperationReport(
	ctx context.Context,
	event daemonprotocol.ReportedEvent,
) (handled bool, ack daemonprotocol.AckStatus, err error) {
	processID, err := publicid.Decode(publicid.KindProcess, event.ProcessID)
	if err != nil {
		return false, "", nil //nolint:nilerr // the agent-process path reports the decode failure
	}
	operation, found, err := s.machineOperation(ctx, processID)
	if err != nil || !found {
		return false, "", err
	}
	switch event.Type {
	case daemonprotocol.EventProcessStarted:
		if err := s.server.store.Execution().MarkMachineOperationRunning(
			ctx, s.orgID, s.machineID, operation.ID,
		); err != nil {
			return true, "", err
		}
		return true, daemonprotocol.AckStatusCommitted, nil
	case daemonprotocol.EventProcessFinished:
		if _, err := s.server.store.Execution().CompleteMachineOperation(ctx, executionstore.CompleteMachineOperationInput{
			OrgID:              s.orgID,
			MachineID:          s.machineID,
			ID:                 operation.ID,
			DaemonRuntimeID:    s.runtimeID,
			State:              executionstore.MachineOperationState(event.State),
			ExitCode:           event.ExitCode,
			ExitSignal:         event.ExitSignal,
			StateReasonCode:    event.StateReasonCode,
			StateReasonMessage: event.StateReasonMessage,
			Result:             event.Result,
		}); err != nil {
			return true, "", err
		}
		s.recordSocketEvent("machine_operation_report", string(event.State), event.StateReasonCode)
		return true, daemonprotocol.AckStatusCommitted, nil
	default:
		// Machine operations never offer actions, so any action report is
		// stale and only needs local cleanup.
		return true, daemonprotocol.AckStatusCleanupOnly, nil
	}
}

// machineOperationTransfer resolves a daemon file-route request that belongs
// to a machine operation.
func (s strictOpenAPIServer) machineOperationTransfer(
	ctx context.Context,
	processID string,
	direction processcmd.FileTransferDirection,
) (machineDaemonScope, uuid.UUID, bool, error) {
	scope, scopeErr := machineDaemonScopeFromContext(ctx)
	if scopeErr != nil {
		// Leave the error to the agent-process path, which reports it.
		return machineDaemonScope{}, uuid.Nil, false, nil //nolint:nilerr // see comment above
	}
	id, ok := parseOpenAPIPublicID(publicid.KindProcess, processID)
	if !ok {
		return machineDaemonScope{}, uuid.Nil, false, nil
	}
	operation, found, err := s.server.store.Execution().GetMachineOperation(ctx, scope.OrgID, scope.MachineID, id)
	if err != nil || !found {
		return machineDaemonScope{}, uuid.Nil, false, err
	}
	transfer := operation.ExecutionSpec.FileTransfer
	if transfer == nil || transfer.Direction != direction ||
		(operation.State != executionstore.MachineOperationStateStarting &&
			operation.State != executionstore.MachineOperationStateRunning) {
		return machineDaemonScope{}, uuid.Nil, true, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	return scope, id, true, nil
}

func (s strictOpenAPIServer) uploadMachineOperationFile(
	ctx context.Context,
	req openapi.UploadDaemonFileRequestObject,
) (openapi.UploadDaemonFileResponseObject, bool, error) {
	scope, id, handled, err := s.machineOperationTransfer(ctx, req.ProcessID, processcmd.FileTransferUpload)
	if !handled || err != nil {
		return nil, handled, err
	}
	content, err := readMachineOperationBody(ctx, req.Body, daemonprotocol.MaxFileTransferBytes)
	if err != nil {
		return nil, true, err
	}
	if err := s.putMachineOperationBlob(
		ctx, scope.OrgID, scope.MachineID, id, machineOperationBlobOutput, content,
	); err != nil {
		return nil, true, err
	}
	return openapi.UploadDaemonFile201JSONResponse{
		Path:   toolcatalog.ArtifactVFSRoot + "/" + req.ProcessID,
		Digest: blobstore.ContentDigest(content),
	}, true, nil
}

func (s strictOpenAPIServer) downloadMachineOperationFile(
	ctx context.Context,
	req openapi.DownloadDaemonFileRequestObject,
) (openapi.DownloadDaemonFileResponseObject, bool, error) {
	scope, id, handled, err := s.machineOperationTransfer(ctx, req.ProcessID, processcmd.FileTransferDownload)
	if !handled || err != nil {
		return nil, handled, err
	}
	content, digest, err := s.getMachineOperationBlob(ctx, scope.OrgID, scope.MachineID, id, machineOperationBlobInput)
	if err != nil {
		return nil, true, err
	}
	etag := `"` + digest + `"`
	return openapi.DownloadDaemonFile200AsteriskResponse{
		Body: bytes.NewReader(content), ContentType: "application/octet-stream", ContentLength: int64(len(content)),
		Headers: openapi.DownloadDaemonFile200ResponseHeaders{ETag: &etag, XOmnaraFileDigest: &digest},
	}, true, nil
}
