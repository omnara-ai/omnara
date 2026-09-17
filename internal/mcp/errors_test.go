package mcp

import (
	"fmt"
	"net/http"
	"testing"
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
