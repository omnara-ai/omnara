package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/stretchr/testify/require"
)

func requireNativeFileTools(t *testing.T) {
	t.Helper()
	for _, name := range []string{"rg", "sed"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatalf("Windows file-tool tests require %s on PATH: %v", name, err)
		}
	}
	buildSandboxLauncher(t)
}

func TestWindowsFileTools(t *testing.T) {
	requireNativeFileTools(t)
	require.NoError(t, CheckFileSearchSupport(t.Context()))
	require.NoError(t, CheckScriptEditSupport(t.Context()))
	for _, test := range []struct{ name, script, content, want string }{
		{"unicode", "s/./x/g", "é😀\n", "xx\n"},
		{"CRLF", "s/a/b/", "a\r\n", "b\r\n"},
		{"large script", "#" + strings.Repeat("x", 40000) + "\ns/a/b/", "a", "b"},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := editFileText(t.Context(), []byte(test.content), test.script)
			require.NoError(t, err)
			require.Equal(t, test.want, string(output))
		})
	}
	for _, script := range []string{"e echo forbidden", "r forbidden.txt", "w forbidden.txt", "s/a/echo forbidden/e"} {
		t.Run(script, func(t *testing.T) {
			_, err := editFileText(t.Context(), []byte("a"), script)
			require.ErrorContains(t, err, "sandbox")
		})
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err := editFileText(ctx, []byte("a"), ":again;b again")
	require.Error(t, err)
	require.True(t, errors.Is(err, context.DeadlineExceeded), "%v", err)
}

func TestWindowsScriptFileCleanup(t *testing.T) {
	requireNativeFileTools(t)
	dir := t.TempDir()
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
	for _, script := range []string{"s/a/b/", "s/[//"} {
		output, err := editFileText(t.Context(), []byte("a"), script)
		if script == "s/a/b/" {
			require.NoError(t, err)
			require.Equal(t, "b", string(output))
		} else {
			require.ErrorContains(t, err, "script execution failed")
			require.NotContains(t, err.Error(), dir)
		}
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		require.Empty(t, entries)
	}
}

func TestWindowsScriptMemoryLimit(t *testing.T) {
	requireNativeFileTools(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := editFileText(ctx, []byte(strings.Repeat("x", 1024)), ":again;h;G;b again")
	require.Error(t, err)
	require.NotErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "memory")
	output, err := editFileText(t.Context(), []byte("a"), "s/a/b/")
	require.NoError(t, err)
	require.Equal(t, "b", string(output))
}

func TestWindowsScriptSupportCanRecover(t *testing.T) {
	requireNativeFileTools(t)
	toolPath := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir())
	require.Error(t, CheckScriptEditSupport(t.Context()))
	call := asyncToolContext{Call: model.ToolCall{
		Input: json.RawMessage(`{"path":"/memory/notes/a.txt","script":"s/a/b/"}`),
	}}
	_, err := runStorageWriteAsync(t.Context(), call)
	require.ErrorIs(t, err, errFileToolUnavailable)
	cause := errors.New("caller canceled the write")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	_, err = runStorageWriteAsync(ctx, call)
	require.ErrorIs(t, err, context.Canceled)
	t.Setenv("PATH", toolPath)
	require.NoError(t, CheckScriptEditSupport(t.Context()))
	output, err := editFileText(t.Context(), []byte("a"), "s/a/b/")
	require.NoError(t, err)
	require.Equal(t, "b", string(output))
}

func TestWindowsScriptRejectsIncorrectTextHandling(t *testing.T) {
	requireNativeFileTools(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "main.go")
	require.NoError(t, os.WriteFile(source, []byte(`package main

import (
    "fmt"
    "os"
    "path/filepath"
)

func main() {
    executable, err := os.Executable()
    if err != nil { panic(err) }
    output := map[string]string{
        "unicode": "xxxxxx\nb\r\n",
        "lf": "xx\r\nb\r\n",
        "crlf": "xx\nb\n",
    }[filepath.Base(filepath.Dir(executable))]
    fmt.Print(output)
}
`), 0600))
	binary := filepath.Join(dir, "sed.exe")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, source)
	output, err := build.CombinedOutput()
	require.NoError(t, err, "%s", output)
	contents, err := os.ReadFile(binary)
	require.NoError(t, err)
	for _, name := range []string{"unicode", "lf", "crlf"} {
		t.Run(name, func(t *testing.T) {
			fixture := filepath.Join(dir, name)
			require.NoError(t, os.Mkdir(fixture, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(fixture, "sed.exe"), contents, 0700))
			t.Setenv("PATH", fixture+string(os.PathListSeparator)+os.Getenv("PATH"))
			require.EqualError(t, CheckScriptEditSupport(t.Context()),
				"scripted editing requires a working C.UTF-8 locale and binary I/O")
			call := asyncToolContext{Call: model.ToolCall{
				Input: json.RawMessage(`{"path":"/memory/notes/a.txt","script":"s/a/b/"}`),
			}}
			_, err := runStorageWriteAsync(t.Context(), call)
			require.ErrorIs(t, err, errFileToolUnavailable)
		})
	}
}
