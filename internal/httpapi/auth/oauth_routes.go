package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"golang.org/x/oauth2"
)

const (
	maxOAuthResponseBytes     = 1024 * 1024
	oauthFlowCookieName       = "omnara_oauth_flow"
	oauthFlowHostCookieName   = "__Host-omnara_oauth_flow"
	oauthFlowCookieTTL        = 10 * time.Minute
	oauthLoginRateLimit       = 30
	oauthLoginClientRateLimit = 120
	googleOIDCIssuer          = "https://accounts.google.com"
)

func (h *Handler) connectorLoginRoute(w http.ResponseWriter, r *http.Request) {
	h.startExternalLogin(w, r, r.PathValue("connector"))
}

func (h *Handler) connectorCallbackRoute(w http.ResponseWriter, r *http.Request) {
	h.completeExternalLogin(w, r, r.PathValue("connector"))
}

func (h *Handler) startExternalLogin(w http.ResponseWriter, r *http.Request, slug string) {
	if h.publicURL == "" {
		apierror.Write(w, openapi.ErrorCodeAuthenticationUnavailable)
		return
	}
	connector, ok := h.enabledConnector(w, r, slug)
	if !ok {
		return
	}
	if h.oauthStates == nil {
		apierror.Write(w, openapi.ErrorCodeAuthenticationUnavailable)
		return
	}
	now := time.Now().UTC()
	state, err := RandomURLToken(32)
	if err != nil {
		h.writeAuthServerError(w, r, err)
		return
	}
	nonce, err := RandomURLToken(32)
	if err != nil {
		h.writeAuthServerError(w, r, err)
		return
	}
	browserBinding, err := RandomURLToken(32)
	if err != nil {
		h.writeAuthServerError(w, r, err)
		return
	}
	codeVerifier := oauth2.GenerateVerifier()
	returnTo := SafeReturnTo(r.URL.Query().Get("return_to"))
	if !h.requireOAuthLoginRateLimits(w, r, connector.Slug) {
		return
	}
	if _, err := h.oauthStates.Create(
		r.Context(),
		OAuthStateCreateInput{
			AuthConnectorID:     connector.ID,
			State:               state,
			BrowserBindingToken: browserBinding,
			CodeVerifier:        codeVerifier,
			Nonce:               nonce,
			ReturnTo:            returnTo,
		},
	); err != nil {
		h.writeAuthServerError(w, r, err)
		return
	}
	oauthConfig, err := h.oauthConfig(r.Context(), connector, callbackPath(slug), nil)
	if err != nil {
		h.writeAuthServerError(w, r, err)
		return
	}
	options := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(codeVerifier)}
	if connector.Kind == identitystore.AuthConnectorKindGitHub ||
		(connector.Kind == identitystore.AuthConnectorKindOIDC && connector.Issuer == googleOIDCIssuer) {
		options = append(options, oauth2.SetAuthURLParam("prompt", "select_account"))
	}
	if connector.Kind == identitystore.AuthConnectorKindOIDC {
		options = append(options, oauth2.SetAuthURLParam("nonce", nonce))
	}
	setOAuthFlowCookie(w, r, h.publicURL, browserBinding, now.Add(oauthFlowCookieTTL))
	http.Redirect(w, r, oauthConfig.AuthCodeURL(state, options...), http.StatusFound)
}

func (h *Handler) completeExternalLogin(w http.ResponseWriter, r *http.Request, slug string) {
	if h.publicURL == "" {
		apierror.Write(w, openapi.ErrorCodeAuthenticationUnavailable)
		return
	}
	connector, ok := h.enabledConnector(w, r, slug)
	if !ok {
		return
	}
	if h.oauthStates == nil {
		apierror.Write(w, openapi.ErrorCodeAuthenticationUnavailable)
		return
	}
	stateValue := r.URL.Query().Get("state")
	if stateValue == "" {
		redirectAuthOutcome(w, r, "/", url.Values{"auth_error": []string{"invalid_callback"}})
		return
	}
	browserBinding, err := oauthFlowCookie(r, h.publicURL)
	if err != nil {
		redirectAuthOutcome(w, r, "/", url.Values{"auth_error": []string{"invalid_state"}})
		return
	}
	state, err := h.oauthStates.Consume(r.Context(), connector.ID, stateValue, browserBinding.Value)
	if errors.Is(err, storeerr.ErrUnauthorized) {
		redirectAuthOutcome(w, r, "/", url.Values{"auth_error": []string{"invalid_state"}})
		return
	}
	if err != nil {
		h.writeAuthServerError(w, r, err)
		return
	}
	clearOAuthFlowCookie(w, r, h.publicURL)
	if providerError := r.URL.Query().Get("error"); providerError != "" {
		redirectAuthOutcome(w, r, state.ReturnTo, url.Values{"auth_error": []string{providerError}})
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		redirectAuthOutcome(w, r, state.ReturnTo, url.Values{"auth_error": []string{"invalid_callback"}})
		return
	}
	var provider *oidc.Provider
	if connector.Kind == identitystore.AuthConnectorKindOIDC {
		provider, err = oidc.NewProvider(h.oauthContext(r.Context()), connector.Issuer)
		if err != nil {
			h.writeAuthServerError(w, r, fmt.Errorf("discover oidc provider: %w", err))
			return
		}
	}
	oauthConfig, err := h.oauthConfig(r.Context(), connector, callbackPath(slug), provider)
	if err != nil {
		h.writeAuthServerError(w, r, err)
		return
	}
	ctx := h.oauthContext(r.Context())
	token, err := oauthConfig.Exchange(ctx, code, oauth2.VerifierOption(state.CodeVerifier))
	if err != nil {
		redirectAuthOutcome(w, r, state.ReturnTo, url.Values{"auth_error": []string{"token_exchange_failed"}})
		return
	}
	identity, err := h.externalIdentity(ctx, connector, token, state.Nonce, provider)
	if err != nil {
		redirectAuthOutcome(w, r, state.ReturnTo, url.Values{"auth_error": []string{"identity_failed"}})
		return
	}
	sessionToken, csrfToken, err := newBrowserSessionTokens()
	if err != nil {
		h.writeAuthServerError(w, r, err)
		return
	}
	if _, err := h.store.ResolveAuthIdentityUserAndCreateSession(
		r.Context(),
		identitystore.ResolveAuthIdentitySessionInput{
			ResolveAuthIdentityInput: identitystore.ResolveAuthIdentityInput{
				AuthConnectorID: connector.ID,
				Issuer:          identity.Issuer,
				Subject:         identity.Subject,
				Email:           identity.Email,
				EmailVerified:   identity.EmailVerified,
				DisplayName:     identity.DisplayName,
			},
			SessionToken:     sessionToken,
			SessionCSRFToken: csrfToken,
			SessionTTL:       browserSessionTTL,
		},
	); err != nil {
		if errors.Is(err, storeerr.ErrUnauthorized) {
			redirectAuthOutcome(w, r, state.ReturnTo, url.Values{"auth_error": []string{"identity_failed"}})
			return
		}
		h.writeAuthServerError(w, r, err)
		return
	}
	SetBrowserSessionCookies(w, r, h.publicURL, sessionToken, csrfToken, browserSessionTTL)
	setLastLoginMethodCookie(w, r, h.publicURL, "connector:"+connector.Slug, time.Now().UTC())
	redirectAuthOutcome(w, r, state.ReturnTo, nil)
}

type externalIdentity struct {
	Issuer        string
	Subject       string
	Email         string
	EmailVerified bool
	DisplayName   string
}

type oidcClaims struct {
	Subject         string `json:"sub"`
	AuthorizedParty string `json:"azp"`
	Email           string `json:"email"`
	EmailVerified   *bool  `json:"email_verified"`
	HostedDomain    string `json:"hd"`
	Name            string `json:"name"`
	Nonce           string `json:"nonce"`
}

func (h *Handler) externalIdentity(
	ctx context.Context,
	connector identitystore.AuthConnectorRecord,
	token *oauth2.Token,
	nonce string,
	provider *oidc.Provider,
) (externalIdentity, error) {
	switch connector.Kind {
	case identitystore.AuthConnectorKindOIDC:
		return h.oidcIdentity(ctx, connector, token, nonce, provider)
	case identitystore.AuthConnectorKindGitHub:
		return h.githubIdentity(ctx, connector, token)
	default:
		return externalIdentity{}, fmt.Errorf("unsupported auth connector kind %q", connector.Kind)
	}
}

func (h *Handler) oidcIdentity(
	ctx context.Context,
	connector identitystore.AuthConnectorRecord,
	token *oauth2.Token,
	nonce string,
	provider *oidc.Provider,
) (externalIdentity, error) {
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return externalIdentity{}, errors.New("oidc token response missing id_token")
	}
	if provider == nil {
		var err error
		provider, err = oidc.NewProvider(ctx, connector.Issuer)
		if err != nil {
			return externalIdentity{}, fmt.Errorf("discover oidc provider: %w", err)
		}
	}
	idToken, err := provider.Verifier(&oidc.Config{ClientID: connector.ClientID}).Verify(ctx, rawIDToken)
	if err != nil {
		return externalIdentity{}, fmt.Errorf("verify oidc id token: %w", err)
	}
	var claims oidcClaims
	if err := idToken.Claims(&claims); err != nil {
		return externalIdentity{}, fmt.Errorf("parse oidc claims: %w", err)
	}
	if claims.Subject == "" {
		return externalIdentity{}, errors.New("oidc subject is required")
	}
	if claims.Nonce != nonce {
		return externalIdentity{}, errors.New("oidc nonce mismatch")
	}
	if len(idToken.Audience) > 1 && claims.AuthorizedParty == "" {
		return externalIdentity{}, errors.New("oidc authorized party is required")
	}
	if claims.AuthorizedParty != "" && claims.AuthorizedParty != connector.ClientID {
		return externalIdentity{}, errors.New("oidc authorized party mismatch")
	}
	if oidcClaimsNeedUserInfo(claims) {
		if userInfo, err := provider.UserInfo(ctx, oauth2.StaticTokenSource(token)); err == nil {
			var userInfoClaims oidcClaims
			if err := userInfo.Claims(&userInfoClaims); err != nil {
				return externalIdentity{}, fmt.Errorf("parse oidc userinfo claims: %w", err)
			}
			if userInfoClaims.Subject == "" || userInfoClaims.Subject != claims.Subject {
				return externalIdentity{}, errors.New("oidc userinfo subject mismatch")
			}
			if claims.Email != "" && userInfoClaims.Email != "" && !strings.EqualFold(claims.Email, userInfoClaims.Email) {
				return externalIdentity{}, errors.New("oidc userinfo email mismatch")
			}
			if claims.Email == "" {
				claims.Email = userInfoClaims.Email
				claims.EmailVerified = userInfoClaims.EmailVerified
			} else if claims.EmailVerified == nil &&
				userInfoClaims.Email != "" &&
				strings.EqualFold(claims.Email, userInfoClaims.Email) {
				claims.EmailVerified = userInfoClaims.EmailVerified
			}
			if claims.Name == "" {
				claims.Name = userInfoClaims.Name
			}
			if claims.HostedDomain == "" {
				claims.HostedDomain = userInfoClaims.HostedDomain
			}
		} else if h.log != nil {
			h.log.WarnContext(ctx, "oidc userinfo lookup failed", "issuer", connector.Issuer, "error", err)
		}
	}
	emailVerified := claims.EmailVerified != nil && *claims.EmailVerified
	if emailVerified && connector.Issuer == googleOIDCIssuer {
		emailVerified = googleEmailAuthoritative(claims.Email, claims.HostedDomain)
	}
	return externalIdentity{
		Issuer:        idToken.Issuer,
		Subject:       claims.Subject,
		Email:         claims.Email,
		EmailVerified: emailVerified,
		DisplayName:   claims.Name,
	}, nil
}

func oidcClaimsNeedUserInfo(claims oidcClaims) bool {
	return claims.Email == "" || claims.EmailVerified == nil
}

func (h *Handler) githubIdentity(
	ctx context.Context,
	connector identitystore.AuthConnectorRecord,
	token *oauth2.Token,
) (externalIdentity, error) {
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := h.getOAuthJSON(ctx, connector.UserinfoURL, token.AccessToken, &user); err != nil {
		return externalIdentity{}, err
	}
	if user.ID == 0 {
		return externalIdentity{}, errors.New("github user id is required")
	}
	email, verified, err := h.githubPrimaryEmail(ctx, connector, token.AccessToken)
	if err != nil {
		return externalIdentity{}, err
	}
	displayName := strings.TrimSpace(user.Name)
	if displayName == "" {
		displayName = user.Login
	}
	return externalIdentity{
		Issuer:        connector.Issuer,
		Subject:       strconv.FormatInt(user.ID, 10),
		Email:         email,
		EmailVerified: verified,
		DisplayName:   displayName,
	}, nil
}

func (h *Handler) githubPrimaryEmail(
	ctx context.Context,
	connector identitystore.AuthConnectorRecord,
	accessToken string,
) (string, bool, error) {
	emailURL := strings.TrimRight(connector.UserinfoURL, "/") + "/emails"
	if connector.UserinfoURL == "https://api.github.com/user" {
		emailURL = "https://api.github.com/user/emails"
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := h.getOAuthJSON(ctx, emailURL, accessToken, &emails); err != nil {
		return "", false, err
	}
	for _, email := range emails {
		if email.Primary && email.Verified {
			return email.Email, true, nil
		}
	}
	return "", false, nil
}

func googleEmailAuthoritative(email, hostedDomain string) bool {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return false
	}
	domain := strings.ToLower(email[at+1:])
	return domain == "gmail.com" || domain == "googlemail.com" || strings.TrimSpace(hostedDomain) != ""
}

func (h *Handler) getOAuthJSON(ctx context.Context, endpoint, accessToken string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxOAuthResponseBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxOAuthResponseBytes {
		return errors.New("oauth profile response exceeds the byte limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("oauth profile request failed with status %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return err
	}
	return nil
}

func (h *Handler) oauthConfig(
	ctx context.Context,
	connector identitystore.AuthConnectorRecord,
	callbackPath string,
	provider *oidc.Provider,
) (oauth2.Config, error) {
	endpoint := oauth2.Endpoint{AuthURL: connector.AuthorizationURL, TokenURL: connector.TokenURL}
	if connector.Kind == identitystore.AuthConnectorKindOIDC {
		if provider == nil {
			var err error
			provider, err = oidc.NewProvider(h.oauthContext(ctx), connector.Issuer)
			if err != nil {
				return oauth2.Config{}, fmt.Errorf("discover oidc provider: %w", err)
			}
		}
		endpoint = provider.Endpoint()
	}
	if endpoint.AuthURL == "" || endpoint.TokenURL == "" {
		return oauth2.Config{}, errors.New("auth connector endpoints are incomplete")
	}
	return oauth2.Config{
		ClientID:     connector.ClientID,
		ClientSecret: connector.ClientSecret,
		Endpoint:     endpoint,
		RedirectURL:  h.authURL(callbackPath, nil),
		Scopes:       connector.Scopes,
	}, nil
}

func (h *Handler) enabledConnector(
	w http.ResponseWriter,
	r *http.Request,
	slug string,
) (identitystore.AuthConnectorRecord, bool) {
	connector, err := h.store.GetEnabledAuthConnectorBySlug(r.Context(), slug)
	if errors.Is(err, storeerr.ErrNotFound) {
		apierror.Write(w, openapi.ErrorCodeNotFound)
		return identitystore.AuthConnectorRecord{}, false
	}
	if err != nil {
		h.writeAuthServerError(w, r, err)
		return identitystore.AuthConnectorRecord{}, false
	}
	return connector, true
}

func (h *Handler) oauthContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, h.httpClient)
}

func callbackPath(slug string) string {
	return authConnectorsPath + "/" + url.PathEscape(slug) + "/callback"
}

func redirectAuthOutcome(w http.ResponseWriter, r *http.Request, returnTo string, values url.Values) {
	target := SafeReturnTo(returnTo)
	if values.Get("auth_error") != "" {
		if target != "/" && !strings.HasPrefix(target, "/login") {
			values.Set("return_to", target)
		}
		target = "/login"
	}
	if values != nil {
		separator := "?"
		if strings.Contains(target, "?") {
			separator = "&"
		}
		target += separator + values.Encode()
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func setOAuthFlowCookie(w http.ResponseWriter, r *http.Request, publicURL, value string, expires time.Time) {
	secure := requestScheme(publicURL, r) == "https"
	name := oauthFlowCookieName
	if secure {
		name = oauthFlowHostCookieName
	}
	http.SetCookie(
		w,
		&http.Cookie{
			Name:     name,
			Value:    value,
			Path:     "/",
			HttpOnly: true,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
			Expires:  expires,
		},
	)
}

func clearOAuthFlowCookie(w http.ResponseWriter, r *http.Request, publicURL string) {
	secure := requestScheme(publicURL, r) == "https"
	http.SetCookie(
		w,
		&http.Cookie{
			Name:     oauthFlowCookieName,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   -1,
			Expires:  time.Unix(0, 0),
		},
	)
	http.SetCookie(
		w,
		&http.Cookie{
			Name:     oauthFlowHostCookieName,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   -1,
			Expires:  time.Unix(0, 0),
		},
	)
}

func oauthFlowCookie(r *http.Request, publicURL string) (*http.Cookie, error) {
	if cookie, err := r.Cookie(oauthFlowHostCookieName); err == nil {
		return cookie, nil
	}
	if requestScheme(publicURL, r) == "https" {
		return nil, http.ErrNoCookie
	}
	return r.Cookie(oauthFlowCookieName)
}
