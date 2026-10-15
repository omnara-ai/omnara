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

func TestReadFileMachineDispatch(t *testing.T) {
	allow := toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow)
	deny := toolpermission.DefaultSelection(toolpermission.ModeAlwaysDeny)
	for _, test := range []struct {
		name          string
		machines      int
		input         func(machineIDs []string) string
		upload        *toolpermission.Selection
		wantMachine   int
		wantErrorCode string
		wantError     string
	}{
		{
			name: "only machine by default", machines: 1,
			input: func([]string) string { return `{"path":"/tmp/shot.png"}` },
		},
		{
			name: "explicit machine", machines: 2, wantMachine: 1,
			input: func(ids []string) string { return `{"path":"shots/one.png","machine_id":"` + ids[1] + `"}` },
		},
		{
			name: "ambiguous machine", machines: 2, wantErrorCode: ErrMachineSelectionRequired.Error(),
			input: func([]string) string { return `{"path":"/tmp/shot.png"}` },
		},
		{
			name: "unavailable machine", machines: 1, wantErrorCode: ErrMachineIDUnavailable.Error(),
			input: func([]string) string { return `{"path":"/tmp/shot.png","machine_id":"mch_aaaaaaaaaaaaaaaaaaaaaaaaae"}` },
		},
		{
			name: "no machine", wantError: errReadFileMachineUnavailable.Error(),
			input: func([]string) string { return `{"path":"notes.txt"}` },
		},
		{
			name: "upload denied", machines: 1, upload: &deny, wantError: errReadFileMachineTransferDisabled.Error(),
			input: func([]string) string { return `{"path":"/tmp/shot.png"}` },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			label := "rfm-" + strings.ReplaceAll(test.name, " ", "-")
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
			}
			input := test.input(machineIDs)
			call := fixture.recordToolCall(t, ctx, "call_read_machine", "read_file", input, fixture.Now.Add(20*time.Second))
			turn := fixture.turn()
			uploadPermission := allow
			if test.upload != nil {
				uploadPermission = *test.upload
			}
			turn.Tools = map[string]ToolSpec{
				"read_file": {Permission: allow}, "upload_file": {Permission: uploadPermission},
			}
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
				var parts []struct {
					Value map[string]any `json:"value"`
				}
				if err := json.Unmarshal(result.ContentParts, &parts); err != nil || len(parts) != 1 {
					t.Fatalf("failure parts = %s, %v", result.ContentParts, err)
				}
				if test.wantErrorCode != "" && parts[0].Value["error_code"] != test.wantErrorCode {
					t.Fatalf("error_code = %v, want %s", parts[0].Value["error_code"], test.wantErrorCode)
				}
				if test.wantError != "" && parts[0].Value["error"] != test.wantError {
					t.Fatalf("error = %v, want %s", parts[0].Value["error"], test.wantError)
				}
				return
			}
			if result.Disposition != DispatchDeferred || !found || wakes != 1 {
				t.Fatalf("dispatch=%+v process=%t wakes=%d, want deferred machine transfer", result, found, wakes)
			}
			var request struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal([]byte(input), &request); err != nil {
				t.Fatal(err)
			}
			want := processcmd.ForFileTransfer(processcmd.FileTransfer{
				Direction: processcmd.FileTransferUpload, LocalPath: request.Path,
				Target: processcmd.FileTarget{Artifact: &processcmd.ArtifactTarget{}},
			})
			transfer := process.ExecutionSpec.FileTransfer
			if process.AgentMachineBindingID != bindingIDs[test.wantMachine] ||
				process.TimeoutSeconds != fileUploadProcessTimeoutSeconds || transfer == nil ||
				!reflect.DeepEqual(*transfer, *want.FileTransfer) {
				t.Fatalf("process = %+v, want transfer %+v", process, want.FileTransfer)
			}
		})
	}
}

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
		"read_file":   {Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)},
		"upload_file": {Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow)},
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

func TestReadFileMachineApprovalFailsWithoutUploadFile(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixtureWithOptions(t, ctx, "rfm-approval-off", toolFixtureOptions{})
	attachReadFileMachine(t, ctx, fixture, "rfm-approval-off")
	call := fixture.recordPendingToolCall(
		t, ctx, "call_read_off", "read_file", `{"path":"/tmp/shot.png"}`, fixture.Now.Add(20*time.Second),
	)
	turn := fixture.turn()
	turn.Tools = map[string]ToolSpec{
		"read_file": {Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)},
	}
	if err := (Executor{Store: fixture.Store}).PrepareToolCallPermission(ctx, turn, call); err != nil {
		t.Fatal(err)
	}
	callID := fixture.toolCallID(t, ctx, call.ID)
	if _, found, err := fixture.Store.Execution().GetAgentInteractionByToolCallKind(
		ctx, toolsTestProjectID, fixture.Agent.ID, callID, executionstore.AgentInteractionKindPermission,
	); err != nil || found {
		t.Fatalf("permission interaction found=%t err=%v, want none", found, err)
	}
	record, err := fixture.Store.Execution().GetToolCall(ctx, toolsTestProjectID, fixture.Agent.ID, callID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Outcome != executionstore.ToolResultOutcomeFailed ||
		!strings.Contains(string(record.ResultContentParts), errReadFileMachineTransferDisabled.Error()) {
		t.Fatalf("read without upload_file = %s %s", record.Outcome, record.ResultContentParts)
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
