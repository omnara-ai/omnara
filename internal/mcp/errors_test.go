package mcp

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"testing"

	"github.com/omnara-ai/omnara/internal/ssrf"
)

func TestHTTPStatusSeesTokenEndpointErrors(t *testing.T) {
	err := fmt.Errorf("refresh: %w", &tokenEndpointError{statusCode: http.StatusServiceUnavailable})
	status, ok := HTTPStatus(err)
	if !ok || status != http.StatusServiceUnavailable {
		t.Fatalf("HTTPStatus() = %d, %v; want 503, true", status, ok)
	}
	if !IsRetryableConnectionFailure(err) {
		t.Fatalf("IsRetryableConnectionFailure() = false, want true")
	}
	if IsRetryableConnectionFailure(&tokenEndpointError{statusCode: http.StatusBadRequest}) {
		t.Fatalf("IsRetryableConnectionFailure() = true for 400, want false")
	}
}

func TestIsRetryableConnectionFailureClassifiesTransportErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "connection refused", err: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, want: true},
		{name: "connection reset", err: &net.OpError{Op: "read", Err: syscall.ECONNRESET}, want: true},
		{name: "dns not found", err: &net.DNSError{Err: "no such host", IsNotFound: true}, want: true},
		{
			name: "dial error wrapped by the http client",
			err:  &url.Error{Op: "Post", URL: "https://mcp.example", Err: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}},
			want: true,
		},
		{name: "unexpected eof", err: fmt.Errorf("read body: %w", io.ErrUnexpectedEOF), want: true},
		{name: "deadline exceeded", err: context.DeadlineExceeded, want: true},
		{name: "canceled", err: context.Canceled, want: false},
		{name: "blocked address", err: fmt.Errorf("dial: %w", ssrf.ErrBlockedAddress), want: false},
		{name: "unsupported response", err: ErrUnsupportedResponse, want: false},
		{name: "response too large", err: ErrResponseTooLarge, want: false},
		{name: "http 400", err: &HTTPError{Status: http.StatusBadRequest}, want: false},
		{name: "http 503", err: &HTTPError{Status: http.StatusServiceUnavailable}, want: true},
		{name: "plain error", err: fmt.Errorf("something else"), want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRetryableConnectionFailure(tt.err); got != tt.want {
				t.Fatalf("IsRetryableConnectionFailure(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
