package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"

	jsonrpc "github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/ssrf"
)

var (
	ErrSessionExpired = errors.New("mcp: session expired (server returned 404)")

	ErrIncompleteStream = errors.New("mcp: SSE stream closed before matching response arrived")

	ErrUnsupportedResponse = errors.New("mcp: unsupported response content type")

	ErrResponseTooLarge = errors.New("mcp: response body exceeds configured limit")

	ErrMalformedResponse = errors.New("mcp: server returned a malformed response")

	ErrOAuthStateTooLarge = errors.New("mcp auth: oauth flow does not fit in the state parameter")

	ErrOAuthMetadataUnavailable = errors.New(
		"mcp auth: server requires authorization but publishes no usable OAuth metadata",
	)

	ErrInputRequired = errors.New("mcp: server requested client input that this client does not support")

	ErrInternal = errors.New("mcp: internal failure")

	ErrCredential = errors.New("mcp: credential failure")

	ErrRefreshBusy = errors.New("mcp: refresh in progress")

	ErrStoreTimeout = errors.New("mcp: storage operation timed out")

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
	var tokenErr *tokenEndpointError
	if errors.As(err, &tokenErr) && tokenErr.statusCode != 0 {
		return tokenErr.statusCode, true
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

func IsServerInternalError(err error) bool {
	var rpcErr *RPCError
	return errors.As(err, &rpcErr) && rpcErr.Code == jsonrpc.CodeInternalError
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
	if IsTimeout(cause) || errors.Is(cause, ErrIncompleteStream) || errors.Is(cause, ErrRefreshBusy) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(cause, &dnsErr) {
		return !dnsErr.IsNotFound
	}
	var opErr *net.OpError
	if errors.As(cause, &opErr) && opErr.Op != "remote error" && opErr.Op != "local error" {
		return true
	}
	if errors.Is(cause, io.ErrUnexpectedEOF) {
		return true
	}
	var urlErr *url.Error
	if errors.As(cause, &urlErr) && errors.Is(urlErr.Err, io.EOF) {
		return true
	}
	if status, ok := HTTPStatus(cause); ok {
		return isRetryableHTTPStatus(status)
	}
	return false
}

func IsTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func internalFailure(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", ErrStoreTimeout, err)
	}
	return fmt.Errorf("%w: %w", ErrInternal, err)
}

func leaseWaitFailure(err error, lease string) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: timed out waiting for %s: %w", ErrRefreshBusy, lease, err)
	}
	return err
}
