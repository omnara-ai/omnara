// Package outboundhttp composes redirect policy with either public-network
// SSRF protection or a caller-owned transport.
package outboundhttp

import (
	"errors"
	"net/http"
	"time"

	"github.com/omnara-ai/omnara/internal/ssrf"
)

var ErrRedirect = errors.New("outbound HTTP redirect is not permitted")

type PublicClientOptions struct {
	AllowLoopback                bool
	DisableResponseHeaderTimeout bool
	Timeout                      time.Duration
}

func NewPublicClient(opts PublicClientOptions) *http.Client {
	return CloneWithoutRedirects(&http.Client{
		Transport: ssrf.NewTransport(ssrf.TransportOptions{
			AllowLoopback:                opts.AllowLoopback,
			DisableResponseHeaderTimeout: opts.DisableResponseHeaderTimeout,
		}),
		Timeout: opts.Timeout,
	})
}

func CloneWithoutRedirects(client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	cloned := *client
	cloned.CheckRedirect = stopRedirect
	return &cloned
}

func CloneRejectingRedirects(client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	cloned := *client
	cloned.CheckRedirect = rejectRedirect
	return &cloned
}

func stopRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

func rejectRedirect(*http.Request, []*http.Request) error {
	return ErrRedirect
}
