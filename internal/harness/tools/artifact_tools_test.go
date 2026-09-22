package tools

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/processaction"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func TestUploadArtifactApprovalPinsBindingAndPath(t *testing.T) {
	bindingID := uuid.New()
	approvedInput, err := uploadArtifactAuthorizationInput(bindingID, "shot.png")
	if err != nil {
		t.Fatalf("build upload authorization: %v", err)
	}
	call := model.ToolCall{
		ID:    "call_upload",
		Name:  "upload_file",
		Input: json.RawMessage(`{"path":"/artifacts","source":"shot.png"}`),
	}
	authorization, err := toolpermission.NewAuthorization(call.Name, approvedInput)
	if err != nil {
		t.Fatalf("permission authorization: %v", err)
	}
	selection := toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)
	mode, ok := toolpermission.FindMode(toolpermission.CommonModeDescriptors(), selection.Mode)
	if !ok {
		t.Fatal("always_ask descriptor missing")
	}
	value, err := toolpermission.NewAllowDenyForm("Permission requested for upload_file", nil)
	if err != nil {
		t.Fatalf("permission interaction form: %v", err)
	}
	request, err := toolpermission.NewRequest(mode, selection, authorization, value)
	if err != nil {
		t.Fatalf("permission request: %v", err)
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal permission request: %v", err)
	}
	action := executionstore.AgentInteractionRecord{
		ProviderCallID:  call.ID,
		InteractionKind: executionstore.AgentInteractionKindPermission,
		Request:         requestJSON,
	}
	if !toolCallAuthorizationMatches(action, call, uuid.Nil, selection, approvedInput) {
		t.Fatal("approved upload authorization did not match")
	}
	otherPath, err := uploadArtifactAuthorizationInput(bindingID, "other.png")
	if err != nil {
		t.Fatalf("build other-path authorization: %v", err)
	}
	if toolCallAuthorizationMatches(action, call, uuid.Nil, selection, otherPath) {
		t.Fatal("different path matched approved upload authorization")
	}
	otherBinding, err := uploadArtifactAuthorizationInput(uuid.New(), "shot.png")
	if err != nil {
		t.Fatalf("build other-binding authorization: %v", err)
	}
	if toolCallAuthorizationMatches(action, call, uuid.Nil, selection, otherBinding) {
		t.Fatal("different binding matched approved upload authorization")
	}
}

func TestUploadArtifactProcessInput(t *testing.T) {
	toolCallID, err := publicid.Encode(publicid.KindToolCall, uuid.New())
	if err != nil {
		t.Fatalf("encode tool call id: %v", err)
	}
	path := "screenshots/a file.png"
	input := uploadArtifactProcessInput(toolCallID, path)
	encodedPath := base64.RawURLEncoding.EncodeToString([]byte(path))
	wantCommand := `"$OMNARA_HOME/bin/omnarad" __omnara_upload_artifact ` + toolCallID + " " + encodedPath
	if input.Command != wantCommand ||
		input.ShellSelector != processcmd.ShellDefault ||
		input.IOMode != processcmd.IOModePipe ||
		input.Cwd != "" ||
		input.InitialWaitMS != processaction.MaxWaitMilliseconds ||
		input.TimeoutSeconds != 30 {
		t.Fatalf("upload process input = %+v, want command %q", input, wantCommand)
	}
}

func TestDownloadArtifactAuthorizationPinsBindingArtifactAndPath(t *testing.T) {
	bindingID := uuid.New()
	artifactID, err := publicid.Encode(publicid.KindArtifact, uuid.New())
	if err != nil {
		t.Fatalf("encode artifact id: %v", err)
	}
	raw, err := downloadArtifactAuthorizationInput(bindingID, artifactID, "downloads/report.pdf")
	if err != nil {
		t.Fatalf("build download authorization: %v", err)
	}
	var authorization downloadArtifactAuthorization
	if err := json.Unmarshal(raw, &authorization); err != nil {
		t.Fatalf("decode download authorization: %v", err)
	}
	if authorization.AgentMachineBindingID != bindingID.String() ||
		authorization.ArtifactID != artifactID || authorization.Path != "downloads/report.pdf" {
		t.Fatalf("download authorization = %+v", authorization)
	}
}

func TestDownloadArtifactProcessInput(t *testing.T) {
	toolCallID, err := publicid.Encode(publicid.KindToolCall, uuid.New())
	if err != nil {
		t.Fatalf("encode tool call id: %v", err)
	}
	artifactID, err := publicid.Encode(publicid.KindArtifact, uuid.New())
	if err != nil {
		t.Fatalf("encode artifact id: %v", err)
	}
	path := "downloads/a file.pdf"
	input := downloadArtifactProcessInput(toolCallID, artifactID, path)
	encodedPath := base64.RawURLEncoding.EncodeToString([]byte(path))
	wantCommand := `"$OMNARA_HOME/bin/omnarad" __omnara_download_artifact ` +
		toolCallID + " " + artifactID + " " + encodedPath
	if input.Command != wantCommand ||
		input.ShellSelector != processcmd.ShellDefault ||
		input.IOMode != processcmd.IOModePipe ||
		input.Cwd != "" ||
		input.InitialWaitMS != processaction.MaxWaitMilliseconds ||
		input.TimeoutSeconds != downloadArtifactProcessTimeoutSeconds {
		t.Fatalf("download process input = %+v, want command %q", input, wantCommand)
	}
}
