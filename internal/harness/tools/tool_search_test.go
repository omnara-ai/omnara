package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestResolveToolSearchRequest(t *testing.T) {
	resolved, err := resolveToolSearchRequest(json.RawMessage(`{"pattern":" weather "}`))
	require.NoError(t, err)
	require.Equal(t, resolvedToolSearchRequest{Pattern: "weather", MaxResults: 5}, resolved)

	resolved, err = resolveToolSearchRequest(json.RawMessage(`{"pattern":"weather","max_results":7}`))
	require.NoError(t, err)
	require.Equal(t, 7, resolved.MaxResults)

	for name, input := range map[string]string{
		"missing pattern":   `{}`,
		"blank pattern":     `{"pattern":"  "}`,
		"unknown field":     `{"pattern":"x","limit":3}`,
		"null max_results":  `{"pattern":"x","max_results":null}`,
		"zero max_results":  `{"pattern":"x","max_results":0}`,
		"large max_results": `{"pattern":"x","max_results":51}`,
		"long pattern":      `{"pattern":"` + strings.Repeat("a", 201) + `"}`,
	} {
		_, err := resolveToolSearchRequest(json.RawMessage(input))
		require.Error(t, err, name)
	}
}

func TestToolSearchIsRegisteredAsAlwaysAllow(t *testing.T) {
	registry, err := loadBuiltInToolImplementations()
	require.NoError(t, err)
	implementation, ok := registry.tools[toolcatalog.ToolNameToolSearch]
	require.True(t, ok)
	require.Len(t, implementation.permissionModes, 1)
	require.NoError(t, implementation.validateInput(json.RawMessage(`{"pattern":"weather"}`)))
	require.Error(t, implementation.validateInput(json.RawMessage(`{}`)))
}

func toolSearchTurn() Turn {
	return Turn{Tools: map[string]ToolSpec{
		toolcatalog.ToolNameToolSearch: {Type: toolcatalog.ToolTypeBuiltIn, Description: "search"},
		"get_time":                     {Type: toolcatalog.ToolTypeBuiltIn, Description: "Get the current time."},
		"get_weather": {
			Type:        toolcatalog.ToolTypeCustom,
			Description: "Get the current weather for a city.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
			Deferred:    true,
		},
		"mcp__crm__list_orders": {
			Type:        toolcatalog.ToolTypeMCP,
			Description: "List open orders for a customer.",
			Deferred:    true,
		},
	}}
}

func TestRunToolSearchLoadsMatchingDeferredTools(t *testing.T) {
	dispatch, err := runToolSearch(context.Background(), transactionalToolContext{
		Turn: toolSearchTurn(),
		Call: model.ToolCall{Name: toolcatalog.ToolNameToolSearch, Input: json.RawMessage(`{"pattern":"weather|orders"}`)},
	})
	require.NoError(t, err)
	completed, ok := dispatch.(completeTransaction)
	require.True(t, ok)
	parts := decodeParts(t, mustContentParts(t, completed.content))
	text := partText(t, parts, "text", "text")
	require.Contains(t, text, "Loaded 2 tool(s)")
	require.Contains(t, text, "get_weather: Get the current weather for a city.")
	search, ok := modelcontext.ToolSearchResultFromToolResult(modelcontext.ToolResultRef{
		Name:         toolcatalog.ToolNameToolSearch,
		ContentParts: mustContentParts(t, completed.content),
	})
	require.True(t, ok)
	require.Equal(t, modelcontext.ToolSearchResult{
		Pattern:            "weather|orders",
		ToolNames:          []string{"get_weather", "mcp__crm__list_orders"},
		TotalDeferredTools: 2,
	}, search)
}

func TestRunToolSearchReportsNoMatchesAndInvalidPatterns(t *testing.T) {
	dispatch, err := runToolSearch(context.Background(), transactionalToolContext{
		Turn: toolSearchTurn(),
		Call: model.ToolCall{Name: toolcatalog.ToolNameToolSearch, Input: json.RawMessage(`{"pattern":"time"}`)},
	})
	require.NoError(t, err)
	completed, ok := dispatch.(completeTransaction)
	require.True(t, ok)
	parts := decodeParts(t, mustContentParts(t, completed.content))
	require.Contains(t, partText(t, parts, "text", "text"), "No deferred tools matched")

	dispatch, err = runToolSearch(context.Background(), transactionalToolContext{
		Turn: toolSearchTurn(),
		Call: model.ToolCall{Name: toolcatalog.ToolNameToolSearch, Input: json.RawMessage(`{"pattern":"("}`)},
	})
	require.NoError(t, err)
	failed, ok := dispatch.(failTransaction)
	require.True(t, ok)
	require.ErrorContains(t, failed.cause, "invalid regular expression")
	require.Contains(t, string(mustContentParts(t, failed.content)), `"error_code":"invalid_tool_input"`)
}

func mustContentParts(t *testing.T, content toolResultContent) json.RawMessage {
	t.Helper()
	parts, err := content.contentParts()
	require.NoError(t, err)
	return parts
}
