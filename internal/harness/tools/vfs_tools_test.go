package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
)

func TestResolveUploadFileRequest(t *testing.T) {
	resolved, err := resolveUploadFileRequest(json.RawMessage(
		`{"path":"/artifacts","source":"reports/final.pdf","machine_ref":"  mchr-first1  "}`,
	))
	if err != nil {
		t.Fatalf("resolve upload: %v", err)
	}
	if resolved.Source != "reports/final.pdf" || resolved.MachineRef != "mchr-first1" {
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
	if artifact.ArtifactID != artifactID ||
		artifact.Destination != "downloads/final.pdf" {
		t.Fatalf("resolved artifact download = %+v", artifact)
	}
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "trailing slash", raw: `{"path":"/artifacts/","destination":"a"}`, want: "download path must be /artifacts"},
		{name: "artifact root", raw: `{"path":"/artifacts"}`, want: "download path must be /artifacts"},
		{name: "artifact destination", raw: `{"path":"/artifacts/` + artifactID + `"}`, want: "destination is required"},
		{
			name: "unsupported root", raw: `{"path":"/skills/deploy","destination":"deploy"}`,
			want: "download path must be /artifacts",
		},
		{name: "invalid artifact", raw: `{"path":"/artifacts/not-an-id","destination":"a"}`, want: "valid artifact ID"},
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
