package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

func registerAgainst(t *testing.T, status int, body string) error {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	_, err := oauthex.RegisterClient(
		context.Background(),
		server.URL+"/register",
		&oauthex.ClientRegistrationMetadata{RedirectURIs: []string{"https://example.com/callback"}},
		server.Client(),
	)
	if err == nil {
		t.Fatalf("RegisterClient returned no error for status %d", status)
	}
	return err
}

func TestClientRegistrationFailureRecoversStatusFromSDKError(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "service unavailable", status: http.StatusServiceUnavailable, body: `{"error":"temporarily_unavailable"}`},
		{name: "too many requests", status: http.StatusTooManyRequests, body: ""},
		{name: "forbidden", status: http.StatusForbidden, body: "denied"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := ClientRegistrationFailure(registerAgainst(t, tt.status, tt.body))
			got, ok := HTTPStatus(err)
			if !ok || got != tt.status {
				t.Fatalf(
					"HTTPStatus = %d, %v for SDK error %q; want %d (the SDK error message format may have changed)",
					got, ok, err, tt.status,
				)
			}
		})
	}
}

func TestClientRegistrationFailureKeepsTypedRejection(t *testing.T) {
	err := ClientRegistrationFailure(
		registerAgainst(t, http.StatusBadRequest, `{"error":"invalid_redirect_uri","error_description":"nope"}`),
	)
	var rejected *oauthex.ClientRegistrationError
	if !errors.As(err, &rejected) || rejected.ErrorCode != "invalid_redirect_uri" {
		t.Fatalf("err = %v, want the SDK's typed registration error", err)
	}
	if _, ok := HTTPStatus(err); ok {
		t.Fatalf("400 rejection should not carry an HTTP status: %v", err)
	}
}
