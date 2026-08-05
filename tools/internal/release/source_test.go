package release

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateCommit(t *testing.T) {
	t.Parallel()

	if err := ValidateCommit(strings.Repeat("a", 40)); err != nil {
		t.Fatalf("validate commit: %v", err)
	}
	for _, commit := range []string{"", strings.Repeat("a", 39), strings.Repeat("A", 40), strings.Repeat("g", 40)} {
		if err := ValidateCommit(commit); err == nil {
			t.Fatalf("ValidateCommit(%q) succeeded", commit)
		}
	}
}

func TestAppendGitHubOutputs(t *testing.T) {
	t.Parallel()

	output := filepath.Join(t.TempDir(), "github-output")
	if err := os.WriteFile(output, []byte("existing=value\n"), 0o600); err != nil {
		t.Fatalf("write output: %v", err)
	}
	if err := AppendGitHubOutputs(
		output,
		Output{Name: "version", Value: "v1.2.3"},
		Output{Name: "source_commit", Value: strings.Repeat("a", 40)},
	); err != nil {
		t.Fatalf("append outputs: %v", err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	want := "existing=value\nversion=v1.2.3\nsource_commit=" + strings.Repeat("a", 40) + "\n"
	if string(data) != want {
		t.Fatalf("outputs = %q, want %q", data, want)
	}
}

func TestAppendGitHubOutputsRejectsUnsafeValues(t *testing.T) {
	t.Parallel()

	output := filepath.Join(t.TempDir(), "github-output")
	if err := os.WriteFile(output, nil, 0o600); err != nil {
		t.Fatalf("write output: %v", err)
	}
	for _, outputs := range [][]Output{
		{{Name: "bad-name", Value: "value"}},
		{
			{Name: "version", Value: "v1.2.3"},
			{Name: "source_commit", Value: "abc\nother=value"},
		},
	} {
		if err := AppendGitHubOutputs(output, outputs...); err == nil {
			t.Fatalf("AppendGitHubOutputs(%+v) succeeded", outputs)
		}
		data, err := os.ReadFile(output)
		if err != nil {
			t.Fatalf("read output: %v", err)
		}
		if len(data) != 0 {
			t.Fatalf("output changed to %q", data)
		}
	}
}

func TestResolveSourceCommit(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	runGitTest(t, directory, "init", "--initial-branch=main")
	runGitTest(t, directory, "config", "user.email", "release@example.com")
	runGitTest(t, directory, "config", "user.name", "Release Test")
	if err := os.WriteFile(filepath.Join(directory, "file"), []byte("main"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGitTest(t, directory, "add", "file")
	runGitTest(t, directory, "commit", "-m", "main")

	mainCommit := gitOutputTest(t, directory, "rev-parse", "HEAD")
	got, err := ResolveSourceCommit(context.Background(), directory, "refs/heads/main")
	if err != nil {
		t.Fatalf("resolve main source: %v", err)
	}
	if got != mainCommit {
		t.Fatalf("source commit = %q, want %q", got, mainCommit)
	}

	runGitTest(t, directory, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(directory, "file"), []byte("feature"), 0o600); err != nil {
		t.Fatalf("write feature file: %v", err)
	}
	runGitTest(t, directory, "add", "file")
	runGitTest(t, directory, "commit", "-m", "feature")
	if _, err := ResolveSourceCommit(context.Background(), directory, "refs/heads/main"); err == nil ||
		!strings.Contains(err.Error(), "must belong to main") {
		t.Fatalf("resolve feature source error = %v", err)
	}

	missingRef := "refs/heads/missing"
	if _, err := ResolveSourceCommit(context.Background(), directory, missingRef); err == nil ||
		!strings.Contains(err.Error(), missingRef) {
		t.Fatalf("resolve missing main ref error = %v", err)
	}
}

func runGitTest(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func gitOutputTest(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output))
}
