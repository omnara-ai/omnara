package webaccess

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/webaccess/telemlineage"
)

const (
	telemSearchURL = "https://router.telem.ai/v1/search"
	telemUserAgent = "omnara-telem/v0"

	// Telem fans a search out to several providers, so allow more time and a
	// larger body than a single-vendor search.
	telemRequestTimeout  = 30 * time.Second
	telemMaxResponseSize = 1024 * 1024
	// telemDomainFilterResults is how many results each provider is asked for
	// when a domain filter runs on the merged results. Some providers charge
	// more at higher counts.
	telemDomainFilterResults = 10
	telemMaxRetryAfter       = 5 * time.Second
	telemDefaultRetryWait    = time.Second

	telemAllProvidersFailed    = "all_providers_failed"
	telemNoProviderKeyCapacity = "no_provider_key_capacity"
)

// telemTier is the result tier other Telem clients use. The tier also sets the
// provider cost; see the Telem search parameters docs.
const telemTier = "default"

var defaultTelemHTTPClient = outboundhttp.NewPublicClient(
	outboundhttp.PublicClientOptions{Timeout: telemRequestTimeout},
)

// TelemProvider searches through the Telem router, which fans each query out
// to several search providers. Empty provider lists keep Telem's default
// providers.
type TelemProvider struct {
	APIKey           string
	ProvidersInclude []string
	ProvidersExclude []string
	// AutoRouting lets Telem pick the providers for each query, within the
	// include and exclude lists; "" runs the provider list for every query.
	AutoRouting string
	HTTPClient  *http.Client
}

func (p TelemProvider) Search(ctx context.Context, req SearchRequest) (SearchResponse, error) {
	if strings.TrimSpace(req.Query) == "" {
		return SearchResponse{}, &ProviderError{Code: ErrorCodeProviderFailed, Message: "query is required"}
	}
	include, exclude := splitDomains(req.Domains)
	request := p.searchRequest(req, len(include)+len(exclude) > 0)
	request.Metadata = telemlineage.Metadata(req.Lineage, "search")
	payload, err := json.Marshal(request)
	if err != nil {
		return SearchResponse{}, fmt.Errorf("marshal search request: %w", err)
	}
	status, respBody, err := p.postSearch(ctx, p.httpClient(), payload)
	if err != nil {
		return SearchResponse{}, err
	}
	if status != http.StatusOK {
		return SearchResponse{}, telemStatusError(status, respBody)
	}
	var decoded telemSearchResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return SearchResponse{}, &ProviderError{
			Code:    ErrorCodeProviderFailed,
			Message: fmt.Sprintf("decode telem response: %v", err),
		}
	}
	return SearchResponse{
		Provider: "telem",
		Results:  mergeTelemResults(decoded.PreprocessorRuns, include, exclude, req.NumResults),
	}, nil
}

type telemSearchRequest struct {
	UserInput telemUserInput     `json:"user_input"`
	Metadata  map[string]any     `json:"metadata,omitempty"`
	Search    telemSearchOptions `json:"search"`
}

type telemUserInput struct {
	Query string `json:"query"`
}

type telemSearchOptions struct {
	NumResults  int             `json:"num_results"`
	Tier        string          `json:"tier"`
	Providers   *telemProviders `json:"providers,omitempty"`
	AutoRouting string          `json:"auto_routing,omitempty"`
}

type telemProviders struct {
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

type telemSearchResponse struct {
	PreprocessorRuns []telemRun `json:"preprocessor_runs"`
}

type telemRun struct {
	Status        string `json:"status"`
	OutputPayload struct {
		Results []telemResult `json:"results"`
	} `json:"output_payload"`
}

type telemResult struct {
	URL     string  `json:"url"`
	Title   *string `json:"title"`
	Summary *string `json:"summary"`
}

type telemErrorDetail struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

func (p TelemProvider) searchRequest(req SearchRequest, domainFilter bool) telemSearchRequest {
	// num_results is per provider. A domain filter runs on the merged results,
	// so ask each provider for more rows to keep enough after it.
	numResults := req.NumResults
	if domainFilter {
		numResults = max(numResults, telemDomainFilterResults)
	}
	request := telemSearchRequest{
		UserInput: telemUserInput{Query: req.Query},
		Search: telemSearchOptions{
			NumResults:  numResults,
			Tier:        telemTier,
			AutoRouting: p.AutoRouting,
		},
	}
	if len(p.ProvidersInclude) > 0 || len(p.ProvidersExclude) > 0 {
		request.Search.Providers = &telemProviders{Include: p.ProvidersInclude, Exclude: p.ProvidersExclude}
	}
	return request
}

// mergeTelemResults interleaves the succeeded providers' results by rank so
// the top of the list mixes providers, then drops repeated URLs and results
// outside the domain filter.
func mergeTelemResults(runs []telemRun, include, exclude []string, limit int) []SearchResult {
	out := make([]SearchResult, 0, limit)
	seen := map[string]bool{}
	for rank := 0; len(out) < limit; rank++ {
		more := false
		for _, run := range runs {
			if run.Status != "succeeded" || rank >= len(run.OutputPayload.Results) {
				continue
			}
			more = true
			result := run.OutputPayload.Results[rank]
			key := telemDedupeKey(result.URL)
			if result.URL == "" || seen[key] || !urlMatchesDomains(result.URL, include, exclude) {
				continue
			}
			seen[key] = true
			out = append(out, SearchResult{
				URL:     result.URL,
				Title:   derefString(result.Title),
				Snippet: truncateString(derefString(result.Summary), exaSnippetChars),
			})
			if len(out) == limit {
				break
			}
		}
		if !more {
			break
		}
	}
	return out
}

// telemDedupeKey spells one page one way: http and https are the same, the
// host is lowercased
// without a leading www., the fragment, trailing slashes, blank and tracking
// query parameters are dropped, and the rest of the query is sorted. The port
// and path case are kept.
func telemDedupeKey(rawURL string) string {
	opaque := strings.TrimRight(strings.ToLower(strings.TrimSpace(rawURL)), "/")
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return opaque
	}
	host := strings.TrimPrefix(strings.TrimLeft(strings.ToLower(parsed.Hostname()), "."), "www.")
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := parsed.Port(); port != "" {
		host += ":" + port
	}
	key := "https://" + host + strings.TrimRight(parsed.EscapedPath(), "/")
	if query := canonicalQuery(parsed.RawQuery); query != "" {
		key += "?" + query
	}
	return key
}

// canonicalQuery splits on "&" only and keeps pairs that hold ";" or a bad
// escape, which url.ParseQuery would drop, so such URLs do not collapse into
// one key.
func canonicalQuery(rawQuery string) string {
	var pairs []string
	for _, pair := range strings.Split(rawQuery, "&") {
		name, value, _ := strings.Cut(pair, "=")
		name, value = queryUnescapeOrRaw(name), queryUnescapeOrRaw(value)
		lower := strings.ToLower(name)
		if value == "" || strings.HasPrefix(lower, "utm_") || lower == "fbclid" || lower == "gclid" {
			continue
		}
		pairs = append(pairs, url.QueryEscape(name)+"="+url.QueryEscape(value))
	}
	slices.Sort(pairs)
	return strings.Join(pairs, "&")
}

func queryUnescapeOrRaw(value string) string {
	if unescaped, err := url.QueryUnescape(value); err == nil {
		return unescaped
	}
	return value
}

func urlMatchesDomains(rawURL string, include, exclude []string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	for _, domain := range exclude {
		if hostInDomain(host, domain) {
			return false
		}
	}
	if len(include) == 0 {
		return true
	}
	for _, domain := range include {
		if hostInDomain(host, domain) {
			return true
		}
	}
	return false
}

func hostInDomain(host, domain string) bool {
	domain = normalizeDomain(domain)
	return domain != "" && (host == domain || strings.HasSuffix(host, "."+domain))
}

// normalizeDomain reduces a model-written domain entry such as
// "https://www.arxiv.org/abs", "*.gov" or "example.com:8080" to a bare host.
func normalizeDomain(entry string) string {
	entry = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(entry)), "*.")
	if !strings.Contains(entry, "://") {
		entry = "//" + entry
	}
	parsed, err := url.Parse(entry)
	if err != nil {
		return ""
	}
	host := strings.TrimPrefix(parsed.Hostname(), "*.")
	return strings.TrimSuffix(strings.TrimPrefix(host, "www."), ".")
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// postSearch sends one search and retries it once when the search did not
// run. err is only for failures without an API response.
func (p TelemProvider) postSearch(ctx context.Context, client *http.Client, payload []byte) (int, []byte, error) {
	status, header, body, err := p.postSearchOnce(ctx, client, payload)
	if err != nil {
		return 0, nil, err
	}
	wait, retry := telemRetryAfter(status, header, body)
	if !retry {
		return status, body, nil
	}
	select {
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	case <-time.After(wait):
	}
	status, _, body, err = p.postSearchOnce(ctx, client, payload)
	return status, body, err
}

func (p TelemProvider) postSearchOnce(
	ctx context.Context,
	client *http.Client,
	payload []byte,
) (int, http.Header, []byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, telemRequestTimeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, telemSearchURL, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("build search request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)
	httpReq.Header.Set("User-Agent", telemUserAgent)
	resp, err := client.Do(httpReq)
	if err != nil {
		// Not retryable even on a timeout: the search may already have run.
		return 0, nil, nil, &ProviderError{
			Code:    ErrorCodeProviderFailed,
			Message: fmt.Sprintf("telem request failed: %v", err),
		}
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, telemMaxResponseSize+1))
	if err != nil {
		return 0, nil, nil, &ProviderError{
			Code:    ErrorCodeProviderFailed,
			Message: fmt.Sprintf("read telem response: %v", err),
		}
	}
	if len(body) > telemMaxResponseSize {
		return 0, nil, nil, &ProviderError{Code: ErrorCodeProviderFailed, Message: "telem response exceeded size cap"}
	}
	return resp.StatusCode, resp.Header, body, nil
}

// telemRetryAfter reports whether a failed search may be retried here and
// after how long. Only failures where the search did not run qualify:
// a 429, a 503 with a string detail (nothing ran), and all_providers_failed
// for no_provider_key_capacity. Waits over telemMaxRetryAfter go back to the
// model instead.
func telemRetryAfter(status int, header http.Header, body []byte) (time.Duration, bool) {
	if !telemRetryable(status, body) {
		return 0, false
	}
	wait := telemDefaultRetryWait
	if seconds, err := strconv.Atoi(header.Get("Retry-After")); err == nil && seconds >= 0 {
		wait = time.Duration(seconds) * time.Second
	}
	return wait, wait <= telemMaxRetryAfter
}

func telemRetryable(status int, body []byte) bool {
	switch status {
	case http.StatusTooManyRequests:
		return true
	case http.StatusServiceUnavailable:
		_, detail := telemErrorDetails(body)
		return detail.Code == "" ||
			(detail.Code == telemAllProvidersFailed && detail.Reason == telemNoProviderKeyCapacity)
	default:
		return false
	}
}

// telemErrorDetails reads FastAPI's detail, which is a string or an object.
func telemErrorDetails(body []byte) (string, telemErrorDetail) {
	var envelope struct {
		Detail json.RawMessage `json:"detail"`
	}
	_ = json.Unmarshal(body, &envelope)
	var text string
	var detail telemErrorDetail
	if json.Unmarshal(envelope.Detail, &text) != nil {
		_ = json.Unmarshal(envelope.Detail, &detail)
		text = strings.TrimSpace(detail.Code + " " + detail.Reason)
	}
	return text, detail
}

func telemStatusError(status int, body []byte) error {
	text, _ := telemErrorDetails(body)
	message := fmt.Sprintf("telem request failed (status %d)", status)
	if text != "" {
		message += ": " + text
	}
	code := ErrorCodeProviderFailed
	if status == http.StatusTooManyRequests {
		code = ErrorCodeRateLimited
	}
	return &ProviderError{Code: code, Message: message, Retryable: telemRetryable(status, body)}
}

func (p TelemProvider) httpClient() *http.Client {
	base := p.HTTPClient
	if base == nil {
		base = defaultTelemHTTPClient
	}
	client := *base
	if client.Timeout == 0 || client.Timeout > telemRequestTimeout {
		client.Timeout = telemRequestTimeout
	}
	return outboundhttp.CloneWithoutRedirects(&client)
}
