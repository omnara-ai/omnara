package omnarad

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
)

func TestFileTransferUploadSupportsAbsoluteRelativeAndHomePaths(t *testing.T) {
	toolCallID := fileTransferTestPublicID(t, publicid.KindToolCall)
	artifactID := fileTransferTestPublicID(t, publicid.KindArtifact)
	var wantName atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedFilename, ok := wantName.Load().(string)
		if !ok {
			t.Error("expected filename is not configured")
		}
		if r.Method != http.MethodPost ||
			r.URL.Path != "/api/v1/daemon/tool-calls/"+toolCallID+"/file" ||
			r.URL.Query().Get("filename") != expectedFilename {
			t.Errorf("unexpected upload request: %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer token-a" ||
			r.Header.Get("Content-Type") != "application/octet-stream" ||
			r.Header.Get("Accept") != "application/json" {
			t.Errorf("unexpected upload headers: %+v", r.Header)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upload body: %v", err)
		}
		if string(body) != "artifact bytes" || r.ContentLength != int64(len(body)) {
			t.Errorf("upload body = %q length=%d", body, r.ContentLength)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"artifact_id":%q}`, artifactID)
	}))
	defer server.Close()

	daemonHome := filepath.Join(t.TempDir(), "daemon-home")
	setConfiguredDaemonEnvironment(t, daemonHome, server.URL, "")
	fileDir := t.TempDir()
	path := filepath.Join(fileDir, "shot.png")
	if err := os.WriteFile(path, []byte("artifact bytes"), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	relativePath, err := filepath.Rel(workingDirectory, path)
	if err != nil {
		t.Fatalf("build relative path: %v", err)
	}
	home := t.TempDir()
	homePath := filepath.Join(home, "home-shot.png")
	if err := os.WriteFile(homePath, []byte("artifact bytes"), 0o600); err != nil {
		t.Fatalf("write home artifact: %v", err)
	}
	t.Setenv("HOME", home)

	tests := []struct {
		name string
		path string
	}{
		{name: "absolute", path: path},
		{name: "relative", path: relativePath},
		{name: "home", path: "~/home-shot.png"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wantName.Store(filepath.Base(test.path))
			var stdout bytes.Buffer
			err := runFileTransfer(context.Background(), fileTransferRequest{
				direction:      "upload",
				toolCallID:     toolCallID,
				encodedPath:    base64.RawURLEncoding.EncodeToString([]byte(test.path)),
				endpointSuffix: "/file",
			}, &stdout)
			if err != nil {
				t.Fatalf("upload artifact: %v", err)
			}
			want := `{"artifact_id":"` + artifactID + `"}` + "\n"
			if stdout.String() != want {
				t.Fatalf("stdout = %q, want %q", stdout.String(), want)
			}
		})
	}
}

func TestFileTransferUploadRejectsInvalidFiles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("invalid upload reached the server")
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	setConfiguredDaemonEnvironment(t, t.TempDir(), server.URL, "")
	toolCallID := fileTransferTestPublicID(t, publicid.KindToolCall)
	tests := []struct {
		name string
		path func(*testing.T) string
		want string
	}{
		{
			name: "empty",
			path: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "empty")
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
				return path
			},
			want: "artifact file cannot be empty",
		},
		{
			name: "oversized",
			path: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "large")
				file, err := os.Create(path)
				if err != nil {
					t.Fatalf("create large file: %v", err)
				}
				if err := file.Truncate(daemonprotocol.MaxFileTransferBytes + 1); err != nil {
					t.Fatalf("truncate large file: %v", err)
				}
				if err := file.Close(); err != nil {
					t.Fatalf("close large file: %v", err)
				}
				return path
			},
			want: "exceeds",
		},
		{
			name: "directory",
			path: func(t *testing.T) string {
				return t.TempDir()
			},
			want: "regular file",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := test.path(t)
			err := runFileTransfer(context.Background(), fileTransferRequest{
				direction:      "upload",
				toolCallID:     toolCallID,
				encodedPath:    base64.RawURLEncoding.EncodeToString([]byte(path)),
				endpointSuffix: "/file",
			}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestFileTransferUploadRejectsRedirectAndOversizedResponse(t *testing.T) {
	toolCallID := fileTransferTestPublicID(t, publicid.KindToolCall)
	path := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(path, []byte("artifact bytes"), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	encodedPath := base64.RawURLEncoding.EncodeToString([]byte(path))

	var followed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		followed.Store(true)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	setConfiguredDaemonEnvironment(t, filepath.Join(t.TempDir(), "redirect-home"), redirect.URL, "")
	transfer := fileTransferRequest{
		direction: "upload", toolCallID: toolCallID, encodedPath: encodedPath, endpointSuffix: "/file",
	}
	err := runFileTransfer(context.Background(), transfer, io.Discard)
	if err == nil {
		t.Fatal("redirected upload succeeded")
	}
	if followed.Load() {
		t.Fatal("artifact upload followed redirect")
	}

	largeResponse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", daemonprotocol.MaxMessageBytes+1))
	}))
	defer largeResponse.Close()
	setConfiguredDaemonEnvironment(t, filepath.Join(t.TempDir(), "large-response-home"), largeResponse.URL, "")
	err = runFileTransfer(context.Background(), transfer, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "response is too large") {
		t.Fatalf("oversized response error = %v", err)
	}
}
