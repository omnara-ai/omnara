package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/omnara-ai/omnara/internal/jsoncanonical"
)

// APIError deliberately omits response bodies, URLs, tokens and transport error
// text. StatusCode remains available for the HTTP owner to classify native errors.
type APIError struct {
	StatusCode int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("GitHub setup request failed (HTTP %d)", e.StatusCode)
}

func (c *Client) get(ctx context.Context, path, token string, out any) error {
	return c.request(ctx, http.MethodGet, c.apiURL+path, token, "", out)
}

func (c *Client) request(ctx context.Context, method, endpoint, token, form string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(form))
	if err != nil {
		return ErrInvalidConfig
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-Github-Api-Version", "2026-03-10")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return safeRequestError(ctx)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return &APIError{StatusCode: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, responseMaxBytes+1))
	if err != nil {
		return safeRequestError(ctx)
	}
	if len(body) > responseMaxBytes {
		return ErrResponseTooLarge
	}
	// Duplicate names, invalid UTF-8, deep containers and trailing values must not
	// allow inconsistent identity/permission interpretations. Numbers stay exact.
	if _, err := jsoncanonical.ParseObject(body, responseMaxBytes); err != nil {
		return ErrInvalidResponse
	}
	if err := json.Unmarshal(body, out); err != nil {
		return ErrInvalidResponse
	}
	return nil
}

func safeRequestError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return &APIError{}
}

// nativeID preserves every digit, including IDs above JavaScript's safe integer
// range. REST IDs must be positive integral JSON numbers, not exponent notation.
type nativeID string

func (id *nativeID) UnmarshalJSON(raw []byte) error {
	value := string(raw)
	if !validID(value) {
		return ErrInvalidResponse
	}
	*id = nativeID(value)
	return nil
}
