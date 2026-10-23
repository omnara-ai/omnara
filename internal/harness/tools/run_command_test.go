package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/processaction"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func TestResolveRunCommandRequestKeepsShellIntentUnresolved(t *testing.T) {
	cases := []struct {
		name        string
		input       json.RawMessage
		command     string
		selector    processcmd.ShellSelector
		waitMs      int
		ioMode      processcmd.IOMode
		detach      bool
		stdin       bool
		notifyMatch string
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
			name:        "notify match",
			input:       json.RawMessage(`{"command":"npm run dev","detach":true,"notify_match":"listening|ERROR"}`),
			command:     "npm run dev",
			selector:    "default",
			ioMode:      "pipe",
			detach:      true,
			notifyMatch: "listening|ERROR",
		},
		{
			name:     "detach",
			input:    json.RawMessage(`{"command":"npm run dev","detach":true}`),
			command:  "npm run dev",
			selector: "default",
			ioMode:   "pipe",
			detach:   true,
		},
		{
			name:     "stdin",
			input:    json.RawMessage(`{"command":"python3 repl.py","stdin":true}`),
			command:  "python3 repl.py",
			selector: "default",
			ioMode:   "pipe",
			stdin:    true,
		},
		{
			name:     "max wait",
			input:    json.RawMessage(`{"command":"make test","wait_ms":300000}`),
			command:  "make test",
			selector: "default",
			waitMs:   processaction.MaxWaitMilliseconds,
			ioMode:   "pipe",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolved, err := resolveRunCommandRequest(tc.input)
			if err != nil {
				t.Fatalf("resolve run_command: %v", err)
			}
			wantWait := tc.waitMs
			if wantWait == 0 {
				wantWait = processaction.DefaultCommandWaitMilliseconds
			}
			if resolved.Command != tc.command || resolved.Selector != tc.selector ||
				resolved.WaitMs != wantWait ||
				resolved.IOMode != tc.ioMode || resolved.Detach != tc.detach || resolved.Stdin != tc.stdin ||
				resolved.NotifyMatch != tc.notifyMatch {
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
			name: "null machine ID",
			raw:  json.RawMessage(`{"command":"echo ok","machine_id":null}`),
			want: "machine_id cannot be null",
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
		{name: "huge wait", raw: json.RawMessage(`{"command":"echo ok","wait_ms":300001}`), want: "wait_ms must be at most"},
		{
			name: "invalid notify_match",
			raw:  json.RawMessage(`{"command":"npm run dev","notify_match":"(listening"}`),
			want: "notify_match is not a valid regular expression",
		},
		{
			name: "empty notify_match",
			raw:  json.RawMessage(`{"command":"npm run dev","notify_match":""}`),
			want: "notify_match must be 1 to 500 bytes",
		},
		{
			name: "NUL notify_match",
			raw:  json.RawMessage(`{"command":"npm run dev","notify_match":"listening\u0000"}`),
			want: "notify_match cannot contain NUL",
		},
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
	bindingID := uuid.UUID{1}
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
		ToolCallID:      uuid.Nil,
		ProviderCallID:  "call_1",
		InteractionKind: "permission",
		Request:         requestJSON,
	}
	if !toolCallPermissionMatches(
		action,
		call,
		uuid.Nil,
		selection,
	) {
		t.Fatal("expected raw tool approval to match")
	}
	if !toolCallAuthorizationMatches(
		action,
		call,
		uuid.Nil,
		selection,
		authorizationInput,
	) {
		t.Fatal("expected approved machine binding to match authorization input")
	}
	otherBindingInput, err := runCommandAuthorizationInput(uuid.UUID{2}, resolved)
	if err != nil {
		t.Fatalf("build command authorization for another binding: %v", err)
	}
	if toolCallAuthorizationMatches(
		action,
		call,
		uuid.Nil,
		selection,
		otherBindingInput,
	) {
		t.Fatal("expected a different machine binding to reject authorization input")
	}
}

func TestRunCommandExecutionSpecClosesPipeStdinByDefault(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		noStdin bool
	}{
		{name: "pipe", input: `{"command":"apt-get install jq"}`, noStdin: true},
		{name: "pipe with stdin", input: `{"command":"python3 repl.py","stdin":true}`, noStdin: false},
		{name: "pty", input: `{"command":"python3","io_mode":"pty"}`, noStdin: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolved, err := resolveRunCommandRequest(json.RawMessage(tc.input))
			if err != nil {
				t.Fatalf("resolve run_command: %v", err)
			}
			spec := resolved.executionSpec()
			if spec.Shell.NoStdin != tc.noStdin {
				t.Fatalf("no_stdin = %t, want %t", spec.Shell.NoStdin, tc.noStdin)
			}
			if err := spec.ValidateLocal(); err != nil {
				t.Fatalf("validate spec: %v", err)
			}
		})
	}
}

func TestProcessHasNoStdinTrustsOnlyDaemonsThatHonorIt(t *testing.T) {
	noStdin := processcmd.ForShell("apt-get install foo", processcmd.ShellDefault, processcmd.IOModePipe)
	noStdin.Shell.NoStdin = true
	withStdin := processcmd.ForShell("python3", processcmd.ShellDefault, processcmd.IOModePipe)
	for _, test := range []struct {
		name      string
		execution executionstore.ProcessExecution
		want      bool
	}{
		{name: "current daemon", execution: executionstore.ProcessExecution{Spec: noStdin, DaemonVersion: processaction.OutputWindowDaemonVersion}, want: true},
		{name: "no daemon connected", execution: executionstore.ProcessExecution{Spec: noStdin}, want: true},
		{name: "old daemon keeps stdin", execution: executionstore.ProcessExecution{Spec: noStdin, DaemonVersion: "0.2.10"}},
		{name: "stdin requested", execution: executionstore.ProcessExecution{Spec: withStdin, DaemonVersion: processaction.OutputWindowDaemonVersion}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := processHasNoStdin(test.execution); got != test.want {
				t.Fatalf("processHasNoStdin = %t, want %t", got, test.want)
			}
		})
	}
}
