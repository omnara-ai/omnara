package mcp

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/secrets"
)

func TestAuthorizationServerMetadataURLs(t *testing.T) {
	cases := []struct {
		issuer string
		want   []string
	}{
		{
			issuer: "https://issuer.example",
			want: []string{
				"https://issuer.example/.well-known/oauth-authorization-server",
				"https://issuer.example/.well-known/openid-configuration",
			},
		},
		{
			// A bare "/" path is the same issuer as no path and must not
			// produce double-slash candidates.
			issuer: "https://issuer.example/",
			want: []string{
				"https://issuer.example/.well-known/oauth-authorization-server",
				"https://issuer.example/.well-known/openid-configuration",
			},
		},
		{
			issuer: "https://issuer.example/tenant/v2.0",
			want: []string{
				"https://issuer.example/.well-known/oauth-authorization-server/tenant/v2.0",
				"https://issuer.example/.well-known/openid-configuration/tenant/v2.0",
				"https://issuer.example/tenant/v2.0/.well-known/openid-configuration",
			},
		},
		{
			issuer: "https://issuer.example/tenant/",
			want: []string{
				"https://issuer.example/.well-known/oauth-authorization-server/tenant",
				"https://issuer.example/.well-known/openid-configuration/tenant",
				"https://issuer.example/tenant/.well-known/openid-configuration",
			},
		},
	}
	for _, tc := range cases {
		if got := authorizationServerMetadataURLs(tc.issuer); !slices.Equal(got, tc.want) {
			t.Fatalf("authorizationServerMetadataURLs(%q) = %v, want %v", tc.issuer, got, tc.want)
		}
	}
}

func TestParseOAuthTokenResponseValidatesExpiresInDuration(t *testing.T) {
	for _, expiresIn := range []string{"9223372037", "0", "-1", `""`} {
		body := []byte(`{"access_token":"token","token_type":"Bearer","expires_in":` + expiresIn + `}`)
		if _, err := parseOAuthTokenResponse(body); err == nil {
			t.Fatalf("parseOAuthTokenResponse accepted expires_in %s", expiresIn)
		}
	}
	for _, body := range [][]byte{
		[]byte(`{"access_token":"token","token_type":"Bearer"}`),
		[]byte(`{"access_token":"token","token_type":"Bearer","expires_in":null}`),
	} {
		token, err := parseOAuthTokenResponse(body)
		if err != nil {
			t.Fatalf("parseOAuthTokenResponse(%s): %v", body, err)
		}
		if token.ExpiresIn != 0 {
			t.Fatalf("parseOAuthTokenResponse(%s) ExpiresIn = %s, want zero", body, token.ExpiresIn)
		}
	}

	body := []byte(`{"access_token":"token","token_type":"Bearer","expires_in":9223372036}`)
	token, err := parseOAuthTokenResponse(body)
	if err != nil {
		t.Fatalf("parseOAuthTokenResponse: %v", err)
	}
	want := time.Duration(secrets.MaxOAuthAccessTokenTTLSeconds) * time.Second
	if token.ExpiresIn != want {
		t.Fatalf("ExpiresIn = %s, want %s", token.ExpiresIn, want)
	}
}

func TestParseOAuthTokenResponseRequiresBearerTokenType(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{name: "canonical", body: `{"access_token":"token","token_type":"Bearer"}`},
		{name: "case insensitive", body: `{"access_token":"token","token_type":"bearer"}`},
		{name: "missing", body: `{"access_token":"token"}`, wantErr: "missing token_type"},
		{name: "unsupported", body: `{"access_token":"token","token_type":"MAC"}`, wantErr: "not Bearer"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			token, err := parseOAuthTokenResponse([]byte(test.body))
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("parseOAuthTokenResponse error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseOAuthTokenResponse: %v", err)
			}
			if token.TokenType != oauthBearerTokenType {
				t.Fatalf("TokenType = %q, want %q", token.TokenType, oauthBearerTokenType)
			}
		})
	}
}

func TestSanitizeOAuthErrorCode(t *testing.T) {
	validBoundaries := string([]byte{0x20, 0x21, 0x23, 0x5b, 0x5d, 0x7e})
	tests := []struct {
		name string
		code string
		want string
	}{
		{name: "standard", code: "invalid_client", want: "invalid_client"},
		{name: "allowed boundaries", code: validBoundaries, want: validBoundaries},
		{
			name: "maximum length",
			code: strings.Repeat("a", maxOAuthErrorCodeBytes),
			want: strings.Repeat("a", maxOAuthErrorCodeBytes),
		},
		{name: "too long", code: strings.Repeat("a", maxOAuthErrorCodeBytes+1)},
		{name: "control character", code: "invalid\nclient"},
		{name: "quote", code: `invalid"client`},
		{name: "backslash", code: `invalid\client`},
		{name: "non ASCII", code: "invalid_clïent"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := sanitizeOAuthErrorCode(test.code); got != test.want {
				t.Fatalf("sanitizeOAuthErrorCode(%q) = %q, want %q", test.code, got, test.want)
			}
		})
	}
}

func TestOAuthTokenLifetimeIncludesPostExchangeDelay(t *testing.T) {
	token := OAuthTokenSet{
		ExpiresIn:       5 * time.Second,
		lifetimeStarted: time.Now().Add(-2 * time.Second),
	}
	remaining, err := token.AccessTokenLifetime().Remaining()
	if err != nil {
		t.Fatalf("remaining lifetime: %v", err)
	}
	if remaining <= 2*time.Second || remaining > 3*time.Second {
		t.Fatalf("remaining lifetime = %s, want approximately 3s", remaining)
	}

	token.ExpiresIn = time.Second
	if _, err := token.AccessTokenLifetime().Remaining(); err == nil {
		t.Fatal("expired token retained a persistable lifetime")
	}
}
