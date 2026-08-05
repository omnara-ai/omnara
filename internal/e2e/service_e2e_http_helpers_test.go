//go:build integration && servicee2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
)

func (e *serviceE2EEnvironment) newAPIRequest(
	ctx context.Context,
	method, path string,
	body io.Reader,
) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, e.apiURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Host = e.publicURLHost
	return req, nil
}

func (e *serviceE2EEnvironment) requestJSON(
	t *testing.T,
	ctx context.Context,
	method, path string,
	body any,
	idempotencyKey, token string,
	want int,
) map[string]any {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(mustJSON(body))
	}
	req, err := e.newAPIRequest(ctx, method, path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, resp.StatusCode, want, data)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode response %s: %v", data, err)
	}
	return out
}

func (e *serviceE2EEnvironment) requestBrowserJSON(
	t *testing.T,
	ctx context.Context,
	method, path string,
	body any,
	idempotencyKey, sessionToken, csrfToken string,
	want int,
) map[string]any {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(mustJSON(body))
	}
	req, err := e.newAPIRequest(ctx, method, path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", e.publicURL)
	req.Header.Set(httpauth.CSRFHeaderName, csrfToken)
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: sessionToken})
	req.AddCookie(&http.Cookie{Name: httpauth.CSRFCookieName, Value: csrfToken})
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, resp.StatusCode, want, data)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode response %s: %v", data, err)
	}
	return out
}

func (e *serviceE2EEnvironment) requestJSONArray(
	t *testing.T,
	ctx context.Context,
	method, path string,
	body any,
	idempotencyKey, token string,
	want int,
) []map[string]any {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(mustJSON(body))
	}
	req, err := e.newAPIRequest(ctx, method, path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, resp.StatusCode, want, data)
	}
	var out []map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode response array %s: %v", data, err)
	}
	return out
}

func mustJSON(value any) []byte {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return body
}

// requestRaw sends an arbitrary content-type body. Used for the agent config
// endpoints which accept YAML or JSON in the request body itself rather than a
// JSON envelope.
func (e *serviceE2EEnvironment) requestRaw(
	t *testing.T,
	ctx context.Context,
	method, path, contentType string,
	body []byte,
	idempotencyKey, token string,
	want int,
) map[string]any {
	t.Helper()
	req, err := e.newAPIRequest(ctx, method, path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", contentType)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, resp.StatusCode, want, data)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode response %s: %v", data, err)
	}
	return out
}
