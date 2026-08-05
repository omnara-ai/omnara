package webaccess

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func exaProviderForServer(t *testing.T, server *httptest.Server, apiKey string) ExaProvider {
	t.Helper()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse Exa test server URL: %v", err)
	}
	base := server.Client()
	return ExaProvider{
		APIKey: apiKey,
		HTTPClient: &http.Client{Transport: rewriteExaTransport{
			target: target,
			base:   base.Transport,
		}},
	}
}

type rewriteExaTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func newExaMCPServer(
	t *testing.T,
	jsonResponse bool,
	handle func(exaMCPSearchArguments) *sdkmcp.CallToolResult,
) *httptest.Server {
	t.Helper()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "exa-test", Version: "v0"}, nil)
	sdkmcp.AddTool(
		server,
		&sdkmcp.Tool{Name: exaMCPSearchTool},
		func(
			_ context.Context,
			_ *sdkmcp.CallToolRequest,
			args exaMCPSearchArguments,
		) (*sdkmcp.CallToolResult, any, error) {
			return handle(args), nil, nil
		},
	)
	httpServer := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(
		func(*http.Request) *sdkmcp.Server { return server },
		&sdkmcp.StreamableHTTPOptions{JSONResponse: jsonResponse},
	))
	t.Cleanup(httpServer.Close)
	return httpServer
}

func (t rewriteExaTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	clone.Host = t.target.Host
	return t.base.RoundTrip(clone)
}

func TestExaProviderDirectSearchMapsRequestAndResponse(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "test-key" {
			t.Errorf("expected x-api-key header, got %q", r.Header.Get("X-Api-Key"))
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = w.Write(
			[]byte(
				`{"results":[{"url":"https://go.dev/blog/x","title":"Go Blog","text":"snippet text",` +
					`"publishedDate":"2026-01-02"}]}`,
			),
		)
	}))
	defer server.Close()

	provider := exaProviderForServer(t, server, "test-key")
	resp, err := provider.Search(
		context.Background(),
		SearchRequest{Query: "go generics", NumResults: 3, Recency: "week", Domains: []string{"go.dev", "-example.com"}},
	)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if resp.Provider != "exa" {
		t.Fatalf("provider = %q, want exa", resp.Provider)
	}
	if len(resp.Results) != 1 || resp.Results[0].URL != "https://go.dev/blog/x" ||
		resp.Results[0].Snippet != "snippet text" {
		t.Fatalf("unexpected results: %+v", resp.Results)
	}
	if captured["query"] != "go generics" {
		t.Fatalf("query = %v", captured["query"])
	}
	if captured["numResults"] != float64(3) {
		t.Fatalf("numResults = %v", captured["numResults"])
	}
	if captured["startPublishedDate"] == nil {
		t.Fatal("expected startPublishedDate for recency=week")
	}
	include, _ := captured["includeDomains"].([]any)
	exclude, _ := captured["excludeDomains"].([]any)
	if len(include) != 1 || include[0] != "go.dev" || len(exclude) != 1 || exclude[0] != "example.com" {
		t.Fatalf("domains include=%v exclude=%v", include, exclude)
	}
}

func TestExaProviderDoesNotFollowRedirects(t *testing.T) {
	requests := 0
	provider := ExaProvider{
		APIKey: "test-key",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			return &http.Response{
				StatusCode: http.StatusTemporaryRedirect,
				Header:     http.Header{"Location": {"https://169.254.169.254/latest/meta-data/"}},
				Body:       http.NoBody,
				Request:    req,
			}, nil
		})},
	}

	_, err := provider.Search(context.Background(), SearchRequest{Query: "anything", NumResults: 1})
	if err == nil {
		t.Fatal("expected redirect response to fail")
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestExaProviderKeylessUsesAdvancedMCPFilters(t *testing.T) {
	var captured exaMCPSearchArguments
	server := newExaMCPServer(t, true, func(args exaMCPSearchArguments) *sdkmcp.CallToolResult {
		captured = args
		payload := `{"results":[{"url":"https://example.org/a","title":"A","text":"alpha"}]}`
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: payload}},
		}
	})

	provider := exaProviderForServer(t, server, "")
	resp, err := provider.Search(
		context.Background(),
		SearchRequest{Query: "rust async", NumResults: 4, Recency: "week", Domains: []string{"docs.rs", "-spam.com"}},
	)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if resp.Provider != "exa" {
		t.Fatalf("provider = %q, want exa", resp.Provider)
	}
	if len(resp.Results) != 1 || resp.Results[0].URL != "https://example.org/a" {
		t.Fatalf("unexpected results: %+v", resp.Results)
	}
	if captured.Query != "rust async" || captured.Type != "auto" || captured.NumResults != 4 ||
		captured.TextMaxCharacters != exaSnippetChars {
		t.Fatalf("unexpected MCP search arguments: %+v", captured)
	}
	if len(captured.IncludeDomains) != 1 || captured.IncludeDomains[0] != "docs.rs" ||
		len(captured.ExcludeDomains) != 1 || captured.ExcludeDomains[0] != "spam.com" ||
		captured.StartPublishedDate == "" {
		t.Fatalf("MCP filters = %+v", captured)
	}
}

func TestExaProviderKeylessSupportsSSE(t *testing.T) {
	server := newExaMCPServer(t, false, func(exaMCPSearchArguments) *sdkmcp.CallToolResult {
		inner := `{"results":[{"url":"https://example.org/sse","title":"SSE","text":"via sse"}]}`
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: inner}},
		}
	})

	provider := exaProviderForServer(t, server, "")
	resp, err := provider.Search(context.Background(), SearchRequest{Query: "anything", NumResults: 1})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].URL != "https://example.org/sse" {
		t.Fatalf("unexpected results: %+v", resp.Results)
	}
}

func TestExaProviderKeylessSurfacesMCPToolErrors(t *testing.T) {
	server := newExaMCPServer(t, true, func(exaMCPSearchArguments) *sdkmcp.CallToolResult {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "search was rejected"}},
			IsError: true,
		}
	})

	provider := exaProviderForServer(t, server, "")
	_, err := provider.Search(context.Background(), SearchRequest{Query: "anything", NumResults: 1})
	providerErr, ok := AsProviderError(err)
	if !ok || providerErr.Code != ErrorCodeProviderFailed || providerErr.Retryable {
		t.Fatalf("expected non-retryable MCP tool error, got %v", err)
	}
}

func TestExaProviderKeylessMapsHTTPRateLimit(t *testing.T) {
	mcpServer := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "exa-test", Version: "v0"}, nil)
	handler := sdkmcp.NewStreamableHTTPHandler(
		func(*http.Request) *sdkmcp.Server { return mcpServer },
		&sdkmcp.StreamableHTTPOptions{JSONResponse: true},
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var request struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(body, &request) == nil && request.Method == "tools/call" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32000,"message":"rate limited"},"id":null}`))
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)

	provider := exaProviderForServer(t, server, "")
	_, err := provider.Search(context.Background(), SearchRequest{Query: "anything", NumResults: 1})
	providerErr, ok := AsProviderError(err)
	if !ok || providerErr.Code != ErrorCodeRateLimited || !providerErr.Retryable {
		t.Fatalf("expected retryable HTTP rate-limit error, got %v", err)
	}
}

func TestExaProviderKeylessCapsMCPResponses(t *testing.T) {
	server := newExaMCPServer(t, true, func(exaMCPSearchArguments) *sdkmcp.CallToolResult {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: strings.Repeat("x", exaMaxResponseSize)}},
		}
	})

	provider := exaProviderForServer(t, server, "")
	_, err := provider.Search(context.Background(), SearchRequest{Query: "anything", NumResults: 1})
	providerErr, ok := AsProviderError(err)
	if !ok || providerErr.Code != ErrorCodeProviderFailed {
		t.Fatalf("expected capped provider failure, got %v", err)
	}
}

func TestExaProviderErrorMapping(t *testing.T) {
	cases := []struct {
		status    int
		wantCode  string
		retryable bool
	}{
		{http.StatusTooManyRequests, ErrorCodeRateLimited, true},
		{http.StatusUnauthorized, ErrorCodeProviderFailed, false},
		{http.StatusBadRequest, ErrorCodeProviderFailed, false},
	}
	for _, tc := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		}))
		provider := exaProviderForServer(t, server, "k")
		_, err := provider.Search(context.Background(), SearchRequest{Query: "q", NumResults: 1})
		server.Close()
		providerErr, ok := AsProviderError(err)
		if !ok {
			t.Fatalf("status %d: expected ProviderError, got %v", tc.status, err)
		}
		if providerErr.Code != tc.wantCode || providerErr.Retryable != tc.retryable {
			t.Fatalf(
				"status %d: got code=%s retryable=%v, want code=%s retryable=%v",
				tc.status,
				providerErr.Code,
				providerErr.Retryable,
				tc.wantCode,
				tc.retryable,
			)
		}
	}
}

func TestExaProviderDoesNotImmediatelyRetryRateLimit(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	provider := exaProviderForServer(t, server, "k")
	_, err := provider.Search(context.Background(), SearchRequest{Query: "q", NumResults: 1})
	providerErr, ok := AsProviderError(err)
	if !ok || providerErr.Code != ErrorCodeRateLimited || !providerErr.Retryable {
		t.Fatalf("expected model-retryable rate limit error, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("rate limit attempts = %d, want no immediate retry", attempts)
	}
}

func TestExaProviderRetriesServerErrorsOnce(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"url":"https://ok.example","title":"OK","text":"fine"}]}`))
	}))
	defer server.Close()

	provider := exaProviderForServer(t, server, "k")
	resp, err := provider.Search(context.Background(), SearchRequest{Query: "q", NumResults: 1})
	if err != nil {
		t.Fatalf("search after retry: %v", err)
	}
	if attempts != 2 || len(resp.Results) != 1 {
		t.Fatalf("attempts=%d results=%d", attempts, len(resp.Results))
	}
}

func TestExaProviderTruncatesSnippetsOnRuneBoundary(t *testing.T) {
	text := strings.Repeat("é", exaSnippetChars+5)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).
			Encode(map[string]any{"results": []map[string]any{{"url": "https://example.com", "title": "Unicode", "text": text}}})
	}))
	defer server.Close()

	provider := exaProviderForServer(t, server, "k")
	resp, err := provider.Search(context.Background(), SearchRequest{Query: "q", NumResults: 1})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results = %+v", resp.Results)
	}
	snippet := resp.Results[0].Snippet
	if !utf8.ValidString(snippet) || utf8.RuneCountInString(snippet) != exaSnippetChars {
		t.Fatalf("snippet valid=%v runes=%d", utf8.ValidString(snippet), utf8.RuneCountInString(snippet))
	}
}

func TestExaProviderResponseSizeCap(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"title":"`))
		_, _ = w.Write(make([]byte, exaMaxResponseSize+10))
	}))
	defer server.Close()

	provider := exaProviderForServer(t, server, "k")
	_, err := provider.Search(context.Background(), SearchRequest{Query: "q", NumResults: 1})
	providerErr, ok := AsProviderError(err)
	if !ok || !strings.Contains(providerErr.Message, "size cap") {
		t.Fatalf("expected size cap error, got %v", err)
	}
}

func TestRecencyStart(t *testing.T) {
	now := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	if start, ok := recencyStart("week", now); !ok || now.Sub(start) != 7*24*time.Hour {
		t.Fatalf("week recency wrong: %v %v", start, ok)
	}
	if _, ok := recencyStart("", now); ok {
		t.Fatal("empty recency should not produce a start date")
	}
}
