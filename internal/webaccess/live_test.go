//go:build live

package webaccess

import (
	"context"
	"net/http"
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

func TestFetcherLiveFetchesOmnaraDocs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fetcher := NewFetcher(FetcherOptions{})
	for _, format := range []string{"markdown", "text"} {
		result, err := fetcher.Fetch(ctx, FetchRequest{URL: "https://docs.omnara.com/", Format: format, TimeoutSeconds: 20})
		if err != nil {
			t.Fatalf("live %s fetch: %v", format, err)
		}
		if result.StatusCode != http.StatusOK {
			t.Fatalf("%s status=%d, want 200", format, result.StatusCode)
		}
		if !strings.HasPrefix(result.FinalURL, "https://docs.omnara.com/") {
			t.Fatalf("%s final url = %q, want docs.omnara.com https URL", format, result.FinalURL)
		}
		if result.Bytes == 0 || strings.TrimSpace(result.Content) == "" {
			t.Fatalf("%s bytes=%d content=%q", format, result.Bytes, result.Content)
		}
		htmlTitled := strings.HasPrefix(result.ContentType, "text/html") && strings.Contains(result.Title, "Omnara")
		if format == "text" && !htmlTitled {
			t.Fatalf("text content type=%q title=%q, want text/html titled Omnara", result.ContentType, result.Title)
		}
		if strings.Contains(result.Content, "<script") || strings.Contains(result.Content, "<nav") {
			t.Fatalf("%s content appears to include raw page chrome: %q", format, result.Content[:min(500, len(result.Content))])
		}
	}
}
