package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/webaccess"
)

func TestResolveWebSearchRequest(t *testing.T) {
	resolved, err := resolveWebSearchRequest(
		json.RawMessage(`{"query":"go generics","num_results":7,"recency":"week","domains":["go.dev","-spam.com"]}`),
	)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Query != "go generics" || resolved.NumResults != 7 || resolved.Recency != "week" ||
		len(resolved.Domains) != 2 {
		t.Fatalf("resolved = %+v", resolved)
	}
	defaulted, err := resolveWebSearchRequest(json.RawMessage(`{"query":"x"}`))
	if err != nil || defaulted.NumResults != webSearchDefaultResults {
		t.Fatalf("defaults: %+v err=%v", defaulted, err)
	}
	bad := []string{
		`{}`,
		`{"query":"   "}`,
		`{"query":"x","num_results":0}`,
		`{"query":"x","num_results":21}`,
		`{"query":"x","recency":"decade"}`,
		`{"query":"x","domains":[" "]}`,
		`{"query":"x","unknown_field":true}`,
		`{"query":null}`,
	}
	for _, input := range bad {
		if _, err := resolveWebSearchRequest(json.RawMessage(input)); err == nil {
			t.Fatalf("input %s should fail validation", input)
		}
	}
}

func TestResolveWebFetchRequest(t *testing.T) {
	resolved, err := resolveWebFetchRequest(
		json.RawMessage(`{"url":"https://example.com/docs","format":"text","timeout_seconds":10}`),
	)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.URL != "https://example.com/docs" || resolved.Format != "text" || resolved.TimeoutSeconds != 10 {
		t.Fatalf("resolved = %+v", resolved)
	}
	defaulted, err := resolveWebFetchRequest(json.RawMessage(`{"url":"http://example.com"}`))
	if err != nil || defaulted.Format != "markdown" || defaulted.TimeoutSeconds != webaccess.DefaultFetchTimeoutSeconds {
		t.Fatalf("defaults: %+v err=%v", defaulted, err)
	}
	bad := []string{
		`{}`,
		`{"url":"ftp://example.com"}`,
		`{"url":"example.com"}`,
		`{"url":"https://example.com","format":"html"}`,
		`{"url":"https://example.com","timeout_seconds":0}`,
		`{"url":"https://example.com","timeout_seconds":121}`,
		`{"url":"https://example.com","extra":1}`,
	}
	for _, input := range bad {
		if _, err := resolveWebFetchRequest(json.RawMessage(input)); err == nil {
			t.Fatalf("input %s should fail validation", input)
		}
	}
}

func TestWebInputValidatorsAreRegistered(t *testing.T) {
	if err := validateRegisteredToolInput(
		"web_search",
		json.RawMessage(`{"query":"x"}`),
	); err != nil {
		t.Fatalf("valid web_search call rejected: %v", err)
	}
	if err := validateRegisteredToolInput(
		"web_fetch",
		json.RawMessage(`{"url":"https://x.example"}`),
	); err != nil {
		t.Fatalf("valid web_fetch call rejected: %v", err)
	}
	if err := validateRegisteredToolInput("web_search", json.RawMessage(`{}`)); err == nil {
		t.Fatal("web_search without query must fail semantic validation")
	}
}

type fakeSearchProvider struct {
	response webaccess.SearchResponse
	err      error
	gotReq   webaccess.SearchRequest
}

func (f *fakeSearchProvider) Search(
	ctx context.Context,
	req webaccess.SearchRequest,
) (webaccess.SearchResponse, error) {
	f.gotReq = req
	return f.response, f.err
}

func decodeParts(t *testing.T, raw json.RawMessage) []map[string]json.RawMessage {
	t.Helper()
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		t.Fatalf("decode content parts: %v (%s)", err, raw)
	}
	return parts
}

func partText(t *testing.T, parts []map[string]json.RawMessage, partType, field string) string {
	t.Helper()
	for _, part := range parts {
		var pt string
		_ = json.Unmarshal(part["type"], &pt)
		if pt == partType {
			var value string
			_ = json.Unmarshal(part[field], &value)
			return value
		}
	}
	return ""
}

func asyncCompletionContent(t *testing.T, result asyncPhaseResult) json.RawMessage {
	t.Helper()
	completed, ok := result.(completeAsync)
	if !ok {
		t.Fatalf("async result = %T, want complete", result)
	}
	content, err := completed.content.contentParts()
	if err != nil {
		t.Fatalf("marshal completed async content: %v", err)
	}
	return content
}

func asyncFailureContent(t *testing.T, result asyncPhaseResult) json.RawMessage {
	t.Helper()
	failed, ok := result.(failAsync)
	if !ok {
		t.Fatalf("async result = %T, want failure", result)
	}
	if failed.cause == nil {
		t.Fatal("async failure has no cause")
	}
	content, err := failed.content.contentParts()
	if err != nil {
		t.Fatalf("marshal failed async content: %v", err)
	}
	return content
}

func TestWebSearchHandlerSuccessParts(t *testing.T) {
	provider := &fakeSearchProvider{response: webaccess.SearchResponse{
		Provider: "exa",
		Results: []webaccess.SearchResult{
			{URL: "https://go.dev/blog/a", Title: "Post A", Snippet: "alpha snippet"},
			{URL: "https://go.dev/blog/b", Title: "Post B", Snippet: "beta snippet"},
		},
	}}
	executor := Executor{WebSearch: provider}
	dispatch, err := runWebSearch(
		context.Background(),
		asyncToolContext{
			Executor: executor,
			Call: model.ToolCall{
				Name:  "web_search",
				Input: json.RawMessage(`{"query":"go releases","num_results":2}`),
			},
		},
	)
	if err != nil {
		t.Fatalf("run web search: %v", err)
	}
	if provider.gotReq.Query != "go releases" || provider.gotReq.NumResults != 2 {
		t.Fatalf("provider request = %+v", provider.gotReq)
	}
	parts := decodeParts(t, asyncCompletionContent(t, dispatch))
	text := partText(t, parts, "text", "text")
	if !strings.Contains(text, "Post A") || !strings.Contains(text, "https://go.dev/blog/b") {
		t.Fatalf("text part missing results: %q", text)
	}
	var structuredFound bool
	for _, part := range parts {
		var pt string
		_ = json.Unmarshal(part["type"], &pt)
		if pt == "structured_data" {
			structuredFound = true
			var value struct {
				Query   string                   `json:"query"`
				Results []webaccess.SearchResult `json:"results"`
			}
			if err := json.Unmarshal(part["value"], &value); err != nil {
				t.Fatalf("decode structured value: %v", err)
			}
			if value.Query != "go releases" || len(value.Results) != 2 {
				t.Fatalf("structured value = %+v", value)
			}
		}
	}
	if !structuredFound {
		t.Fatal("structured_data part missing")
	}
}

func TestWebSearchHandlerFailuresPreserveStructuredContent(t *testing.T) {
	provider := &fakeSearchProvider{
		err: &webaccess.ProviderError{Code: webaccess.ErrorCodeRateLimited, Message: "slow down", Retryable: true},
	}
	dispatch, err := runWebSearch(
		context.Background(),
		asyncToolContext{
			Executor: Executor{WebSearch: provider},
			Call: model.ToolCall{
				Name:  "web_search",
				Input: json.RawMessage(`{"query":"q"}`),
			},
		},
	)
	if err != nil {
		t.Fatalf("run web search failure: %v", err)
	}
	content := asyncFailureContent(t, dispatch)
	if !strings.Contains(string(content), webaccess.ErrorCodeRateLimited) ||
		!strings.Contains(string(content), `"retryable":true`) ||
		strings.Contains(string(content), `"ok"`) {
		t.Fatalf("structured failure missing fields: %s", content)
	}

	// Nil provider → structured unconfigured result.
	dispatch, err = runWebSearch(
		context.Background(),
		asyncToolContext{
			Executor: Executor{},
			Call: model.ToolCall{
				Name:  "web_search",
				Input: json.RawMessage(`{"query":"q"}`),
			},
		},
	)
	content = asyncFailureContent(t, dispatch)
	if err != nil || !strings.Contains(string(content), webaccess.ErrorCodeSearchUnavailable) {
		t.Fatalf("nil provider: err=%v result=%s", err, content)
	}

	// Malformed input → structured failure, not error.
	dispatch, err = runWebSearch(
		context.Background(),
		asyncToolContext{
			Executor: Executor{WebSearch: provider},
			Call: model.ToolCall{
				Name:  "web_search",
				Input: json.RawMessage(`{}`),
			},
		},
	)
	content = asyncFailureContent(t, dispatch)
	if err != nil || !strings.Contains(string(content), `"error"`) {
		t.Fatalf("malformed input: err=%v result=%s", err, content)
	}
}

func TestWebFetchHandlerSuccessAndBlockedParts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("plain body content"))
	}))
	defer server.Close()

	fetcher := webaccess.NewFetcher(webaccess.FetcherOptions{AllowLoopback: true})
	executor := Executor{WebFetcher: fetcher}
	input, err := json.Marshal(map[string]string{"url": server.URL})
	if err != nil {
		t.Fatalf("marshal fetch input: %v", err)
	}
	dispatch, err := runWebFetch(
		context.Background(),
		asyncToolContext{
			Executor: executor,
			Call:     model.ToolCall{Name: "web_fetch", Input: input},
		},
	)
	if err != nil {
		t.Fatalf("run web fetch: %v", err)
	}
	parts := decodeParts(t, asyncCompletionContent(t, dispatch))
	if text := partText(t, parts, "text", "text"); !strings.Contains(text, "plain body content") {
		t.Fatalf("text part = %q", text)
	}

	blockedFetcher := webaccess.NewFetcher(webaccess.FetcherOptions{})
	dispatch, err = runWebFetch(
		context.Background(),
		asyncToolContext{
			Executor: Executor{WebFetcher: blockedFetcher},
			Call: model.ToolCall{
				Name:  "web_fetch",
				Input: json.RawMessage(`{"url":"http://169.254.169.254/latest/"}`),
			},
		},
	)
	if err != nil {
		t.Fatalf("run blocked web fetch: %v", err)
	}
	content := asyncFailureContent(t, dispatch)
	if !strings.Contains(string(content), webaccess.ErrorCodeFetchBlocked) ||
		!strings.Contains(string(content), "run_command") {
		t.Fatalf("blocked result missing guidance: %s", content)
	}
}

func TestWebFetchHandlerFailuresPreserveStructuredContent(t *testing.T) {
	dispatch, err := runWebFetch(
		context.Background(),
		asyncToolContext{
			Executor: Executor{},
			Call: model.ToolCall{
				Name:  "web_fetch",
				Input: json.RawMessage(`{"url":"https://example.com"}`),
			},
		},
	)
	if err != nil {
		t.Fatalf("run unconfigured web fetch: %v", err)
	}
	content := asyncFailureContent(t, dispatch)
	if !strings.Contains(string(content), "web fetch is not configured") {
		t.Fatalf("nil fetcher result missing structured failure: %s", content)
	}
}

func TestWebFetchToolResultContentRendersTitleAndPreservesFullExtractedContent(t *testing.T) {
	const runeCount = 30_005
	content := strings.Repeat("é", runeCount)
	result, err := webFetchToolResultContent(webaccess.FetchResult{
		URL:         "https://example.com/start",
		FinalURL:    "https://example.com/final",
		Title:       "Example Title",
		StatusCode:  200,
		ContentType: "text/html",
		Content:     content,
		Bytes:       int64(len(content)),
	})
	if err != nil {
		t.Fatalf("content parts: %v", err)
	}
	contentParts, err := result.contentParts()
	if err != nil {
		t.Fatalf("marshal content parts: %v", err)
	}
	parts := decodeParts(t, contentParts)
	text := partText(t, parts, "text", "text")
	if !strings.HasPrefix(text, "# Example Title\n\n") || strings.Contains(text, "[content truncated:") {
		t.Fatalf("rendered text missing title or was unexpectedly truncated: %q", text[:min(len(text), 120)])
	}
	if !utf8.ValidString(text) || strings.Count(text, "é") != runeCount {
		t.Fatalf(
			"rendered text invalid or wrong rune count: valid=%v count=%d",
			utf8.ValidString(text),
			strings.Count(text, "é"),
		)
	}
	var structuredFound bool
	for _, part := range parts {
		var pt string
		_ = json.Unmarshal(part["type"], &pt)
		if pt != "structured_data" {
			continue
		}
		structuredFound = true
		var value struct {
			Title      string `json:"title"`
			StatusCode int    `json:"status_code"`
			Truncated  bool   `json:"truncated"`
		}
		if err := json.Unmarshal(part["value"], &value); err != nil {
			t.Fatalf("decode structured value: %v", err)
		}
		if value.Title != "Example Title" || value.StatusCode != http.StatusOK || value.Truncated {
			t.Fatalf("structured value = %+v", value)
		}
	}
	if !structuredFound {
		t.Fatal("structured_data part missing")
	}
}

func TestWebFetchToolResultContentPreservesEmptyTextBlock(t *testing.T) {
	result, err := webFetchToolResultContent(webaccess.FetchResult{
		URL:         "https://example.com/empty",
		FinalURL:    "https://example.com/empty",
		StatusCode:  http.StatusNoContent,
		ContentType: "text/plain",
	})
	if err != nil {
		t.Fatalf("content parts: %v", err)
	}
	contentParts, err := result.contentParts()
	if err != nil {
		t.Fatalf("marshal content parts: %v", err)
	}
	parts := decodeParts(t, contentParts)
	if len(parts) != 2 {
		t.Fatalf("content parts = %s, want empty text and structured blocks", contentParts)
	}
	if text := partText(t, parts, "text", "text"); text != "" {
		t.Fatalf("text content = %q, want empty", text)
	}
	if !strings.Contains(string(contentParts), `"status_code":204`) {
		t.Fatalf("structured content = %s, want status code 204", contentParts)
	}
}

func TestWebSearchToolResultContentTruncatesSnippetOnRuneBoundary(t *testing.T) {
	snippet := strings.Repeat("é", webSearchInlineSnippetChars+5)
	result, err := webSearchToolResultContent("unicode", webaccess.SearchResponse{
		Provider: "test",
		Results:  []webaccess.SearchResult{{URL: "https://example.com", Title: "Unicode", Snippet: snippet}},
	})
	if err != nil {
		t.Fatalf("content parts: %v", err)
	}
	contentParts, err := result.contentParts()
	if err != nil {
		t.Fatalf("marshal content parts: %v", err)
	}
	text := partText(t, decodeParts(t, contentParts), "text", "text")
	if !utf8.ValidString(text) ||
		strings.Count(text, "é") != webSearchInlineSnippetChars ||
		!strings.Contains(text, "…") {
		t.Fatalf(
			"search text invalid or not truncated on rune boundary: valid=%v count=%d text=%q",
			utf8.ValidString(text),
			strings.Count(text, "é"),
			text[:min(len(text), 120)],
		)
	}
}
