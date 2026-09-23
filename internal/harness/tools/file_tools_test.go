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
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func TestResolveUploadFileRequest(t *testing.T) {
	resolved, err := resolveUploadFileRequest(json.RawMessage(
		`{"path":"/artifacts","source":"reports/final.pdf","machine_id":"mch_aaaaaaaaaaaaaaaaaaaaaaaaae"}`,
	))
	if err != nil {
		t.Fatalf("resolve upload: %v", err)
	}
	if resolved.Source != "reports/final.pdf" ||
		resolved.MachineID != uuid.MustParse("00000000-0000-0000-0000-000000000001") {
		t.Fatalf("resolved upload = %+v", resolved)
	}

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "trailing slash", raw: `{"path":"/artifacts/","source":"a"}`, want: "upload path must be /artifacts"},
		{name: "missing source", raw: `{"path":"/artifacts"}`, want: "source is required"},
		{
			name: "artifact identity", raw: `{"path":"/artifacts/art_invalid","source":"a"}`,
			want: "upload path must be /artifacts",
		},
		{name: "unsupported root", raw: `{"path":"/skills/deploy","source":"a"}`, want: "upload path must be /artifacts"},
		{
			name: "nul source",
			raw:  "{\"path\":\"/artifacts\",\"source\":\"bad\\u0000path\"}",
			want: "source cannot contain NUL",
		},
		{name: "null machine", raw: `{"path":"/artifacts","source":"a","machine_id":null}`, want: "cannot be null"},
		{name: "extra field", raw: `{"path":"/artifacts","source":"a","extra":true}`, want: "unknown field"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := resolveUploadFileRequest(json.RawMessage(test.raw))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestResolveDownloadFileRequest(t *testing.T) {
	artifactID, err := publicid.Encode(publicid.KindArtifact, uuid.New())
	if err != nil {
		t.Fatalf("encode artifact id: %v", err)
	}
	artifact, err := resolveDownloadFileRequest(json.RawMessage(
		`{"path":"/artifacts/` + artifactID + `","destination":"downloads/final.pdf"}`,
	))
	if err != nil {
		t.Fatalf("resolve artifact download: %v", err)
	}
	if artifact.Path != "/artifacts/"+artifactID ||
		artifact.Destination != "downloads/final.pdf" {
		t.Fatalf("resolved artifact download = %+v", artifact)
	}
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "trailing slash",
			raw:  `{"path":"/artifacts/","destination":"a"}`,
			want: "path must be /artifacts/<artifact_id> or /memory/<store>/<file>"},
		{name: "artifact root", raw: `{"path":"/artifacts"}`,
			want: "path must be /artifacts/<artifact_id> or /memory/<store>/<file>"},
		{name: "artifact destination", raw: `{"path":"/artifacts/` + artifactID + `"}`, want: "destination is required"},
		{
			name: "unsupported root", raw: `{"path":"/skills/deploy","destination":"deploy"}`,
			want: "path must be /artifacts/<artifact_id> or /memory/<store>/<file>",
		},
		{name: "invalid artifact",
			raw:  `{"path":"/artifacts/not-an-id","destination":"a"}`,
			want: "path must be /artifacts/<artifact_id> or /memory/<store>/<file>"},
		{
			name: "nested artifact",
			raw:  `{"path":"/artifacts/` + artifactID + `/file","destination":"a"}`,
			want: "must be /artifacts",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := resolveDownloadFileRequest(json.RawMessage(test.raw))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestFileTransferApprovalPinsBindingAndInput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tool  string
		input string
	}{
		{name: "artifact upload", tool: "upload_file", input: `{"path":"/artifacts","source":"shot.png"}`},
		{name: "artifact download", tool: "download_file",
			input: `{"path":"/artifacts/art_aaaaaaaaaaaaaaaaaaaaaaaaae","destination":"shot.png"}`},
		{name: "memory upload", tool: "upload_file",
			input: `{"path":"/memory/team/shot.png","source":"shot.png","expected_digest":"sha256:` +
				strings.Repeat("a", 64) + `"}`},
		{name: "memory download", tool: "download_file",
			input: `{"path":"/memory/team/shot.png","destination":"shot.png"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bindingID := uuid.New()
			call := model.ToolCall{ID: "call_transfer", Name: tc.tool, Input: json.RawMessage(tc.input)}
			approvedInput, err := fileTransferAuthorizationInput(bindingID, call.Input)
			if err != nil {
				t.Fatal(err)
			}
			authorization, err := toolpermission.NewAuthorization(call.Name, approvedInput)
			if err != nil {
				t.Fatal(err)
			}
			selection := toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)
			mode, ok := toolpermission.FindMode(toolpermission.CommonModeDescriptors(), selection.Mode)
			if !ok {
				t.Fatal("always_ask descriptor missing")
			}
			value, err := toolpermission.NewAllowDenyForm("Permission requested for "+call.Name, nil)
			if err != nil {
				t.Fatal(err)
			}
			request, err := toolpermission.NewRequest(mode, selection, authorization, value)
			if err != nil {
				t.Fatal(err)
			}
			requestJSON, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			action := executionstore.AgentInteractionRecord{
				ProviderCallID: call.ID, InteractionKind: executionstore.AgentInteractionKindPermission,
				Request: requestJSON,
			}
			if !toolCallAuthorizationMatches(action, call, uuid.Nil, selection, approvedInput) {
				t.Fatal("approved authorization did not match")
			}
			otherBinding, err := fileTransferAuthorizationInput(uuid.New(), call.Input)
			if err != nil {
				t.Fatal(err)
			}
			if toolCallAuthorizationMatches(action, call, uuid.Nil, selection, otherBinding) {
				t.Fatal("different binding matched approved authorization")
			}
			var fields map[string]string
			if err := json.Unmarshal(call.Input, &fields); err != nil {
				t.Fatal(err)
			}
			for field, original := range fields {
				fields[field] = original + "-changed"
				changedInput, err := json.Marshal(fields)
				fields[field] = original
				if err != nil {
					t.Fatal(err)
				}
				changedAuthorization, err := fileTransferAuthorizationInput(bindingID, changedInput)
				if err != nil {
					t.Fatal(err)
				}
				if toolCallAuthorizationMatches(action, call, uuid.Nil, selection, changedAuthorization) {
					t.Fatalf("changed %s matched approved authorization", field)
				}
			}
		})
	}
}

func TestUploadFileArtifactProcessInput(t *testing.T) {
	toolCallID, err := publicid.Encode(publicid.KindToolCall, uuid.New())
	if err != nil {
		t.Fatalf("encode tool call id: %v", err)
	}
	path := "screenshots/a file.png"
	input := fileTransferProcessInput("upload", toolCallID, path, "/artifacts")
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

func TestDownloadFileArtifactProcessInput(t *testing.T) {
	toolCallID, err := publicid.Encode(publicid.KindToolCall, uuid.New())
	if err != nil {
		t.Fatalf("encode tool call id: %v", err)
	}
	path := "downloads/a file.pdf"
	artifactID := "art_aaaaaaaaaaaaaaaaaaaaaaaaae"
	input := fileTransferProcessInput("download", toolCallID, path, "/artifacts/"+artifactID)
	encodedPath := base64.RawURLEncoding.EncodeToString([]byte(path))
	wantCommand := `"$OMNARA_HOME/bin/omnarad" __omnara_download_artifact ` +
		toolCallID + " " + artifactID + " " + encodedPath
	if input.Command != wantCommand ||
		input.ShellSelector != processcmd.ShellDefault ||
		input.IOMode != processcmd.IOModePipe ||
		input.Cwd != "" ||
		input.InitialWaitMS != processaction.MaxWaitMilliseconds ||
		input.TimeoutSeconds != 0 {
		t.Fatalf("download process input = %+v, want command %q", input, wantCommand)
	}
}

func TestMemoryTransferProcessInput(t *testing.T) {
	toolCallID, err := publicid.Encode(publicid.KindToolCall, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	localPath := "notes/a file.md"
	encodedPath := base64.RawURLEncoding.EncodeToString([]byte(localPath))
	for _, direction := range []string{"upload", "download"} {
		input := fileTransferProcessInput(
			direction, toolCallID, localPath, "/memory/team/file.md",
		)
		want := `"$OMNARA_HOME/bin/omnarad" __omnara_file_transfer ` + direction +
			" " + toolCallID + " " + encodedPath
		if input.Command != want || input.TimeoutSeconds != fileTransferProcessTimeoutSeconds {
			t.Fatalf("%s process input = %+v, want command %q", direction, input, want)
		}
	}
}

func TestMemoryUploadDigestValidation(t *testing.T) {
	tool, ok, err := toolImplementationFor(toolcatalog.ToolNameUploadFile)
	if err != nil || !ok {
		t.Fatalf("upload_file registration: %v", err)
	}
	for _, expected := range []string{`""`, `null`, `"invalid"`, `123`, `"sha256:` + strings.Repeat("A", 64) + `"`} {
		raw := json.RawMessage(`{"path":"/memory/team/file","source":"file","expected_digest":` + expected + `}`)
		if err := tool.validateInput(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	digest := "sha256:" + strings.Repeat("0", 64)
	if err := tool.validateInput(
		json.RawMessage(`{"path":"/memory/team/file","source":"file","expected_digest":"` + digest + `"}`),
	); err != nil {
		t.Fatal(err)
	}
	if err := tool.validateInput(
		json.RawMessage(`{"path":"/artifacts","source":"file","expected_digest":"` + digest + `"}`),
	); err == nil {
		t.Fatal("artifact accepted digest precondition")
	}
	if err := validateListFiles(json.RawMessage(`{"pattern":"/memory/*","cursor":"old"}`)); err == nil {
		t.Fatal("list_files accepted removed cursor")
	}
}
