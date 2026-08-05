package webaccess

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/textutil"
)

const (
	fetchMaxBodyBytes      = 5_242_880 // 5MB raw download cap
	fetchMaxRedirects      = 5
	fetchExtractedCapChars = 100_000
)

const (
	DefaultFetchTimeoutSeconds = 30
	MaxFetchTimeoutSeconds     = 120
)

// ErrBlockedAddress marks SSRF rejections (loopback, private, link-local, and
// other special-use addresses).
var ErrBlockedAddress = ssrf.ErrBlockedAddress

type FetcherOptions struct {
	// AllowLoopback opens loopback targets and disables the http→https
	// upgrade for local development (wired from the same dev flag as the MCP
	// transport).
	AllowLoopback bool
	UserAgent     string
}

// Fetcher retrieves model-controlled public URLs. Every connection — including
// each redirect hop — dials through the SSRF guard with the validated IP
// pinned, so DNS rebinding and redirect tricks cannot reach private addresses.
type Fetcher struct {
	client            *http.Client
	userAgent         string
	allowInsecureHTTP bool
}

type FetchRequest struct {
	URL            string
	Format         string // "markdown" (default) or "text"
	TimeoutSeconds int    // clamped 1..120, default 30
}

type FetchResult struct {
	URL         string `json:"url"`
	FinalURL    string `json:"final_url"`
	Title       string `json:"title,omitempty"`
	StatusCode  int    `json:"status_code"`
	ContentType string `json:"content_type"`
	Content     string `json:"-"`
	Bytes       int64  `json:"bytes"`
	Truncated   bool   `json:"truncated"`
}

func NewFetcher(opts FetcherOptions) *Fetcher {
	userAgent := opts.UserAgent
	if userAgent == "" {
		userAgent = "omnara-agent/1.0"
	}
	return &Fetcher{
		client:            newSSRFHTTPClient(opts.AllowLoopback),
		userAgent:         userAgent,
		allowInsecureHTTP: opts.AllowLoopback,
	}
}

func newSSRFHTTPClient(allowLoopback bool) *http.Client {
	client := outboundhttp.NewPublicClient(outboundhttp.PublicClientOptions{AllowLoopback: allowLoopback})
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= fetchMaxRedirects {
			return &ProviderError{
				Code:    ErrorCodeFetchTooManyHops,
				Message: fmt.Sprintf("stopped after %d redirects", fetchMaxRedirects),
			}
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return &ProviderError{
				Code:    ErrorCodeFetchInvalidURL,
				Message: fmt.Sprintf("redirect to unsupported scheme %q", req.URL.Scheme),
			}
		}
		if !allowLoopback && req.URL.Scheme == "http" {
			return &ProviderError{
				Code:    ErrorCodeFetchInvalidURL,
				Message: "redirect to insecure http url is not allowed",
			}
		}
		return nil
	}
	return client
}

func (f *Fetcher) Fetch(ctx context.Context, req FetchRequest) (FetchResult, error) {
	target, err := normalizeFetchURL(req.URL, f.allowInsecureHTTP)
	if err != nil {
		return FetchResult{}, err
	}
	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = DefaultFetchTimeoutSeconds * time.Second
	}
	if timeout > MaxFetchTimeoutSeconds*time.Second {
		timeout = MaxFetchTimeoutSeconds * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target, nil)
	if err != nil {
		return FetchResult{}, &ProviderError{
			Code:    ErrorCodeFetchInvalidURL,
			Message: fmt.Sprintf("invalid url: %v", err),
		}
	}
	httpReq.Header.Set("User-Agent", f.userAgent)
	httpReq.Header.Set(
		"Accept",
		"text/html,application/xhtml+xml,text/plain;q=0.9,text/markdown;q=0.9,application/json;q=0.8,*/*;q=0.5",
	)

	resp, err := f.client.Do(httpReq)
	if err != nil {
		return FetchResult{}, mapFetchError(err, reqCtx)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.ContentLength > fetchMaxBodyBytes {
		return FetchResult{}, &ProviderError{
			Code: ErrorCodeFetchTooLarge,
			Message: fmt.Sprintf(
				"response of %d bytes exceeds the %d byte limit",
				resp.ContentLength,
				fetchMaxBodyBytes,
			),
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return FetchResult{}, &ProviderError{
			Code:    ErrorCodeFetchFailed,
			Message: fmt.Sprintf("request failed with status %d", resp.StatusCode),
		}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchMaxBodyBytes+1))
	if err != nil {
		return FetchResult{}, mapFetchError(err, reqCtx)
	}
	if int64(len(body)) > fetchMaxBodyBytes {
		return FetchResult{}, &ProviderError{
			Code:    ErrorCodeFetchTooLarge,
			Message: fmt.Sprintf("response exceeded the %d byte limit", fetchMaxBodyBytes),
		}
	}

	finalURL := target
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	contentType := resp.Header.Get("Content-Type")
	extracted, err := extractContent(body, contentType, finalURL, req.Format)
	if err != nil {
		return FetchResult{}, err
	}
	content := extracted.Content
	truncated := false
	if len(content) > fetchExtractedCapChars {
		content = textutil.TruncateRunes(content, fetchExtractedCapChars)
		truncated = true
	}
	return FetchResult{
		URL:         req.URL,
		FinalURL:    finalURL,
		Title:       extracted.Title,
		StatusCode:  resp.StatusCode,
		ContentType: contentType,
		Content:     content,
		Bytes:       int64(len(body)),
		Truncated:   truncated,
	}, nil
}

func normalizeFetchURL(raw string, allowInsecureHTTP bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", &ProviderError{Code: ErrorCodeFetchInvalidURL, Message: fmt.Sprintf("invalid url: %v", err)}
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		if !allowInsecureHTTP {
			parsed.Scheme = "https"
		}
	default:
		return "", &ProviderError{
			Code:    ErrorCodeFetchInvalidURL,
			Message: fmt.Sprintf("unsupported scheme %q: only http and https URLs can be fetched", parsed.Scheme),
		}
	}
	if parsed.Host == "" {
		return "", &ProviderError{Code: ErrorCodeFetchInvalidURL, Message: "url host is required"}
	}
	return parsed.String(), nil
}

func mapFetchError(err error, ctx context.Context) error {
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		return providerErr
	}
	if errors.Is(err, ErrBlockedAddress) {
		return &ProviderError{Code: ErrorCodeFetchBlocked, Message: BlockedAddressGuidance}
	}
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return &ProviderError{Code: ErrorCodeFetchTimeout, Message: "fetch timed out", Retryable: true}
	}
	return &ProviderError{Code: ErrorCodeFetchFailed, Message: fmt.Sprintf("fetch failed: %v", err)}
}
