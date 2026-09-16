package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type toolSearchRequest struct {
	Pattern    string          `json:"pattern"`
	MaxResults json.RawMessage `json:"max_results,omitempty"`
}

type resolvedToolSearchRequest struct {
	Pattern    string
	MaxResults int
}

func validateToolSearchInput(input json.RawMessage) error {
	_, err := resolveToolSearchRequest(input)
	return err
}

func resolveToolSearchRequest(raw json.RawMessage) (resolvedToolSearchRequest, error) {
	var input toolSearchRequest
	if err := decodeSingleStrictJSON(raw, &input, "tool_search request"); err != nil {
		return resolvedToolSearchRequest{}, fmt.Errorf("parse tool_search request: %w", err)
	}
	pattern := strings.TrimSpace(input.Pattern)
	if pattern == "" {
		return resolvedToolSearchRequest{}, errors.New("pattern is required")
	}
	if len(pattern) > toolcatalog.ToolSearchMaxPatternLength {
		return resolvedToolSearchRequest{}, fmt.Errorf(
			"pattern must be at most %d characters",
			toolcatalog.ToolSearchMaxPatternLength,
		)
	}
	resolved := resolvedToolSearchRequest{Pattern: pattern, MaxResults: toolcatalog.ToolSearchDefaultResults}
	if len(input.MaxResults) != 0 {
		var maxResults *int
		if err := json.Unmarshal(input.MaxResults, &maxResults); err != nil {
			return resolvedToolSearchRequest{}, fmt.Errorf("parse max_results: %w", err)
		}
		if maxResults == nil {
			return resolvedToolSearchRequest{}, errors.New("max_results cannot be null")
		}
		if *maxResults < 1 || *maxResults > toolcatalog.ToolSearchMaxResults {
			return resolvedToolSearchRequest{}, fmt.Errorf(
				"max_results must be between 1 and %d",
				toolcatalog.ToolSearchMaxResults,
			)
		}
		resolved.MaxResults = *maxResults
	}
	return resolved, nil
}

func runToolSearch(_ context.Context, call transactionalToolContext) (transactionalPhaseResult, error) {
	request, err := resolveToolSearchRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	specs := searchableToolSpecs(call.Turn.Tools)
	search, err := modelcontext.SearchDeferredTools(specs, request.Pattern, request.MaxResults)
	if err != nil {
		content, contentErr := toolFailureContent("invalid_tool_input", err.Error(), true)
		if contentErr != nil {
			return nil, contentErr
		}
		return failInTransaction(content, err), nil
	}
	structured, err := structuredToolResultPart(search)
	if err != nil {
		return nil, err
	}
	return completeInTransaction(newToolResultContent(
		textToolResultPart(toolSearchSummary(specs, search)),
		structured,
	)), nil
}

func searchableToolSpecs(turnTools map[string]ToolSpec) []modelcontext.ToolSpec {
	names := make([]string, 0, len(turnTools))
	for name := range turnTools {
		names = append(names, name)
	}
	sort.Strings(names)
	specs := make([]modelcontext.ToolSpec, 0, len(names))
	for _, name := range names {
		tool := turnTools[name]
		specs = append(specs, modelcontext.ToolSpec{
			Name:        name,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
			Deferred:    tool.Deferred,
			Type:        tool.Type,
			Permission:  tool.Permission,
		})
	}
	return specs
}

func toolSearchSummary(specs []modelcontext.ToolSpec, search modelcontext.ToolSearchResult) string {
	if len(search.ToolNames) == 0 {
		return fmt.Sprintf(
			"No deferred tools matched %q (%d deferred tools available). Try a broader pattern.",
			search.Pattern,
			search.TotalDeferredTools,
		)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Loaded %d tool(s) matching %q:", len(search.ToolNames), search.Pattern)
	for _, name := range search.ToolNames {
		b.WriteString("\n- ")
		b.WriteString(name)
		if spec, ok := modelcontext.ToolSpecByName(specs, name); ok && spec.Description != "" {
			b.WriteString(": ")
			b.WriteString(spec.Description)
		}
	}
	return b.String()
}
