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
	req := e.newJSONAPIRequest(t, ctx, method, path, body, idempotencyKey)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return doServiceJSONRequest(t, req, want)
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
	req := e.newJSONAPIRequest(t, ctx, method, path, body, idempotencyKey)
	req.Header.Set("Origin", e.publicURL)
	req.Header.Set(httpauth.CSRFHeaderName, csrfToken)
	req.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionCookieName, Value: sessionToken})
	req.AddCookie(&http.Cookie{Name: httpauth.CSRFCookieName, Value: csrfToken})
	return doServiceJSONRequest(t, req, want)
}

func mustJSON(value any) []byte {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return body
}

func (e *serviceE2EEnvironment) newJSONAPIRequest(
	t *testing.T,
	ctx context.Context,
	method, path string,
	body any,
	idempotencyKey string,
) *http.Request {
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
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	return req
}

func doServiceJSONRequest(t *testing.T, req *http.Request, want int) map[string]any {
	t.Helper()
	method, path := req.Method, req.URL.RequestURI()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s response: %v", method, path, err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, resp.StatusCode, want, data)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode %s %s response: %v body=%s", method, path, err, data)
	}
	return out
}
