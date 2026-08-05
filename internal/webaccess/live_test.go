//go:build live

package webaccess

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestExaProviderLiveKeylessSearch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	response, err := (ExaProvider{}).Search(ctx, SearchRequest{
		Query:      "Go programming language release notes",
		NumResults: 3,
		Recency:    "year",
		Domains:    []string{"go.dev"},
	})
	if err != nil {
		if providerErr, ok := AsProviderError(err); ok && providerErr.Code == ErrorCodeRateLimited {
			t.Skipf("live keyless exa search rate limited: %v", err)
		}
		t.Fatalf("live keyless exa search: %v", err)
	}
	assertLiveExaSearchResponse(t, response)
}

func TestExaProviderLiveDirectSearch(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("EXA_API_KEY"))
	if apiKey == "" {
		t.Skip("EXA_API_KEY is not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	response, err := (ExaProvider{APIKey: apiKey}).Search(ctx, SearchRequest{
		Query:      "Go programming language release notes",
		NumResults: 3,
		Recency:    "year",
		Domains:    []string{"go.dev"},
	})
	if err != nil {
		t.Fatalf("live direct exa search: %v", err)
	}
	assertLiveExaSearchResponse(t, response)
}

func assertLiveExaSearchResponse(t *testing.T, response SearchResponse) {
	t.Helper()
	if response.Provider != "exa" {
		t.Fatalf("provider = %q, want exa", response.Provider)
	}
	if len(response.Results) == 0 {
		t.Fatal("expected at least one live search result")
	}
	for _, result := range response.Results {
		if strings.TrimSpace(result.Title) != "" || strings.TrimSpace(result.Snippet) != "" ||
			strings.TrimSpace(result.URL) != "" {
			return
		}
	}
	t.Fatalf("live search results had no usable title, snippet, or URL: %+v", response.Results)
}

func TestFetcherLiveExtractsPublicHTML(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fetcher := NewFetcher(FetcherOptions{})
	markdown, err := fetcher.Fetch(ctx, FetchRequest{URL: "https://example.com/", Format: "markdown", TimeoutSeconds: 20})
	if err != nil {
		t.Fatalf("live markdown fetch: %v", err)
	}
	if markdown.FinalURL == "" || !strings.HasPrefix(markdown.FinalURL, "https://example.com") {
		t.Fatalf("final url = %q, want example.com https URL", markdown.FinalURL)
	}
	if markdown.Bytes == 0 || markdown.Truncated {
		t.Fatalf("bytes=%d truncated=%v", markdown.Bytes, markdown.Truncated)
	}
	if !strings.Contains(markdown.Title, "Example Domain") {
		t.Fatalf("markdown title = %q, want Example Domain", markdown.Title)
	}
	if !strings.Contains(markdown.Content, "documentation examples") {
		t.Fatalf("markdown content missing expected body text: %q", markdown.Content)
	}
	if !strings.Contains(markdown.Content, "](") {
		t.Fatalf("markdown content does not appear to preserve links: %q", markdown.Content)
	}

	text, err := fetcher.Fetch(ctx, FetchRequest{URL: "https://example.com/", Format: "text", TimeoutSeconds: 20})
	if err != nil {
		t.Fatalf("live text fetch: %v", err)
	}
	if !strings.Contains(text.Title, "Example Domain") {
		t.Fatalf("text title = %q, want Example Domain", text.Title)
	}
	if !strings.Contains(text.Content, "documentation examples") {
		t.Fatalf("text content missing expected body text: %q", text.Content)
	}
	if strings.Contains(text.Content, "](") {
		t.Fatalf("text content unexpectedly contains markdown link syntax: %q", text.Content)
	}
}

func TestFetcherLiveExtractsGoDocsHTML(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fetcher := NewFetcher(FetcherOptions{})
	result, err := fetcher.Fetch(
		ctx,
		FetchRequest{URL: "https://go.dev/doc/effective_go", Format: "markdown", TimeoutSeconds: 20},
	)
	if err != nil {
		t.Fatalf("live go docs fetch: %v", err)
	}
	if result.FinalURL == "" || !strings.HasPrefix(result.FinalURL, "https://go.dev/doc/effective_go") {
		t.Fatalf("final url = %q, want go.dev effective go URL", result.FinalURL)
	}
	if result.Bytes == 0 {
		t.Fatalf("bytes=%d", result.Bytes)
	}
	if !strings.Contains(result.Title, "Effective Go") {
		t.Fatalf("title = %q, want Effective Go", result.Title)
	}
	for _, want := range []string{"Introduction", "Formatting", "gofmt"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("markdown content missing %q", want)
		}
	}
	if strings.Contains(result.Content, "<script") || strings.Contains(result.Content, "<nav") {
		t.Fatalf("markdown content appears to include raw page chrome: %q", result.Content[:min(500, len(result.Content))])
	}
}
