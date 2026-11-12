package modelcontext

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const fakeOmnarad = `#!/bin/sh
if [ "$2" = storage_list ]; then
  echo '{"entries":[{"path":"/memory/notes/good one.md"},{"path":"/memory/notes/bad.md"}]}'
  exit 0
fi
input=$(cat)
touch "$FAKE_DIR/started.$$"
while [ ! -e "$FAKE_DIR/release" ]; do sleep 0.05; done
case "$input" in *bad.md*) exit 1 ;; esac
printf '%s\n' "$input"
`

func TestProgrammaticParallelExampleWaitsForItsCalls(t *testing.T) {
	for _, tool := range []string{"sh", "jq", "xargs", "tr"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "omnarad")
	if err := os.WriteFile(bin, []byte(fakeOmnarad), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "sh", "-c", programmaticParallelExample)
	command.Dir = dir
	command.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_DIR="+dir)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()

	deadline := time.After(10 * time.Second)
	for {
		started, err := filepath.Glob(filepath.Join(dir, "started.*"))
		if err != nil {
			t.Fatal(err)
		}
		if len(started) == 2 {
			break
		}
		select {
		case err := <-exited:
			t.Fatalf("command exited before its calls started: %v", err)
		case <-deadline:
			t.Fatalf("calls did not start in parallel: %d started", len(started))
		case <-time.After(20 * time.Millisecond):
		}
	}
	select {
	case err := <-exited:
		t.Fatalf("command exited while its calls were still running: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-exited; err == nil {
		t.Fatal("command succeeded although a call failed")
	}
	result, err := os.ReadFile(filepath.Join(dir, "good one.md.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(result), "{\"path\":\"/memory/notes/good one.md\"}\n"; got != want {
		t.Fatalf("result = %q, want %q", got, want)
	}
}
