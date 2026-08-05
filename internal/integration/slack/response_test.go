package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCreateManifestAppIncludesSlackErrorOnHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		writeSlackTestJSON(w, map[string]any{"ok": false, "error": "invalid_manifest"})
	}))
	defer server.Close()

	_, err := CreateManifestApp(
		context.Background(),
		OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()},
		"xoxe-test",
		BuildAppManifest(
			"Test",
			"https://example.test/events",
			"https://example.test/actions",
			"https://example.test/callback",
		),
	)
	if err == nil || !strings.Contains(err.Error(), "status 400") || !strings.Contains(err.Error(), "invalid_manifest") {
		t.Fatalf("CreateManifestApp error = %v, want status and slack error", err)
	}
}

func TestCreateManifestAppIncludesManifestValidationErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSlackTestJSON(w, map[string]any{
			"ok":    false,
			"error": "invalid_manifest",
			"errors": []map[string]string{{
				"pointer": "/features/bot_user/display_name",
				"message": "Bot user display name is invalid",
			}},
		})
	}))
	defer server.Close()

	_, err := CreateManifestApp(
		context.Background(),
		OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()},
		"xoxe-test",
		BuildAppManifest(
			"Test",
			"https://example.test/events",
			"https://example.test/actions",
			"https://example.test/callback",
		),
	)
	if err == nil ||
		!strings.Contains(err.Error(), "invalid_manifest") ||
		!strings.Contains(err.Error(), "/features/bot_user/display_name") ||
		!strings.Contains(err.Error(), "Bot user display name is invalid") {
		t.Fatalf("CreateManifestApp error = %v, want manifest validation details", err)
	}
}

func TestCompleteOAuthIncludesSlackErrorOnHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		writeSlackTestJSON(w, map[string]any{"ok": false, "error": "invalid_code"})
	}))
	defer server.Close()

	_, err := CompleteOAuth(
		context.Background(),
		OAuthConfig{AccessURL: server.URL, HTTPClient: server.Client()},
		"client",
		"secret",
		"code",
		"https://example.test/callback",
	)
	if err == nil || !strings.Contains(err.Error(), "status 400") || !strings.Contains(err.Error(), "invalid_code") {
		t.Fatalf("CompleteOAuth error = %v, want status and slack error", err)
	}
}

func TestValidActionResponseURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"slack", "https://hooks.slack.com/actions/T123/123/secret", true},
		{"govslack", "https://hooks.slack-gov.com/actions/T123/123/secret", true},
		{"http", "http://hooks.slack.com/actions/T123/123/secret", false},
		{"wrong path", "https://hooks.slack.com/services/T123/123/secret", false},
		{"lookalike host", "https://hooks.slack.com.evil.test/actions/T123/123/secret", false},
		{"localhost", "https://localhost/actions/T123/123/secret", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidActionResponseURL(tt.raw); got != tt.want {
				t.Fatalf("ValidActionResponseURL(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestPostMessageClassifiesNon2XXSlackError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		writeSlackTestJSON(w, map[string]any{"ok": false, "error": "token_revoked"})
	}))
	defer server.Close()

	result, err := PostMessage(
		context.Background(),
		slackTestClient(server),
		MessageTarget{TargetRef: "slack-abcd", Channel: "C123", BotToken: "xoxb-test"},
		"agt_test",
		"call_test",
		"hello",
	)
	if err != nil {
		t.Fatalf("PostMessage: %v", err)
	}
	if !result.PermanentFailure || result.Code != "integration_disabled" {
		t.Fatalf("PostMessage result = %+v, want integration_disabled", result)
	}
}

func TestPostMessageKeepsUnknown5XXSlackErrorTransient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		writeSlackTestJSON(w, map[string]any{"ok": false, "error": "rollup_error"})
	}))
	defer server.Close()

	result, err := PostMessage(
		context.Background(),
		slackTestClient(server),
		MessageTarget{TargetRef: "slack-abcd", Channel: "C123", BotToken: "xoxb-test"},
		"agt_test",
		"call_test",
		"hello",
	)
	if err != nil {
		t.Fatalf("PostMessage: %v", err)
	}
	if !result.TransientFailure || result.PermanentFailure || result.Code != "transient_failure" ||
		!strings.Contains(result.Message, "rollup_error") {
		t.Fatalf("PostMessage result = %+v, want transient with slack error", result)
	}
}

func writeSlackTestJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		panic(err)
	}
}

func slackTestClient(server *httptest.Server) *http.Client {
	target, err := url.Parse(server.URL)
	if err != nil {
		panic(err)
	}
	base := server.Client()
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &http.Client{
		Transport: slackTestTransport{target: target, base: transport},
	}
}

type slackTestTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (t slackTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	clone.URL.Path = strings.TrimPrefix(clone.URL.Path, "/api")
	return t.base.RoundTrip(clone)
}
