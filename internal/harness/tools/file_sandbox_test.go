package tools

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	log "github.com/omnara-ai/omnara/observability/wideevent"
	"github.com/stretchr/testify/require"
)

func TestFileToolSandboxFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses shell fixtures")
	}
	for _, test := range []struct {
		name, script, diagnostic string
		unavailable              bool
	}{
		{name: "missing executable", unavailable: true},
		{name: "setup failure", script: "printf 'Landlock failed: private host detail' >&2; exit 125", unavailable: true},
		{name: "syntax error", script: "printf 'invalid expression' >&2; exit 2", diagnostic: "invalid expression"},
		{name: "script exit", script: "exit 125", diagnostic: "exit status 125"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "rg"), []byte("#!/bin/sh\nexit 0\n"), 0700))
			if test.script != "" {
				for _, name := range []string{"omnara-sandbox", "omnara-chroot-sandbox"} {
					require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+test.script+"\n"), 0700))
				}
			}
			for _, operation := range []string{"search", "edit"} {
				var logs bytes.Buffer
				ctx := log.WithLogger(t.Context(), slog.New(slog.NewJSONHandler(&logs, nil)))
				var err error
				if operation == "edit" {
					_, err = editFileText(ctx, []byte("x"), "d")
				} else {
					output := searchOutput{input: searchRequestForTest(t, []string{"-e", "x"}, 10)}
					err = output.search(ctx, searchSource{path: "/artifacts/test", content: []byte("x")})
				}
				require.Error(t, err)
				require.Equal(t, test.unavailable, errors.Is(err, errFileToolUnavailable), "%s: %v", operation, err)
				if test.unavailable {
					if operation == "edit" {
						require.EqualError(t, err, scriptEditUnavailableMessage)
					} else {
						require.EqualError(t, err, fileSearchUnavailableMessage)
					}
					require.Contains(t, logs.String(), "worker.file_tools_unavailable")
					require.NotContains(t, err.Error(), "Landlock")
					if test.script != "" {
						require.Contains(t, logs.String(), "private host detail")
					}
				} else {
					require.Contains(t, err.Error(), test.diagnostic)
					require.Empty(t, logs.String())
				}
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := editFileText(ctx, nil, "d")
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, strings.Contains(err.Error(), "unavailable"))
}
