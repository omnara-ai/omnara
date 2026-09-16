package github

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type AuthorizeInput struct {
	RedirectURI string
	State       string
	// Supply S256 PKCE for an application-initiated authorization flow.
	CodeChallenge string
}

type ExchangeInput struct {
	Code        string
	RedirectURI string
	// Optional only when the correlated authorization flow sent no challenge,
	// such as GitHub's built-in authorization-during-installation flow.
	CodeVerifier string
}

// UserOAuth is ephemeral setup authority, not an installation credential. The
// caller owns encrypted, short-lived storage and consumes its OAuth state once.
type UserOAuth struct {
	AccessToken string
	ExpiresAt   time.Time
}

func (c *Client) AuthorizeURL(input AuthorizeInput) (string, error) {
	if !validRedirect(input.RedirectURI) || !boundedText(input.State, 4096) {
		return "", ErrInvalidInput
	}
	if input.CodeChallenge != "" {
		challenge, err := base64.RawURLEncoding.DecodeString(input.CodeChallenge)
		if err != nil || len(challenge) != 32 || len(input.CodeChallenge) != 43 {
			return "", ErrInvalidInput
		}
	}
	parsed, err := url.Parse(c.authorizeURL)
	if err != nil {
		return "", ErrInvalidConfig
	}
	query := parsed.Query()
	query.Set("client_id", c.clientID)
	query.Set("redirect_uri", input.RedirectURI)
	query.Set("state", input.State)
	if input.CodeChallenge != "" {
		query.Set("code_challenge", input.CodeChallenge)
		query.Set("code_challenge_method", "S256")
	}
	parsed.RawQuery = query.Encode()
	result := parsed.String()
	if len(result) > 8000 {
		return "", ErrInvalidInput
	}
	return result, nil
}

func (c *Client) ExchangeCode(ctx context.Context, input ExchangeInput) (UserOAuth, error) {
	if !boundedText(input.Code, 4096) || !validRedirect(input.RedirectURI) ||
		(input.CodeVerifier != "" && !validVerifier(input.CodeVerifier)) {
		return UserOAuth{}, ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, setupTimeout)
	defer cancel()
	values := url.Values{
		"client_id": {c.clientID}, "client_secret": {c.clientSecret}, "code": {input.Code},
		"redirect_uri": {input.RedirectURI},
	}
	if input.CodeVerifier != "" {
		values.Set("code_verifier", input.CodeVerifier)
	}
	var response struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
	}
	startedAt := time.Now()
	if err := c.request(ctx, http.MethodPost, c.tokenURL, "", values.Encode(), &response); err != nil {
		return UserOAuth{}, err
	}
	if response.Error != "" {
		return UserOAuth{}, ErrOAuthExchange
	}
	if response.ExpiresIn <= 0 {
		return UserOAuth{}, ErrExpiringTokenRequired
	}
	if response.ExpiresIn > 8*60*60 || response.TokenType != "bearer" || response.Scope != "" ||
		!strings.HasPrefix(response.AccessToken, "ghu_") || !boundedText(response.AccessToken, 4096) {
		return UserOAuth{}, ErrInvalidResponse
	}
	return UserOAuth{
		AccessToken: response.AccessToken, ExpiresAt: startedAt.Add(time.Duration(response.ExpiresIn) * time.Second),
	}, nil
}

func validVerifier(value string) bool {
	if len(value) < 43 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || strings.ContainsRune("-._~", char)) {
			return false
		}
	}
	return true
}

func validRedirect(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && boundedText(value, 2048) && parsed.Host != "" && parsed.User == nil &&
		parsed.Fragment == "" && (parsed.Scheme == "https" || parsed.Scheme == "http")
}

func validateOAuth(user UserOAuth) error {
	if !strings.HasPrefix(user.AccessToken, "ghu_") || !boundedText(user.AccessToken, 4096) {
		return ErrInvalidInput
	}
	if !time.Now().Before(user.ExpiresAt) {
		return ErrOAuthExpired
	}
	return nil
}
