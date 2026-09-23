package tools

import (
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestEditFileTextMissingExecutable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := editFileText(t.Context(), nil, "")
	if !errors.Is(err, exec.ErrNotFound) || !strings.Contains(err.Error(), "start script execution:") {
		t.Fatalf("missing executable: %v", err)
	}
}

func TestWriteFileInput(t *testing.T) {
	tool, ok, err := toolImplementationFor(toolcatalog.ToolNameWriteFile)
	if err != nil || !ok {
		t.Fatalf("write_file registration: %v", err)
	}
	for _, test := range []struct {
		input string
		valid bool
	}{
		{`{"path":"/memory/notes/a.txt","content":""}`, true},
		{`{"path":"/memory/notes/a.txt","content":"é😀","append":true}`, true},
		{`{"path":"/memory/notes/a.txt","script":""}`, true},
		{`{"path":"/memory/notes/a.txt","content":"x","expected_digest":"sha256:` + strings.Repeat("a", 64) + `"}`, true},
		{`{"path":"/memory/notes/a.txt"}`, false},
		{`{"path":"/memory/notes/a.txt","content":null}`, false},
		{`{"path":"/memory/notes/a.txt","content":"","script":""}`, false},
		{`{"path":"/memory/notes/a.txt","script":"d","append":true}`, false},
		{`{"path":"/memory/notes/a.txt","content":"x","expected_digest":null}`, false},
		{`{"path":"/memory/notes/a.txt","content":"x","expected_digest":"bad"}`, false},
		{`{"path":"/memory/notes/a.txt","content":"\u0000"}`, false},
		{`{"path":"/memory/notes/a.txt","script":"\u0000"}`, false},
		{`{"path":"/artifacts","content":"x"}`, false},
		{`{"path":"/memory/notes/../a.txt","content":"x"}`, false},
		{`{"path":"/memory/notes/*.txt","content":"x"}`, false},
		{`{"path":"/memory/notes/a.txt","content":"x","extra":true}`, false},
	} {
		if err := tool.validateInput(json.RawMessage(test.input)); (err == nil) != test.valid {
			t.Errorf("input %s: %v", test.input, err)
		}
	}
	for field, limit := range map[string]int{"content": daemonprotocol.MaxFileTransferBytes, "script": 64 * 1024} {
		raw, err := json.Marshal(map[string]any{"path": "/memory/notes/a.txt", field: strings.Repeat("x", limit+1)})
		if err != nil {
			t.Fatal(err)
		}
		if err := tool.validateInput(raw); err == nil {
			t.Errorf("oversized %s accepted", field)
		}
	}
}
