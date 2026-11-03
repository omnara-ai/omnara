package omnarad

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/stretchr/testify/require"
)

func TestMemoryTransferRoundTripAndFailedDownload(t *testing.T) {
	processID := fileTransferTestPublicID(t, publicid.KindProcess)
	memoryPath := "/memory/team/nested/file.bin"
	want := []byte{0xff, 0x00, 0x80, 0x42}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(want))
	responseDigest := digest
	content := append([]byte(nil), want...)
	fail := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/daemon/processes/"+processID+"/file" || r.Header.Get(
			"Authorization",
		) != "Bearer token-a" {
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
		if fail {
			http.Error(w, "conflict", http.StatusConflict)
			return
		}
		result := map[string]string{"path": memoryPath, "digest": digest, "filename": "file.bin"}
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			content = body
		} else {
			w.Header().Set("ETag", `W/"`+digest+`"`)
			w.Header().Set("X-Omnara-File-Digest", responseDigest)
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
	var output bytes.Buffer
	args := []string{"__omnara_file_transfer", "download", processID, path}
	result, err := runFileTransferTestCommand(t, args, &output)
	require.NoError(t, err)
	if string(result) != `{"digest":"`+digest+`"}`+"\n" || output.Len() != 0 {
		t.Fatalf("download result = %s, output = %s", result, &output)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("download %q %v", got, err)
	}
	direction := processcmd.FileTransferDownload
	fail = true
	if err = runFileTransfer(context.Background(), direction, processID, path, &output); err == nil {
		t.Fatal("failed download succeeded")
	}
	got, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("failed download changed destination")
	}
	fail = false
	content = []byte("incorrect response content")
	for _, invalid := range []string{"", "invalid", digest} {
		responseDigest = invalid
		output.Reset()
		if err = runFileTransfer(context.Background(), direction, processID, path, &output); err == nil {
			t.Fatalf("invalid download: error=%v output=%q", err, output.String())
		}
		var result daemonprotocol.FileTransferResult
		require.NoError(t, json.Unmarshal(output.Bytes(), &result))
		require.NotNil(t, result.Error)
		require.Equal(t, "file_transfer_failed", result.Error.Code)
		require.Equal(t, err.Error(), result.Error.Message)
		got, err = os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("invalid digest changed destination")
		}
		entries, err := os.ReadDir(filepath.Dir(path))
		if err != nil || len(entries) != 1 {
			t.Fatalf("failed download left temporary files: %v %v", entries, err)
		}
	}
	responseDigest = digest
	direction = processcmd.FileTransferUpload
	output.Reset()
	if err = os.WriteFile(path, want, 0600); err != nil {
		t.Fatal(err)
	}
	if err = runFileTransfer(context.Background(), direction, processID, path, &output); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, want) {
		t.Fatal("binary upload changed content")
	}
	var uploaded map[string]string
	if err := json.Unmarshal(output.Bytes(), &uploaded); err != nil {
		t.Fatal(err)
	}
	if len(uploaded) != 2 || uploaded["path"] != memoryPath || uploaded["digest"] != digest {
		t.Fatalf("unexpected upload result: %s", output.Bytes())
	}
	if err = os.WriteFile(path, make([]byte, daemonprotocol.MaxFileTransferBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if err = runFileTransfer(context.Background(), direction, processID, path, &output); err == nil {
		t.Fatal("oversized upload succeeded")
	}
	if err = os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err = runFileTransfer(context.Background(), direction, processID, path, &output); err != nil {
		t.Fatal(err)
	}
	if len(content) != 0 {
		t.Fatal("empty upload changed content")
	}
}

func TestFileTransferArtifactRoundTrip(t *testing.T) {
	processID := fileTransferTestPublicID(t, publicid.KindProcess)
	artifactID := fileTransferTestPublicID(t, publicid.KindArtifact)
	content := []byte{0xff, 0x00, 0x80, 0x42}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(content))
	uploadResult := map[string]string{"path": "/artifacts/" + artifactID, "digest": digest, "filename": "file.bin"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/daemon/processes/"+processID+"/file" || r.Header.Get("Authorization") != "Bearer token-a" {
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(r.Body)
			if err != nil || !bytes.Equal(body, content) || r.URL.Query().Get("filename") != "file.bin" {
				t.Errorf("unexpected upload: %q, %v", body, err)
			}
			_ = json.NewEncoder(w).Encode(uploadResult)
			return
		}
		w.Header().Set("X-Omnara-File-Digest", digest)
		_, _ = w.Write(content)
	}))
	defer server.Close()
	setConfiguredDaemonEnvironment(t, filepath.Join(t.TempDir(), "daemon"), server.URL, "")
	path := filepath.Join(t.TempDir(), "file.bin")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	for _, direction := range []string{"upload", "download"} {
		var output bytes.Buffer
		if direction == "download" {
			if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		args := []string{"__omnara_file_transfer", direction, processID, path}
		result, err := runFileTransferTestCommand(t, args, &output)
		require.NoError(t, err)
		wantUpload := `{"path":"/artifacts/` + artifactID + `","digest":"` + digest + `"}` + "\n"
		if direction == "upload" && (output.Len() != 0 || string(result) != wantUpload) {
			t.Fatalf("unexpected upload result: %s, output=%s", result, &output)
		}
		if direction == "download" && (output.Len() != 0 || string(result) != `{"digest":"`+digest+`"}`+"\n") {
			t.Fatalf("unexpected download result: %s, output=%s", result, &output)
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

func TestFileTransferCommandHelper(t *testing.T) {
	if os.Getenv("OMNARA_FILE_TRANSFER_TEST") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Exit(Run(context.Background(), os.Args[i+1:], nil, os.Stdout, os.Stderr, discardLogger()))
		}
	}
	t.Fatal("missing helper arguments")
}

func runFileTransferTestCommand(t *testing.T, args []string, output *bytes.Buffer) ([]byte, error) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close(); _ = writer.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	commandArgs := append([]string{
		"-test.run=^TestFileTransferCommandHelper$", "--", args[0], "--",
	}, args[1:]...)
	command := exec.CommandContext(ctx, os.Args[0], commandArgs...)
	command.Env = append(os.Environ(), "OMNARA_FILE_TRANSFER_TEST=1")
	command.ExtraFiles = []*os.File{writer}
	command.Stdout = output
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	result, readErr := io.ReadAll(reader)
	waitErr := command.Wait()
	require.NoError(t, readErr)
	if waitErr != nil {
		return result, fmt.Errorf("file transfer: %w: %s", waitErr, &stderr)
	}
	return result, nil
}

func TestFileTransferRequiresResultPipe(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "not-a-pipe")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	for _, extraFiles := range [][]*os.File{nil, {file}} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0],
			"-test.run=^TestFileTransferCommandHelper$", "--", "__omnara_file_transfer", "--",
			"upload", fileTransferTestPublicID(t, publicid.KindProcess), "file.txt")
		command.Env = append(os.Environ(), "OMNARA_FILE_TRANSFER_TEST=1")
		command.ExtraFiles = extraFiles
		output, err := command.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "file transfer requires a result pipe") {
			t.Fatalf("err=%v output=%s", err, output)
		}
	}
}

func TestFileTransferAPIErrorResult(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, direction := range []string{"upload", "download"} {
		for _, tc := range []struct {
			name, body, wantResult, wantError string
		}{
			{
				name: "conflict", wantError: "file changed",
				body:       `{"code":"file_content_conflict","error":"file changed","current_digest":"` + digest + `"}`,
				wantResult: `{"error":{"code":"file_content_conflict","error":"file changed","current_digest":"` + digest + `"}}`,
			},
			{
				name: "not found", body: `{"code":"not_found","error":"missing"}`, wantError: "missing",
				wantResult: `{"error":{"code":"not_found","error":"missing"}}`,
			},
			{
				name: "plain text", body: "proxy failure", wantError: "proxy failure",
				wantResult: `{"error":{"code":"file_transfer_failed","error":"transfer file: proxy failure"}}`,
			},
			{
				name: "empty", wantError: "409 Conflict",
				wantResult: `{"error":{"code":"file_transfer_failed","error":"transfer file: 409 Conflict"}}`,
			},
			{
				name: "long conflict", wantError: "<&>",
				body: `{"code":"file_content_conflict","error":"` + strings.Repeat("<&>", 1024) +
					`","current_digest":"` + digest + `"}`,
				wantResult: `{"error":{"code":"file_content_conflict","error":"` + strings.Repeat("<&>", 341) +
					`<","current_digest":"` + digest + `"}}`,
			},
		} {
			t.Run(direction+"/"+tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusConflict)
					_, _ = io.WriteString(w, tc.body)
				}))
				defer server.Close()
				setConfiguredDaemonEnvironment(t, filepath.Join(t.TempDir(), "daemon"), server.URL, "")
				path := filepath.Join(t.TempDir(), "file.txt")
				require.NoError(t, os.WriteFile(path, []byte("preserved"), 0600))
				var stdout bytes.Buffer
				result, err := runFileTransferTestCommand(t, []string{
					"__omnara_file_transfer", direction, fileTransferTestPublicID(t, publicid.KindProcess), path,
				}, &stdout)
				require.ErrorContains(t, err, tc.wantError)
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr)
				require.Equal(t, 1, exitErr.ExitCode())
				require.Empty(t, stdout.String())
				require.JSONEq(t, tc.wantResult, string(result))
				content, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, "preserved", string(content))
			})
		}
	}
}
