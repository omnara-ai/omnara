package webaccess

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"
)

func outcrawlProviderForServer(t *testing.T, server *httptest.Server, apiKey string) OutcrawlProvider {
	t.Helper()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse Outcrawl test server URL: %v", err)
	}
	base := server.Client().Transport
	return OutcrawlProvider{
		APIKey: apiKey,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			clone := req.Clone(req.Context())
			clone.URL.Scheme = target.Scheme
			clone.URL.Host = target.Host
			clone.Host = target.Host
			return base.RoundTrip(clone)
		})},
	}
}

func TestOutcrawlProviderSearchMapsRequestAndResponse(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/search" {
			t.Errorf("request = %s %s, want POST /v1/search", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("expected bearer authorization, got %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(
			`{"hits":[{"url":"https://go.dev/blog/x","title":"Go Blog","snippet":"snippet text","rank":1}],` +
				`"usage":{"credits":"1"}}`,
		))
	}))
	defer server.Close()

	provider := outcrawlProviderForServer(t, server, "test-key")
	resp, err := provider.Search(
		context.Background(),
		SearchRequest{Query: "go generics", NumResults: 3, Domains: []string{"go.dev", "-example.com"}},
	)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if resp.Provider != "outcrawl" {
		t.Fatalf("provider = %q, want outcrawl", resp.Provider)
	}
	if len(resp.Results) != 1 || resp.Results[0].URL != "https://go.dev/blog/x" ||
		resp.Results[0].Title != "Go Blog" || resp.Results[0].Snippet != "snippet text" {
		t.Fatalf("unexpected results: %+v", resp.Results)
	}
	if captured["query"] != "(go generics) site:go.dev -site:example.com" {
		t.Fatalf("query = %v", captured["query"])
	}
	if captured["limit"] != float64(3) {
		t.Fatalf("limit = %v", captured["limit"])
	}
	if len(captured) != 2 {
		t.Fatalf("request carried unexpected fields: %v", captured)
	}
}

func TestOutcrawlProviderRejectsRecency(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
	}))
	defer server.Close()

	provider := outcrawlProviderForServer(t, server, "k")
	_, err := provider.Search(context.Background(), SearchRequest{Query: "q", NumResults: 1, Recency: "week"})
	providerErr, ok := AsProviderError(err)
	if !ok || providerErr.Code != ErrorCodeProviderFailed || providerErr.Retryable ||
		!strings.Contains(providerErr.Message, "recency") {
		t.Fatalf("expected non-retryable recency error, got %v", err)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want none", requests)
	}
}

func TestOutcrawlProviderRequiresQueryAndKey(t *testing.T) {
	if _, err := (OutcrawlProvider{APIKey: "k"}).Search(context.Background(), SearchRequest{Query: " "}); err == nil {
		t.Fatal("expected blank query to fail")
	}
	_, err := (OutcrawlProvider{}).Search(context.Background(), SearchRequest{Query: "q", NumResults: 1})
	providerErr, ok := AsProviderError(err)
	if !ok || providerErr.Code != ErrorCodeSearchUnavailable {
		t.Fatalf("expected unconfigured error without a key, got %v", err)
	}
}

func TestOutcrawlProviderDoesNotFollowRedirects(t *testing.T) {
	requests := 0
	provider := OutcrawlProvider{
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
		t.Fatalf("requests = %d, want the redirect not to be followed", requests)
	}
}

func TestOutcrawlProviderErrorMapping(t *testing.T) {
	cases := []struct {
		status      int
		body        string
		wantCode    string
		wantMessage string
		retryable   bool
	}{
		{http.StatusTooManyRequests, ``, ErrorCodeRateLimited, "rate limit", true},
		{http.StatusUnauthorized, ``, ErrorCodeProviderFailed, "auth failed (status 401)", false},
		{
			http.StatusBadRequest,
			`{"code":"bad_request","message":"search: unexpected field(s): foo"}`,
			ErrorCodeProviderFailed,
			"status 400, bad_request",
			false,
		},
		{http.StatusPaymentRequired, `not json`, ErrorCodeProviderFailed, "status 402)", false},
	}
	for _, tc := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		provider := outcrawlProviderForServer(t, server, "k")
		_, err := provider.Search(context.Background(), SearchRequest{Query: "q", NumResults: 1})
		server.Close()
		providerErr, ok := AsProviderError(err)
		if !ok {
			t.Fatalf("status %d: expected ProviderError, got %v", tc.status, err)
		}
		if providerErr.Code != tc.wantCode || providerErr.Retryable != tc.retryable ||
			!strings.Contains(providerErr.Message, tc.wantMessage) {
			t.Fatalf(
				"status %d: got code=%s retryable=%v message=%q, want code=%s retryable=%v message containing %q",
				tc.status,
				providerErr.Code,
				providerErr.Retryable,
				providerErr.Message,
				tc.wantCode,
				tc.retryable,
				tc.wantMessage,
			)
		}
	}
}

func TestOutcrawlProviderDoesNotImmediatelyRetryRateLimit(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	provider := outcrawlProviderForServer(t, server, "k")
	_, err := provider.Search(context.Background(), SearchRequest{Query: "q", NumResults: 1})
	providerErr, ok := AsProviderError(err)
	if !ok || providerErr.Code != ErrorCodeRateLimited || !providerErr.Retryable {
		t.Fatalf("expected model-retryable rate limit error, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("rate limit attempts = %d, want no immediate retry", attempts)
	}
}

func TestOutcrawlProviderRetriesServerErrorsOnce(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"hits":[{"url":"https://ok.example","title":"OK","snippet":"fine"}]}`))
	}))
	defer server.Close()

	provider := outcrawlProviderForServer(t, server, "k")
	resp, err := provider.Search(context.Background(), SearchRequest{Query: "q", NumResults: 1})
	if err != nil {
		t.Fatalf("search after retry: %v", err)
	}
	if attempts != 2 || len(resp.Results) != 1 {
		t.Fatalf("attempts=%d results=%d", attempts, len(resp.Results))
	}
}

func TestOutcrawlProviderTruncatesSnippetsOnRuneBoundary(t *testing.T) {
	snippet := strings.Repeat("é", outcrawlSnippetChars+5)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(
			map[string]any{"hits": []map[string]any{{"url": "https://example.com", "title": "Unicode", "snippet": snippet}}},
		)
	}))
	defer server.Close()

	provider := outcrawlProviderForServer(t, server, "k")
	resp, err := provider.Search(context.Background(), SearchRequest{Query: "q", NumResults: 1})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results = %+v", resp.Results)
	}
	got := resp.Results[0].Snippet
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != outcrawlSnippetChars {
		t.Fatalf("snippet valid=%v runes=%d", utf8.ValidString(got), utf8.RuneCountInString(got))
	}
}

func TestOutcrawlProviderResponseSizeCap(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"hits":[{"title":"`))
		_, _ = w.Write(make([]byte, outcrawlMaxResponseSize+10))
	}))
	defer server.Close()

	provider := outcrawlProviderForServer(t, server, "k")
	_, err := provider.Search(context.Background(), SearchRequest{Query: "q", NumResults: 1})
	providerErr, ok := AsProviderError(err)
	if !ok || !strings.Contains(providerErr.Message, "size cap") {
		t.Fatalf("expected size cap error, got %v", err)
	}
}

func TestOutcrawlProviderRejectsInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html>not json</html>`))
	}))
	defer server.Close()

	provider := outcrawlProviderForServer(t, server, "k")
	_, err := provider.Search(context.Background(), SearchRequest{Query: "q", NumResults: 1})
	providerErr, ok := AsProviderError(err)
	if !ok || providerErr.Code != ErrorCodeProviderFailed || !strings.Contains(providerErr.Message, "decode outcrawl") {
		t.Fatalf("expected decode failure, got %v", err)
	}
}

func TestOutcrawlQueryGroupsDomainFilters(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		domains []string
		want    string
	}{
		{name: "no filters", query: "a OR b", want: "a OR b"},
		{name: "blank filters", query: "go", domains: []string{" ", "-"}, want: "go"},
		{name: "single include", query: "go", domains: []string{"go.dev"}, want: "(go) site:go.dev"},
		{
			name:    "multiple includes are a union",
			query:   "go generics",
			domains: []string{"go.dev", "github.com"},
			want:    "(go generics) (site:go.dev OR site:github.com)",
		},
		{
			name:    "query operators stay inside the group",
			query:   "a OR b",
			domains: []string{"go.dev", "github.com"},
			want:    "(a OR b) (site:go.dev OR site:github.com)",
		},
		{
			name:    "excludes only",
			query:   "go",
			domains: []string{"-example.com", "-spam.net"},
			want:    "(go) -site:example.com -site:spam.net",
		},
		{
			name:    "mixed includes and excludes",
			query:   "go",
			domains: []string{"go.dev", "github.com", "-example.com"},
			want:    "(go) (site:go.dev OR site:github.com) -site:example.com",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := outcrawlQuery(tc.query, tc.domains); got != tc.want {
				t.Fatalf("outcrawlQuery = %q, want %q", got, tc.want)
			}
		})
	}
}
