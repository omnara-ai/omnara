package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/programoutput"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/webaccess"
	"github.com/stretchr/testify/require"
)

func requireProgramOutputSchema(t *testing.T, spec modelcontext.ToolSpec) json.RawMessage {
	t.Helper()
	schema, err := modelcontext.ProgramOutputSchema(spec)
	require.NoError(t, err)
	require.NotEmpty(t, schema, spec.Name)
	require.NoError(t, jsonschema.ValidateSchema(schema), spec.Name)
	return schema
}

func requireMatchesOutputSchema(t *testing.T, schema json.RawMessage, value any) {
	t.Helper()
	raw, ok := value.(json.RawMessage)
	if !ok {
		var err error
		raw, err = json.Marshal(value)
		require.NoError(t, err)
	}
	require.NoError(t, jsonschema.Validate(schema, raw), string(raw))
}

func firstStructuredValue(t *testing.T, parts json.RawMessage) json.RawMessage {
	t.Helper()
	for _, part := range decodeParts(t, parts) {
		if string(part["type"]) == `"structured_data"` {
			return part["value"]
		}
	}
	t.Fatalf("no structured part in %s", parts)
	return nil
}

func builtInSpec(name string) modelcontext.ToolSpec {
	return modelcontext.ToolSpec{Name: name, Type: toolcatalog.ToolTypeBuiltIn}
}

func integrationReadSpec(kind integrationdefinition.Kind) modelcontext.ToolSpec {
	return modelcontext.ToolSpec{
		Name: toolcatalog.IntegrationToolName("chat", toolcatalog.IntegrationOperationRead),
		Type: toolcatalog.ToolTypeBuiltIn, IntegrationKind: kind,
	}
}

func TestProgramOutputSchemasCoverCallableBuiltIns(t *testing.T) {
	for _, name := range []string{toolcatalog.ToolNameStorageRead, toolcatalog.ToolNameStorageList,
		toolcatalog.ToolNameStorageSearch, toolcatalog.ToolNameWebSearch, toolcatalog.ToolNameWebFetch} {
		requireProgramOutputSchema(t, builtInSpec(name))
	}
	reads := 0
	for _, definition := range integrationdefinition.All() {
		if slices.ContainsFunc(toolcatalog.IntegrationTools(definition.IntegrationKind),
			func(tool toolcatalog.IntegrationToolDefinition) bool {
				return tool.Operation == toolcatalog.IntegrationOperationRead
			}) {
			requireProgramOutputSchema(t, integrationReadSpec(definition.IntegrationKind))
			reads++
		}
	}
	require.Equal(t, 3, reads)

	post := integrationReadSpec(integrationdefinition.SlackThread)
	post.Name = toolcatalog.IntegrationToolName("chat", toolcatalog.IntegrationOperationPostMessage)
	for _, spec := range []modelcontext.ToolSpec{post, builtInSpec(toolcatalog.ToolNameStorageWrite)} {
		schema, err := modelcontext.ProgramOutputSchema(spec)
		require.NoError(t, err)
		require.Nil(t, schema, spec.Name)
	}
	mcp := modelcontext.ToolSpec{Name: "mcp__docs__greet", Type: toolcatalog.ToolTypeMCP,
		OutputSchema: json.RawMessage(`{"type":"object"}`)}
	schema, err := modelcontext.ProgramOutputSchema(mcp)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"object"}`, string(schema))
	mcp.OutputSchema = nil
	schema = requireProgramOutputSchema(t, mcp)
	require.Contains(t, string(schema), "declares no output schema",
		"tool_search describes what commands get from a server that declares no schema")
}

func populated(v reflect.Value) {
	if v.Type() == reflect.TypeFor[json.RawMessage]() {
		v.SetBytes([]byte(`"x"`))
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1.5)
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		populated(v.Elem())
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		populated(v.Index(0))
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		value := reflect.New(v.Type().Elem()).Elem()
		populated(value)
		v.SetMapIndex(reflect.ValueOf("k").Convert(v.Type().Key()), value)
	case reflect.Struct:
		if v.Type() == reflect.TypeFor[time.Time]() {
			v.Set(reflect.ValueOf(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)))
			return
		}
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				populated(v.Field(i))
			}
		}
	default:
	}
}

func TestProgramOutputSchemasAcceptReflectedResults(t *testing.T) {
	cases := []struct {
		spec   modelcontext.ToolSpec
		values []any
	}{
		{builtInSpec(toolcatalog.ToolNameStorageList), []any{listing.FilePage{}}},
		{builtInSpec(toolcatalog.ToolNameStorageSearch), []any{searchResult{}}},
		{builtInSpec(toolcatalog.ToolNameWebSearch), []any{programoutput.WebSearch{}}},
		{builtInSpec(toolcatalog.ToolNameWebFetch), []any{programoutput.WebFetch{}}},
		{integrationReadSpec(integrationdefinition.SlackThread), []any{slack.MessagePage{}}},
		{integrationReadSpec(integrationdefinition.DiscordThread), []any{discord.MessagePage{}}},
		{integrationReadSpec(integrationdefinition.GitHubPR), []any{
			github.PullRequest{}, github.DiscussionCommentsPage{}, github.ReviewCommentsPage{},
			github.ReviewsPage{}, github.Review{}, programoutput.GitHubPendingReview{}, github.ReviewThreadsPage{},
			github.FilesPage{}, github.Diff{},
		}},
	}
	for _, test := range cases {
		schema := requireProgramOutputSchema(t, test.spec)
		for _, value := range test.values {
			requireMatchesOutputSchema(t, schema, value)
			full := reflect.New(reflect.TypeOf(value))
			populated(full.Elem())
			requireMatchesOutputSchema(t, schema, full.Interface())
		}
	}
}

func TestStorageReadResultsMatchOutputSchema(t *testing.T) {
	schema := requireProgramOutputSchema(t, builtInSpec(toolcatalog.ToolNameStorageRead))
	read := func(content, input string) map[string]any {
		t.Helper()
		request, err := resolveStorageReadRequest(json.RawMessage(input))
		require.NoError(t, err)
		return storageTextPage([]byte(content), request, "text/plain; charset=utf-8")
	}
	longLine := strings.Repeat("a", toolcatalog.FilePageBytes+10)
	pages := map[string]map[string]any{
		"whole file":  read("one\ntwo\n", `{"path":"/memory/notes/a.md"}`),
		"more lines":  read("one\ntwo\nthree\n", `{"path":"/memory/notes/a.md","limit_lines":1}`),
		"long line":   read(longLine, `{"path":"/memory/notes/a.md"}`),
		"empty file":  read("", `{"path":"/memory/notes/a.md"}`),
		"characters":  read("héllo", `{"path":"/memory/notes/a.md","offset_char":1,"limit_chars":2}`),
		"char at end": read("héllo", `{"path":"/memory/notes/a.md","offset_char":3}`),
	}
	require.Contains(t, pages["more lines"], "next_offset_line")
	require.Contains(t, pages["long line"], "notice")
	require.Contains(t, pages["characters"], "next_offset_char")
	for name, page := range pages {
		t.Run(name, func(t *testing.T) { requireMatchesOutputSchema(t, schema, page) })
	}

	image, err := completeImageRead("/memory/notes/a.png", "image/png", "digest", 42, uuid.New(), "")
	require.NoError(t, err)
	requireMatchesOutputSchema(t, schema, firstStructuredValue(t, asyncCompletionContent(t, image)))
}

func TestWebToolsReturnStructuredResultsToProgramCalls(t *testing.T) {
	programTurn := Turn{authority: programClaimAuthority{}}
	search := func(response webaccess.SearchResponse) json.RawMessage {
		t.Helper()
		result, err := runWebSearch(context.Background(), asyncToolContext{
			Executor: Executor{WebSearch: &fakeSearchProvider{response: response}},
			Turn:     programTurn,
			Call:     model.ToolCall{Name: "web_search", Input: json.RawMessage(`{"query":"go releases"}`)},
		})
		require.NoError(t, err)
		parts := asyncCompletionContent(t, result)
		require.Len(t, decodeParts(t, parts), 1, "commands get the structured result only")
		return firstStructuredValue(t, parts)
	}
	searchSchema := requireProgramOutputSchema(t, builtInSpec(toolcatalog.ToolNameWebSearch))
	found := search(webaccess.SearchResponse{Provider: "exa", Results: []webaccess.SearchResult{
		{URL: "https://go.dev/blog/a", Title: "Post A", Snippet: "alpha", Published: "2026-01-02"},
		{URL: "https://go.dev/blog/b", Title: "Post B"},
	}})
	requireMatchesOutputSchema(t, searchSchema, found)
	require.JSONEq(t, `{"query":"go releases","results":[
		{"url":"https://go.dev/blog/a","title":"Post A","snippet":"alpha","published":"2026-01-02"},
		{"url":"https://go.dev/blog/b","title":"Post B","snippet":""}]}`, string(found))
	none := search(webaccess.SearchResponse{Provider: "exa"})
	requireMatchesOutputSchema(t, searchSchema, none)
	require.JSONEq(t, `{"query":"go releases","results":[]}`, string(none))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("plain body content"))
	}))
	defer server.Close()
	input, err := json.Marshal(map[string]string{"url": server.URL})
	require.NoError(t, err)
	fetched, err := runWebFetch(context.Background(), asyncToolContext{
		Executor: Executor{WebFetcher: webaccess.NewFetcher(webaccess.FetcherOptions{AllowLoopback: true})},
		Turn:     programTurn,
		Call:     model.ToolCall{Name: "web_fetch", Input: input},
	})
	require.NoError(t, err)
	parts := asyncCompletionContent(t, fetched)
	require.Len(t, decodeParts(t, parts), 1, "commands get the structured result only")
	page := firstStructuredValue(t, parts)
	requireMatchesOutputSchema(t, requireProgramOutputSchema(t, builtInSpec(toolcatalog.ToolNameWebFetch)), page)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(page, &decoded))
	require.Equal(t, "plain body content", decoded["content"])
	require.Equal(t, server.URL, decoded["url"])
}
