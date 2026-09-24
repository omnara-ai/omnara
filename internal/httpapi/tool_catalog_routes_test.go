package httpapi

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestToolCatalogImplicit(t *testing.T) {
	response, err := (strictOpenAPIServer{}).GetToolCatalog(t.Context(), openapi.GetToolCatalogRequestObject{})
	require.NoError(t, err)
	catalog, ok := response.(openapi.GetToolCatalog200JSONResponse)
	require.True(t, ok)
	byName := make(map[string]bool)
	for _, entry := range catalog.BuiltInTools {
		require.NotNil(t, entry.Implicit)
		byName[entry.Name] = *entry.Implicit
	}
	for _, names := range [][]string{
		toolcatalog.MachineToolNames(), toolcatalog.MachinePoolToolNames(), toolcatalog.SubagentToolNames(),
		{toolcatalog.ToolNameSkill,
			toolcatalog.ToolNameReadFile, toolcatalog.ToolNameSearchFiles, toolcatalog.ToolNameToolSearch},
	} {
		for _, name := range names {
			require.True(t, byName[name], name)
		}
	}
	for _, name := range []string{
		toolcatalog.ToolNameWebSearch, toolcatalog.ToolNameWebFetch,
		toolcatalog.ToolNameAskQuestion,
		toolcatalog.ToolNameListInteractionHandlers, toolcatalog.ToolNameSetInteractionHandler,
	} {
		require.Contains(t, byName, name)
		require.False(t, byName[name], name)
	}
}
