package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/processaction"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func TestResolveRunCommandRequestKeepsShellIntentUnresolved(t *testing.T) {
	cases := []struct {
		name     string
		input    json.RawMessage
		command  string
		selector processcmd.ShellSelector
		waitMs   int
		ioMode   processcmd.IOMode
	}{
		{
			name:     "default",
			input:    json.RawMessage(`{"command":"echo ok"}`),
			command:  "echo ok",
			selector: "default",
			ioMode:   "pipe",
		},
		{
			name:     "sh",
			input:    json.RawMessage(`{"command":"echo ok","shell":"sh","wait_ms":5}`),
			command:  "echo ok",
			selector: "sh",
			waitMs:   5,
			ioMode:   "pipe",
		},
		{
			name:     "bash",
			input:    json.RawMessage(`{"command":"echo ok","shell":"bash"}`),
			command:  "echo ok",
			selector: "bash",
			ioMode:   "pipe",
		},
		{
			name:     "zsh",
			input:    json.RawMessage(`{"command":"echo ok","shell":"zsh"}`),
			command:  "echo ok",
			selector: "zsh",
			ioMode:   "pipe",
		},
		{
			name:     "pwsh",
			input:    json.RawMessage(`{"command":"Write-Output ok","shell":"pwsh"}`),
			command:  "Write-Output ok",
			selector: "pwsh",
			ioMode:   "pipe",
		},
		{
			name:     "powershell",
			input:    json.RawMessage(`{"command":"Write-Output ok","shell":"powershell"}`),
			command:  "Write-Output ok",
			selector: "powershell",
			ioMode:   "pipe",
		},
		{
			name:     "cmd",
			input:    json.RawMessage(`{"command":"dir","shell":"cmd"}`),
			command:  "dir",
			selector: "cmd",
			ioMode:   "pipe",
		},
		{
			name:     "pty",
			input:    json.RawMessage(`{"command":"echo ok","io_mode":"pty"}`),
			command:  "echo ok",
			selector: "default",
			ioMode:   "pty",
		},
		{
			name:     "max wait",
			input:    json.RawMessage(`{"command":"echo ok","wait_ms":10000}`),
			command:  "echo ok",
			selector: "default",
			waitMs:   processaction.MaxWaitMilliseconds,
			ioMode:   "pipe",
		},
		{
			name:     "blank machine ref",
			input:    json.RawMessage(`{"command":"echo ok","machine_ref":""}`),
			command:  "echo ok",
			selector: "default",
			ioMode:   "pipe",
		},
		{
			name:     "space padded machine ref",
			input:    json.RawMessage(`{"command":"echo ok","machine_ref":" "}`),
			command:  "echo ok",
			selector: "default",
			ioMode:   "pipe",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolved, err := resolveRunCommandRequest(tc.input)
			if err != nil {
				t.Fatalf("resolve run_command: %v", err)
			}
			if resolved.Command != tc.command || resolved.Selector != tc.selector || resolved.WaitMs != tc.waitMs ||
				resolved.IOMode != tc.ioMode {
				t.Fatalf("unexpected resolution: %+v", resolved)
			}
		})
	}
}

func TestResolveRunCommandRequestRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{name: "bad json", raw: json.RawMessage(`{"command":`), want: "parse run_command request"},
		{name: "empty command", raw: json.RawMessage(`{"command":"  "}`), want: "command is required"},
		{name: "unknown field", raw: json.RawMessage(`{"command":"echo ok","timeout_seconds":5}`), want: "unknown field"},
		{
			name: "null machine ref",
			raw:  json.RawMessage(`{"command":"echo ok","machine_ref":null}`),
			want: "machine_ref cannot be null",
		},
		{
			name: "unsupported selector",
			raw:  json.RawMessage(`{"command":"echo ok","shell":"/tmp/run"}`),
			want: "unsupported shell selector",
		},
		{
			name: "uppercase selector",
			raw:  json.RawMessage(`{"command":"echo ok","shell":"BASH"}`),
			want: "unsupported shell selector",
		},
		{
			name: "space padded selector",
			raw:  json.RawMessage(`{"command":"echo ok","shell":" sh "}`),
			want: "leading or trailing whitespace",
		},
		{
			name: "blank selector",
			raw:  json.RawMessage(`{"command":"echo ok","shell":"  "}`),
			want: "shell selector cannot be blank",
		},
		{
			name: "null selector",
			raw:  json.RawMessage(`{"command":"echo ok","shell":null}`),
			want: "shell selector cannot be null",
		},
		{name: "nul command", raw: json.RawMessage("{\"command\":\"echo \\u0000\"}"), want: "NUL"},
		{
			name: "nul cwd",
			raw:  json.RawMessage("{\"command\":\"echo ok\",\"cwd\":\"/tmp\\u0000x\"}"),
			want: "cwd cannot contain NUL",
		},
		{name: "zero wait", raw: json.RawMessage(`{"command":"echo ok","wait_ms":0}`), want: "wait_ms must be positive"},
		{name: "negative wait", raw: json.RawMessage(`{"command":"echo ok","wait_ms":-1}`), want: "wait_ms must be positive"},
		{name: "null wait", raw: json.RawMessage(`{"command":"echo ok","wait_ms":null}`), want: "wait_ms cannot be null"},
		{name: "huge wait", raw: json.RawMessage(`{"command":"echo ok","wait_ms":10001}`), want: "wait_ms must be at most"},
		{name: "null io mode", raw: json.RawMessage(`{"command":"echo ok","io_mode":null}`), want: "io_mode cannot be null"},
		{
			name: "unsupported io mode",
			raw:  json.RawMessage(`{"command":"echo ok","io_mode":"raw"}`),
			want: "unsupported io_mode",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveRunCommandRequest(tc.raw)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestToolCallApprovalPinsResolvedMachineBinding(t *testing.T) {
	call := model.ToolCall{
		ID:    "call_1",
		Name:  "run_command",
		Input: json.RawMessage(`{"command":"pwd"}`),
	}
	resolved, err := resolveRunCommandRequest(call.Input)
	if err != nil {
		t.Fatalf("resolve command: %v", err)
	}
	bindingID := storage.ID{1}
	authorizationInput, err := runCommandAuthorizationInput(bindingID, resolved)
	if err != nil {
		t.Fatalf("build command authorization: %v", err)
	}
	authorization, err := toolpermission.NewAuthorization(
		call.Name,
		authorizationInput,
	)
	if err != nil {
		t.Fatalf("permission authorization: %v", err)
	}
	selection := toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)
	mode, ok := toolpermission.FindMode(
		toolpermission.CommonModeDescriptors(),
		selection.Mode,
	)
	if !ok {
		t.Fatal("always_ask descriptor missing")
	}
	value, err := toolpermission.NewAllowDenyForm(
		"Permission requested for run_command",
		nil,
	)
	if err != nil {
		t.Fatalf("permission interaction form: %v", err)
	}
	request, err := toolpermission.NewRequest(
		mode,
		selection,
		authorization,
		value,
	)
	if err != nil {
		t.Fatalf("permission request: %v", err)
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal permission request: %v", err)
	}
	action := executionstore.AgentInteractionRecord{
		ToolCallID:      storage.NilID,
		ProviderCallID:  "call_1",
		InteractionKind: "permission",
		Request:         requestJSON,
	}
	if !toolCallPermissionMatches(
		action,
		call,
		storage.NilID,
		selection,
	) {
		t.Fatal("expected raw tool approval to match")
	}
	if !toolCallAuthorizationMatches(
		action,
		call,
		storage.NilID,
		selection,
		authorizationInput,
	) {
		t.Fatal("expected approved machine binding to match authorization input")
	}
	otherBindingInput, err := runCommandAuthorizationInput(storage.ID{2}, resolved)
	if err != nil {
		t.Fatalf("build command authorization for another binding: %v", err)
	}
	if toolCallAuthorizationMatches(
		action,
		call,
		storage.NilID,
		selection,
		otherBindingInput,
	) {
		t.Fatal("expected a different machine binding to reject authorization input")
	}
}
