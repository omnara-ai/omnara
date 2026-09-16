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
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
)

func TestMemoryTransferRoundTripAndFailedDownload(t *testing.T) {
	toolID := fileTransferTestPublicID(t, publicid.KindToolCall)
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("file")))
	responseDigest := digest
	want := []byte{0xff, 0x00, 0x80, 0x42}
	content := append([]byte(nil), want...)
	fail := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/daemon/tool-calls/"+toolID+"/file" || r.Header.Get(
			"Authorization",
		) != "Bearer token-a" {
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
		if fail {
			http.Error(w, "conflict", http.StatusConflict)
			return
		}
		result := map[string]string{"digest": digest}
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			content = body
		} else {
			w.Header().Set("X-Omnara-Memory-Digest", responseDigest)
			_, _ = w.Write(content)
			return
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()
	setConfiguredDaemonEnvironment(t, filepath.Join(t.TempDir(), "daemon"), server.URL, "")
	path := filepath.Join(t.TempDir(), "file.bin")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString([]byte(path))
	var output bytes.Buffer
	var stderr bytes.Buffer
	args := []string{"__omnara_file_transfer", "download", toolID, encoded, "--require-digest"}
	if code := Run(context.Background(), args, nil, &output, &stderr, discardLogger()); code != 0 {
		t.Fatalf("download exited %d: %s", code, stderr.String())
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("download %q %v", got, err)
	}
	if !bytes.Contains(output.Bytes(), []byte(digest)) {
		t.Fatal("digest token missing")
	}
	transfer := fileTransferRequest{
		direction: "download", toolCallID: toolID, encodedPath: encoded,
		endpointSuffix: "/file", requireDigest: true,
	}
	fail = true
	if err = runFileTransfer(context.Background(), transfer, &output); err == nil {
		t.Fatal("failed download succeeded")
	}
	got, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("failed download changed destination")
	}
	fail = false
	for _, invalid := range []string{"", "invalid"} {
		responseDigest = invalid
		if err = runFileTransfer(context.Background(), transfer, &output); err == nil {
			t.Fatal("download with invalid digest succeeded")
		}
		got, err = os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("invalid digest changed destination")
		}
	}
	responseDigest = digest
	transfer.direction = "upload"
	if err = os.WriteFile(path, want, 0600); err != nil {
		t.Fatal(err)
	}
	if err = runFileTransfer(context.Background(), transfer, &output); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, want) {
		t.Fatal("binary upload changed content")
	}
	if err = os.WriteFile(path, make([]byte, daemonprotocol.MaxFileTransferBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if err = runFileTransfer(context.Background(), transfer, &output); err == nil {
		t.Fatal("oversized upload succeeded")
	}
	if err = os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err = runFileTransfer(context.Background(), transfer, &output); err != nil {
		t.Fatal(err)
	}
	if len(content) != 0 {
		t.Fatal("empty upload changed content")
	}
}

func TestFileTransferArtifactRoundTrip(t *testing.T) {
	toolID := fileTransferTestPublicID(t, publicid.KindToolCall)
	artifactID := fileTransferTestPublicID(t, publicid.KindArtifact)
	content := []byte{0xff, 0x00, 0x80, 0x42}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/daemon/tool-calls/"+toolID+"/file" || r.Header.Get("Authorization") != "Bearer token-a" {
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(r.Body)
			if err != nil || !bytes.Equal(body, content) || r.URL.Query().Get("filename") != "file.bin" {
				t.Errorf("unexpected upload: %q, %v", body, err)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"artifact_id": artifactID})
			return
		}
		_, _ = w.Write(content)
	}))
	defer server.Close()
	setConfiguredDaemonEnvironment(t, filepath.Join(t.TempDir(), "daemon"), server.URL, "")
	path := filepath.Join(t.TempDir(), "file.bin")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString([]byte(path))
	for _, direction := range []string{"upload", "download"} {
		var output, stderr bytes.Buffer
		if direction == "download" {
			if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		args := []string{"__omnara_file_transfer", direction, toolID, encoded}
		code := Run(context.Background(), args, nil, &output, &stderr, discardLogger())
		if code != 0 {
			t.Fatalf("%s exited %d: %s", direction, code, stderr.String())
		}
		if direction == "upload" && !bytes.Contains(output.Bytes(), []byte(artifactID)) {
			t.Fatal("artifact ID missing")
		}
		if direction == "download" && output.Len() != 0 {
			t.Fatalf("unexpected download output: %s", output.String())
		}
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("download %q: %v", got, err)
	}
}

func fileTransferTestPublicID(t *testing.T, kind publicid.Kind) string {
	t.Helper()
	id, err := publicid.Encode(kind, uuid.New())
	if err != nil {
		t.Fatalf("encode %s id: %v", kind, err)
	}
	return id
}
