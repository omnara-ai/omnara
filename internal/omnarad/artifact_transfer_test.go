package omnarad

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/publicid"
)

func TestLegacyArtifactTransferCommands(t *testing.T) {
	toolID := fileTransferTestPublicID(t, publicid.KindToolCall)
	artifactID := fileTransferTestPublicID(t, publicid.KindArtifact)
	content := []byte("artifact bytes")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(content))
	responseDigest := digest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token-a" {
			t.Error("missing machine token")
		}
		switch r.Method {
		case http.MethodPost:
			if r.URL.Path != "/api/v1/daemon/tool-calls/"+toolID+"/artifact" || r.URL.Query().Get("filename") != "file.bin" {
				t.Errorf("unexpected legacy upload URL: %s", r.URL)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil || !bytes.Equal(body, content) {
				t.Errorf("upload content %q: %v", body, err)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"artifact_id": artifactID, "filename": "file.bin"})
		case http.MethodGet:
			if r.URL.Path != "/api/v1/daemon/tool-calls/"+toolID+"/artifacts/"+artifactID+"/content" {
				t.Errorf("unexpected legacy download URL: %s", r.URL)
			}
			w.Header().Set("ETag", `W/"`+digest+`"`)
			w.Header().Set("X-Omnara-File-Digest", responseDigest)
			_, _ = w.Write(content)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()
	setConfiguredDaemonEnvironment(t, filepath.Join(t.TempDir(), "daemon"), server.URL, "")
	path := filepath.Join(t.TempDir(), "file.bin")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString([]byte(path))
	var stdout, stderr bytes.Buffer
	args := []string{"__omnara_upload_artifact", toolID, encoded}
	if code := Run(context.Background(), args, nil, &stdout, &stderr, discardLogger()); code != 0 {
		t.Fatalf("legacy upload exited %d: %s", code, &stderr)
	}
	if stdout.String() != `{"artifact_id":"`+artifactID+`"}`+"\n" {
		t.Fatalf("legacy upload result: %s", &stdout)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runUploadArtifactCommand(context.Background(), toolID, encoded, io.Discard); err == nil ||
		!strings.Contains(err.Error(), "cannot be empty") {
		t.Fatalf("empty legacy upload: %v", err)
	}
	stdout.Reset()
	args = []string{"__omnara_download_artifact", toolID, artifactID, encoded}
	if code := Run(context.Background(), args, nil, &stdout, &stderr, discardLogger()); code != 0 {
		t.Fatalf("legacy download exited %d: %s", code, &stderr)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, content) || stdout.String() != `{"digest":"`+digest+`"}`+"\n" {
		t.Fatalf("legacy download content=%q output=%q: %v", got, &stdout, err)
	}
	responseDigest = ""
	stdout.Reset()
	if err := runDownloadArtifactCommand(context.Background(), toolID, artifactID, encoded, &stdout); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(got, content) || stdout.Len() != 0 {
		t.Fatalf("legacy download without digest: content=%q output=%q error=%v", got, &stdout, err)
	}
	responseDigest = digest
	content = []byte("tampered")
	if err := runDownloadArtifactCommand(context.Background(), toolID, artifactID, encoded, &stdout); err == nil {
		t.Fatal("legacy download accepted mismatched digest")
	}
	if err := runDownloadArtifactCommand(context.Background(), toolID, "invalid", encoded, &stdout); err == nil {
		t.Fatal("legacy download accepted invalid artifact ID")
	}
}
