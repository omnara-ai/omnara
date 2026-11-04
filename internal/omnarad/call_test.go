package omnarad

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCallCommandRequiresRunCommandEnvironment(t *testing.T) {
	t.Setenv("OMNARA_PROCESS_ID", "")
	t.Setenv("OMNARA_TOOL_ENDPOINT", "")
	t.Setenv("OMNARA_TOOL_CAPABILITY", "")
	var stdout, stderr bytes.Buffer
	code := Run(t.Context(), []string{"call", "storage_list", `{"prefix":"a"}`, "--json"}, nil,
		&stdout, &stderr, discardLogger())
	require.Equal(t, 2, code)
	require.Empty(t, stdout.String())
	require.Equal(t, "omnarad call works only inside an Omnara run_command process "+
		"on a machine with programmatic tool calls enabled\n", stderr.String())
}

func TestCallCommandUsageErrorsExitTwo(t *testing.T) {
	for _, args := range [][]string{{"call"}, {"call", "storage_list", "--timeout", "soon"}} {
		var stdout, stderr bytes.Buffer
		code := Run(t.Context(), args, nil, &stdout, &stderr, discardLogger())
		require.Equal(t, 2, code, args)
		require.Contains(t, stderr.String(), "error:", args)
	}
}

func TestCallCommandIsListedInHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	require.Equal(t, 0, Run(t.Context(), []string{"--help"}, nil, &stdout, &stderr, discardLogger()))
	require.Contains(t, stdout.String(), "call")
	require.NotContains(t, stdout.String(), "__omnara_git_credential")
	stdout.Reset()
	require.Equal(t, 0, Run(t.Context(), []string{"call", "--help"}, nil, &stdout, &stderr, discardLogger()))
	require.Contains(t, stdout.String(), "--timeout")
	require.Contains(t, stdout.String(), "--json")
}
