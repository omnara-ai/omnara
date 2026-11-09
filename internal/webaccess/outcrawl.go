package webaccess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/outboundhttp"
)

const (
	outcrawlSearchURL = "https://api.outcrawl.ai/v1/search"

	outcrawlRequestTimeout  = 15 * time.Second
	outcrawlMaxResponseSize = 256 * 1024
	outcrawlSnippetChars    = 3000
	outcrawlMaxErrorCodeLen = 64
)

var defaultOutcrawlHTTPClient = outboundhttp.NewPublicClient(
	outboundhttp.PublicClientOptions{Timeout: outcrawlRequestTimeout},
)

// OutcrawlProvider searches the web through the Outcrawl search API. It needs
// an API key; the search endpoint accepts a query and a result limit, so domain
// filters are sent as site: operators in the query and a recency window is
// rejected instead of being silently ignored.
type OutcrawlProvider struct {
	APIKey     string
	HTTPClient *http.Client
}

func (p OutcrawlProvider) Search(ctx context.Context, req SearchRequest) (SearchResponse, error) {
	if strings.TrimSpace(req.Query) == "" {
		return SearchResponse{}, &ProviderError{Code: ErrorCodeProviderFailed, Message: "query is required"}
	}
	apiKey := strings.TrimSpace(p.APIKey)
	if apiKey == "" {
		return SearchResponse{}, &ProviderError{
			Code:    ErrorCodeSearchUnavailable,
			Message: "outcrawl api key is not configured",
		}
	}
	if req.Recency != "" {
		return SearchResponse{}, &ProviderError{
			Code:    ErrorCodeProviderFailed,
			Message: "outcrawl search does not support the recency filter; retry without recency",
		}
	}
	payload, err := json.Marshal(outcrawlSearchRequest{
		Query: outcrawlQuery(req.Query, req.Domains),
		Limit: req.NumResults,
	})
	if err != nil {
		return SearchResponse{}, fmt.Errorf("marshal search request: %w", err)
	}
	respBody, err := p.postSearch(ctx, apiKey, payload)
	if err != nil {
		return SearchResponse{}, err
	}
	results, err := decodeOutcrawlSearchResults(respBody)
	if err != nil {
		return SearchResponse{}, &ProviderError{
			Code:    ErrorCodeProviderFailed,
			Message: fmt.Sprintf("decode outcrawl response: %v", err),
		}
	}
	return SearchResponse{Provider: "outcrawl", Results: results}, nil
}

func (p OutcrawlProvider) httpClient() *http.Client {
	base := p.HTTPClient
	if base == nil {
		base = defaultOutcrawlHTTPClient
	}
	client := *base
	if client.Timeout == 0 || client.Timeout > outcrawlRequestTimeout {
		client.Timeout = outcrawlRequestTimeout
	}
	return outboundhttp.CloneWithoutRedirects(&client)
}

func (p OutcrawlProvider) postSearch(ctx context.Context, apiKey string, payload []byte) (json.RawMessage, error) {
	client := p.httpClient()
	var lastErr error
	for attempt := range 2 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		respBody, retryable, err := p.postSearchOnce(ctx, client, apiKey, payload)
		if err == nil {
			return respBody, nil
		}
		lastErr = err
		if !retryable {
			return nil, err
		}
	}
	return nil, lastErr
}

func (p OutcrawlProvider) postSearchOnce(
	ctx context.Context,
	client *http.Client,
	apiKey string,
	payload []byte,
) (json.RawMessage, bool, error) {
	reqCtx, cancel := context.WithTimeout(ctx, outcrawlRequestTimeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, outcrawlSearchURL, bytes.NewReader(payload))
	if err != nil {
		return nil, false, fmt.Errorf("build search request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(httpReq)
	if err != nil {
		retryable := errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil
		return nil, retryable, &ProviderError{
			Code:      ErrorCodeProviderFailed,
			Message:   fmt.Sprintf("outcrawl request failed: %v", err),
			Retryable: retryable,
		}
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, outcrawlMaxResponseSize+1))
	if err != nil {
		return nil, false, &ProviderError{
			Code:    ErrorCodeProviderFailed,
			Message: fmt.Sprintf("read outcrawl response: %v", err),
		}
	}
	if len(respBody) > outcrawlMaxResponseSize {
		return nil, false, &ProviderError{Code: ErrorCodeProviderFailed, Message: "outcrawl response exceeded size cap"}
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, false, &ProviderError{
			Code:      ErrorCodeRateLimited,
			Message:   "outcrawl rate limit exceeded",
			Retryable: true,
		}
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, false, &ProviderError{
			Code:    ErrorCodeProviderFailed,
			Message: fmt.Sprintf("outcrawl auth failed (status %d)", resp.StatusCode),
		}
	case resp.StatusCode >= 500:
		return nil, true, &ProviderError{
			Code:      ErrorCodeProviderFailed,
			Message:   fmt.Sprintf("outcrawl server error (status %d)", resp.StatusCode),
			Retryable: true,
		}
	case resp.StatusCode != http.StatusOK:
		return nil, false, &ProviderError{
			Code:    ErrorCodeProviderFailed,
			Message: outcrawlStatusMessage(resp.StatusCode, respBody),
		}
	}
	return respBody, false, nil
}

// outcrawlStatusMessage names the error code Outcrawl returns in its
// {"code","message"} error body so the model can tell a bad request from an
// exhausted plan.
func outcrawlStatusMessage(status int, body []byte) string {
	var decoded struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &decoded); err == nil && decoded.Code != "" &&
		len(decoded.Code) <= outcrawlMaxErrorCodeLen {
		return fmt.Sprintf("outcrawl request failed (status %d, %s)", status, decoded.Code)
	}
	return fmt.Sprintf("outcrawl request failed (status %d)", status)
}

// outcrawlQuery encodes domain filters as search operators. With any filter the
// original query is wrapped in parentheses so operators inside it cannot bind
// to the domain limit, included domains are OR-ed in one group (a union, as the
// Exa provider treats them), and each excluded domain gets a -site: prefix. A
// "-" prefix on a domain excludes it.
func outcrawlQuery(query string, domains []string) string {
	include, exclude := splitDomains(domains)
	if len(include) == 0 && len(exclude) == 0 {
		return query
	}
	var b strings.Builder
	b.WriteString("(")
	b.WriteString(query)
	b.WriteString(")")
	switch len(include) {
	case 0:
	case 1:
		b.WriteString(" site:")
		b.WriteString(include[0])
	default:
		b.WriteString(" (")
		for i, domain := range include {
			if i > 0 {
				b.WriteString(" OR ")
			}
			b.WriteString("site:")
			b.WriteString(domain)
		}
		b.WriteString(")")
	}
	for _, domain := range exclude {
		b.WriteString(" -site:")
		b.WriteString(domain)
	}
	return b.String()
}

func decodeOutcrawlSearchResults(payload []byte) ([]SearchResult, error) {
	var decoded outcrawlSearchResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(decoded.Hits))
	for _, hit := range decoded.Hits {
		out = append(out, SearchResult{
			URL:     hit.URL,
			Title:   hit.Title,
			Snippet: truncateString(hit.Snippet, outcrawlSnippetChars),
		})
	}
	return out, nil
}

type outcrawlSearchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type outcrawlSearchResponse struct {
	Hits []outcrawlSearchHit `json:"hits"`
}

type outcrawlSearchHit struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
}
