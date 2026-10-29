package agentexecution

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
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
			name: "head and tail",
			row:  exited,
			observed: `{"output":"start\n","omitted_cursor":2048,"tail_cursor":180000,` +
				`"output_tail":"partial line\nlast line\n","next_cursor":183000,"truncated":false}`,
			want: []string{
				"Output: 178.7 KiB. Last lines (use read_process for the rest):\n\n```\npartial line\nlast line\n```",
			},
			notWant: []string{"start"},
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

func TestRenderProcessOutputMatch(t *testing.T) {
	row := executiondb.ReadExecutionProcessOwnerRow{
		ExecutionSpec: processcmd.ForShell("npm run dev", "sh", ""),
	}
	got := renderProcessOutputMatch("prc_test", row, daemonprotocol.OutputMatchResult{
		Pattern: "listening|ERROR", Lines: []string{"listening on :3000", "ERROR boom"}, Dropped: 2,
	})
	want := "Background process prc_test (`npm run dev`) printed lines matching `listening|ERROR`:\n\n" +
		"```\nlistening on :3000\nERROR boom\n```\n\n" +
		"2 more matching lines are not shown; use read_process to read them."
	if got != want {
		t.Fatalf("render =\n%s\nwant\n%s", got, want)
	}
}
