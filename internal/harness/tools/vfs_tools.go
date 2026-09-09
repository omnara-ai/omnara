package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/skills"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

type vfsPathKind uint8

const (
	vfsPathArtifactRoot vfsPathKind = iota + 1
	vfsPathArtifact
	vfsPathSkill
)

type resolvedVFSPath struct {
	Value      string
	Kind       vfsPathKind
	ArtifactID string
	SkillName  string
}

type uploadFileRequest struct {
	Path       string          `json:"path"`
	Source     string          `json:"source"`
	MachineRef json.RawMessage `json:"machine_ref,omitempty"`
}

type resolvedUploadFileRequest struct {
	Path       resolvedVFSPath
	Source     string
	MachineRef string
}

type downloadFileRequest struct {
	Path        string          `json:"path"`
	Destination string          `json:"destination,omitempty"`
	MachineRef  json.RawMessage `json:"machine_ref,omitempty"`
}

type resolvedDownloadFileRequest struct {
	Path        resolvedVFSPath
	Destination string
	MachineRef  string
}

func resolveVFSPath(value string) (resolvedVFSPath, error) {
	if value == "" {
		return resolvedVFSPath{}, errors.New("path is required")
	}
	if strings.Contains(value, "\x00") {
		return resolvedVFSPath{}, errors.New("path cannot contain NUL")
	}
	if value == toolcatalog.ArtifactVFSRoot {
		return resolvedVFSPath{Value: value, Kind: vfsPathArtifactRoot}, nil
	}
	if artifactID, ok := strings.CutPrefix(value, toolcatalog.ArtifactVFSRoot+"/"); ok {
		if artifactID == "" {
			return resolvedVFSPath{}, errors.New("use /artifacts for uploads or /artifacts/<artifact_id> for downloads")
		}
		if strings.Contains(artifactID, "/") {
			return resolvedVFSPath{}, errors.New("artifact path must be /artifacts/<artifact_id>")
		}
		if _, err := publicid.Decode(publicid.KindArtifact, artifactID); err != nil {
			return resolvedVFSPath{}, errors.New("artifact path must contain a valid artifact ID")
		}
		return resolvedVFSPath{Value: value, Kind: vfsPathArtifact, ArtifactID: artifactID}, nil
	}
	if skillName, ok := strings.CutPrefix(value, "/skills/"); ok {
		if err := skills.ValidateName(skillName); err != nil {
			return resolvedVFSPath{}, fmt.Errorf("skill path name %w", err)
		}
		return resolvedVFSPath{Value: value, Kind: vfsPathSkill, SkillName: skillName}, nil
	}
	return resolvedVFSPath{}, errors.New("path must be /artifacts, /artifacts/<artifact_id>, or /skills/<name>")
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
	path, err := resolveVFSPath(input.Path)
	if err != nil {
		return resolvedUploadFileRequest{}, err
	}
	if path.Kind != vfsPathArtifactRoot {
		return resolvedUploadFileRequest{}, errors.New("upload path must currently be /artifacts")
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
	return resolvedUploadFileRequest{Path: path, Source: input.Source, MachineRef: machineRef}, nil
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
	path, err := resolveVFSPath(input.Path)
	if err != nil {
		return resolvedDownloadFileRequest{}, err
	}
	if path.Kind == vfsPathArtifactRoot {
		return resolvedDownloadFileRequest{}, errors.New("download path must identify a resource")
	}
	if path.Kind == vfsPathArtifact && input.Destination == "" {
		return resolvedDownloadFileRequest{}, errors.New("destination is required for artifacts")
	}
	if path.Kind == vfsPathSkill && input.Destination != "" {
		return resolvedDownloadFileRequest{}, errors.New("destination must be omitted for skills")
	}
	if strings.Contains(input.Destination, "\x00") {
		return resolvedDownloadFileRequest{}, errors.New("destination cannot contain NUL")
	}
	machineRef, err := resolveOptionalMachineRef(input.MachineRef)
	if err != nil {
		return resolvedDownloadFileRequest{}, err
	}
	return resolvedDownloadFileRequest{Path: path, Destination: input.Destination, MachineRef: machineRef}, nil
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
		ArtifactID: resolved.Path.ArtifactID,
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
	if resolved.Path.Kind == vfsPathSkill {
		binding, err := resolveMachineExecutionTargetForToolCall(ctx, call.Reader, resolved.MachineRef)
		if err != nil {
			return processToolMachineResolutionError(err)
		}
		authorizationInput, err := skillDownloadAuthorizationInput(binding.ID, resolved.Path.Value)
		if err != nil {
			return nil, err
		}
		if err := authorizeToolExecution(ctx, call.Reader, call.Turn, call.Call, authorizationInput); err != nil {
			return nil, fmt.Errorf("authorize %s: %w", call.Call.Name, err)
		}
		return continueAsync(), nil
	}
	artifactCall, err := artifactDownloadCall(call.Call, resolved)
	if err != nil {
		return nil, err
	}
	call.Call = artifactCall
	return runDownloadArtifact(ctx, call)
}

func wakeDownloadFile(ctx context.Context, call backgroundToolContext) error {
	resolved, err := resolveDownloadFileRequest(call.Call.Input)
	if err != nil {
		return err
	}
	if resolved.Path.Kind == vfsPathSkill {
		return nil
	}
	return wakeProcessTool(ctx, call)
}

func downloadFileRunsAsync(input json.RawMessage) bool {
	resolved, err := resolveDownloadFileRequest(input)
	return err != nil || resolved.Path.Kind == vfsPathSkill
}

func runDownloadFileAsync(
	ctx context.Context,
	call asyncToolContext,
) (asyncPhaseResult, error) {
	resolved, err := resolveDownloadFileRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	if resolved.Path.Kind != vfsPathSkill {
		return nil, errors.New("async download requires a skill path")
	}
	binding, err := call.Executor.ResolveMachineExecutionTarget(ctx, call.Turn, resolved.MachineRef)
	if err != nil {
		if errors.Is(err, ErrNoActiveAgentMachineBinding) ||
			errors.Is(err, ErrMachineRefUnavailable) || errors.Is(err, ErrMachineSelectionRequired) {
			unavailable, resultErr := machineUnavailableToolResult(err)
			if resultErr != nil {
				return nil, resultErr
			}
			return failAsynchronously(unavailable.Content, unavailable.Cause), nil
		}
		return nil, err
	}
	approval, found, err := call.Executor.Store.Execution().GetAgentInteractionByToolCallKind(
		ctx,
		call.Turn.ProjectID,
		call.Turn.AgentID,
		call.ToolCallID,
		executionstore.AgentInteractionKindPermission,
	)
	if err != nil {
		return nil, err
	}
	if found {
		authorizationInput, err := skillDownloadAuthorizationInput(binding.ID, resolved.Path.Value)
		if err != nil {
			return nil, err
		}
		if approval.State != executionstore.AgentInteractionStateResolved || !toolCallAuthorizationMatches(
			approval, call.Call, call.ToolCallID, call.Turn.Tools[call.Call.Name].Permission, authorizationInput,
		) {
			return nil, fmt.Errorf("%w: the approved skill download target no longer matches", ErrToolAuthorizationInvalidated)
		}
	}
	match, available, attachedCount, err := lookupAttachedSkill(
		ctx,
		call.Executor,
		call.Turn,
		resolved.Path.SkillName,
	)
	if err != nil {
		return nil, err
	}
	if attachedCount == 0 {
		return failDownloadFile("no skills are attached to this agent")
	}
	if match == nil {
		return failDownloadFile(fmt.Sprintf(
			"skill %q is not attached; available: %s",
			resolved.Path.SkillName,
			strings.Join(available, ", "),
		))
	}
	skillID, err := publicid.Encode(publicid.KindSkill, match.ID)
	if err != nil {
		return nil, fmt.Errorf("encode skill public id for %q: %w", match.Name, err)
	}
	if match.ArchiveDigest == "" {
		return nil, fmt.Errorf("skill %q has no archive digest", match.Name)
	}
	revisionID, err := publicid.Encode(publicid.KindSkillRevision, match.RevisionID)
	if err != nil {
		return nil, fmt.Errorf("encode skill revision id for %q: %w", match.Name, err)
	}
	if call.Executor.SkillBroadcaster == nil {
		return failDownloadFile("skill broadcaster is not configured on this worker")
	}
	outcomes, err := call.Executor.SkillBroadcaster.BroadcastAndAwait(
		ctx,
		skillID,
		revisionID,
		match.ArchiveDigest,
		[]skills.BroadcastTarget{{
			OrgID:      binding.OrgID,
			MachineID:  binding.MachineID,
			MachineRef: binding.MachineRef,
		}},
		DefaultSkillSyncTimeout,
	)
	if err != nil {
		return nil, fmt.Errorf("broadcast skill offer: %w", err)
	}
	if len(outcomes) != 1 {
		return failDownloadFile(fmt.Sprintf("skill %q could not be installed", match.Name))
	}
	if outcome := outcomes[0]; !outcome.IsReady() {
		message := fmt.Sprintf("skill %q could not be installed", match.Name)
		if outcome.Error != "" {
			message += ": " + outcome.Error
		}
		content, err := structuredToolResultContent(map[string]any{
			"error": message, "error_code": outcome.ErrorCode, "state": outcome.State,
		})
		if err != nil {
			return nil, err
		}
		return failAsynchronously(content, errors.New(message)), nil
	}
	content, err := structuredToolResultContent(map[string]any{
		"install_path": SkillInstallPath(skillID, revisionID),
		"machine_ref":  binding.MachineRef,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal download result: %w", err)
	}
	return completeAsynchronously(content), nil
}

func failDownloadFile(message string) (asyncPhaseResult, error) {
	content, err := structuredToolResultContent(map[string]any{"error": message})
	if err != nil {
		return nil, err
	}
	return failAsynchronously(content, errors.New(message)), nil
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
	if resolved.Path.Kind == vfsPathArtifact {
		artifactCall, err := artifactDownloadCall(call, resolved)
		if err != nil {
			return toolpermission.Request{}, err
		}
		return downloadArtifactPermissionChallenge(ctx, executor, turn, artifactCall, mode)
	}
	binding, err := executor.ResolveMachineExecutionTarget(ctx, turn, resolved.MachineRef)
	if err != nil {
		return toolpermission.Request{}, executor.machinePreparationError(err)
	}
	authorizationInput, err := skillDownloadAuthorizationInput(binding.ID, resolved.Path.Value)
	if err != nil {
		return toolpermission.Request{}, err
	}
	return permissionChallenge(
		call,
		mode,
		authorizationInput,
		interactionform.ContextItem{Label: "Source", Value: resolved.Path.Value},
		interactionform.ContextItem{Label: "Machine", Value: binding.MachineRef},
	)
}

func skillDownloadAuthorizationInput(bindingID storage.ID, path string) (json.RawMessage, error) {
	return marshalJSON(map[string]string{
		"agent_machine_binding_id": bindingID.String(),
		"path":                     path,
	})
}
