package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"

	jsonrpc "github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/ssrf"
)

var (
	ErrSessionExpired = errors.New("mcp: session expired (server returned 404)")

	ErrIncompleteStream = errors.New("mcp: SSE stream closed before matching response arrived")

	ErrUnsupportedResponse = errors.New("mcp: unsupported response content type")

	ErrResponseTooLarge = errors.New("mcp: response body exceeds configured limit")

	ErrOAuthStateTooLarge = errors.New("mcp auth: oauth flow does not fit in the state parameter")

	ErrOAuthMetadataUnavailable = errors.New(
		"mcp auth: server requires authorization but publishes no usable OAuth metadata",
	)

	ErrInputRequired = errors.New("mcp: server requested client input that this client does not support")

	errAuthServerMetadataNotFound = errors.New("mcp auth: authorization server metadata not found")
)

const (
	CodeHeaderMismatch                  = -32020
	CodeMissingRequiredClientCapability = -32021
	CodeUnsupportedProtocolVersion      = -32022
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
	Code       int
	Message    string
	Data       json.RawMessage
	HTTPStatus int
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	if e.HTTPStatus != 0 && e.HTTPStatus != http.StatusOK {
		return fmt.Sprintf("mcp: jsonrpc error %d (HTTP %d): %s", e.Code, e.HTTPStatus, e.Message)
	}
	return fmt.Sprintf("mcp: jsonrpc error %d: %s", e.Code, e.Message)
}

func HTTPStatus(err error) (int, bool) {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Status, true
	}
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) && rpcErr.HTTPStatus != 0 {
		return rpcErr.HTTPStatus, true
	}
	return 0, false
}

func isStatelessProtocolCode(code int) bool {
	switch code {
	case CodeHeaderMismatch, CodeMissingRequiredClientCapability, CodeUnsupportedProtocolVersion:
		return true
	default:
		return false
	}
}

func IsStatelessProtocolError(err error) bool {
	var rpcErr *RPCError
	return errors.As(err, &rpcErr) && isStatelessProtocolCode(rpcErr.Code)
}

func UnsupportedProtocolVersions(err error) ([]string, bool) {
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != CodeUnsupportedProtocolVersion || len(rpcErr.Data) == 0 {
		return nil, false
	}
	var data struct {
		Supported []string `json:"supported"`
	}
	if json.Unmarshal(rpcErr.Data, &data) != nil {
		return nil, false
	}
	return data.Supported, true
}

func IndicatesLegacyServer(err error) bool {
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) {
		switch rpcErr.Code {
		case jsonrpc.CodeInvalidRequest, jsonrpc.CodeMethodNotFound, jsonrpc.CodeInvalidParams:
			return rpcErr.HTTPStatus == 0 || rpcErr.HTTPStatus == http.StatusOK || isLegacyProbeStatus(rpcErr.HTTPStatus)
		default:
			return false
		}
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return isLegacyProbeStatus(httpErr.Status)
	}
	return false
}

func isLegacyProbeStatus(status int) bool {
	if status < 400 || status >= 500 {
		return false
	}
	switch status {
	case http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusRequestTimeout,
		http.StatusConflict,
		http.StatusTooEarly,
		http.StatusTooManyRequests:
		return false
	default:
		return true
	}
}

func isRetryableHTTPStatus(status int) bool {
	switch status {
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
	if status, ok := HTTPStatus(cause); ok {
		return isRetryableHTTPStatus(status)
	}
	return false
}
