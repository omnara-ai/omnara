package omnarad

import (
	"context"
	"encoding/base64"
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

func TestRunDownloadArtifactCommandSupportsAbsoluteRelativeAndHomePaths(t *testing.T) {
	toolCallID := artifactUploadTestPublicID(t, publicid.KindToolCall)
	artifactID := artifactUploadTestPublicID(t, publicid.KindArtifact)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet ||
			r.URL.Path != "/api/v1/daemon/tool-calls/"+toolCallID+"/artifacts/"+artifactID+"/content" {
			t.Errorf("unexpected download request: %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer token-a" ||
			r.Header.Get("Accept") != "application/octet-stream" {
			t.Errorf("unexpected download headers: %+v", r.Header)
		}
		_, _ = io.WriteString(w, "artifact bytes")
	}))
	defer server.Close()
	setConfiguredDaemonEnvironment(t, filepath.Join(t.TempDir(), "daemon-home"), server.URL, "")

	absoluteDir := t.TempDir()
	absolutePath := filepath.Join(absoluteDir, "absolute.txt")
	longPath := filepath.Join(absoluteDir, strings.Repeat("a", 240))
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	relativeDir := t.TempDir()
	relativePath, err := filepath.Rel(workingDirectory, filepath.Join(relativeDir, "relative.txt"))
	if err != nil {
		t.Fatalf("build relative path: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	homePath := filepath.Join(home, "home.txt")

	tests := []struct {
		name string
		path string
		full string
	}{
		{name: "absolute", path: absolutePath, full: absolutePath},
		{name: "long basename", path: longPath, full: longPath},
		{name: "relative", path: relativePath, full: filepath.Join(relativeDir, "relative.txt")},
		{name: "home", path: "~/home.txt", full: homePath},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(test.full, []byte("old bytes"), 0o600); err != nil {
				t.Fatalf("write destination: %v", err)
			}
			if err := os.Chmod(test.full, 0o640); err != nil {
				t.Fatalf("chmod destination: %v", err)
			}
			err := runDownloadArtifactCommand(
				context.Background(),
				toolCallID,
				artifactID,
				base64.RawURLEncoding.EncodeToString([]byte(test.path)),
			)
			if err != nil {
				t.Fatalf("download artifact: %v", err)
			}
			content, err := os.ReadFile(test.full)
			if err != nil {
				t.Fatalf("read destination: %v", err)
			}
			if string(content) != "artifact bytes" {
				t.Fatalf("destination = %q", content)
			}
			info, err := os.Stat(test.full)
			if err != nil {
				t.Fatalf("stat destination: %v", err)
			}
			if got := info.Mode().Perm(); got != 0o640 {
				t.Fatalf("destination permissions = %v", got)
			}
		})
	}
}

func TestRunDownloadArtifactCommandPreservesDestinationOnFailures(t *testing.T) {
	toolCallID := artifactUploadTestPublicID(t, publicid.KindToolCall)
	artifactID := artifactUploadTestPublicID(t, publicid.KindArtifact)
	destinationDir := t.TempDir()
	destination := filepath.Join(destinationDir, "artifact.bin")
	if err := os.WriteFile(destination, []byte("existing bytes"), 0o600); err != nil {
		t.Fatalf("write destination: %v", err)
	}
	encodedPath := base64.RawURLEncoding.EncodeToString([]byte(destination))

	var followed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		followed.Store(true)
	}))
	defer target.Close()
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{
			name: "error response",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
			},
			want: "unavailable",
		},
		{
			name: "redirect",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			},
			want: "Temporary Redirect",
		},
		{
			name: "truncated response",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Length", "100")
				_, _ = io.WriteString(w, "short")
			},
			want: "write artifact",
		},
		{
			name: "oversized response",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, strings.Repeat("x", daemonprotocol.MaxArtifactUploadBytes+1))
			},
			want: "exceeds the size limit",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()
			setConfiguredDaemonEnvironment(t, filepath.Join(t.TempDir(), "daemon-home"), server.URL, "")
			err := runDownloadArtifactCommand(context.Background(), toolCallID, artifactID, encodedPath)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			content, readErr := os.ReadFile(destination)
			if readErr != nil || string(content) != "existing bytes" {
				t.Fatalf("destination = %q err=%v", content, readErr)
			}
			matches, globErr := filepath.Glob(filepath.Join(destinationDir, ".omnara-artifact-*"))
			if globErr != nil || len(matches) != 0 {
				t.Fatalf("temporary files = %v err=%v", matches, globErr)
			}
		})
	}
	if followed.Load() {
		t.Fatal("artifact download followed redirect")
	}
}

func TestRunDownloadArtifactCommandDoesNotCreateParentDirectory(t *testing.T) {
	toolCallID := artifactUploadTestPublicID(t, publicid.KindToolCall)
	artifactID := artifactUploadTestPublicID(t, publicid.KindArtifact)
	var requested atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requested.Store(true)
		_, _ = io.WriteString(w, "artifact bytes")
	}))
	defer server.Close()
	setConfiguredDaemonEnvironment(t, filepath.Join(t.TempDir(), "daemon-home"), server.URL, "")
	destination := filepath.Join(t.TempDir(), "missing", "artifact.bin")
	err := runDownloadArtifactCommand(
		context.Background(),
		toolCallID,
		artifactID,
		base64.RawURLEncoding.EncodeToString([]byte(destination)),
	)
	if err == nil || !strings.Contains(err.Error(), "create temporary artifact file") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(filepath.Dir(destination)); !os.IsNotExist(err) {
		t.Fatalf("parent directory was created: %v", err)
	}
	if requested.Load() {
		t.Fatal("artifact was requested before validating the destination")
	}
}
