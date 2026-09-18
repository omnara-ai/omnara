package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type oidcTestStore struct {
	Store
	key           *rsa.PrivateKey
	authenticated identitystore.OAuthAccessTokenAuthentication
	email         identitystore.UserEmailRecord
}

func (s oidcTestStore) OIDCSigningKey(context.Context) (*rsa.PrivateKey, error) { return s.key, nil }
func (s oidcTestStore) AuthenticateOAuthAccessToken(
	_ context.Context, token string,
) (identitystore.OAuthAccessTokenAuthentication, error) {
	if token != "valid" {
		return identitystore.OAuthAccessTokenAuthentication{}, storeerr.ErrUnauthorized
	}
	return s.authenticated, nil
}
func (s oidcTestStore) PrimaryVerifiedEmailForUser(
	context.Context, uuid.UUID,
) (identitystore.UserEmailRecord, bool, error) {
	return s.email, s.email.VerifiedAt != nil, nil
}

func TestOIDCScopes(t *testing.T) {
	for scope, valid := range map[string]bool{
		"": true, "openid": true, "openid email": true, "email openid": true,
		"email": false, "profile": false, "openid files:read": false,
	} {
		t.Run(scope, func(t *testing.T) {
			values := validAuthorizeValues()
			values.Set("scope", scope)
			values.Set("nonce", "nonce-123")
			request, err := resolveAuthorizeValues(values)
			if (err == nil) != valid {
				t.Fatalf("scope %q: %v", scope, err)
			}
			if valid && (request.Scope != scope || request.Nonce != "nonce-123") {
				t.Fatalf("lost OIDC context: %+v", request)
			}
		})
	}
}

func TestOIDCDiscoveryUsesConfiguredTunnel(t *testing.T) {
	const issuer = "https://agents-demo-tunnel.omnara.ai"
	handler := &Handler{publicURL: issuer}
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	for _, path := range []string{OIDCDiscoveryPath, OAuthAuthorizationServerMetadataPath} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, issuer+path, nil))
		var metadata map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &metadata); err != nil {
			t.Fatal(err)
		}
		for field, suffix := range map[string]string{
			"issuer": "", "authorization_endpoint": OAuthAuthorizePagePath, "token_endpoint": OAuthTokenPath,
			"userinfo_endpoint": OIDCUserInfoPath, "jwks_uri": OIDCJWKSPath,
		} {
			if metadata[field] != issuer+suffix {
				t.Errorf("%s = %v", field, metadata[field])
			}
		}
		if strings.Contains(rec.Body.String(), "app.omnara.com") {
			t.Fatal("discovery points to production")
		}
	}
}

func TestOIDCUserInfoRequiresOAuthScopesAndVerifiedEmail(t *testing.T) {
	id := uuid.New()
	now := time.Now()
	for _, tc := range []struct {
		name, token, scope, resource string
		verified                     bool
		status                       int
		email                        bool
	}{
		{"verified email", "valid", "openid email", testResource, true, 200, true},
		{"openid only", "valid", "openid", testResource, true, 200, false},
		{"unverified email", "valid", "openid email", testResource, false, 200, false},
		{"missing scopes", "valid", "", testResource, true, 403, false},
		{"foreign resource", "valid", "openid email", "https://other.example/mcp", true, 401, false},
		{"expired or revoked token", "invalid", "openid email", testResource, true, 401, false},
		{"browser cookie only", "", "openid email", testResource, true, 401, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := oidcTestStore{
				authenticated: identitystore.OAuthAccessTokenAuthentication{
					Principal: identitystore.NewOAuthAccessTokenPrincipal(id, uuid.New()),
					Resource:  tc.resource, Scope: tc.scope,
				},
				email: identitystore.UserEmailRecord{Email: "user@example.com"},
			}
			if tc.verified {
				store.email.VerifiedAt = &now
			}
			handler := &Handler{store: store, publicURL: "https://omnara.test"}
			req := httptest.NewRequest(http.MethodGet, "https://omnara.test"+OIDCUserInfoPath, nil)
			req.AddCookie(&http.Cookie{Name: BrowserSessionHostCookieName, Value: "session"})
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rec := httptest.NewRecorder()
			handler.oidcUserInfoRoute(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var claims map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &claims); err != nil {
				t.Fatal(err)
			}
			_, hasEmail := claims["email"]
			if hasEmail != tc.email {
				t.Fatalf("email disclosure: %v", claims)
			}
			if tc.email && (claims["email_verified"] != true || claims["email"] != "user@example.com") {
				t.Fatalf("email claims: %v", claims)
			}
			if tc.status == 200 && claims["sub"] != id.String() {
				t.Fatalf("subject: %v", claims)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("userinfo must not be cached")
			}
		})
	}
}

func TestOIDCIDTokenVerifiesWithPublishedJWKS(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	id := uuid.New()
	handler := &Handler{
		publicURL: "https://omnara.test",
		store:     oidcTestStore{key: key, email: identitystore.UserEmailRecord{Email: "user@example.com", VerifiedAt: &now}},
	}
	req := httptest.NewRequest(http.MethodGet, "https://omnara.test"+OIDCJWKSPath, nil)
	rec := httptest.NewRecorder()
	handler.oidcJWKSRoute(rec, req)
	var keys jose.JSONWebKeySet
	if err := json.Unmarshal(rec.Body.Bytes(), &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys.Keys) != 1 || !keys.Keys[0].IsPublic() {
		t.Fatal("JWKS must contain only the public key")
	}
	for _, scope := range []string{"openid", "openid email"} {
		signed, err := handler.oidcIDToken(req, identitystore.OAuthTokenSetRecord{
			UserID: id, ClientID: "https://client.example/client.json", Scope: scope, Nonce: "nonce-123",
		})
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := jwt.ParseSigned(signed, []jose.SignatureAlgorithm{jose.RS256})
		if err != nil {
			t.Fatal(err)
		}
		var claims jwt.Claims
		var extra map[string]any
		if err := parsed.Claims(keys.Keys[0].Key, &claims, &extra); err != nil {
			t.Fatal(err)
		}
		if err := claims.Validate(jwt.Expected{
			Issuer: handler.publicURL, Subject: id.String(),
			AnyAudience: jwt.Audience{"https://client.example/client.json"}, Time: now,
		}); err != nil {
			t.Fatal(err)
		}
		if extra["nonce"] != "nonce-123" || parsed.Headers[0].KeyID != keys.Keys[0].KeyID {
			t.Fatal("nonce or key ID missing")
		}
		_, email := extra["email"]
		if email != hasOAuthScope(scope, "email") {
			t.Fatalf("unexpected email claims: %v", extra)
		}
	}
}
