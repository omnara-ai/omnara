package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/textutil"
	"github.com/omnara-ai/omnara/internal/webaccess"
)

const (
	webSearchDefaultResults     = 5
	webSearchMaxResults         = 20
	webSearchInlineSnippetChars = 600
)

type webSearchRequest struct {
	Query      string          `json:"query"`
	NumResults json.RawMessage `json:"num_results,omitempty"`
	Recency    json.RawMessage `json:"recency,omitempty"`
	Domains    json.RawMessage `json:"domains,omitempty"`
}

type resolvedWebSearchRequest struct {
	Query      string
	NumResults int
	Recency    string
	Domains    []string
}

func validateWebSearchInput(input json.RawMessage) error {
	_, err := resolveWebSearchRequest(input)
	return err
}

func resolveWebSearchRequest(raw json.RawMessage) (resolvedWebSearchRequest, error) {
	var input webSearchRequest
	if err := decodeSingleStrictJSON(raw, &input, "web_search request"); err != nil {
		return resolvedWebSearchRequest{}, fmt.Errorf("parse web_search request: %w", err)
	}
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return resolvedWebSearchRequest{}, errors.New("query is required")
	}
	resolved := resolvedWebSearchRequest{Query: query, NumResults: webSearchDefaultResults}
	if len(input.NumResults) != 0 {
		var numResults *int
		if err := json.Unmarshal(input.NumResults, &numResults); err != nil {
			return resolvedWebSearchRequest{}, fmt.Errorf("parse num_results: %w", err)
		}
		if numResults == nil {
			return resolvedWebSearchRequest{}, errors.New("num_results cannot be null")
		}
		if *numResults < 1 || *numResults > webSearchMaxResults {
			return resolvedWebSearchRequest{}, fmt.Errorf(
				"num_results must be between 1 and %d",
				webSearchMaxResults,
			)
		}
		resolved.NumResults = *numResults
	}
	if len(input.Recency) != 0 {
		var recency *string
		if err := json.Unmarshal(input.Recency, &recency); err != nil {
			return resolvedWebSearchRequest{}, fmt.Errorf("parse recency: %w", err)
		}
		if recency == nil {
			return resolvedWebSearchRequest{}, errors.New("recency cannot be null")
		}
		switch *recency {
		case "day", "week", "month", "year":
			resolved.Recency = *recency
		default:
			return resolvedWebSearchRequest{}, fmt.Errorf(
				"recency must be one of day, week, month, year (got %q)",
				*recency,
			)
		}
	}
	if len(input.Domains) != 0 {
		var domains []string
		if err := json.Unmarshal(input.Domains, &domains); err != nil {
			return resolvedWebSearchRequest{}, fmt.Errorf("parse domains: %w", err)
		}
		for _, domain := range domains {
			if strings.TrimSpace(domain) == "" || strings.TrimSpace(domain) == "-" {
				return resolvedWebSearchRequest{}, errors.New("domains entries cannot be blank")
			}
		}
		resolved.Domains = domains
	}
	return resolved, nil
}

func runWebSearch(
	ctx context.Context,
	call asyncToolContext,
) (asyncPhaseResult, error) {
	resolved, err := resolveWebSearchRequest(call.Call.Input)
	if err != nil {
		return failWebTool(
			webaccess.ErrorCodeProviderFailed,
			err.Error(),
			false,
			err,
		)
	}
	if call.Executor.WebSearch == nil {
		message := "web search is not configured on this deployment"
		return failWebTool(
			webaccess.ErrorCodeSearchUnavailable,
			message,
			false,
			errors.New(message),
		)
	}
	response, err := call.Executor.WebSearch.Search(ctx, webaccess.SearchRequest{
		Query:      resolved.Query,
		NumResults: resolved.NumResults,
		Recency:    resolved.Recency,
		Domains:    resolved.Domains,
	})
	if err != nil {
		if providerErr, ok := webaccess.AsProviderError(err); ok {
			return failWebTool(
				providerErr.Code,
				providerErr.Message,
				providerErr.Retryable,
				providerErr,
			)
		}
		return failWebTool(
			webaccess.ErrorCodeProviderFailed,
			err.Error(),
			false,
			err,
		)
	}
	content, err := webSearchToolResultContent(resolved.Query, response)
	if err != nil {
		return nil, err
	}
	return completeAsynchronously(content), nil
}

func webSearchToolResultContent(
	query string,
	response webaccess.SearchResponse,
) (toolResultContent, error) {
	var rendered strings.Builder
	fmt.Fprintf(&rendered, "Web search results for %q:\n", query)
	if len(response.Results) == 0 {
		rendered.WriteString("No results found. Try a different query.")
	}
	for index, result := range response.Results {
		fmt.Fprintf(&rendered, "\n%d. %s\n   %s\n", index+1, result.Title, result.URL)
		snippet := strings.TrimSpace(result.Snippet)
		if snippet != "" {
			if len(snippet) > webSearchInlineSnippetChars {
				snippet = textutil.TruncateRunes(snippet, webSearchInlineSnippetChars) + "…"
			}
			fmt.Fprintf(&rendered, "   %s\n", snippet)
		}
	}
	structured := map[string]any{
		"query":    query,
		"provider": response.Provider,
		"results":  response.Results,
	}
	structuredPart, err := structuredToolResultPart(structured)
	if err != nil {
		return toolResultContent{}, err
	}
	return newToolResultContent(
		textToolResultPart(rendered.String()),
		structuredPart,
	), nil
}
