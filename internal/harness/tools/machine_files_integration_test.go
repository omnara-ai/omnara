//go:build integration

package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/fileread"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func TestMachineFileToolsDispatch(t *testing.T) {
	allow := toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow)
	content := "hello"
	offset := 3
	for _, test := range []struct {
		name          string
		tool          string
		machines      int
		daemonVersion string
		input         func(machineIDs []string) string
		wantMachine   int
		wantKind      processcmd.ExecutionKind
		wantOperation processcmd.FileOperation
		wantErrorCode string
		wantError     string
	}{
		{
			name: "read only machine by default", tool: "read_file", machines: 1,
			input:    func([]string) string { return `{"path":"/tmp/shot.png"}` },
			wantKind: processcmd.KindFileRead,
			wantOperation: processcmd.FileOperation{Path: "/tmp/shot.png",
				Paging: fileread.Paging{OffsetLine: intPointer(1), LimitLines: intPointer(toolcatalog.ReadFileDefaultLines)}},
		},
		{
			name: "read explicit machine", tool: "read_file", machines: 2, wantMachine: 1,
			input:    func(ids []string) string { return `{"path":"shots/one.png","machine_id":"` + ids[1] + `"}` },
			wantKind: processcmd.KindFileRead,
			wantOperation: processcmd.FileOperation{Path: "shots/one.png",
				Paging: fileread.Paging{OffsetLine: intPointer(1), LimitLines: intPointer(toolcatalog.ReadFileDefaultLines)}},
		},
		{
			name: "write", tool: "write_file", machines: 1,
			input:         func([]string) string { return `{"path":"~/notes.md","content":"hello","append":true}` },
			wantKind:      processcmd.KindFileWrite,
			wantOperation: processcmd.FileOperation{Path: "~/notes.md", Content: &content, Append: true},
		},
		{
			name: "list", tool: "list_files", machines: 1,
			input:         func([]string) string { return `{"pattern":"src/**/*.go"}` },
			wantKind:      processcmd.KindFileList,
			wantOperation: processcmd.FileOperation{Path: "src/**/*.go", Limit: toolcatalog.ListFilesDefaultLimit},
		},
		{
			name: "search", tool: "search_files", machines: 1,
			input:    func([]string) string { return `{"path":"main.go","args":["-e","TODO"],"offset_line":3}` },
			wantKind: processcmd.KindFileSearch,
			wantOperation: processcmd.FileOperation{Path: "main.go", Args: []string{"-e", "TODO"},
				Limit: toolcatalog.SearchDefaultMatches, Paging: fileread.Paging{OffsetLine: &offset}},
		},
		{
			name: "ambiguous machine", tool: "list_files", machines: 2, wantErrorCode: ErrMachineSelectionRequired.Error(),
			input: func([]string) string { return `{"pattern":"*.txt"}` },
		},
		{
			name: "unavailable machine", tool: "search_files", machines: 1, wantErrorCode: ErrMachineIDUnavailable.Error(),
			input: func([]string) string {
				return `{"path":"a.txt","args":["-e","x"],"machine_id":"mch_aaaaaaaaaaaaaaaaaaaaaaaaae"}`
			},
		},
		{
			name: "no machine", tool: "read_file", wantError: errMachineFileMachineUnavailable.Error(),
			input: func([]string) string { return `{"path":"notes.txt"}` },
		},
		{
			name: "old daemon", tool: "write_file", machines: 1, daemonVersion: "0.2.5",
			wantError: "this machine's omnarad 0.2.5 does not support file tools on machines; " +
				"update omnarad to 0.2.6 or later, or use run_command",
			input: func([]string) string { return `{"path":"notes.txt","content":"x"}` },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			label := "mft-" + strings.ReplaceAll(test.name, " ", "-")
			fixture := newIntegrationToolFixtureWithOptions(t, ctx, label, toolFixtureOptions{})
			var machineIDs []string
			var bindingIDs []uuid.UUID
			for index := range test.machines {
				machineID, bindingID := attachReadFileMachine(t, ctx, fixture, label+"-"+string(rune('a'+index)))
				publicMachineID, err := publicid.Encode(publicid.KindMachine, machineID)
				if err != nil {
					t.Fatal(err)
				}
				machineIDs, bindingIDs = append(machineIDs, publicMachineID), append(bindingIDs, bindingID)
				if test.daemonVersion != "" {
					if _, err := fixture.Pool.Exec(ctx,
						`UPDATE daemon_runtimes SET daemon_version = $1 WHERE machine_id = $2`,
						test.daemonVersion, machineID,
					); err != nil {
						t.Fatal(err)
					}
				}
			}
			input := test.input(machineIDs)
			call := fixture.recordToolCall(t, ctx, "call_machine_file", test.tool, input, fixture.Now.Add(20*time.Second))
			turn := fixture.turn()
			turn.Tools = map[string]ToolSpec{test.tool: {Permission: allow}}
			wakes := 0
			executor := Executor{Store: fixture.Store, BackgroundRunner: backgroundRunnerFunc(
				func(string, func(context.Context) error) bool { wakes++; return true },
			)}
			result, err := executor.Dispatch(ctx, turn, call)
			if err != nil {
				t.Fatal(err)
			}
			callID := fixture.toolCallID(t, ctx, call.ID)
			process, found, err := fixture.Store.Execution().GetProcessByToolCall(
				ctx, toolsTestProjectID, fixture.Agent.ID, callID,
			)
			if err != nil {
				t.Fatal(err)
			}
			if test.wantErrorCode != "" || test.wantError != "" {
				if result.Disposition != DispatchCompleted || found || wakes != 0 {
					t.Fatalf("dispatch=%+v process=%t wakes=%d, want completed failure", result, found, wakes)
				}
				if !strings.Contains(string(result.ContentParts), test.wantErrorCode) ||
					!strings.Contains(string(result.ContentParts), test.wantError) {
					t.Fatalf("failure parts = %s, want %q %q", result.ContentParts, test.wantErrorCode, test.wantError)
				}
				return
			}
			if result.Disposition != DispatchDeferred || !found || wakes != 1 {
				t.Fatalf("dispatch=%+v process=%t wakes=%d, want deferred machine operation", result, found, wakes)
			}
			operation := process.ExecutionSpec.File
			if process.AgentMachineBindingID != bindingIDs[test.wantMachine] ||
				process.TimeoutSeconds != machineFileProcessTimeoutSeconds ||
				process.ExecutionSpec.Kind != test.wantKind || operation == nil {
				t.Fatalf("process = %+v, want %s on binding %s", process, test.wantKind, bindingIDs[test.wantMachine])
			}
			if test.wantKind == processcmd.KindFileList {
				if operation.CursorScope == "" {
					t.Fatal("machine listing has no cursor scope")
				}
				operation.CursorScope = ""
			}
			if !reflect.DeepEqual(*operation, test.wantOperation) {
				t.Fatalf("operation = %+v, want %+v", *operation, test.wantOperation)
			}
		})
	}
}

func intPointer(value int) *int { return &value }

func TestReadFileMachineApprovalPinsBindingAndPath(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixtureWithOptions(t, ctx, "rfm-approval", toolFixtureOptions{})
	machineID, bindingID := attachReadFileMachine(t, ctx, fixture, "rfm-approval")
	publicMachineID, err := publicid.Encode(publicid.KindMachine, machineID)
	if err != nil {
		t.Fatal(err)
	}
	input := `{"path":"/tmp/shot.png","machine_id":"` + publicMachineID + `"}`
	call := fixture.recordPendingToolCall(
		t, ctx, "call_read_approval", "read_file", input, fixture.Now.Add(20*time.Second),
	)
	turn := fixture.turn()
	turn.Tools = map[string]ToolSpec{
		"read_file": {Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)},
	}
	executor := Executor{Store: fixture.Store, BackgroundRunner: backgroundRunnerFunc(
		func(string, func(context.Context) error) bool { return true },
	)}
	if err := executor.PrepareToolCallPermission(ctx, turn, call); err != nil {
		t.Fatal(err)
	}
	callID := fixture.toolCallID(t, ctx, call.ID)
	interaction := integrationToolInteraction(t, ctx, fixture, callID, "permission")
	request, err := toolpermission.ParseRequest(interaction.Request)
	if err != nil {
		t.Fatal(err)
	}
	wantAuthorization, err := fileTransferAuthorizationInput(bindingID, json.RawMessage(input))
	if err != nil {
		t.Fatal(err)
	}
	if !jsoncanonical.Equal(request.Authorization.Input, wantAuthorization) {
		t.Fatalf("authorization = %s, want %s", request.Authorization.Input, wantAuthorization)
	}
	wantContext := []interactionform.ContextItem{
		{Label: "Path", Value: "/tmp/shot.png"},
		{Label: "Machine", Value: publicMachineID},
	}
	if !slices.Equal(request.Form.Context, wantContext) {
		t.Fatalf("approval context = %+v, want %+v", request.Form.Context, wantContext)
	}
	approveToolPermissionForTest(t, ctx, fixture.Store.Execution(), interaction, fixture.User.ID)
	result, err := executor.Dispatch(ctx, turn, call)
	if err != nil {
		t.Fatal(err)
	}
	process, found, err := fixture.Store.Execution().GetProcessByToolCall(
		ctx, toolsTestProjectID, fixture.Agent.ID, callID,
	)
	if err != nil || !found || result.Disposition != DispatchDeferred || process.AgentMachineBindingID != bindingID {
		t.Fatalf("approved dispatch=%+v process=%+v found=%t err=%v", result, process, found, err)
	}
}

func TestReadFileOmnaraPathDispatchStaysAsync(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixtureWithOptions(
		t, ctx, "rfm-omnara", toolFixtureOptions{},
		storage.WithBlobStore(integrationblob.MustOpen(t, ctx)),
	)
	attachReadFileMachine(t, ctx, fixture, "rfm-omnara")
	artifact, err := fixture.Store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
		ProjectID: toolsTestProjectID, AgentID: fixture.Agent.ID, ContentType: "text/plain",
		Content: []byte("first\nTARGET\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	publicArtifactID, err := publicid.Encode(publicid.KindArtifact, artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := `{"path":"/artifacts/` + publicArtifactID + `","offset_line":2}`
	call := fixture.recordToolCall(t, ctx, "call_read_artifact", "read_file", input, fixture.Now.Add(20*time.Second))
	callID := fixture.toolCallID(t, ctx, call.ID)
	implementation, ok, err := toolImplementationFor(toolcatalog.ToolNameReadFile)
	if err != nil || !ok {
		t.Fatalf("read_file implementation: %v", err)
	}
	dispatchTestAsyncHandler(t, ctx, Executor{Store: fixture.Store}, fixture.turn(), call, callID, implementation.handler)
	if _, found, err := fixture.Store.Execution().GetProcessByToolCall(
		ctx, toolsTestProjectID, fixture.Agent.ID, callID,
	); err != nil || found {
		t.Fatalf("Omnara read started a machine process: found=%t err=%v", found, err)
	}
	record, err := fixture.Store.Execution().GetToolCall(ctx, toolsTestProjectID, fixture.Agent.ID, callID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Outcome != executionstore.ToolResultOutcomeSucceeded ||
		!strings.Contains(string(record.ResultContentParts), "TARGET") {
		t.Fatalf("Omnara read result = %s %s", record.Outcome, record.ResultContentParts)
	}
}

func attachReadFileMachine(
	t *testing.T,
	ctx context.Context,
	fixture integrationToolFixture,
	name string,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	machine := createExecutableBinding(t, ctx, fixture.Store, fixture.User.ID, name, fixture.Now.Add(time.Second))
	var bindingID uuid.UUID
	if err := fixture.Pool.QueryRow(ctx, `
		INSERT INTO agent_machine_bindings(
			org_id, project_id, agent_id, machine_id, binding_kind, state, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'explicit', 'attached', $5, $5)
		RETURNING id`,
		toolsTestOrgID, toolsTestProjectID, fixture.Agent.ID, machine.MachineID, fixture.Now,
	).Scan(&bindingID); err != nil {
		t.Fatal(err)
	}
	return machine.MachineID, bindingID
}

func testImages(t *testing.T) map[string][]byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 1, 1))
	var pngContent, jpegContent, gifContent bytes.Buffer
	if err := png.Encode(&pngContent, img); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&jpegContent, img, nil); err != nil {
		t.Fatal(err)
	}
	if err := gif.Encode(&gifContent, img, nil); err != nil {
		t.Fatal(err)
	}
	webpContent, err := base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")
	if err != nil {
		t.Fatal(err)
	}
	return map[string][]byte{
		"image/png":  pngContent.Bytes(),
		"image/jpeg": jpegContent.Bytes(),
		"image/gif":  gifContent.Bytes(),
		"image/webp": webpContent,
	}
}
