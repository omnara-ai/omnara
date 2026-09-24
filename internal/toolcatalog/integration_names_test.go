package toolcatalog

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIntegrationNamesPreserveNamespaceAndOperation(t *testing.T) {
	for _, integration := range []string{"engineering", "Support-2", strings.Repeat("a", 32)} {
		name := IntegrationToolName(integration, "post__message")
		gotIntegration, operation, ok := SplitIntegrationToolName(name)
		require.True(t, ok)
		require.Equal(t, integration, gotIntegration)
		require.Equal(t, "post__message", operation)
	}
	for _, integration := range []string{
		"", "1team", "support_team", "support__admin", " team", "team ", "équipe", strings.Repeat("a", 33),
	} {
		require.Error(t, ValidateIntegrationName(integration), integration)
	}
	for _, name := range []string{
		"int__", "int__team", "int__team__", "int__team__bad name", "mcp__team__post", "ordinary__custom",
	} {
		_, _, ok := SplitIntegrationToolName(name)
		require.False(t, ok, name)
	}
	require.True(t, UsesIntegrationToolNamespace("int__"), "malformed reserved names cannot become custom tools")
	require.False(t, UsesIntegrationToolNamespace("ordinary__custom"), "ordinary double underscores are not reserved")
}

func TestIntegrationToolNamesRejectOverflowWithoutTruncation(t *testing.T) {
	integration := strings.Repeat("a", 32)
	operation := strings.Repeat("b", 25)
	require.Len(t, IntegrationToolName(integration, operation), 64)
	require.NoError(t, ValidateIntegrationToolName(integration, operation))
	require.ErrorContains(t, ValidateIntegrationToolName(integration, operation+"b"), "exceeds 64")
	require.NoError(t, ValidateIntegrationToolName("a", strings.Repeat("b", 56)))
}
