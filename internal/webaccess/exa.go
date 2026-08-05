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

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/textutil"
)

const (
	exaSearchURL         = "https://api.exa.ai/search"
	exaKeylessMCPBaseURL = "https://mcp.exa.ai/mcp"
	exaMCPSearchTool     = "web_search_advanced_exa"
	exaKeylessMCPURL     = exaKeylessMCPBaseURL + "?tools=" + exaMCPSearchTool
	exaMCPClientName     = "omnara-web-search"
	exaMCPClientVersion  = "v0"
	exaSearchType        = "auto"

	exaRequestTimeout  = 15 * time.Second
	exaMaxResponseSize = 256 * 1024
	exaSnippetChars    = 3000
)

var errExaMCPRateLimited = errors.New("exa mcp rate limited")

var defaultExaHTTPClient = outboundhttp.NewPublicClient(
	outboundhttp.PublicClientOptions{Timeout: exaRequestTimeout},
)

// ExaProvider uses the direct search API when configured with an API key and
// Exa's public keyless MCP endpoint otherwise.
type ExaProvider struct {
	APIKey     string
	HTTPClient *http.Client
}

func (p ExaProvider) Search(ctx context.Context, req SearchRequest) (SearchResponse, error) {
	if strings.TrimSpace(req.Query) == "" {
		return SearchResponse{}, &ProviderError{Code: ErrorCodeProviderFailed, Message: "query is required"}
	}
	if apiKey := strings.TrimSpace(p.APIKey); apiKey != "" {
		return p.searchDirect(ctx, req, apiKey)
	}
	return p.searchKeylessMCP(ctx, req)
}

func (p ExaProvider) searchDirect(ctx context.Context, req SearchRequest, apiKey string) (SearchResponse, error) {
	include, exclude := splitDomains(req.Domains)
	request := exaDirectSearchRequest{
		Query:          req.Query,
		NumResults:     req.NumResults,
		Type:           exaSearchType,
		IncludeDomains: include,
		ExcludeDomains: exclude,
		Contents: exaSearchContents{
			Text: exaSearchTextContents{MaxCharacters: exaSnippetChars},
		},
	}
	if start, ok := recencyStart(req.Recency, time.Now().UTC()); ok {
		request.StartPublishedDate = start.Format(time.RFC3339)
	}
	respBody, err := p.postSearch(ctx, apiKey, request)
	if err != nil {
		return SearchResponse{}, err
	}
	results, err := decodeExaSearchResults(respBody)
	if err != nil {
		return SearchResponse{}, &ProviderError{
			Code:    ErrorCodeProviderFailed,
			Message: fmt.Sprintf("decode exa response: %v", err),
		}
	}
	return SearchResponse{Provider: "exa", Results: results}, nil
}

func (p ExaProvider) searchKeylessMCP(ctx context.Context, req SearchRequest) (SearchResponse, error) {
	requestCtx, cancel := context.WithTimeout(ctx, exaRequestTimeout)
	defer cancel()

	client := sdkmcp.NewClient(
		&sdkmcp.Implementation{Name: exaMCPClientName, Version: exaMCPClientVersion},
		&sdkmcp.ClientOptions{Capabilities: &sdkmcp.ClientCapabilities{}},
	)
	session, err := client.Connect(requestCtx, &sdkmcp.StreamableClientTransport{
		Endpoint:             exaKeylessMCPURL,
		HTTPClient:           p.mcpHTTPClient(),
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return SearchResponse{}, exaMCPRequestError("connect", err)
	}
	defer func() { _ = session.Close() }()

	arguments := exaMCPSearchArguments{
		Query:             req.Query,
		NumResults:        req.NumResults,
		Type:              exaSearchType,
		TextMaxCharacters: exaSnippetChars,
	}
	arguments.IncludeDomains, arguments.ExcludeDomains = splitDomains(req.Domains)
	if start, ok := recencyStart(req.Recency, time.Now().UTC()); ok {
		arguments.StartPublishedDate = start.Format(time.DateOnly)
	}
	result, err := session.CallTool(requestCtx, &sdkmcp.CallToolParams{
		Name:      exaMCPSearchTool,
		Arguments: arguments,
	})
	if err != nil {
		return SearchResponse{}, exaMCPRequestError("search", err)
	}
	text, err := exaMCPResultText(result)
	if err != nil {
		return SearchResponse{}, err
	}
	results, err := decodeExaSearchResults([]byte(text))
	if err != nil {
		return SearchResponse{}, &ProviderError{
			Code:    ErrorCodeProviderFailed,
			Message: fmt.Sprintf("decode exa mcp response: %v", err),
		}
	}
	return SearchResponse{Provider: "exa", Results: results}, nil
}

type exaDirectSearchRequest struct {
	Query              string            `json:"query"`
	NumResults         int               `json:"numResults"`
	Type               string            `json:"type"`
	Contents           exaSearchContents `json:"contents"`
	IncludeDomains     []string          `json:"includeDomains,omitempty"`
	ExcludeDomains     []string          `json:"excludeDomains,omitempty"`
	StartPublishedDate string            `json:"startPublishedDate,omitempty"`
}

type exaSearchContents struct {
	Text exaSearchTextContents `json:"text"`
}

type exaSearchTextContents struct {
	MaxCharacters int `json:"maxCharacters"`
}

type exaMCPSearchArguments struct {
	Query              string   `json:"query"`
	NumResults         int      `json:"numResults"`
	Type               string   `json:"type"`
	IncludeDomains     []string `json:"includeDomains,omitempty"`
	ExcludeDomains     []string `json:"excludeDomains,omitempty"`
	StartPublishedDate string   `json:"startPublishedDate,omitempty"`
	TextMaxCharacters  int      `json:"textMaxCharacters"`
}

func (p ExaProvider) mcpHTTPClient() *http.Client {
	client := p.httpClient()
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = exaMCPTransport{base: transport}
	return client
}

func (p ExaProvider) httpClient() *http.Client {
	base := p.HTTPClient
	if base == nil {
		base = defaultExaHTTPClient
	}
	client := *base
	if client.Timeout == 0 || client.Timeout > exaRequestTimeout {
		client.Timeout = exaRequestTimeout
	}
	return outboundhttp.CloneWithoutRedirects(&client)
}

type exaMCPTransport struct {
	base http.RoundTripper
}

func (t exaMCPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		_ = resp.Body.Close()
		return nil, errExaMCPRateLimited
	}
	if resp.Body != nil {
		resp.Body = http.MaxBytesReader(nil, resp.Body, exaMaxResponseSize)
	}
	return resp, nil
}

func exaMCPResultText(result *sdkmcp.CallToolResult) (string, error) {
	if result == nil {
		return "", &ProviderError{Code: ErrorCodeProviderFailed, Message: "exa mcp returned no result"}
	}
	parts := make([]string, 0, len(result.Content))
	for _, content := range result.Content {
		if text, ok := content.(*sdkmcp.TextContent); ok && strings.TrimSpace(text.Text) != "" {
			parts = append(parts, text.Text)
		}
	}
	text := strings.Join(parts, "\n")
	if result.IsError {
		message := strings.TrimSpace(text)
		if message == "" {
			message = "exa mcp search failed"
		}
		return "", &ProviderError{Code: ErrorCodeProviderFailed, Message: message}
	}
	if len(parts) != 1 {
		return "", &ProviderError{
			Code:    ErrorCodeProviderFailed,
			Message: "exa mcp response must contain exactly one text payload",
		}
	}
	return parts[0], nil
}

func exaMCPRequestError(action string, err error) error {
	if errors.Is(err, errExaMCPRateLimited) {
		return &ProviderError{
			Code:      ErrorCodeRateLimited,
			Message:   "exa free search rate limit exceeded",
			Retryable: true,
		}
	}
	return &ProviderError{
		Code:      ErrorCodeProviderFailed,
		Message:   fmt.Sprintf("exa mcp %s failed: %v", action, err),
		Retryable: errors.Is(err, context.DeadlineExceeded),
	}
}

func (p ExaProvider) postSearch(
	ctx context.Context,
	apiKey string,
	request exaDirectSearchRequest,
) (json.RawMessage, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal search request: %w", err)
	}
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

func (p ExaProvider) postSearchOnce(
	ctx context.Context,
	client *http.Client,
	apiKey string,
	payload []byte,
) (json.RawMessage, bool, error) {
	reqCtx, cancel := context.WithTimeout(ctx, exaRequestTimeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, exaSearchURL, bytes.NewReader(payload))
	if err != nil {
		return nil, false, fmt.Errorf("build search request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Api-Key", apiKey)
	resp, err := client.Do(httpReq)
	if err != nil {
		retryable := errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil
		return nil, retryable, &ProviderError{
			Code:      ErrorCodeProviderFailed,
			Message:   fmt.Sprintf("exa request failed: %v", err),
			Retryable: retryable,
		}
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, exaMaxResponseSize+1))
	if err != nil {
		return nil, false, &ProviderError{
			Code:    ErrorCodeProviderFailed,
			Message: fmt.Sprintf("read exa response: %v", err),
		}
	}
	if len(respBody) > exaMaxResponseSize {
		return nil, false, &ProviderError{Code: ErrorCodeProviderFailed, Message: "exa response exceeded size cap"}
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, false, &ProviderError{
			Code:      ErrorCodeRateLimited,
			Message:   "exa rate limit exceeded",
			Retryable: true,
		}
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, false, &ProviderError{
			Code:    ErrorCodeProviderFailed,
			Message: fmt.Sprintf("exa auth failed (status %d)", resp.StatusCode),
		}
	case resp.StatusCode >= 500:
		return nil, true, &ProviderError{
			Code:      ErrorCodeProviderFailed,
			Message:   fmt.Sprintf("exa server error (status %d)", resp.StatusCode),
			Retryable: true,
		}
	case resp.StatusCode != http.StatusOK:
		return nil, false, &ProviderError{
			Code:    ErrorCodeProviderFailed,
			Message: fmt.Sprintf("exa request failed (status %d)", resp.StatusCode),
		}
	}
	return respBody, false, nil
}

func decodeExaSearchResults(payload []byte) ([]SearchResult, error) {
	var decoded exaSearchResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(decoded.Results))
	for _, result := range decoded.Results {
		out = append(out, SearchResult{
			URL:       result.URL,
			Title:     result.Title,
			Snippet:   truncateString(result.Text, exaSnippetChars),
			Published: result.PublishedDate,
		})
	}
	return out, nil
}

type exaSearchResponse struct {
	Results []exaSearchResult `json:"results"`
}

type exaSearchResult struct {
	URL           string `json:"url"`
	Title         string `json:"title"`
	Text          string `json:"text"`
	PublishedDate string `json:"publishedDate"`
}

func splitDomains(domains []string) (include, exclude []string) {
	for _, domain := range domains {
		domain = strings.TrimSpace(domain)
		if domain == "" || domain == "-" {
			continue
		}
		if rest, found := strings.CutPrefix(domain, "-"); found {
			exclude = append(exclude, rest)
			continue
		}
		include = append(include, domain)
	}
	return include, exclude
}

func recencyStart(recency string, now time.Time) (time.Time, bool) {
	switch recency {
	case "day":
		return now.AddDate(0, 0, -1), true
	case "week":
		return now.AddDate(0, 0, -7), true
	case "month":
		return now.AddDate(0, -1, 0), true
	case "year":
		return now.AddDate(-1, 0, 0), true
	default:
		return time.Time{}, false
	}
}

func truncateString(s string, limit int) string {
	return textutil.TruncateRunes(s, limit)
}
