package tools

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/listing"
)

func TestMachineFileToolsRouteByPathAndMachineID(t *testing.T) {
	const machine = "mch_aaaaaaaaaaaaaaaaaaaaaaaaae"
	for _, test := range []struct {
		name    string
		resolve machineFileResolver
		input   string
		kind    processcmd.ExecutionKind
		invalid string
	}{
		{name: "read memory", resolve: machineReadFile, input: `{"path":"/memory/team/a.md"}`},
		{name: "read machine", resolve: machineReadFile, input: `{"path":"a.md"}`, kind: processcmd.KindFileRead},
		{name: "write memory", resolve: machineWriteFile, input: `{"path":"/memory/team/a.md","content":""}`},
		{
			name: "write machine id", resolve: machineWriteFile, kind: processcmd.KindFileWrite,
			input: `{"path":"/memory/team/a.md","content":"","machine_id":"` + machine + `"}`,
		},
		{
			name: "write machine script", resolve: machineWriteFile, invalid: "script only applies to memory files",
			input: `{"path":"a.md","script":"s/a/b/"}`,
		},
		{
			name: "write machine size", resolve: machineWriteFile, invalid: "at most 512 KiB",
			input: `{"path":"a.md","content":"` + strings.Repeat("x", maxMachineWriteBytes+1) + `"}`,
		},
		{name: "list artifacts", resolve: machineListFiles, input: `{"pattern":"/artifacts/*.png"}`},
		{name: "list machine", resolve: machineListFiles, input: `{"pattern":"~/*.png"}`, kind: processcmd.KindFileList},
		{name: "list machine glob", resolve: machineListFiles, input: `{"pattern":"[*"}`, invalid: "invalid glob"},
		{name: "search memory", resolve: machineSearchFiles, input: `{"path":"/memory/team/*","args":["-e","x"]}`},
		{
			name: "search machine", resolve: machineSearchFiles, kind: processcmd.KindFileSearch,
			input: `{"path":"src/**/*.go","args":["-e","x"]}`,
		},
		{
			name: "search machine regex", resolve: machineSearchFiles, invalid: "invalid search pattern",
			input: `{"path":"a.go","args":["-e","(?<=x)"]}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			call, onMachine, err := test.resolve(json.RawMessage(test.input))
			if test.invalid != "" {
				if err == nil || !strings.Contains(err.Error(), test.invalid) {
					t.Fatalf("error = %v, want %q", err, test.invalid)
				}
				return
			}
			if err != nil || onMachine != (test.kind != "") || call.kind != test.kind {
				t.Fatalf("call = %+v machine=%t err=%v, want kind %q", call, onMachine, err, test.kind)
			}
		})
	}
}

func TestMachineFileCallPrepare(t *testing.T) {
	turn := Turn{ProjectID: uuid.New(), AgentID: uuid.New()}
	binding := executionstore.AgentMachineBindingRecord{MachineID: uuid.New(), DaemonVersion: "0.2.5"}
	read := machineFileCall{kind: processcmd.KindFileRead, operation: processcmd.FileOperation{Path: "a"}}
	if err := read.prepare(turn, binding); err == nil || !strings.Contains(err.Error(), "update omnarad to 0.2.6") {
		t.Fatalf("old daemon error = %v", err)
	}
	for _, version := range []string{"", processcmd.FileOperationDaemonVersion} {
		binding.DaemonVersion = version
		if err := read.prepare(turn, binding); err != nil {
			t.Fatalf("daemon %q: %v", version, err)
		}
	}

	list := machineFileCall{kind: processcmd.KindFileList, operation: processcmd.FileOperation{Path: "*.go"}}
	if err := list.prepare(turn, binding); err != nil || list.operation.CursorScope == "" {
		t.Fatalf("list scope = %q, %v", list.operation.CursorScope, err)
	}
	cursor, err := listing.FileCursor{
		Version: listing.FileCursorVersion, Scope: list.operation.CursorScope, Key: "b.go",
	}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	next := list
	next.cursor = cursor
	if err := next.prepare(turn, binding); err != nil || next.operation.After != "b.go" {
		t.Fatalf("continued listing after %q: %v", next.operation.After, err)
	}
	otherMachine := binding
	otherMachine.MachineID = uuid.New()
	next.operation.After = ""
	if err := next.prepare(turn, otherMachine); !errors.Is(err, errInvalidFileListCursor) {
		t.Fatalf("cursor from another machine: %v", err)
	}
}
