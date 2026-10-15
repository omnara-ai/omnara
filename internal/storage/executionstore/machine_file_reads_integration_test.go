//go:build integration

package executionstore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestMachineReadFileCompletionRendersReadFileResult(t *testing.T) {
	t.Parallel()
	var screenshot bytes.Buffer
	if err := png.Encode(&screenshot, image.NewGray(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name        string
		input       string
		filename    string
		content     []byte
		wantOutcome executionstore.ToolResultOutcome
		wantImage   bool
		dropBlob    bool
		wantFields  map[string]any
	}{
		{
			name: "image", input: `{"path":"/tmp/shot.png"}`, filename: "shot.png", content: screenshot.Bytes(),
			wantOutcome: executionstore.ToolResultOutcomeSucceeded, wantImage: true,
			wantFields: map[string]any{"path": "/tmp/shot.png", "content_type": "image/png"},
		},
		{
			name: "text lines", input: `{"path":"notes.txt","offset_line":2,"limit_lines":1}`,
			filename: "notes.txt", content: []byte("one\ntwo\nthree\n"),
			wantOutcome: executionstore.ToolResultOutcomeSucceeded,
			wantFields: map[string]any{
				"path": "notes.txt", "content": "two\n", "lines_read": float64(1), "offset_line": float64(2),
				"has_more": true, "next_offset_line": float64(3),
			},
		},
		{
			name: "text chars", input: `{"path":"notes.txt","offset_char":4,"limit_chars":3}`,
			filename: "notes.txt", content: []byte("one\ntwo\nthree\n"),
			wantOutcome: executionstore.ToolResultOutcomeSucceeded,
			wantFields: map[string]any{
				"content": "two", "offset_char": float64(4), "has_more": true, "next_offset_char": float64(7),
			},
		},
		{
			name: "binary", input: `{"path":"blob.bin"}`, filename: "blob.bin", content: []byte{0, 1, 2, 0xff},
			wantOutcome: executionstore.ToolResultOutcomeFailed,
			wantFields: map[string]any{
				"path":  "blob.bin",
				"error": "file must contain UTF-8 text without NUL bytes, or be a PNG, JPEG, GIF, or WebP image",
			},
		},
		{
			name:     "digest mismatch",
			input:    `{"path":"notes.txt","expected_digest":"sha256:` + strings.Repeat("0", 64) + `"}`,
			filename: "notes.txt", content: []byte("changed\n"),
			wantOutcome: executionstore.ToolResultOutcomeFailed,
			wantFields:  map[string]any{"error_code": "file_content_conflict"},
		},
		{
			name: "missing snapshot", input: `{"path":"notes.txt"}`, filename: "notes.txt", content: []byte("gone\n"),
			dropBlob: true, wantOutcome: executionstore.ToolResultOutcomeFailed,
			wantFields: map[string]any{
				"error": "the uploaded file snapshot is unavailable; read the file again", "size_bytes": nil,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := openIntegrationDB(t, ctx)
			seedMigratedDB(t, ctx, pool)
			blobs := &overflowBlobs{content: map[string][]byte{}}
			store := newIntegrationStore(
				pool,
				storage.WithModelCallRetryBackoff(func(int, string) time.Duration { return 0 }),
				storage.WithBlobStore(blobs),
			)
			name := "machine_read_" + strings.ReplaceAll(test.name, " ", "_")
			user := mustCreateProjectOperatorUser(t, ctx, store, name+"@example.com", "Machine Reader")
			fixture := newProcessDaemonFixtureInStore(
				t, ctx, store, user.ID, name, time.Date(2026, 5, 17, 10, 0, 0, 0, time.UTC),
			)
			toolCallID := createToolCallBatchForProcessTest(t, ctx, fixture, name, []processToolCallBatchItem{{
				TestName: test.name, ToolName: toolcatalog.ToolNameReadFile, ToolType: toolcatalog.ToolTypeBuiltIn,
				Allowed: true, Input: json.RawMessage(test.input),
			}})[0]
			var request struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal([]byte(test.input), &request); err != nil {
				t.Fatal(err)
			}
			process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
				ProjectID: testProjectID, AgentID: fixture.AgentID, ToolCallID: toolCallID, RuntimeLockID: fixture.Lock.ID,
			}, executionstore.CreateProcessInput{
				ExecutionSpec: processcmd.ForFileTransfer(processcmd.FileTransfer{
					Direction: processcmd.FileTransferUpload, LocalPath: request.Path,
					Target: processcmd.FileTarget{Artifact: &processcmd.ArtifactTarget{}},
				}),
				AgentMachineBindingID: fixture.BindingID,
			})
			if err != nil {
				t.Fatalf("start machine read: %v", err)
			}
			if _, found, err := acceptDaemonProcessForTest(
				ctx, fixture.Store, testOrgID, fixture.MachineID, fixture.RuntimeID, process.ID,
			); err != nil || !found {
				t.Fatalf("accept machine read: found=%t err=%v", found, err)
			}
			artifact, err := fixture.Store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
				ProjectID: testProjectID, AgentID: fixture.AgentID, ContentType: contentTypeForTest(test.content),
				Filename: test.filename, Content: test.content, MaxBytes: daemonprotocol.MaxFileTransferBytes,
				IdempotencyKey: executionstore.UploadArtifactIdempotencyKey(toolCallID),
			})
			if err != nil {
				t.Fatalf("create uploaded snapshot: %v", err)
			}
			if test.dropBlob {
				blobs.mu.Lock()
				clear(blobs.content)
				blobs.mu.Unlock()
			}
			artifactPath := toolcatalog.ArtifactVFSRoot + "/" + publicResourceID(publicid.KindArtifact, artifact.ID)
			exitCode := 0
			completion := executionstore.CompleteDaemonProcessInput{
				ProjectID: testProjectID, AgentID: fixture.AgentID, ID: process.ID, Authority: fixture.authority(),
				State: executionstore.ProcessStateExited, ExitCode: &exitCode,
				Result: mustTestRawJSON(t, map[string]any{
					"output": "", "cursor": 0, "next_cursor": 0, "truncated": false,
					"file_transfer": map[string]string{"path": artifactPath, "digest": artifact.Digest},
				}),
				SourceStartedAt: fixture.Now.Add(time.Second),
				SourceEndedAt:   fixture.Now.Add(2 * time.Second),
			}
			completed, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, completion)
			if err != nil || !completed.ToolResultCommitted {
				t.Fatalf("complete machine read: committed=%t err=%v", completed.ToolResultCommitted, err)
			}
			toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
			if err != nil {
				t.Fatal(err)
			}
			if toolCall.Outcome != test.wantOutcome {
				t.Fatalf("outcome = %s, want %s: %s", toolCall.Outcome, test.wantOutcome, toolCall.ResultContentParts)
			}
			modelResult := completedToolCallForTest(t, fixture.Store, fixture.AgentID, toolCall.TurnID, toolCallID)
			var parts []map[string]any
			if err := json.Unmarshal(modelResult.ResultContentParts, &parts); err != nil {
				t.Fatal(err)
			}
			wantParts := 1
			if test.wantImage {
				wantParts = 2
			}
			if len(parts) != wantParts || parts[0]["type"] != "structured_data" {
				t.Fatalf("model-visible parts = %s", modelResult.ResultContentParts)
			}
			if test.wantImage && (parts[1]["type"] != "media_ref" || parts[1]["artifact_id"] != artifact.ID.String()) {
				t.Fatalf("image read is not model-visible media: %s", modelResult.ResultContentParts)
			}
			value, ok := parts[0]["value"].(map[string]any)
			if !ok {
				t.Fatalf("structured value = %v", parts[0]["value"])
			}
			want := map[string]any{
				"machine_id":    publicResourceID(publicid.KindMachine, fixture.MachineID),
				"artifact_path": artifactPath,
				"digest":        artifact.Digest,
				"size_bytes":    float64(len(test.content)),
			}
			for key, field := range test.wantFields {
				want[key] = field
			}
			for key, field := range want {
				if value[key] != field {
					t.Fatalf("result %s = %v, want %v in %s", key, value[key], field, modelResult.ResultContentParts)
				}
			}
			if test.wantImage && value["content"] != nil {
				t.Fatalf("image read returned text content: %v", value)
			}
			replayed, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, completion)
			if err != nil || !replayed.ToolResultCommitted {
				t.Fatalf("replay machine read completion: committed=%t err=%v", replayed.ToolResultCommitted, err)
			}
		})
	}
}

func contentTypeForTest(content []byte) string {
	if bytes.HasPrefix(content, []byte("\x89PNG")) {
		return "image/png"
	}
	if bytes.IndexByte(content, 0) >= 0 {
		return "application/octet-stream"
	}
	return "text/plain; charset=utf-8"
}
