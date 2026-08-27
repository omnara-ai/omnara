package tools

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/processaction"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func TestResolveUploadArtifactRequest(t *testing.T) {
	resolved, err := resolveUploadArtifactRequest(json.RawMessage(
		`{"path":"screenshots/latest.png","machine_ref":"  mchr_machine1  "}`,
	))
	if err != nil {
		t.Fatalf("resolve upload_artifact: %v", err)
	}
	if resolved.Path != "screenshots/latest.png" || resolved.MachineRef != "mchr_machine1" {
		t.Fatalf("resolved upload_artifact = %+v", resolved)
	}

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty path", raw: `{"path":""}`, want: "path is required"},
		{name: "nul path", raw: "{\"path\":\"bad\\u0000path\"}", want: "path cannot contain NUL"},
		{name: "null machine ref", raw: `{"path":"a","machine_ref":null}`, want: "machine_ref cannot be null"},
		{name: "unknown field", raw: `{"path":"a","artifact_id":"art_x"}`, want: "unknown field"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := resolveUploadArtifactRequest(json.RawMessage(test.raw))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestUploadArtifactApprovalPinsBindingAndPath(t *testing.T) {
	bindingID := uuid.New()
	approvedInput, err := uploadArtifactAuthorizationInput(bindingID, "shot.png")
	if err != nil {
		t.Fatalf("build upload authorization: %v", err)
	}
	call := model.ToolCall{
		ID:    "call_upload",
		Name:  "upload_artifact",
		Input: json.RawMessage(`{"path":"shot.png"}`),
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
	value, err := toolpermission.NewAllowDenyForm("Permission requested for upload_artifact", nil)
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
	if !toolCallAuthorizationMatches(action, call, storage.NilID, selection, approvedInput) {
		t.Fatal("approved upload authorization did not match")
	}
	otherPath, err := uploadArtifactAuthorizationInput(bindingID, "other.png")
	if err != nil {
		t.Fatalf("build other-path authorization: %v", err)
	}
	if toolCallAuthorizationMatches(action, call, storage.NilID, selection, otherPath) {
		t.Fatal("different path matched approved upload authorization")
	}
	otherBinding, err := uploadArtifactAuthorizationInput(uuid.New(), "shot.png")
	if err != nil {
		t.Fatalf("build other-binding authorization: %v", err)
	}
	if toolCallAuthorizationMatches(action, call, storage.NilID, selection, otherBinding) {
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
