package main

import (
	"bytes"
	"io"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/stretchr/testify/require"
)

func TestInboxCommandValidationAndHelp(t *testing.T) {
	t.Parallel()
	project, err := publicid.Encode(publicid.KindProject, uuid.New())
	require.NoError(t, err)
	for _, args := range [][]string{
		{}, {"delete"}, {"list"}, {"list", "--project", project, "--limit", "101"},
		{"list", "--project", project, "--after", "broken"}, {"list", "--project", project, "--state", "other"},
		{"discard", "--project", project, "--receipt", uuid.NewString()},
		{"retry", "--project", project, "--receipt", uuid.Nil.String()},
		{"show", "--project", project, "--receipt", uuid.NewString(), "unexpected"},
	} {
		_, err := parseInboxCommand(args, io.Discard)
		require.Error(t, err, "args=%v", args)
	}
	var help bytes.Buffer
	require.NoError(t, runInboxCLI(t.Context(), []string{"inbox", "discard", "--help"}, io.Discard, &help))
	require.Contains(t, help.String(), "-reason")
	require.Contains(t, help.String(), "-receipt")
}
