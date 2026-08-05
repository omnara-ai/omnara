package webaccess

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func devFetcher() *Fetcher {
	return NewFetcher(FetcherOptions{AllowLoopback: true, UserAgent: "omnara-agent/test"})
}

func prodFetcher() *Fetcher {
	return NewFetcher(FetcherOptions{})
}

const sampleHTML = `<!DOCTYPE html>
<html><head><title>Sample Page</title></head>
<body>
<article>
<h1>Sample Heading</h1>
<p>This is the first paragraph of readable article content with enough words to satisfy readability extraction
heuristics in a small test fixture document.</p>
<p>Second paragraph mentions <a href="https://example.org/link">a link</a> and some <code>inline code</code> for the
markdown converter to handle properly.</p>
</article>
<script>console.log("should never appear")</script>
</body></html>`

const articleLikeHTML = `<!DOCTYPE html>
<html>
<head>
  <title>Product Changelog - Omnara Docs</title>
  <meta name="description" content="Release notes for Omnara web tools">
</head>
<body>
  <header><nav>Navigation Home Pricing Contact</nav></header>
  <main>
    <article>
      <h1>Web Tools Release Notes</h1>
      <p>The web access layer now supports anonymous search and guarded public fetching for agents that need current
      documentation.</p>
      <h2>Highlights</h2>
      <ul>
        <li>Search results include titles, URLs, and snippets.</li>
        <li>Markdown extraction keeps <a href="https://docs.example.com/web-tools">documentation links</a>
        readable.</li>
      </ul>
      <pre><code>web_fetch({"url":"https://example.com/docs"})</code></pre>
      <p>Footer-like article text keeps the document long enough for readability to prefer the main article.</p>
    </article>
  </main>
  <aside>Related posts that should not dominate extraction</aside>
  <script>console.log("navigation script should never appear")</script>
</body>
</html>`

func TestFetchExtractsMarkdownFromHTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != "omnara-agent/test" {
			t.Errorf("user agent = %q", got)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(sampleHTML))
	}))
	defer server.Close()

	result, err := devFetcher().Fetch(context.Background(), FetchRequest{URL: server.URL})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(result.Content, "Sample Heading") || !strings.Contains(result.Content, "first paragraph") {
		t.Fatalf("markdown missing article content: %q", result.Content)
	}
	if strings.Contains(result.Content, "should never appear") {
		t.Fatal("script content leaked into extraction")
	}
	if result.Title != "Sample Page" && result.Title != "Sample Heading" {
		t.Fatalf("unexpected title %q", result.Title)
	}
	if result.Bytes == 0 || result.Truncated {
		t.Fatalf("bytes=%d truncated=%v", result.Bytes, result.Truncated)
	}
	if result.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d", result.StatusCode)
	}
}

func TestFetchExtractsMarkdownFromArticleLikeHTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(articleLikeHTML))
	}))
	defer server.Close()

	result, err := devFetcher().Fetch(context.Background(), FetchRequest{URL: server.URL})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	for _, want := range []string{
		"Web Tools Release Notes",
		"## Highlights",
		"Search results include titles",
		"[documentation links](https://docs.example.com/web-tools)",
		"web_fetch",
	} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("markdown content missing %q:\n%s", want, result.Content)
		}
	}
	for _, unwanted := range []string{"Navigation Home", "navigation script", "Related posts"} {
		if strings.Contains(result.Content, unwanted) {
			t.Fatalf("markdown content retained non-article text %q:\n%s", unwanted, result.Content)
		}
	}
	if result.Title != "Product Changelog - Omnara Docs" && result.Title != "Web Tools Release Notes" {
		t.Fatalf("unexpected title %q", result.Title)
	}
}

func TestFetchTextFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(sampleHTML))
	}))
	defer server.Close()

	result, err := devFetcher().Fetch(context.Background(), FetchRequest{URL: server.URL, Format: "text"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if strings.Contains(result.Content, "#") || !strings.Contains(result.Content, "first paragraph") {
		t.Fatalf("unexpected text content: %q", result.Content[:min(120, len(result.Content))])
	}
}

func TestFetchPassesThroughPlainText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	result, err := devFetcher().Fetch(context.Background(), FetchRequest{URL: server.URL})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if result.Content != `{"ok":true}` {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestFetchRejectsBinaryContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 0x50, 0x4e, 0x47})
	}))
	defer server.Close()

	_, err := devFetcher().Fetch(context.Background(), FetchRequest{URL: server.URL})
	providerErr, ok := AsProviderError(err)
	if !ok || providerErr.Code != ErrorCodeFetchUnsupported {
		t.Fatalf("expected unsupported content error, got %v", err)
	}
}

func TestFetchSizeCap(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(make([]byte, fetchMaxBodyBytes+1024))
	}))
	defer server.Close()

	_, err := devFetcher().Fetch(context.Background(), FetchRequest{URL: server.URL})
	providerErr, ok := AsProviderError(err)
	if !ok || providerErr.Code != ErrorCodeFetchTooLarge {
		t.Fatalf("expected too-large error, got %v", err)
	}
}

func TestFetchFollowsRedirectsWithCap(t *testing.T) {
	var server *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/hop/", func(w http.ResponseWriter, r *http.Request) {
		var n int
		_, _ = fmt.Sscanf(r.URL.Path, "/hop/%d", &n)
		if n <= 0 {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("arrived"))
			return
		}
		http.Redirect(w, r, fmt.Sprintf("%s/hop/%d", server.URL, n-1), http.StatusFound)
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	result, err := devFetcher().Fetch(context.Background(), FetchRequest{URL: server.URL + "/hop/3"})
	if err != nil {
		t.Fatalf("fetch through 3 redirects: %v", err)
	}
	if result.Content != "arrived" || !strings.HasSuffix(result.FinalURL, "/hop/0") {
		t.Fatalf("content=%q finalURL=%q", result.Content, result.FinalURL)
	}

	_, err = devFetcher().Fetch(context.Background(), FetchRequest{URL: server.URL + "/hop/9"})
	providerErr, ok := AsProviderError(err)
	if !ok || providerErr.Code != ErrorCodeFetchTooManyHops {
		t.Fatalf("expected redirect cap error, got %v", err)
	}
}

func TestFetchSSRFBlocksPrivateAddresses(t *testing.T) {
	fetcher := prodFetcher()
	blocked := []string{
		"http://127.0.0.1:8080/",
		"http://localhost:3000/",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.5/internal",
		"http://192.168.1.1/router",
		"http://172.16.0.1/",
		"http://100.64.0.1/",
		"http://[::1]:8080/",
		"http://0.0.0.0/",
	}
	for _, target := range blocked {
		_, err := fetcher.Fetch(context.Background(), FetchRequest{URL: target, TimeoutSeconds: 5})
		providerErr, ok := AsProviderError(err)
		if !ok {
			t.Fatalf("%s: expected ProviderError, got %v", target, err)
		}
		if providerErr.Code != ErrorCodeFetchBlocked {
			t.Fatalf("%s: code = %s, want %s (err: %v)", target, providerErr.Code, ErrorCodeFetchBlocked, err)
		}
		if !strings.Contains(providerErr.Message, "run_command") {
			t.Fatalf("%s: blocked error must include run_command guidance, got %q", target, providerErr.Message)
		}
	}
}

func TestFetchSSRFBlocksRedirectToPrivateAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer server.Close()

	// Dev fetcher allows loopback (so we can reach the test server) but the
	// metadata endpoint is link-local, which stays blocked even in dev mode.
	_, err := devFetcher().Fetch(context.Background(), FetchRequest{URL: server.URL, TimeoutSeconds: 5})
	providerErr, ok := AsProviderError(err)
	if !ok || providerErr.Code != ErrorCodeFetchBlocked {
		t.Fatalf("expected blocked redirect target, got %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestFetchRejectsHTTPSRedirectToHTTPInProduction(t *testing.T) {
	client := newSSRFHTTPClient(false)
	var requests int
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if requests > 1 {
			t.Fatalf("fetch followed insecure redirect to %s", req.URL)
		}
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"http://example.com/plain"}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	})

	_, err := (&Fetcher{client: client, userAgent: "omnara-agent/test"}).Fetch(
		context.Background(),
		FetchRequest{URL: "https://example.com/start"},
	)
	providerErr, ok := AsProviderError(err)
	if !ok || providerErr.Code != ErrorCodeFetchInvalidURL {
		t.Fatalf("expected invalid-url downgrade error, got %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want exactly one before rejecting redirect", requests)
	}
}

func TestFetchSchemeValidationAndUpgrade(t *testing.T) {
	if _, err := prodFetcher().Fetch(context.Background(), FetchRequest{URL: "file:///etc/passwd"}); err == nil {
		t.Fatal("file scheme must be rejected")
	} else if providerErr, ok := AsProviderError(err); !ok || providerErr.Code != ErrorCodeFetchInvalidURL {
		t.Fatalf("expected invalid url error, got %v", err)
	}
	if _, err := prodFetcher().Fetch(context.Background(), FetchRequest{URL: "ftp://example.com/x"}); err == nil {
		t.Fatal("ftp scheme must be rejected")
	}
	upgraded, err := normalizeFetchURL("http://example.com/page", false)
	if err != nil || upgraded != "https://example.com/page" {
		t.Fatalf("https upgrade: %q %v", upgraded, err)
	}
	kept, err := normalizeFetchURL("http://example.com/page", true)
	if err != nil || kept != "http://example.com/page" {
		t.Fatalf("dev mode should keep http: %q %v", kept, err)
	}
}

func TestFetchTimeoutMapsToRetryableProviderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	_, err := devFetcher().Fetch(context.Background(), FetchRequest{URL: server.URL, TimeoutSeconds: 1})
	providerErr, ok := AsProviderError(err)
	if !ok || providerErr.Code != ErrorCodeFetchTimeout || !providerErr.Retryable {
		t.Fatalf("expected retryable timeout error, got %v", err)
	}
}

func TestFetchTruncatesExtractedContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("a", fetchExtractedCapChars+500)))
	}))
	defer server.Close()

	result, err := devFetcher().Fetch(context.Background(), FetchRequest{URL: server.URL})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !result.Truncated || len(result.Content) != fetchExtractedCapChars {
		t.Fatalf("truncated=%v len=%d", result.Truncated, len(result.Content))
	}
}

func TestFetchTruncatesExtractedContentOnRuneBoundary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("é", fetchExtractedCapChars+5)))
	}))
	defer server.Close()

	result, err := devFetcher().Fetch(context.Background(), FetchRequest{URL: server.URL})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !result.Truncated || utf8.RuneCountInString(result.Content) != fetchExtractedCapChars ||
		!utf8.ValidString(result.Content) {
		t.Fatalf(
			"truncated=%v runes=%d valid=%v",
			result.Truncated,
			utf8.RuneCountInString(result.Content),
			utf8.ValidString(result.Content),
		)
	}
}
