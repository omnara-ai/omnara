package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestResolveMachineOperationCwd(t *testing.T) {
	cases := []struct {
		machine, requested, want string
	}{
		{"", "", ""},
		{"/srv/app", "", "/srv/app"},
		{"/srv/app", "sub/dir", "/srv/app/sub/dir"},
		{"/srv/app", "/tmp/../var", "/var"},
		{"~/work", "repo", "~/work/repo"},
		{"/srv/app", "~/other", "~/other"},
		{"", "relative", "relative"},
	}
	for _, tc := range cases {
		if got := resolveMachineOperationCwd(tc.machine, tc.requested); got != tc.want {
			t.Errorf("resolveMachineOperationCwd(%q, %q) = %q, want %q", tc.machine, tc.requested, got, tc.want)
		}
	}
}

func TestExecMachineOperationRequest(t *testing.T) {
	machine := executionstore.MachineRecord{Cwd: "/srv", Env: json.RawMessage(`{"A":"machine","B":"keep"}`)}
	timeout := 12
	shell := openapi.ExecMachineCommandRequestShell("bash")
	env := map[string]string{"A": "request"}
	cwd := "repo"
	request, err := execMachineOperationRequest(machine, openapi.ExecMachineCommandRequest{
		Command: "echo hi", Shell: &shell, TimeoutSeconds: &timeout, Env: &env, Cwd: &cwd,
	})
	if err != nil {
		t.Fatalf("execMachineOperationRequest: %v", err)
	}
	if request.spec.Kind != processcmd.KindShell || request.spec.Shell.Command != "echo hi" ||
		request.spec.Shell.Shell != processcmd.ShellBash || request.spec.Shell.IOMode != processcmd.IOModePipe {
		t.Fatalf("spec = %+v", request.spec)
	}
	if request.cwd != "/srv/repo" || request.timeoutSeconds != 12 {
		t.Fatalf("cwd/timeout = %q/%d", request.cwd, request.timeoutSeconds)
	}
	if request.env["A"] != "request" || request.env["B"] != "keep" {
		t.Fatalf("env = %v", request.env)
	}

	defaults, err := execMachineOperationRequest(
		executionstore.MachineRecord{}, openapi.ExecMachineCommandRequest{Command: "true"},
	)
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if defaults.cwd != "~" || defaults.timeoutSeconds != 300 || defaults.env != nil ||
		defaults.spec.Shell.Shell != processcmd.ShellDefault {
		t.Fatalf("defaults = %+v", defaults)
	}

	tooLong := 3601
	badEnv := map[string]string{"A=B": "x"}
	for name, body := range map[string]openapi.ExecMachineCommandRequest{
		"blank command": {Command: "   "},
		"timeout":       {Command: "true", TimeoutSeconds: &tooLong},
		"env name":      {Command: "true", Env: &badEnv},
	} {
		if _, err := execMachineOperationRequest(executionstore.MachineRecord{}, body); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestExecMachineCommandResponse(t *testing.T) {
	exitCode := 3
	response := execMachineCommandResponse(executionstore.MachineOperationRecord{
		State:    executionstore.MachineOperationStateFailed,
		ExitCode: &exitCode,
		Result:   json.RawMessage(`{"output":"hi\n","truncated":true}`),
	}, 1500*time.Millisecond)
	if got, err := response.ExitCode.Get(); err != nil || got != 3 {
		t.Fatalf("exit code = %v, %v", got, err)
	}
	if response.Output != "hi\n" || !response.OutputTruncated || response.TimedOut ||
		response.DurationMs != 1500 || response.Error != nil {
		t.Fatalf("response = %+v", response)
	}

	timedOut := execMachineCommandResponse(executionstore.MachineOperationRecord{
		State:              executionstore.MachineOperationStateFailed,
		StateReasonCode:    "timeout",
		StateReasonMessage: "process exceeded its configured timeout",
		ExitSignal:         "terminated",
		Result:             json.RawMessage(`{"output":"start\n"}`),
	}, time.Second)
	if !timedOut.TimedOut || !timedOut.ExitCode.IsNull() || timedOut.ExitSignal == nil ||
		timedOut.Error == nil || *timedOut.Error != "process exceeded its configured timeout" {
		t.Fatalf("timed out response = %+v", timedOut)
	}

	invalid := execMachineCommandResponse(executionstore.MachineOperationRecord{
		State:  executionstore.MachineOperationStateExited,
		Result: json.RawMessage("{\"output\":\"bad \xff byte\"}"),
	}, 0)
	if invalid.Output != "bad \uFFFD byte" {
		t.Fatalf("invalid UTF-8 output = %q", invalid.Output)
	}
}

func TestMachineFileTransferError(t *testing.T) {
	zero := 0
	one := 1
	transferResult := func(message string) json.RawMessage {
		result := processTerminalResult{FileTransfer: &daemonprotocol.FileTransferResult{Digest: "sha256:x"}}
		if message != "" {
			result.FileTransfer = &daemonprotocol.FileTransferResult{
				Error: &daemonprotocol.FileTransferError{Code: "file_transfer_failed", Message: message},
			}
		}
		raw, err := json.Marshal(result)
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		return raw
	}
	success := executionstore.MachineOperationRecord{
		State: executionstore.MachineOperationStateExited, ExitCode: &zero, Result: transferResult(""),
	}
	if err := machineFileTransferError(success); err != nil {
		t.Fatalf("success mapped to %v", err)
	}
	cases := map[string]openapi.ErrorCode{
		"open file: open /x: no such file or directory": openapi.ErrorCodeNotFound,
		"source must be a regular file":                 openapi.ErrorCodeNotAFile,
		"file exceeds the upload size limit":            openapi.ErrorCodeRequestTooLarge,
		"open file: open /x: permission denied":         openapi.ErrorCodeForbidden,
		"something else":                                openapi.ErrorCodeUnprocessable,
	}
	for message, want := range cases {
		err := machineFileTransferError(executionstore.MachineOperationRecord{
			State: executionstore.MachineOperationStateFailed, ExitCode: &one, Result: transferResult(message),
		})
		var responseErr apierror.ResponseError
		if !errors.As(err, &responseErr) || responseErr.Code != want {
			t.Errorf("%q mapped to %v, want %s", message, err, want)
		}
	}
	timedOut := machineFileTransferError(executionstore.MachineOperationRecord{
		State: executionstore.MachineOperationStateFailed, StateReasonCode: "timeout",
	})
	var responseErr apierror.ResponseError
	if !errors.As(timedOut, &responseErr) || responseErr.Code != openapi.ErrorCodeServiceUnavailable {
		t.Fatalf("timeout mapped to %v", timedOut)
	}
	parentMissing := executionstore.MachineOperationRecord{
		State: executionstore.MachineOperationStateFailed, ExitCode: &one,
		Result: transferResult("create temporary file: open /a/b/.omnara-file-1: no such file or directory"),
	}
	if !machineFileParentMissing(parentMissing) || machineFileParentMissing(success) {
		t.Fatal("parent-missing detection is wrong")
	}
}

func TestMachineFilePathHelpers(t *testing.T) {
	for _, valid := range []string{"/tmp/x", "~/x", `C:\Users\x`, "C:/x"} {
		if err := validateMachineFilePath(valid); err != nil {
			t.Errorf("validateMachineFilePath(%q) = %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "relative/x", "/tmp/\x00x"} {
		if err := validateMachineFilePath(invalid); err == nil {
			t.Errorf("validateMachineFilePath(%q) accepted", invalid)
		}
	}
	if got := posixDir("~/a/b/c.txt"); got != "~/a/b" {
		t.Errorf("posixDir home = %q", got)
	}
	if got := shellQuotePOSIXPath("~/it's here"); got != `"$HOME"/'it'\''s here'` {
		t.Errorf("shellQuotePOSIXPath home = %q", got)
	}
	if got := shellQuotePOSIXPath("/tmp/a b"); got != `'/tmp/a b'` {
		t.Errorf("shellQuotePOSIXPath = %q", got)
	}
}

func TestRequestBodyLimitMachineFileUpload(t *testing.T) {
	req := httptest.NewRequest(http.MethodPut, "/api/v1/orgs/org_x/machines/mch_x/files?path=/tmp/x", nil)
	if got := requestBodyLimit(req); got != daemonprotocol.MaxFileTransferBytes {
		t.Fatalf("machine file upload body limit = %d, want %d", got, daemonprotocol.MaxFileTransferBytes)
	}
}
