package mcp

import (
	"bytes"
	"io"
	"net/http"
)

type statusErrorTransport struct {
	next    http.RoundTripper
	success func(int) bool
}

func (t statusErrorTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.next.RoundTrip(request)
	if err != nil || t.success(response.StatusCode) {
		return response, err
	}
	defer response.Body.Close() //nolint:errcheck // Response body close errors are not actionable here.
	preview, _ := io.ReadAll(io.LimitReader(response.Body, statusErrorDecodeBytes))
	return nil, &HTTPError{Status: response.StatusCode, Body: bytes.TrimSpace(preview)}
}

func ClientRegistrationHTTPClient(client *http.Client) *http.Client {
	cloned := clientWithoutRedirects(client)
	next := cloned.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	cloned.Transport = statusErrorTransport{
		next: next,
		success: func(status int) bool {
			return status == http.StatusOK || status == http.StatusCreated || status == http.StatusBadRequest
		},
	}
	return cloned
}
