package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/processresult"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/stretchr/testify/require"
)

func TestStructuredToolResultKeepsEmptyObject(t *testing.T) {
	parts, err := ToolResultContentParts(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("convert empty object tool result: %v", err)
	}
	if string(parts) != `[{"type":"structured_data","value":{}}]` {
		t.Fatalf("empty object tool result = %s", parts)
	}
}

func TestCommandTerminalToolResultUsesCanonicalProcessID(t *testing.T) {
	processID := uuid.MustParse("019c0000-0000-7000-8000-000000000001")
	publicProcessID := publicResourceID(publicid.KindProcess, processID)
	tests := []struct {
		name     string
		input    json.RawMessage
		want     string
		wantFail bool
	}{
		{
			name:  "object",
			input: json.RawMessage(`{"done":true,"process_id":"prc_should_not_escape"}`),
			want:  `{"done":true,"process_id":"` + publicProcessID + `"}`,
		},
		{
			name:  "empty object",
			input: json.RawMessage(`{}`),
			want:  `{"process_id":"` + publicProcessID + `"}`,
		},
		{name: "array", input: json.RawMessage(`["done"]`), wantFail: true},
		{name: "string", input: json.RawMessage(`"done"`), wantFail: true},
		{name: "invalid", input: json.RawMessage(`{"done":true`), wantFail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := commandTerminalToolResult(processID, tt.input)
			if tt.wantFail {
				if err == nil {
					t.Fatalf("commandTerminalToolResult succeeded, want failure")
				}
				return
			}
			if err != nil {
				t.Fatalf("commandTerminalToolResult: %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("result = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestStartedProcessToolResultKeepsProcessFactsAuthoritative(t *testing.T) {
	process := ProcessRecord{
		ExecutionSpec: processcmd.ForShell("go test ./...", "", ""),
		ID:            uuid.New(),
		State:         ProcessStateRunning,
	}
	result, err := processresult.Started(
		process.resultFacts(),
		json.RawMessage(`{"state":"exited","command":"other","next_action":"stop","output":"ready"}`),
	)
	if err != nil {
		t.Fatalf("startedProcessToolResult: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(result, &body); err != nil {
		t.Fatalf("decode started process result: %v", err)
	}
	if body["state"] != string(ProcessStateRunning) ||
		body["command"] != process.ExecutionSpec.Shell.Command ||
		body["next_action"] == "stop" ||
		body["output"] != "ready" {
		t.Fatalf("started process result = %s", result)
	}
}

func TestFileTransferToolResultContentParts(t *testing.T) {
	const path = "/memory/team/notes.md"
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, direction := range []processcmd.FileTransferDirection{
		processcmd.FileTransferUpload, processcmd.FileTransferDownload,
	} {
		t.Run(string(direction), func(t *testing.T) {
			metadata := `{"digest":"` + digest + `"}`
			if direction == processcmd.FileTransferUpload {
				metadata = `{"path":"` + path + `","digest":"` + digest + `"}`
			}
			for _, tc := range []struct {
				name, metadata, output string
				wantOK                 bool
			}{
				{name: "metadata without output", metadata: metadata, wantOK: true},
				{name: "metadata with warnings", metadata: metadata, output: "warning on stderr\n", wantOK: true},
				{name: "missing metadata", output: metadata},
				{name: "null metadata", metadata: "null", output: metadata},
				{name: "invalid metadata", metadata: `"not metadata"`, output: metadata},
				{name: "missing digest", metadata: `{"path":"` + path + `"}`},
				{name: "invalid digest", metadata: strings.Replace(metadata, digest, "invalid", 1)},
				{name: "error on success", metadata: `{"error":{"code":"file_transfer_failed","error":"missing file"}}`},
				{name: "wrong path", metadata: `{"path":"/memory/team/other.md","digest":"` + digest + `"}`},
			} {
				t.Run(tc.name, func(t *testing.T) {
					observed := map[string]any{
						"output":    tc.output,
						"truncated": true,
						"state":     "exited",
						"done":      true,
					}
					if tc.metadata != "" {
						observed["file_transfer"] = json.RawMessage(tc.metadata)
					}
					result, err := json.Marshal(observed)
					require.NoError(t, err)
					exitCode := 0
					process := ProcessRecord{
						ExecutionSpec: processcmd.ForFileTransfer(processcmd.FileTransfer{
							Direction: direction,
							Target: processcmd.FileTarget{
								Memory: &processcmd.MemoryTarget{StoreID: uuid.New(), Path: "notes.md"},
							},
						}),
						State:    ProcessStateExited,
						ExitCode: &exitCode,
					}
					outcome, got, err := fileTransferToolResultContentParts(
						context.Background(), dbsqlc.New(memoryTransferNameDB{}), process, result,
					)
					require.NoError(t, err)
					if tc.wantOK {
						require.Equal(t, ToolResultOutcomeSucceeded, outcome)
						require.JSONEq(t, `[{"type":"structured_data","value":`+metadata+`}]`, string(got))
					} else {
						delete(observed, "file_transfer")
						observed["error"] = direction + " completed without valid file metadata"
						want, err := json.Marshal(observed)
						require.NoError(t, err)
						require.Equal(t, ToolResultOutcomeFailed, outcome)
						require.JSONEq(t, `[{"type":"structured_data","value":`+string(want)+`}]`, string(got))
					}
				})
			}
		})
	}
}

func TestFileTransferFailureResult(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	failure := `{"code":"file_content_conflict","error":"file changed","current_digest":"` + digest + `"}`
	for _, tc := range []struct {
		name, reason, metadata string
		state                  ProcessState
		exitCode               int
		wantError              bool
	}{
		{name: "api failure", state: ProcessStateFailed, reason: "nonzero_exit", exitCode: 1,
			metadata: failure, wantError: true},
		{name: "local failure", state: ProcessStateFailed, reason: "nonzero_exit", exitCode: 1,
			metadata: `{"code":"file_transfer_failed","error":"missing file"}`, wantError: true},
		{name: "exited nonzero", state: ProcessStateExited, exitCode: 1, metadata: failure, wantError: true},
		{name: "timeout wins", state: ProcessStateFailed, reason: "timeout", exitCode: 1, metadata: failure},
		{name: "signal wins", state: ProcessStateFailed, reason: "signal", exitCode: -1, metadata: failure},
		{name: "invalid error", state: ProcessStateFailed, reason: "nonzero_exit", exitCode: 1, metadata: `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			process := ProcessRecord{
				ExecutionSpec: processcmd.ForFileTransfer(
					processcmd.FileTransfer{Direction: processcmd.FileTransferUpload},
				),
				State:           tc.state,
				StateReasonCode: tc.reason,
				ExitCode:        &tc.exitCode,
			}
			result := `{"output":"diagnostics", "file_transfer":{"error":` + tc.metadata + `}}`
			outcome, got, err := fileTransferToolResultContentParts(
				context.Background(), nil, process, json.RawMessage(result),
			)
			require.NoError(t, err)
			require.Equal(t, ToolResultOutcomeFailed, outcome)
			want := result
			if tc.wantError {
				var fields map[string]any
				require.NoError(t, json.Unmarshal([]byte(tc.metadata), &fields))
				fields["error_code"] = fields["code"]
				delete(fields, "code")
				fields["output"] = "diagnostics"
				body, err := json.Marshal(fields)
				require.NoError(t, err)
				want = string(body)
			}
			require.JSONEq(t, `[{"type":"structured_data","value":`+want+`}]`, string(got))
		})
	}
}

type memoryTransferNameDB struct{ dbsqlc.DBTX }

func (memoryTransferNameDB) QueryRow(context.Context, string, ...any) pgx.Row {
	return memoryTransferNameRow{}
}

type memoryTransferNameRow struct{}

func (memoryTransferNameRow) Scan(dest ...any) error {
	name, ok := dest[0].(*string)
	if !ok {
		return errors.New("expected memory store name destination")
	}
	*name = "team"
	return nil
}
