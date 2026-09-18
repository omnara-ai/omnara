package auth

import (
	"crypto"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	OIDCDiscoveryPath = "/.well-known/openid-configuration"
	OIDCJWKSPath      = "/.well-known/jwks.json"
	OIDCUserInfoPath  = "/api/auth/userinfo"
)

func hasOAuthScope(scope, wanted string) bool {
	return slices.Contains(strings.Fields(scope), wanted)
}

func validOIDCScope(scope string) bool {
	for _, value := range strings.Fields(scope) {
		if value != "openid" && value != "email" {
			return false
		}
	}
	return !hasOAuthScope(scope, "email") || hasOAuthScope(scope, "openid")
}

func oidcPublicJWK(key *rsa.PrivateKey) (jose.JSONWebKey, error) {
	jwk := jose.JSONWebKey{Key: &key.PublicKey, Algorithm: string(jose.RS256), Use: "sig"}
	thumbprint, err := jwk.Thumbprint(crypto.SHA256)
	if err != nil {
		return jose.JSONWebKey{}, err
	}
	jwk.KeyID = base64.RawURLEncoding.EncodeToString(thumbprint)
	return jwk, nil
}

func (h *Handler) oidcJWKSRoute(w http.ResponseWriter, r *http.Request) {
	key, err := h.store.OIDCSigningKey(r.Context())
	if err != nil {
		h.writeOAuthServerError(w, r, err)
		return
	}
	jwk, err := oidcPublicJWK(key)
	if err != nil {
		h.writeOAuthServerError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
}

func (h *Handler) oidcIDToken(r *http.Request, tokens identitystore.OAuthTokenSetRecord) (string, error) {
	key, err := h.store.OIDCSigningKey(r.Context())
	if err != nil {
		return "", err
	}
	jwk, err := oidcPublicJWK(key)
	if err != nil {
		return "", err
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", jwk.KeyID))
	if err != nil {
		return "", err
	}
	now := time.Now()
	claims := map[string]any{
		"iss": h.issuerURL(r), "sub": tokens.UserID.String(), "aud": tokens.ClientID,
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
	}
	if tokens.Nonce != "" {
		claims["nonce"] = tokens.Nonce
	}
	if hasOAuthScope(tokens.Scope, "email") {
		email, found, err := h.store.PrimaryVerifiedEmailForUser(r.Context(), tokens.UserID)
		if err != nil {
			return "", err
		}
		if found && email.VerifiedAt != nil {
			claims["email"] = email.Email
			claims["email_verified"] = true
		}
	}
	return jwt.Signed(signer).Claims(claims).Serialize()
}

// UserInfo authenticates OAuth access tokens directly: browser cookies, PATs,
// and tokens for a different MCP resource cannot disclose this identity.
func (h *Handler) oidcUserInfoRoute(w http.ResponseWriter, r *http.Request) {
	fields := strings.Fields(r.Header.Get("Authorization"))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeOAuthError(w, http.StatusUnauthorized, "invalid_token", "an OAuth access token is required")
		return
	}
	authenticated, err := h.store.AuthenticateOAuthAccessToken(r.Context(), fields[1])
	if errors.Is(err, storeerr.ErrUnauthorized) ||
		(err == nil && !slices.Contains(h.allowedMCPResourceURLs(r), authenticated.Resource)) {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		writeOAuthError(w, http.StatusUnauthorized, "invalid_token", "the access token is invalid or expired")
		return
	}
	if err != nil {
		h.writeOAuthServerError(w, r, err)
		return
	}
	if !hasOAuthScope(authenticated.Scope, "openid") {
		w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="openid"`)
		writeOAuthError(w, http.StatusForbidden, "insufficient_scope", "openid scope is required")
		return
	}
	claims := map[string]any{"sub": authenticated.Principal.ID.String()}
	if hasOAuthScope(authenticated.Scope, "email") {
		email, found, err := h.store.PrimaryVerifiedEmailForUser(r.Context(), authenticated.Principal.ID)
		if err != nil {
			h.writeOAuthServerError(w, r, err)
			return
		}
		if found && email.VerifiedAt != nil {
			claims["email"] = email.Email
			claims["email_verified"] = true
		}
	}
	writeOAuthJSON(w, http.StatusOK, claims)
}
