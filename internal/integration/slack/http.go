package slack

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/omnara-ai/omnara/internal/outboundhttp"
)

func WithRequestCheck(client *http.Client, check func(context.Context) error) *http.Client {
	if client == nil {
		client = defaultHTTPClient
	}
	clone := *client
	transport := clone.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	clone.Transport = checkedTransport{transport: transport, check: check}
	return &clone
}

type checkedTransport struct {
	transport http.RoundTripper
	check     func(context.Context) error
}

type requestCheckError struct{ cause error }

func (e *requestCheckError) Error() string { return e.cause.Error() }
func (e *requestCheckError) Unwrap() error { return e.cause }

func (t checkedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if t.check != nil {
		if err := t.check(request.Context()); err != nil {
			return nil, &requestCheckError{cause: err}
		}
	}
	return t.transport.RoundTrip(request)
}

var defaultHTTPClient = outboundhttp.NewPublicClient(
	outboundhttp.PublicClientOptions{Timeout: 30 * time.Second},
)

func httpClientWithoutRedirects(client *http.Client) *http.Client {
	if client == nil {
		return defaultHTTPClient
	}
	return outboundhttp.CloneWithoutRedirects(client)
}

func readResponseBody(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("slack response exceeds the byte limit")
	}
	return data, nil
}
