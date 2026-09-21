package toolcatalog

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAppNamesPreserveNamespaceAndOperation(t *testing.T) {
	for _, app := range []string{"engineering", "Support-2", strings.Repeat("a", 32)} {
		name := AppToolName(app, "post__message")
		gotApp, operation, ok := SplitAppToolName(name)
		require.True(t, ok)
		require.Equal(t, app, gotApp)
		require.Equal(t, "post__message", operation)
	}
	for _, app := range []string{
		"", "1team", "support_team", "support__admin", " team", "team ", "équipe", strings.Repeat("a", 33),
	} {
		require.Error(t, ValidateAppName(app), app)
	}
	for _, name := range []string{
		"app__", "app__team", "app__team__", "app__team__bad name", "mcp__team__post", "ordinary__custom",
	} {
		_, _, ok := SplitAppToolName(name)
		require.False(t, ok, name)
	}
	require.True(t, UsesAppToolNamespace("app__"), "malformed reserved names cannot become custom tools")
	require.False(t, UsesAppToolNamespace("ordinary__custom"), "ordinary double underscores are not reserved")
}

func TestAppToolNamesRejectOverflowWithoutTruncation(t *testing.T) {
	app := strings.Repeat("a", 32)
	operation := strings.Repeat("b", 25)
	require.Len(t, AppToolName(app, operation), 64)
	require.NoError(t, ValidateAppToolName(app, operation))
	require.ErrorContains(t, ValidateAppToolName(app, operation+"b"), "exceeds 64")
	// The limit is on the whole tool name, not an arbitrary operation-length cap.
	require.NoError(t, ValidateAppToolName("a", strings.Repeat("b", 56)))
}
