package modelcontext

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func deferredTestSpecs() []ToolSpec {
	return []ToolSpec{
		{Name: "tool_search", Description: "search tools"},
		{Name: "get_time", Description: "Get the current time in a timezone."},
		{
			Name:        "get_weather",
			Description: "Get the current weather for a city.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
			Deferred:    true,
		},
		{
			Name:        "get_stock_price",
			Description: "Get the latest price for a ticker symbol.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"ticker":{"type":"string","description":"Stock market symbol"}}}`),
			Deferred:    true,
		},
	}
}

func TestSearchDeferredToolsMatchesNameDescriptionAndArguments(t *testing.T) {
	specs := deferredTestSpecs()

	byName, err := SearchDeferredTools(specs, "WEATHER", 0)
	require.NoError(t, err)
	require.Equal(
		t,
		ToolSearchResult{Pattern: "WEATHER", ToolNames: []string{"get_weather"}, TotalDeferredTools: 2},
		byName,
	)

	byArgument, err := SearchDeferredTools(specs, "stock market", 5)
	require.NoError(t, err)
	require.Equal(t, []string{"get_stock_price"}, byArgument.ToolNames)

	loadedOnly, err := SearchDeferredTools(specs, "timezone", 5)
	require.NoError(t, err)
	require.Empty(t, loadedOnly.ToolNames)

	capped, err := SearchDeferredTools(specs, "get_.*", 1)
	require.NoError(t, err)
	require.Equal(t, []string{"get_stock_price"}, capped.ToolNames)
}

func TestSearchDeferredToolsRejectsBadPatterns(t *testing.T) {
	specs := deferredTestSpecs()
	_, err := SearchDeferredTools(specs, "(", 5)
	require.ErrorContains(t, err, "invalid regular expression")
	_, err = SearchDeferredTools(specs, "  ", 5)
	require.ErrorContains(t, err, "pattern is required")
	_, err = SearchDeferredTools(specs, strings.Repeat("a", 201), 5)
	require.ErrorContains(t, err, "at most 200")
}

func TestToolSearchResultFromToolResult(t *testing.T) {
	result := ToolResultRef{
		Name: "tool_search",
		ContentParts: json.RawMessage(`[{"type":"structured_data","value":{"outcome":"succeeded"}},` +
			`{"type":"text","text":"Loaded 1 tool(s)"},` +
			`{"type":"structured_data","value":{"pattern":"weather","tool_names":["get_weather"],"total_deferred_tools":2}}]`),
	}
	search, ok := ToolSearchResultFromToolResult(result)
	require.True(t, ok)
	require.Equal(
		t,
		ToolSearchResult{Pattern: "weather", ToolNames: []string{"get_weather"}, TotalDeferredTools: 2},
		search,
	)

	discovered := DiscoveredToolSpecs(
		deferredTestSpecs(),
		ToolSearchResult{ToolNames: []string{"get_weather", "get_time", "missing"}},
	)
	require.Len(t, discovered, 1)
	require.Equal(t, "get_weather", discovered[0].Name)

	_, ok = ToolSearchResultFromToolResult(ToolResultRef{Name: "web_search", ContentParts: result.ContentParts})
	require.False(t, ok)
	_, ok = ToolSearchResultFromToolResult(ToolResultRef{
		Name:         "tool_search",
		ContentParts: json.RawMessage(`[{"type":"structured_data","value":{"error":"bad pattern"}}]`),
	})
	require.False(t, ok)
}

func TestDeferredToolSpecPartitions(t *testing.T) {
	specs := deferredTestSpecs()
	require.True(t, DeferredToolsEnabled(specs))
	require.False(t, DeferredToolsEnabled(specs[:2]))
	require.Len(t, LoadedToolSpecs(specs), 2)
	require.Len(t, DeferredToolSpecs(specs), 2)
}
