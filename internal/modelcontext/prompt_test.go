package modelcontext

import (
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestDefaultSystemPromptUsesProductVocabulary(t *testing.T) {
	prompt := DefaultSystemPrompt()
	for _, forbidden := range []string{"owned coding-agent harness", "owned harness", "runtime authority"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("system prompt leaked implementation vocabulary %q: %s", forbidden, prompt)
		}
	}
	if !strings.Contains(prompt, "autonomous agent") {
		t.Fatalf("system prompt missing product identity: %s", prompt)
	}
	for _, forbidden := range []string{"run_command", "write_process", "read_process", "stop_process", "machine"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("system prompt claimed unavailable capability %q: %s", forbidden, prompt)
		}
	}
}

func TestCapabilityGuidanceNamesOnlyEnabledTools(t *testing.T) {
	tests := []struct {
		name      string
		specs     []ToolSpec
		want      []string
		forbidden []string
	}{
		{
			name:      "no tools",
			forbidden: []string{"run_command", "write_process", "read_process", "stop_process", "machine"},
		},
		{
			name:      "command only",
			specs:     []ToolSpec{{Name: toolcatalog.ToolNameRunCommand}},
			want:      []string{"daemon-backed machine", "run_command"},
			forbidden: []string{"write_process", "read_process", "stop_process"},
		},
		{
			name: "process controls",
			specs: []ToolSpec{
				{Name: toolcatalog.ToolNameWriteProcess},
				{Name: toolcatalog.ToolNameReadProcess},
				{Name: toolcatalog.ToolNameStopProcess},
			},
			want:      []string{"write_process", "read_process", "stop_process"},
			forbidden: []string{"run_command", "daemon-backed machine"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prompt := capabilityGuidance(test.specs)
			for _, want := range test.want {
				if !strings.Contains(prompt, want) {
					t.Fatalf("capability guidance missing %q: %s", want, prompt)
				}
			}
			for _, forbidden := range test.forbidden {
				if strings.Contains(prompt, forbidden) {
					t.Fatalf("capability guidance claimed unavailable capability %q: %s", forbidden, prompt)
				}
			}
		})
	}
}
