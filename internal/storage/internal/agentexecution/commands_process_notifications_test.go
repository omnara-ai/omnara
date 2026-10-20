package agentexecution

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
)

func TestRenderProcessExit(t *testing.T) {
	started := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	ended := started.Add(4*time.Minute + 12*time.Second)
	exitCode := 1
	exited := executiondb.ReadExecutionProcessOwnerRow{
		State:           "exited",
		ExitCode:        new(int32(exitCode)),
		ExecutionSpec:   processcmd.ForShell("npm test", "sh", ""),
		SourceStartedAt: &started,
		SourceEndedAt:   &ended,
	}
	var manyLines strings.Builder
	for i := range 30 {
		fmt.Fprintf(&manyLines, "line %d\n", i)
	}
	tests := []struct {
		name     string
		row      executiondb.ReadExecutionProcessOwnerRow
		observed string
		want     []string
		notWant  []string
	}{
		{
			name:     "complete output",
			row:      exited,
			observed: `{"output":"ok\nFAIL x\n","next_cursor":10,"truncated":false}`,
			want: []string{
				"Background process prc_test (`npm test`) exited with code 1 after 4m12s. Output: 10 bytes.\n\n```\nok\nFAIL x\n```",
			},
			notWant: []string{"read_process"},
		},
		{
			name: "last lines only",
			row:  exited,
			observed: mustJSON(t, map[string]any{
				"output": manyLines.String(), "next_cursor": manyLines.Len(), "truncated": false,
			}),
			want:    []string{"Last lines (use read_process for the rest)", "line 10\n", "line 29\n```"},
			notWant: []string{"line 9\n"},
		},
		{
			name:     "more output than observed",
			row:      exited,
			observed: `{"output":"head","next_cursor":200000,"truncated":true}`,
			want:     []string{"Output: 195.3 KiB. Use read_process to read it."},
			notWant:  []string{"head"},
		},
		{
			name:     "no output",
			row:      exited,
			observed: `{"output":"","next_cursor":0,"truncated":false}`,
			want:     []string{"It produced no output."},
		},
		{
			name: "killed without observation",
			row: executiondb.ReadExecutionProcessOwnerRow{
				State: "killed", ExitSignal: "SIGKILL", ExecutionSpec: processcmd.ForShell("make", "sh", ""),
			},
			want: []string{"(`make`) was killed by SIGKILL. Use read_process to read its output."},
		},
		{
			name:     "fence longer than output backticks",
			row:      exited,
			observed: "{\"output\":\"```code```\",\"next_cursor\":12,\"truncated\":false}",
			want:     []string{"````\n```code```\n````"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := renderProcessExit("prc_test", test.row, json.RawMessage(test.observed))
			for _, want := range test.want {
				if !strings.Contains(got, want) {
					t.Fatalf("render =\n%s\nwant substring %q", got, want)
				}
			}
			for _, notWant := range test.notWant {
				if strings.Contains(got, notWant) {
					t.Fatalf("render =\n%s\nunexpected substring %q", got, notWant)
				}
			}
		})
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
