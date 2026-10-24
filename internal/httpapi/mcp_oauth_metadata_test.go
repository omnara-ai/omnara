package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/mcp"
	"github.com/omnara-ai/omnara/internal/resourcemeta"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
)

func TestStartMCPOAuthRejectsInvalidProspectiveSecretName(t *testing.T) {
	server := strictOpenAPIServer{server: &Server{}}
	_, apiErr, err := server.startMCPOAuth(
		context.Background(),
		mcpOAuthOwner{},
		identitystore.PrincipalRecord{},
		&openapi.MCPOAuthStartRequest{Name: " secret"},
	)
	if err != nil {
		t.Fatalf("start MCP OAuth: %v", err)
	}
	if apiErr == nil || apiErr.Code != openapi.ErrorCodeInvalidRequest {
		t.Fatalf("start MCP OAuth error = %+v, want invalid_request", apiErr)
	}
}

// Google's Calendar MCP server answers the auth probe without a challenge,
// publishes protected resource metadata only at the path-inserted well-known
// URL, and issues refresh tokens only for offline access.
func TestStartMCPOAuthAuthorizesGoogleCalendarShapedServer(t *testing.T) {
	mux := http.NewServeMux()
	mcpServer := httptest.NewServer(mux)
	t.Cleanup(mcpServer.Close)
	mcpURL := mcpServer.URL + "/mcp/v1"
	mux.HandleFunc("/mcp/v1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":-1,"result":{"protocolVersion":"2025-06-18"}}`))
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp/v1", func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(t, w, map[string]any{
			"resource":              mcpURL,
			"authorization_servers": []string{mcpServer.URL + "/"},
			"scopes_supported":      []string{"https://www.googleapis.com/auth/calendar"},
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(t, w, map[string]any{
			"issuer":                           mcpServer.URL,
			"authorization_endpoint":           "https://accounts.google.com/o/oauth2/v2/auth",
			"token_endpoint":                   mcpServer.URL + "/token",
			"code_challenge_methods_supported": []string{"plain", "S256"},
		})
	})
	keyWrapper, err := secrets.NewLocalKeyWrapper(
		"test-key",
		map[string][]byte{"test-key": []byte("0123456789abcdef0123456789abcdef")},
	)
	if err != nil {
		t.Fatalf("new local key wrapper: %v", err)
	}
	server := strictOpenAPIServer{server: &Server{
		publicURL:          "https://app.omnara.com",
		secretKeyWrapper:   keyWrapper,
		mcpOAuthHTTPClient: mcpServer.Client(),
		agentConfigOptions: agentconfig.CompileOptions{AllowInsecureLocalMCPHTTP: true},
	}}
	clientID := "client-123.apps.googleusercontent.com"
	clientSecret := "secret-456"
	scopes := []string{"https://www.googleapis.com/auth/calendar.events"}

	response, apiErr, err := server.startMCPOAuth(
		context.Background(),
		mcpOAuthOwner{OrgID: uuid.New(), OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: uuid.New()},
		identitystore.PrincipalRecord{ID: uuid.New()},
		&openapi.MCPOAuthStartRequest{
			Name:         "gcal-mcp",
			McpUrl:       mcpURL,
			ClientId:     &clientID,
			ClientSecret: &clientSecret,
			Scopes:       &scopes,
		},
	)
	if err != nil {
		t.Fatalf("start MCP OAuth: %v", err)
	}
	if apiErr != nil {
		t.Fatalf("start MCP OAuth error = %+v, want an authorization URL", apiErr)
	}
	authURL, err := url.Parse(response.AuthorizationUrl)
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	query := authURL.Query()
	for key, want := range map[string]string{
		"access_type":  "offline",
		"prompt":       "consent",
		"resource":     mcpURL,
		"client_id":    clientID,
		"redirect_uri": "https://app.omnara.com/api/mcp-oauth/callback",
		"scope":        scopes[0],
	} {
		if got := query.Get(key); got != want {
			t.Fatalf("authorization URL %s = %q, want %q", key, got, want)
		}
	}
	flow, err := mcp.OpenOAuthState(context.Background(), keyWrapper, query.Get("state"))
	if err != nil {
		t.Fatalf("open oauth state: %v", err)
	}
	if flow.TokenEndpoint != mcpServer.URL+"/token" || flow.Resource != mcpURL || flow.ClientSecret != clientSecret {
		t.Fatalf("flow = %+v, want token endpoint, resource, and client secret for the refresh grant", flow)
	}
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("write test response: %v", err)
	}
}

func TestMCPUpstreamFailureMapsErrorOrigins(t *testing.T) {
	for _, tt := range []struct {
		name       string
		err        error
		wantCode   openapi.ErrorCode
		wantStatus int
	}{
		{
			name:       "blocked address",
			err:        fmt.Errorf("dial: %w", ssrf.ErrBlockedAddress),
			wantCode:   openapi.ErrorCodeInvalidRequest,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "transient upstream failure",
			err:        &mcp.HTTPError{Status: http.StatusServiceUnavailable},
			wantCode:   openapi.ErrorCodeUpstreamUnavailable,
			wantStatus: http.StatusFailedDependency,
		},
		{
			name:       "rejected by upstream",
			err:        errors.New("registration rejected"),
			wantCode:   openapi.ErrorCodeUnprocessable,
			wantStatus: http.StatusUnprocessableEntity,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			apiErr := mcpUpstreamFailure(tt.err, "transient: ", "rejected: ")
			if apiErr.Code != tt.wantCode || apiErr.Status != tt.wantStatus {
				t.Fatalf("code=%q status=%d, want %q %d", apiErr.Code, apiErr.Status, tt.wantCode, tt.wantStatus)
			}
		})
	}
}

func TestMCPOAuthSecretMetadataMergesExistingAndProvided(t *testing.T) {
	mcpURL := "https://mcp.example.com/mcp"
	cases := []struct {
		name     string
		existing json.RawMessage
		provided resourcemeta.Metadata
		want     resourcemeta.Metadata
	}{
		{
			name: "no existing no provided",
			want: resourcemeta.Metadata{secrets.KeyMCPURL: mcpURL},
		},
		{
			name:     "existing kept when provided is unset",
			existing: json.RawMessage(`{"team":"support","mcp_url":"https://old.example.com"}`),
			want: resourcemeta.Metadata{
				"team":            "support",
				secrets.KeyMCPURL: mcpURL,
			},
		},
		{
			name:     "provided overlays existing per key",
			existing: json.RawMessage(`{"team":"support","region":"us-east-1"}`),
			provided: resourcemeta.Metadata{"team": "infra"},
			want: resourcemeta.Metadata{
				"team":            "infra",
				"region":          "us-east-1",
				secrets.KeyMCPURL: mcpURL,
			},
		},
		{
			name:     "oversized existing value is dropped",
			existing: json.RawMessage(`{"big":"` + strings.Repeat("v", resourcemeta.MaxValueLength+1) + `","ok":"kept"}`),
			want: resourcemeta.Metadata{
				"ok":              "kept",
				secrets.KeyMCPURL: mcpURL,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mcpOAuthSecretMetadata(tc.existing, tc.provided, mcpURL)
			if err != nil {
				t.Fatalf("merge metadata: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("merge metadata = %v, want %v", got, tc.want)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("merged metadata invalid: %v", err)
			}
		})
	}
}

func TestMCPOAuthSecretMetadataTrimsExistingAtCapacity(t *testing.T) {
	mcpURL := "https://mcp.example.com/mcp"
	existing := resourcemeta.Metadata{}
	for i := range resourcemeta.MaxEntries {
		existing[fmt.Sprintf("key-%02d", i)] = "value"
	}
	raw, err := existing.JSON()
	if err != nil {
		t.Fatalf("encode existing metadata: %v", err)
	}
	provided := resourcemeta.Metadata{"provided": "wins"}
	got, err := mcpOAuthSecretMetadata(raw, provided, mcpURL)
	if err != nil {
		t.Fatalf("merge metadata: %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("merged metadata invalid: %v", err)
	}
	if len(got) != resourcemeta.MaxEntries {
		t.Fatalf("merged metadata has %d entries, want %d", len(got), resourcemeta.MaxEntries)
	}
	if got[secrets.KeyMCPURL] != mcpURL || got["provided"] != "wins" {
		t.Fatalf("merged metadata = %v", got)
	}
	for i := range resourcemeta.MaxEntries - 2 {
		key := fmt.Sprintf("key-%02d", i)
		if got[key] != "value" {
			t.Fatalf("merged metadata dropped %s: %v", key, got)
		}
	}
	for _, key := range []string{
		fmt.Sprintf("key-%02d", resourcemeta.MaxEntries-2),
		fmt.Sprintf("key-%02d", resourcemeta.MaxEntries-1),
	} {
		if _, ok := got[key]; ok {
			t.Fatalf("merged metadata kept %s past capacity: %v", key, got)
		}
	}
}
