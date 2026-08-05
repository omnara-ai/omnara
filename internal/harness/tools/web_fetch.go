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

const webFetchInlineContentChars = 30_000

type webFetchRequest struct {
	URL            string          `json:"url"`
	Format         json.RawMessage `json:"format,omitempty"`
	TimeoutSeconds json.RawMessage `json:"timeout_seconds,omitempty"`
}

type resolvedWebFetchRequest struct {
	URL            string
	Format         string
	TimeoutSeconds int
}

func validateWebFetchInput(input json.RawMessage) error {
	_, err := resolveWebFetchRequest(input)
	return err
}

func resolveWebFetchRequest(raw json.RawMessage) (resolvedWebFetchRequest, error) {
	var input webFetchRequest
	if err := decodeSingleStrictJSON(raw, &input, "web_fetch request"); err != nil {
		return resolvedWebFetchRequest{}, fmt.Errorf("parse web_fetch request: %w", err)
	}
	target := strings.TrimSpace(input.URL)
	if target == "" {
		return resolvedWebFetchRequest{}, errors.New("url is required")
	}
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		return resolvedWebFetchRequest{}, errors.New("url must start with http:// or https://")
	}
	resolved := resolvedWebFetchRequest{
		URL:            target,
		Format:         "markdown",
		TimeoutSeconds: webaccess.DefaultFetchTimeoutSeconds,
	}
	if len(input.Format) != 0 {
		var format *string
		if err := json.Unmarshal(input.Format, &format); err != nil {
			return resolvedWebFetchRequest{}, fmt.Errorf("parse format: %w", err)
		}
		if format == nil {
			return resolvedWebFetchRequest{}, errors.New("format cannot be null")
		}
		switch *format {
		case "markdown", "text":
			resolved.Format = *format
		default:
			return resolvedWebFetchRequest{}, fmt.Errorf(
				"format must be markdown or text (got %q)",
				*format,
			)
		}
	}
	if len(input.TimeoutSeconds) != 0 {
		var timeout *int
		if err := json.Unmarshal(input.TimeoutSeconds, &timeout); err != nil {
			return resolvedWebFetchRequest{}, fmt.Errorf("parse timeout_seconds: %w", err)
		}
		if timeout == nil {
			return resolvedWebFetchRequest{}, errors.New("timeout_seconds cannot be null")
		}
		if *timeout < 1 || *timeout > webaccess.MaxFetchTimeoutSeconds {
			return resolvedWebFetchRequest{}, fmt.Errorf(
				"timeout_seconds must be between 1 and %d",
				webaccess.MaxFetchTimeoutSeconds,
			)
		}
		resolved.TimeoutSeconds = *timeout
	}
	return resolved, nil
}

func runWebFetch(
	ctx context.Context,
	call asyncToolContext,
) (asyncPhaseResult, error) {
	resolved, err := resolveWebFetchRequest(call.Call.Input)
	if err != nil {
		return failWebTool(
			webaccess.ErrorCodeFetchInvalidURL,
			err.Error(),
			false,
			err,
		)
	}
	if call.Executor.WebFetcher == nil {
		message := "web fetch is not configured on this deployment"
		return failWebTool(
			webaccess.ErrorCodeFetchFailed,
			message,
			false,
			errors.New(message),
		)
	}
	result, err := call.Executor.WebFetcher.Fetch(ctx, webaccess.FetchRequest{
		URL:            resolved.URL,
		Format:         resolved.Format,
		TimeoutSeconds: resolved.TimeoutSeconds,
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
			webaccess.ErrorCodeFetchFailed,
			err.Error(),
			false,
			err,
		)
	}
	content, err := webFetchToolResultContent(result)
	if err != nil {
		return nil, err
	}
	return completeAsynchronously(content), nil
}

func webFetchToolResultContent(
	result webaccess.FetchResult,
) (toolResultContent, error) {
	content := result.Content
	inlineTruncated := false
	if len(content) > webFetchInlineContentChars {
		content = textutil.TruncateRunes(content, webFetchInlineContentChars)
		inlineTruncated = true
	}
	var rendered strings.Builder
	if result.Title != "" {
		fmt.Fprintf(&rendered, "# %s\n\n", result.Title)
	}
	rendered.WriteString(content)
	if inlineTruncated || result.Truncated {
		fmt.Fprintf(
			&rendered,
			"\n\n[content truncated: showing the first %d characters]",
			len(content),
		)
	}
	structured := map[string]any{
		"url":          result.URL,
		"final_url":    result.FinalURL,
		"status_code":  result.StatusCode,
		"content_type": result.ContentType,
		"bytes":        result.Bytes,
		"truncated":    result.Truncated || inlineTruncated,
	}
	if result.Title != "" {
		structured["title"] = result.Title
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

func failWebTool(
	code, message string,
	retryable bool,
	cause error,
) (asyncPhaseResult, error) {
	content, err := webToolFailureContent(code, message, retryable)
	if err != nil {
		return nil, err
	}
	return failAsynchronously(content, cause), nil
}

func webToolFailureContent(
	code, message string,
	retryable bool,
) (toolResultContent, error) {
	structuredPart, err := structuredToolResultPart(map[string]any{
		"error_code": code,
		"error":      message,
		"retryable":  retryable,
	})
	if err != nil {
		return toolResultContent{}, err
	}
	return newToolResultContent(
		textToolResultPart(message),
		structuredPart,
	), nil
}
