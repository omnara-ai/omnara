package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicHostGuardScopesExactInternalServiceOriginToConnectorAPI(t *testing.T) {
	server := mustNewUnitServer(
		t,
		WithPublicURL("https://omnara.example.test"),
		WithInternalAPIOrigins([]string{"http://api:8080"}),
	)
	handler := server.publicHostGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	tests := []struct {
		host   string
		path   string
		status int
	}{
		{host: "omnara.example.test", path: "/api/v1/projects", status: http.StatusNoContent},
		{
			host: "api:8080", path: "/api/v1/channel-connector/deliveries/claim",
			status: http.StatusNoContent,
		},
		{host: "api:8080", path: "/api/v1/projects", status: http.StatusNotFound},
		{host: "api:8080", path: "/healthz", status: http.StatusNotFound},
		{
			host: "untrusted:8080", path: "/api/v1/channel-connector/deliveries/claim",
			status: http.StatusNotFound,
		},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodGet, "http://service.test"+test.path, nil)
		request.Host = test.host
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf(
				"host %q path %q status = %d, want %d",
				test.host,
				test.path,
				response.Code,
				test.status,
			)
		}
	}
}

func TestConfiguredOriginMatchesCanonicalHosts(t *testing.T) {
	origin, err := parseConfiguredOrigin(" https://Example.COM./ ")
	if err != nil {
		t.Fatalf("parse configured origin: %v", err)
	}
	for _, host := range []string{"example.com", "example.com:443", "EXAMPLE.COM."} {
		if !origin.matchesHost(host) {
			t.Fatalf("expected configured origin to match host %q", host)
		}
	}
	for _, host := range []string{"", "example.com:444", "other.example.com"} {
		if origin.matchesHost(host) {
			t.Fatalf("expected configured origin not to match host %q", host)
		}
	}
}

func TestConfiguredOriginMatchesIPv6Hosts(t *testing.T) {
	origin, err := parseConfiguredOrigin("https://[2001:db8::1]:8443")
	if err != nil {
		t.Fatalf("parse configured origin: %v", err)
	}
	for _, host := range []string{"[2001:db8::1]:8443", "[2001:DB8::1]:8443"} {
		if !origin.matchesHost(host) {
			t.Fatalf("expected configured origin to match host %q", host)
		}
	}
	for _, host := range []string{"[2001:db8::1]", "[2001:db8::1]:443", "[2001:db8::2]:8443"} {
		if origin.matchesHost(host) {
			t.Fatalf("expected configured origin not to match host %q", host)
		}
	}
}

func TestIsLocalOnlyHost(t *testing.T) {
	for _, host := range []string{
		"localhost",
		"localhost:5173",
		"127.0.0.1:8080",
		"[::1]:8080",
		"Localhost.",
		"host.docker.internal",
		"host.docker.internal:8080",
	} {
		if !isLocalOnlyHost(host) {
			t.Fatalf("expected %q to be a local-only host", host)
		}
	}
	for _, host := range []string{
		"",
		"example.com",
		"example.com:8080",
		"10.0.0.1:8080",
		"localhost.example.com",
		"evil-host.docker.internal.example.com",
	} {
		if isLocalOnlyHost(host) {
			t.Fatalf("expected %q not to be a local-only host", host)
		}
	}
}

func TestConfiguredOriginRejectsMalformedPublicURL(t *testing.T) {
	_, err := New(discardLogger(), nil, WithPublicURL("not-a-url"))
	if err == nil || !strings.Contains(err.Error(), "invalid public URL") {
		t.Fatalf("New error = %v, want invalid public URL", err)
	}
}

func TestPublicHostGuardAcceptsConfiguredAppAndAPIHosts(t *testing.T) {
	server := mustNewUnitServer(
		t,
		WithPublicURL("https://app.omnara.test"),
		WithPublicAPIURL("https://api.omnara.test/v1"),
	)
	handler := server.publicHostGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, host := range []string{"app.omnara.test", "api.omnara.test"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "https://"+host+"/healthz", nil)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("host %q status = %d, want %d", host, recorder.Code, http.StatusNoContent)
		}
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "https://other.omnara.test/healthz", nil)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unconfigured host status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}
