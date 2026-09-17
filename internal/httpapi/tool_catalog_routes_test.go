package httpapi

import (
	"context"
	"testing"

	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestToolCatalogMarksBindingManagedToolsNonConfigurable(t *testing.T) {
	response, err := (strictOpenAPIServer{}).GetToolCatalog(
		context.Background(),
		openapi.GetToolCatalogRequestObject{},
	)
	if err != nil {
		t.Fatalf("get tool catalog: %v", err)
	}
	body, ok := response.(openapi.GetToolCatalog200JSONResponse)
	if !ok {
		t.Fatalf("tool catalog response = %T", response)
	}
	configurable := make(map[string]bool, len(body.BuiltInTools))
	for _, entry := range body.BuiltInTools {
		if entry.Configurable == nil {
			t.Fatalf("tool %q omitted configurable", entry.Name)
		}
		configurable[entry.Name] = *entry.Configurable
	}
	for _, name := range []string{
		toolcatalog.ToolNameListChannels,
		toolcatalog.ToolNameGetChannel,
		toolcatalog.ToolNameSetCurrentChannel,
		toolcatalog.ToolNameSendChannelMessage,
	} {
		value, exists := configurable[name]
		if !exists || value {
			t.Fatalf("binding-managed tool %q present=%t configurable=%t", name, exists, value)
		}
	}
	if !configurable[toolcatalog.ToolNameRunCommand] {
		t.Fatal("ordinary built-in tool is not configurable")
	}
}

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
		{toolcatalog.ToolNameSkill, toolcatalog.ToolNameListChannels, toolcatalog.ToolNameGetChannel,
			toolcatalog.ToolNameSetCurrentChannel, toolcatalog.ToolNameSendChannelMessage, toolcatalog.ToolNameReadChannel,
			toolcatalog.ToolNameReadFile, toolcatalog.ToolNameSearchFiles, toolcatalog.ToolNameToolSearch},
	} {
		for _, name := range names {
			require.True(t, byName[name], name)
		}
	}
	for _, name := range []string{
		toolcatalog.ToolNameWebSearch, toolcatalog.ToolNameWebFetch,
		toolcatalog.ToolNameAskQuestion,
	} {
		require.Contains(t, byName, name)
		require.False(t, byName[name], name)
	}
}
