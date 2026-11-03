package mcp

import (
	"bytes"
	"context"
	"io"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

type statusErrorTransport struct {
	next   http.RoundTripper
	failed func(int) bool
}

func (t statusErrorTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.next.RoundTrip(request)
	if err != nil || !t.failed(response.StatusCode) {
		return response, err
	}
	defer response.Body.Close() //nolint:errcheck // Response body close errors are not actionable here.
	preview, _ := io.ReadAll(io.LimitReader(response.Body, statusErrorDecodeBytes))
	return nil, &HTTPError{Status: response.StatusCode, Body: bytes.TrimSpace(preview)}
}

func withStatusErrors(client *http.Client, failed func(int) bool) *http.Client {
	var cloned http.Client
	if client != nil {
		cloned = *client
	}
	next := cloned.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	cloned.Transport = statusErrorTransport{next: next, failed: failed}
	return &cloned
}

func RegisterClient(
	ctx context.Context,
	registrationEndpoint string,
	clientMeta *oauthex.ClientRegistrationMetadata,
	client *http.Client,
) (*oauthex.ClientRegistrationResponse, error) {
	return oauthex.RegisterClient(ctx, registrationEndpoint, clientMeta, clientRegistrationHTTPClient(client))
}

func clientRegistrationHTTPClient(client *http.Client) *http.Client {
	return withStatusErrors(client, func(status int) bool {
		return status > http.StatusBadRequest
	})
}

func metadataHTTPClient(client *http.Client) *http.Client {
	return withStatusErrors(client, func(status int) bool {
		return status >= http.StatusBadRequest
	})
}
