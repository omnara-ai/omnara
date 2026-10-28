package agentconfig

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSourceRevisionChangesEndAtCurrentRevision(t *testing.T) {
	for index, change := range sourceRevisionChanges {
		require.Equal(t, index+2, change.Revision, "revisions must be contiguous from 2")
		require.NotEmpty(t, change.Notice)
	}
	require.Equal(t, CurrentSourceRevision, len(sourceRevisionChanges)+1)
}

func TestSourceNotices(t *testing.T) {
	changes := []SourceRevisionChange{
		{Revision: 2, Notice: "every source"},
		{Revision: 3, Notice: "renamed old_tool", Affects: func(source string) bool {
			return strings.Contains(source, "old_tool")
		}},
	}
	for name, tc := range map[string]struct {
		revision int
		source   string
		want     []string
	}{
		"current source":                  {revision: 3, source: "tools:\n  old_tool: {}\n"},
		"sourceless":                      {revision: 1},
		"older source using old_tool":     {revision: 1, source: "tools:\n  old_tool: {}\n", want: []string{"every source", "renamed old_tool"}},
		"older source without old_tool":   {revision: 1, source: "instruction: hi\n", want: []string{"every source"}},
		"one revision behind":             {revision: 2, source: "tools:\n  old_tool: {}\n", want: []string{"renamed old_tool"}},
		"one revision behind, unaffected": {revision: 2, source: "instruction: hi\n"},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, sourceNotices(changes, tc.revision, tc.source))
		})
	}
}
