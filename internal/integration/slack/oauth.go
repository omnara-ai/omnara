package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const (
	defaultAuthorizeURL    = "https://slack.com/oauth/v2/authorize"
	authorizeURLMaxBytes   = 8000
	accessResponseMaxBytes = 1024 * 1024
)

var (
	ErrStateTooLarge = errors.New("slack oauth state exceeds maximum size")
	ErrMissingScope  = errors.New("slack oauth missing required scope")
)

var RequiredBotScopes = []string{
	"app_mentions:read",
	"chat:write",
	"channels:history",
	"channels:read",
	"files:read",
	"groups:history",
	"groups:read",
	"im:history",
	"im:read",
	"mpim:history",
	"mpim:read",
	"reactions:write",
	"users:read",
}

type OAuthConfig struct {
	AuthorizeURL string
	AccessURL    string
	APIURL       string
	HTTPClient   *http.Client
}

type oauthAccessResponse struct {
	OK                  bool       `json:"ok"`
	Error               string     `json:"error"`
	AccessToken         string     `json:"access_token"`
	TokenType           string     `json:"token_type"`
	Scope               string     `json:"scope"`
	BotUserID           string     `json:"bot_user_id"`
	AppID               string     `json:"app_id"`
	Team                *oauthTeam `json:"team"`
	IsEnterpriseInstall bool       `json:"is_enterprise_install"`
}

type oauthTeam struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type OAuthInstall struct {
	AccessToken string
	AppID       string
	BotUserID   string
	TenantID    string
	TeamName    string
}

func AuthorizeURL(config OAuthConfig, clientID, redirectURI, state string) (string, error) {
	base := strings.TrimSpace(config.AuthorizeURL)
	if base == "" {
		base = defaultAuthorizeURL
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("client_id", clientID)
	query.Set("scope", strings.Join(RequiredBotScopes, ","))
	query.Set("redirect_uri", redirectURI)
	query.Set("state", state)
	parsed.RawQuery = query.Encode()
	out := parsed.String()
	if len(out) > authorizeURLMaxBytes {
		return "", ErrStateTooLarge
	}
	return out, nil
}

func CompleteOAuth(
	ctx context.Context,
	config OAuthConfig,
	clientID, clientSecret, code, redirectURI string,
) (OAuthInstall, error) {
	response, err := exchangeOAuthCode(ctx, config, clientID, clientSecret, code, redirectURI)
	if err != nil {
		return OAuthInstall{}, err
	}
	grantedScopes := GrantedScopes(response.Scope)
	if missing := missingScopes(grantedScopes); len(missing) > 0 {
		return OAuthInstall{}, ErrMissingScope
	}
	return OAuthInstall{
		AccessToken: response.AccessToken,
		AppID:       response.AppID,
		BotUserID:   response.BotUserID,
		TenantID:    response.Team.ID,
		TeamName:    response.Team.Name,
	}, nil
}

func exchangeOAuthCode(
	ctx context.Context,
	config OAuthConfig,
	clientID, clientSecret, code, redirectURI string,
) (oauthAccessResponse, error) {
	endpoint := strings.TrimSpace(config.AccessURL)
	if endpoint == "" {
		endpoint = endpointURL(config.APIURL, "oauth.v2.access")
	}
	values := url.Values{}
	values.Set("client_id", clientID)
	values.Set("client_secret", clientSecret)
	values.Set("code", code)
	values.Set("redirect_uri", redirectURI)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return oauthAccessResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClientWithoutRedirects(config.HTTPClient).Do(req)
	if err != nil {
		return oauthAccessResponse{}, fmt.Errorf("slack oauth exchange failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := readResponseBody(resp.Body, accessResponseMaxBytes)
	if err != nil {
		return oauthAccessResponse{}, fmt.Errorf("read slack oauth response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return oauthAccessResponse{}, slackStatusError("slack oauth exchange", resp.StatusCode, body)
	}
	var out oauthAccessResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return oauthAccessResponse{}, fmt.Errorf("decode slack oauth response: %w", err)
	}
	if !out.OK {
		if out.Error == "" {
			out.Error = "unknown_error"
		}
		return oauthAccessResponse{}, fmt.Errorf("slack oauth exchange rejected: %s", out.Error)
	}
	if err := validateOAuthAccessResponse(out); err != nil {
		return oauthAccessResponse{}, err
	}
	return out, nil
}

func validateOAuthAccessResponse(out oauthAccessResponse) error {
	if out.AccessToken == "" {
		return errors.New("slack oauth response missing access token")
	}
	if out.TokenType != "bot" {
		return errors.New("slack oauth response token_type must be bot")
	}
	if out.AppID == "" {
		return errors.New("slack oauth response missing app_id")
	}
	if out.BotUserID == "" {
		return errors.New("slack oauth response missing bot_user_id")
	}
	if out.Team == nil || out.Team.ID == "" {
		return errors.New("slack oauth response missing team.id")
	}
	if out.IsEnterpriseInstall {
		return errors.New("slack enterprise grid installs are not supported")
	}
	return nil
}

func GrantedScopes(raw string) map[string]bool {
	out := map[string]bool{}
	for _, scope := range strings.Split(raw, ",") {
		scope = strings.TrimSpace(scope)
		if scope != "" {
			out[scope] = true
		}
	}
	return out
}

func missingScopes(granted map[string]bool) []string {
	var missing []string
	for _, scope := range RequiredBotScopes {
		if !granted[scope] {
			missing = append(missing, scope)
		}
	}
	return missing
}
