package github

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProviderResponsesAreBoundedStrictAndRedacted(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
	}{
		{"too large", `{"extra":"` + strings.Repeat("x", responseMaxBytes) + `"}`},
		{"duplicate ID", strings.Replace(appResponse(), `"id":`, `"id":1,"id":`, 1)},
		{"rounded float ID", strings.Replace(appResponse(), testAppID, "9.007199254740993e15", 1)},
		{"fractional ID", strings.Replace(appResponse(), testAppID, "123.5", 1)},
		{"quoted ID", strings.Replace(appResponse(), testAppID, `"`+testAppID+`"`, 1)},
		{"null ID", strings.Replace(appResponse(), testAppID, "null", 1)},
		{"invalid UTF8", strings.Replace(appResponse(), "acme-reviewer", "\xff", 1)},
		{"deep JSON", `{"nested":` + strings.Repeat("[", 129) + "0" + strings.Repeat("]", 129) + `}`},
		{"trailing value", appResponse() + "{}"},
		{"invalid slug", strings.Replace(appResponse(), "acme-reviewer", "../../../token", 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { respond(w, tc.body) })
			_, err := client.VerifyApp(t.Context())
			require.Error(t, err)
			require.NotContains(t, err.Error(), tc.body)
			require.NotContains(t, err.Error(), "fake-client-secret")
		})
	}
}

func TestNoRedirectsOrAutomaticOAuthRetries(t *testing.T) {
	t.Parallel()
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	t.Cleanup(target.Close)
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusForbidden, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
				respond(w, `{"message":"fake-client-secret provider-error"}`)
			})
			_, err := client.ExchangeCode(t.Context(), ExchangeInput{
				Code: "test-code", RedirectURI: "https://omnara.test/callback",
			})
			var apiError *APIError
			require.ErrorAs(t, err, &apiError)
			require.Equal(t, status, apiError.StatusCode)
			require.NotContains(t, err.Error(), "fake-client-secret")
			require.EqualValues(t, 1, calls.Load())
			require.Zero(t, targetCalls.Load())
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func TestTransportErrorsAreRedactedAndClientIsCloned(t *testing.T) {
	t.Parallel()
	config := newTestConfig(t)
	config.HTTPClient = &http.Client{
		Timeout: time.Minute,
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("raw-transport-token-do-not-log")
		}),
	}
	client, err := NewClient(config)
	require.NoError(t, err)
	require.Equal(t, time.Minute, config.HTTPClient.Timeout)
	require.Nil(t, config.HTTPClient.CheckRedirect)
	require.Equal(t, setupTimeout, client.http.Timeout)
	_, err = client.VerifyApp(t.Context())
	require.Error(t, err)
	require.NotContains(t, err.Error(), "raw-transport")
	var apiError *APIError
	require.ErrorAs(t, err, &apiError)
}

func TestNativeCallsCarryBoundedContext(t *testing.T) {
	t.Parallel()
	config := newTestConfig(t)
	config.HTTPClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			deadline, ok := r.Context().Deadline()
			if !ok || time.Until(deadline) > setupTimeout {
				t.Error("native call missing setup deadline")
			}
			return nil, context.DeadlineExceeded
		}),
	}
	client, err := NewClient(config)
	require.NoError(t, err)
	_, err = client.VerifySelection(t.Context(), testUser(), testInstallID, testRepositoryID, "acme/private-repo")
	require.Error(t, err)
}

func TestConfigAndKeyValidation(t *testing.T) {
	t.Parallel()
	config := newTestConfig(t)
	key, err := parsePrivateKey(config.PrivateKey)
	require.NoError(t, err)
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	config.PrivateKey = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}))
	_, err = NewClient(config)
	require.NoError(t, err)
	for _, endpoint := range []string{"https://user:pass@example.test", "file:///secret", "https://api.test/?secret=1"} {
		bad := config
		bad.APIURL = endpoint
		_, err := NewClient(bad)
		require.ErrorIs(t, err, ErrInvalidConfig)
	}
	config.PrivateKey = "secret-invalid-key"
	_, err = NewClient(config)
	require.ErrorIs(t, err, ErrInvalidConfig)
	require.NotContains(t, err.Error(), "secret-invalid-key")
}
