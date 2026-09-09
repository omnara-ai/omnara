package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func TestSkillDownloadDoesNotWakeArtifactProcess(t *testing.T) {
	err := wakeDownloadFile(context.Background(), backgroundToolContext{
		Call: model.ToolCall{Name: "download_file", Input: json.RawMessage(`{"path":"/skills/deploy"}`)},
	})
	if err != nil {
		t.Fatalf("skill download background phase: %v", err)
	}
}

func TestVFSArtifactTrailingSlashGuidance(t *testing.T) {
	const want = "use /artifacts for uploads or /artifacts/<artifact_id> for downloads"
	for _, input := range []struct {
		name     string
		validate func(json.RawMessage) error
		raw      string
	}{
		{"upload", validateUploadFileInput, `{"path":"/artifacts/","source":"report.pdf"}`},
		{"download", validateDownloadFileInput, `{"path":"/artifacts/","destination":"report.pdf"}`},
	} {
		t.Run(input.name, func(t *testing.T) {
			if err := input.validate(json.RawMessage(input.raw)); err == nil || err.Error() != want {
				t.Fatalf("error = %v, want %q", err, want)
			}
		})
	}
}

func TestSkillDownloadApprovalPinsPathAndMachine(t *testing.T) {
	call := model.ToolCall{ID: "call_skill", Name: "download_file", Input: json.RawMessage(`{"path":"/skills/deploy"}`)}
	selection := toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)
	descriptor, ok := toolpermission.FindMode(toolpermission.CommonModeDescriptors(), selection.Mode)
	if !ok {
		t.Fatal("always_ask descriptor missing")
	}
	approved, err := skillDownloadAuthorizationInput(storage.ID{1}, "/skills/deploy")
	if err != nil {
		t.Fatal(err)
	}
	request, err := permissionChallenge(
		call,
		permissionModeContext{selection: selection, descriptor: descriptor},
		approved,
	)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	action := executionstore.AgentInteractionRecord{
		ProviderCallID:  call.ID,
		InteractionKind: "permission",
		Request:         raw,
	}
	for _, test := range []struct {
		name    string
		binding storage.ID
		path    string
		want    bool
	}{
		{name: "approved", binding: storage.ID{1}, path: "/skills/deploy", want: true},
		{name: "changed machine", binding: storage.ID{2}, path: "/skills/deploy"},
		{name: "changed skill", binding: storage.ID{1}, path: "/skills/review"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input, err := skillDownloadAuthorizationInput(test.binding, test.path)
			if err != nil {
				t.Fatal(err)
			}
			if got := toolCallAuthorizationMatches(action, call, storage.NilID, selection, input); got != test.want {
				t.Fatalf("authorization matches = %t, want %t", got, test.want)
			}
		})
	}
}

func TestResolveUploadFileRequest(t *testing.T) {
	resolved, err := resolveUploadFileRequest(json.RawMessage(
		`{"path":"/artifacts","source":"reports/final.pdf","machine_ref":"  mchr-first1  "}`,
	))
	if err != nil {
		t.Fatalf("resolve upload: %v", err)
	}
	if resolved.Path.Value != "/artifacts" || resolved.Path.Kind != vfsPathArtifactRoot ||
		resolved.Source != "reports/final.pdf" || resolved.MachineRef != "mchr-first1" {
		t.Fatalf("resolved upload = %+v", resolved)
	}

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "missing source", raw: `{"path":"/artifacts"}`, want: "source is required"},
		{name: "artifact identity", raw: `{"path":"/artifacts/art_invalid","source":"a"}`, want: "valid artifact ID"},
		{name: "skill", raw: `{"path":"/skills/deploy","source":"a"}`, want: "currently be /artifacts"},
		{name: "unsupported root", raw: `{"path":"/memory/notes.md","source":"a"}`, want: "path must be"},
		{
			name: "nul source",
			raw:  "{\"path\":\"/artifacts\",\"source\":\"bad\\u0000path\"}",
			want: "source cannot contain NUL",
		},
		{name: "null machine", raw: `{"path":"/artifacts","source":"a","machine_ref":null}`, want: "cannot be null"},
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
	if artifact.Path.Kind != vfsPathArtifact || artifact.Path.ArtifactID != artifactID ||
		artifact.Destination != "downloads/final.pdf" {
		t.Fatalf("resolved artifact download = %+v", artifact)
	}
	skill, err := resolveDownloadFileRequest(json.RawMessage(
		`{"path":"/skills/deploy","machine_ref":"mchr-first1"}`,
	))
	if err != nil {
		t.Fatalf("resolve skill download: %v", err)
	}
	if skill.Path.Kind != vfsPathSkill || skill.Path.SkillName != "deploy" ||
		skill.MachineRef != "mchr-first1" {
		t.Fatalf("resolved skill download = %+v", skill)
	}

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "artifact root", raw: `{"path":"/artifacts"}`, want: "identify a resource"},
		{name: "artifact destination", raw: `{"path":"/artifacts/` + artifactID + `"}`, want: "destination is required"},
		{name: "skill destination", raw: `{"path":"/skills/deploy","destination":"deploy"}`, want: "must be omitted"},
		{name: "invalid artifact", raw: `{"path":"/artifacts/not-an-id","destination":"a"}`, want: "valid artifact ID"},
		{
			name: "nested artifact",
			raw:  `{"path":"/artifacts/` + artifactID + `/file","destination":"a"}`,
			want: "must be /artifacts",
		},
		{name: "invalid skill", raw: `{"path":"/skills/Deploy"}`, want: "skill path name"},
		{name: "unsupported root", raw: `{"path":"/attachments/file"}`, want: "path must be"},
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

func TestVFSArtifactCallsReuseLegacyInputs(t *testing.T) {
	upload, err := resolveUploadFileRequest(json.RawMessage(
		`{"path":"/artifacts","source":"report.pdf","machine_ref":"mchr-first1"}`,
	))
	if err != nil {
		t.Fatalf("resolve upload: %v", err)
	}
	uploadCall, err := artifactUploadCall(model.ToolCall{Name: "upload_file"}, upload)
	if err != nil {
		t.Fatalf("build artifact upload call: %v", err)
	}
	resolvedArtifactUpload, err := resolveUploadArtifactRequest(uploadCall.Input)
	if err != nil {
		t.Fatalf("resolve artifact upload: %v", err)
	}
	if uploadCall.Name != "upload_file" || resolvedArtifactUpload.Path != "report.pdf" ||
		resolvedArtifactUpload.MachineRef != "mchr-first1" {
		t.Fatalf("artifact upload call = %+v resolved=%+v", uploadCall, resolvedArtifactUpload)
	}
	artifactID, err := publicid.Encode(publicid.KindArtifact, uuid.New())
	if err != nil {
		t.Fatalf("encode artifact id: %v", err)
	}
	download, err := resolveDownloadFileRequest(json.RawMessage(
		`{"path":"/artifacts/` + artifactID + `","destination":"report.pdf","machine_ref":"mchr-first1"}`,
	))
	if err != nil {
		t.Fatalf("resolve download: %v", err)
	}
	downloadCall, err := artifactDownloadCall(model.ToolCall{Name: "download_file"}, download)
	if err != nil {
		t.Fatalf("build artifact download call: %v", err)
	}
	resolvedArtifactDownload, err := resolveDownloadArtifactRequest(downloadCall.Input)
	if err != nil {
		t.Fatalf("resolve artifact download: %v", err)
	}
	if downloadCall.Name != "download_file" || resolvedArtifactDownload.ArtifactID != artifactID ||
		resolvedArtifactDownload.Path != "report.pdf" || resolvedArtifactDownload.MachineRef != "mchr-first1" {
		t.Fatalf("artifact download call = %+v resolved=%+v", downloadCall, resolvedArtifactDownload)
	}
}
