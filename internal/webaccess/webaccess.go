// Package webaccess owns all outbound public-web access for agent tools: the
// SearchProvider interface behind the web_search tool and the SSRF-guarded
// Fetcher behind the web_fetch tool. Search providers own fixed vendor
// destinations; fetch destinations are model-controlled URLs and must only be
// reached through the Fetcher.
package webaccess

import (
	"context"
	"errors"
	"fmt"
)

// SearchRequest is a provider-neutral web search request. Handlers clamp and
// default the fields before calling a provider.
type SearchRequest struct {
	Query      string
	NumResults int      // validated as 1..20 by the handler
	Recency    string   // "", "day", "week", "month", "year"
	Domains    []string // "-" prefix excludes a domain
}

type SearchResult struct {
	URL       string `json:"url"`
	Title     string `json:"title"`
	Snippet   string `json:"snippet"`
	Published string `json:"published,omitempty"`
}

type SearchResponse struct {
	Provider string         `json:"provider"`
	Results  []SearchResult `json:"results"`
}

type SearchProvider interface {
	Search(ctx context.Context, req SearchRequest) (SearchResponse, error)
}

// ProviderError is a search/fetch failure intended to surface to the model as
// a structured ok:false tool result.
type ProviderError struct {
	Code      string
	Message   string
	Retryable bool
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func AsProviderError(err error) (*ProviderError, bool) {
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		return providerErr, true
	}
	return nil, false
}

const (
	ErrorCodeRateLimited       = "web_search_rate_limited"
	ErrorCodeProviderFailed    = "web_search_provider_failed"
	ErrorCodeFetchBlocked      = "web_fetch_address_blocked"
	ErrorCodeFetchFailed       = "web_fetch_failed"
	ErrorCodeFetchTooLarge     = "web_fetch_response_too_large"
	ErrorCodeFetchUnsupported  = "web_fetch_unsupported_content"
	ErrorCodeFetchInvalidURL   = "web_fetch_invalid_url"
	ErrorCodeFetchTimeout      = "web_fetch_timeout"
	ErrorCodeSearchUnavailable = "web_search_unconfigured"
	ErrorCodeFetchTooManyHops  = "web_fetch_too_many_redirects"
)

// BlockedAddressGuidance is appended to SSRF rejections so the model is
// redirected to the working path for machine-local services.
const BlockedAddressGuidance = "localhost and private addresses are not reachable from this tool; " +
	"use run_command (e.g. curl) on the machine where the service runs"
