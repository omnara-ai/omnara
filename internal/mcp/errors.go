package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/ssrf"
)

var (
	ErrSessionExpired = errors.New("mcp: session expired (server returned 404)")

	ErrIncompleteStream = errors.New("mcp: SSE stream closed before matching response arrived")

	ErrUnsupportedResponse = errors.New("mcp: unsupported response content type")

	ErrResponseTooLarge = errors.New("mcp: response body exceeds configured limit")

	ErrOAuthStateTooLarge = errors.New("mcp auth: oauth flow does not fit in the state parameter")

	errAuthServerMetadataNotFound = errors.New("mcp auth: authorization server metadata not found")
)

type HTTPError struct {
	Status int
	Body   []byte
}

func (e *HTTPError) Error() string {
	if e == nil {
		return ""
	}
	if len(e.Body) == 0 {
		return fmt.Sprintf("mcp: unexpected HTTP status %d", e.Status)
	}
	return fmt.Sprintf("mcp: unexpected HTTP status %d: %s", e.Status, e.Body)
}

type RPCError struct {
	Code    int
	Message string
	Data    json.RawMessage
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("mcp: jsonrpc error %d: %s", e.Code, e.Message)
}

func IsRetryableConnectionFailure(cause error) bool {
	if cause == nil {
		return false
	}
	if errors.Is(cause, context.Canceled) ||
		errors.Is(cause, ssrf.ErrBlockedAddress) ||
		errors.Is(cause, outboundhttp.ErrRedirect) ||
		errors.Is(cause, ErrUnsupportedResponse) ||
		errors.Is(cause, ErrResponseTooLarge) {
		return false
	}
	if errors.Is(cause, context.DeadlineExceeded) || errors.Is(cause, ErrIncompleteStream) {
		return true
	}
	var netErr net.Error
	if errors.As(cause, &netErr) && netErr.Timeout() {
		return true
	}
	var httpErr *HTTPError
	if errors.As(cause, &httpErr) {
		switch httpErr.Status {
		case http.StatusRequestTimeout,
			http.StatusConflict,
			http.StatusTooEarly,
			http.StatusTooManyRequests,
			http.StatusInternalServerError,
			http.StatusBadGateway,
			http.StatusServiceUnavailable,
			http.StatusGatewayTimeout:
			return true
		default:
			return false
		}
	}
	return false
}
