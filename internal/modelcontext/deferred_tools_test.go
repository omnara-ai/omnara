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
		ToolSearchResult{
			Pattern:            "WEATHER",
			ToolNames:          []string{"get_weather"},
			TotalDeferredTools: 2,
			Tools: []ToolSearchDefinition{{
				Name:        "get_weather",
				Description: "Get the current weather for a city.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
			}},
		},
		byName,
	)

	byArgument, err := SearchDeferredTools(specs, "stock market", 5)
	require.NoError(t, err)
	require.Equal(t, []string{"get_stock_price"}, byArgument.ToolNames)
	require.Len(t, byArgument.Tools, 1)
	require.Equal(t, "get_stock_price", byArgument.Tools[0].Name)
	require.Equal(t, "Get the latest price for a ticker symbol.", byArgument.Tools[0].Description)
	require.NotEmpty(t, byArgument.Tools[0].InputSchema)

	loadedOnly, err := SearchDeferredTools(specs, "timezone", 5)
	require.NoError(t, err)
	require.Empty(t, loadedOnly.ToolNames)
	require.Empty(t, loadedOnly.Tools)

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
			`{"type":"structured_data","value":{"pattern":"weather","tool_names":["get_weather"],"total_deferred_tools":2,` +
			`"tools":[{"name":"get_weather","description":"Get the current weather for a city.",` +
			`"input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}]}}]`),
	}
	search, ok := ToolSearchResultFromToolResult(result)
	require.True(t, ok)
	require.Equal(
		t,
		ToolSearchResult{
			Pattern:            "weather",
			ToolNames:          []string{"get_weather"},
			TotalDeferredTools: 2,
			Tools: []ToolSearchDefinition{{
				Name:        "get_weather",
				Description: "Get the current weather for a city.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
			}},
		},
		search,
	)

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

func TestDeferredToolSearchDefinitionsUsesCurrentSpecs(t *testing.T) {
	specs := deferredTestSpecs()
	search := ToolSearchResult{Tools: []ToolSearchDefinition{
		{Name: "get_weather", Description: "stale", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "get_removed", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "get_time", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}}
	definitions := DeferredToolSearchDefinitions(specs, search)
	require.Len(t, definitions, 1)
	require.Equal(t, "get_weather", definitions[0].Name)
	require.Equal(t, "Get the current weather for a city.", definitions[0].Description)
	require.JSONEq(t, `{"type":"object","properties":{"city":{"type":"string"}}}`, string(definitions[0].InputSchema))
}

func TestSearchToolsReturnsOutputSchemas(t *testing.T) {
	specs := deferredTestSpecs()
	specs[2].OutputSchema = json.RawMessage(`{"type":"object","properties":{"celsius":{"type":"number"}}}`)

	search, err := SearchTools(specs, "get_", 0)
	require.NoError(t, err)
	require.Equal(t, 3, search.Total)
	schemas := map[string]string{}
	for _, tool := range search.Tools {
		schemas[tool.Name] = string(tool.OutputSchema)
	}
	require.Equal(t, map[string]string{
		"get_stock_price": "",
		"get_time":        "",
		"get_weather":     `{"type":"object","properties":{"celsius":{"type":"number"}}}`,
	}, schemas)
	encoded, err := json.Marshal(search.Tools[0])
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "output_schema", "tools without one omit it")

	deferred, err := SearchDeferredTools(specs, "weather", 0)
	require.NoError(t, err)
	require.Empty(t, deferred.Tools[0].OutputSchema, "model-facing tool_search does not load output schemas")
}

func TestSearchDeferredToolsSeparatesCodeModeTools(t *testing.T) {
	specs := []ToolSpec{
		{Name: "orders_list", Deferred: true, CodeMode: true, OutputSchema: json.RawMessage(`{"type":"array"}`)},
		{Name: "orders_write", Deferred: true},
		{Name: "orders_loaded"},
	}
	search, err := SearchDeferredTools(specs, "orders", 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(search.ToolNames) != 1 || search.ToolNames[0] != "orders_write" || len(search.Tools) != 1 {
		t.Fatalf("added tools = %v, want only orders_write", search.ToolNames)
	}
	if len(search.CodeModeTools) != 1 || search.CodeModeTools[0].Name != "orders_list" ||
		string(search.CodeModeTools[0].OutputSchema) != `{"type":"array"}` {
		t.Fatalf("code mode tools = %+v, want orders_list with its output schema", search.CodeModeTools)
	}
	if search.TotalDeferredTools != 2 {
		t.Fatalf("total deferred tools = %d, want 2", search.TotalDeferredTools)
	}
	if loaded := DeferredToolSearchDefinitions(specs, search); len(loaded) != 1 || loaded[0].Name != "orders_write" {
		t.Fatalf("definitions to add = %+v, want only orders_write", loaded)
	}
}
