package mcp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"testing"

	jsonrpc "github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/omnara-ai/omnara/internal/errutil"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
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
		{name: "dns not found", err: &net.DNSError{Err: "no such host", IsNotFound: true}, want: false},
		{
			name: "dns not found during dial",
			err:  &net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host", IsNotFound: true}},
			want: false,
		},
		{name: "dns server failure", err: &net.DNSError{Err: "server misbehaving", IsTemporary: true}, want: true},
		{name: "tls alert from the server", err: &net.OpError{Op: "remote error", Err: tls.AlertError(40)}, want: false},
		{
			name: "dial error wrapped by the http client",
			err:  &url.Error{Op: "Post", URL: "https://mcp.example", Err: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}},
			want: true,
		},
		{name: "unexpected eof", err: fmt.Errorf("read body: %w", io.ErrUnexpectedEOF), want: true},
		{
			name: "connection closed before a response",
			err:  &url.Error{Op: "Post", URL: "https://mcp.example", Err: io.EOF},
			want: true,
		},
		{name: "eof outside the http client", err: fmt.Errorf("decode body: %w", io.EOF), want: false},
		{
			name: "json-rpc internal error",
			err:  &RPCError{Code: jsonrpc.CodeInternalError, Message: "boom", HTTPStatus: http.StatusOK},
			want: false,
		},
		{
			name: "json-rpc invalid params",
			err:  &RPCError{Code: jsonrpc.CodeInvalidParams, Message: "bad", HTTPStatus: http.StatusOK},
			want: false,
		},
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

func TestInternalFailureKeepsCallerCancellationDistinct(t *testing.T) {
	canceled := internalFailure(fmt.Errorf("store mcp catalog: %w", context.Canceled))
	if errors.Is(canceled, ErrInternal) || !errutil.OnlyMatches(canceled, context.Canceled) {
		t.Fatalf("internalFailure(canceled) = %v, want cancellation only", canceled)
	}
	if failed := internalFailure(errors.New("connection reset")); !errors.Is(failed, ErrInternal) {
		t.Fatalf("internalFailure() = %v, want ErrInternal", failed)
	}
	if failed := wrapStoreErr(fmt.Errorf("load secret: %w", context.Canceled)); errors.Is(failed, ErrInternal) {
		t.Fatalf("wrapStoreErr(canceled) = %v, want no ErrInternal", failed)
	}
	deadline := internalFailure(fmt.Errorf("mark mcp catalog fetched: %w", context.DeadlineExceeded))
	if errors.Is(deadline, ErrInternal) || !IsRetryableConnectionFailure(deadline) {
		t.Fatalf("internalFailure(deadline) = %v, want a retryable failure without ErrInternal", deadline)
	}
	invalidSecret := wrapStoreErr(fmt.Errorf("load secret: %w", storeerr.ErrInvalidSecretRequest))
	if errors.Is(invalidSecret, ErrInternal) || !errors.Is(invalidSecret, storeerr.ErrInvalidSecretRequest) {
		t.Fatalf("wrapStoreErr(invalid secret) = %v, want ErrInvalidSecretRequest without ErrInternal", invalidSecret)
	}
}
