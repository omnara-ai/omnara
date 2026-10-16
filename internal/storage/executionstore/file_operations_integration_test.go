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

func TestMachineFileOperationCompletionRendersToolResult(t *testing.T) {
	t.Parallel()
	var screenshot bytes.Buffer
	if err := png.Encode(&screenshot, image.NewGray(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name        string
		kind        processcmd.ExecutionKind
		operation   processcmd.FileOperation
		image       []byte
		result      func(artifactPath string) map[string]any
		wantOutcome executionstore.ToolResultOutcome
		wantFields  map[string]any
	}{
		{
			name: "image", kind: processcmd.KindFileRead, operation: processcmd.FileOperation{Path: "/tmp/shot.png"},
			image: screenshot.Bytes(),
			result: func(path string) map[string]any {
				return map[string]any{"artifact": path, "value": map[string]any{
					"path": "/tmp/shot.png", "content_type": "image/png", "size_bytes": screenshot.Len(),
				}}
			},
			wantOutcome: executionstore.ToolResultOutcomeSucceeded,
			wantFields:  map[string]any{"path": "/tmp/shot.png", "content_type": "image/png"},
		},
		{
			name: "text", kind: processcmd.KindFileRead, operation: processcmd.FileOperation{Path: "notes.txt"},
			result: func(string) map[string]any {
				return map[string]any{"value": map[string]any{"path": "notes.txt", "content": "two\n"}}
			},
			wantOutcome: executionstore.ToolResultOutcomeSucceeded,
			wantFields:  map[string]any{"path": "notes.txt", "content": "two\n"},
		},
		{
			name: "write failure", kind: processcmd.KindFileWrite,
			operation: processcmd.FileOperation{Path: "notes", Content: new(string)},
			result: func(string) map[string]any {
				return map[string]any{"error": map[string]any{
					"code": "file_operation_failed", "error": "path must be a regular file",
				}}
			},
			wantOutcome: executionstore.ToolResultOutcomeFailed,
			wantFields: map[string]any{
				"path": "notes", "error": "path must be a regular file", "error_code": "file_operation_failed",
			},
		},
		{
			name: "missing result", kind: processcmd.KindFileList,
			operation:   processcmd.FileOperation{Path: "*.txt", Limit: 10},
			result:      func(string) map[string]any { return nil },
			wantOutcome: executionstore.ToolResultOutcomeFailed,
			wantFields:  map[string]any{"error": "file operation completed without a valid result", "machine_id": nil},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := openIntegrationDB(t, ctx)
			seedMigratedDB(t, ctx, pool)
			store := newIntegrationStore(
				pool,
				storage.WithModelCallRetryBackoff(func(int, string) time.Duration { return 0 }),
				storage.WithBlobStore(&overflowBlobs{content: map[string][]byte{}}),
			)
			name := "machine_file_" + strings.ReplaceAll(test.name, " ", "_")
			user := mustCreateProjectOperatorUser(t, ctx, store, name+"@example.com", "Machine Files")
			fixture := newProcessDaemonFixtureInStore(
				t, ctx, store, user.ID, name, time.Date(2026, 5, 17, 10, 0, 0, 0, time.UTC),
			)
			toolCallID := createToolCallBatchForProcessTest(t, ctx, fixture, name, []processToolCallBatchItem{{
				TestName: test.name, ToolName: toolcatalog.ToolNameReadFile, ToolType: toolcatalog.ToolTypeBuiltIn,
				Allowed: true, Input: json.RawMessage(`{"path":"` + test.operation.Path + `"}`),
			}})[0]
			process, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
				ProjectID: testProjectID, AgentID: fixture.AgentID, ToolCallID: toolCallID, RuntimeLockID: fixture.Lock.ID,
			}, executionstore.CreateProcessInput{
				ExecutionSpec:         processcmd.ForFileOperation(test.kind, test.operation),
				AgentMachineBindingID: fixture.BindingID,
			})
			if err != nil {
				t.Fatalf("start file operation: %v", err)
			}
			if _, found, err := acceptDaemonProcessForTest(
				ctx, fixture.Store, testOrgID, fixture.MachineID, fixture.RuntimeID, process.ID,
			); err != nil || !found {
				t.Fatalf("accept file operation: found=%t err=%v", found, err)
			}
			scope, found, err := fixture.Store.Execution().GetDaemonFileProcessScope(
				ctx, testOrgID, fixture.MachineID, process.ID, processcmd.FileTransferUpload,
			)
			if err != nil || found != (test.kind == processcmd.KindFileRead) ||
				found && (scope.ToolCallID != toolCallID || scope.Transfer.Target.Artifact == nil) {
				t.Fatalf("upload scope = %+v found=%t err=%v", scope, found, err)
			}
			var artifactPath string
			if test.image != nil {
				artifact, err := fixture.Store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
					ProjectID: testProjectID, AgentID: fixture.AgentID, ContentType: "image/png",
					Filename: "shot.png", Content: test.image, MaxBytes: daemonprotocol.MaxFileTransferBytes,
					IdempotencyKey: executionstore.UploadArtifactIdempotencyKey(toolCallID),
				})
				if err != nil {
					t.Fatalf("create uploaded image: %v", err)
				}
				artifactPath = toolcatalog.ArtifactVFSRoot + "/" + publicResourceID(publicid.KindArtifact, artifact.ID)
			}
			observed := map[string]any{"output": "", "cursor": 0, "next_cursor": 0, "truncated": false}
			if result := test.result(artifactPath); result != nil {
				observed["file_operation"] = result
			}
			exitCode := 0
			completion := executionstore.CompleteDaemonProcessInput{
				ProjectID: testProjectID, AgentID: fixture.AgentID, ID: process.ID, Authority: fixture.authority(),
				State: executionstore.ProcessStateExited, ExitCode: &exitCode, Result: mustTestRawJSON(t, observed),
				SourceStartedAt: fixture.Now.Add(time.Second), SourceEndedAt: fixture.Now.Add(2 * time.Second),
			}
			completed, err := fixture.Store.Execution().CompleteDaemonProcess(ctx, completion)
			if err != nil || !completed.ToolResultCommitted {
				t.Fatalf("complete file operation: committed=%t err=%v", completed.ToolResultCommitted, err)
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
			if test.image != nil {
				wantParts = 2
			}
			if len(parts) != wantParts || parts[0]["type"] != "structured_data" ||
				test.image != nil && (parts[1]["type"] != "media_ref" || parts[1]["exclude_from_model_context"] != nil) {
				t.Fatalf("model-visible parts = %s", modelResult.ResultContentParts)
			}
			value, _ := parts[0]["value"].(map[string]any)
			want := map[string]any{"machine_id": publicResourceID(publicid.KindMachine, fixture.MachineID)}
			for key, field := range test.wantFields {
				want[key] = field
			}
			for key, field := range want {
				if value[key] != field {
					t.Fatalf("result %s = %v, want %v in %s", key, value[key], field, modelResult.ResultContentParts)
				}
			}
		})
	}
}
