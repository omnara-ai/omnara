package github

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// AppIdentity contains provider-verified setup facts, never credentials. The
// App owner, App ID, installation ID and bot user ID are distinct identities.
type AppIdentity struct {
	AppID          int64  `json:"app_id"`
	InstallationID int64  `json:"installation_id"`
	AppSlug        string `json:"app_slug"`
	BotUserID      int64  `json:"bot_user_id"`
	BotLogin       string `json:"bot_login"`
	DisplayName    string `json:"display_name"`
}

// CheckAppIdentity verifies the supplied private key's App, the installation's
// ownership, and its bot account using documented endpoints. Setup must persist
// these observations against the same validated credential revision. All calls
// share the operation deadline and BeforeRequest hook.
//
// The temporary bot-lookup token has metadata-read permission only. Setup has no
// selected repository yet; this token is never cached or used by PR tools, whose
// separate tokens remain repository_ids restricted. Authentication also allows
// bot lookup for organizations using Enterprise Managed Users. Once a token is
// returned, revocation is best effort on every exit using the same operation
// context and BeforeRequest hook; cleanup never replaces the identity result.
func (c *Client) CheckAppIdentity(ctx context.Context) (AppIdentity, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	jwt, err := c.appJWT()
	if err != nil {
		return AppIdentity{}, err
	}
	var app struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	if _, err := c.doJSON(ctx, http.MethodGet, "/app", jwt, nil, &app, false); err != nil {
		return AppIdentity{}, err
	}
	if app.ID != c.appID || !validAppSlug(app.Slug) {
		return AppIdentity{}, &APIError{Code: ScopeMismatch}
	}
	path := "/app/installations/" + strconv.FormatInt(c.installationID, 10)
	var installation Installation
	if _, err := c.doJSON(ctx, http.MethodGet, path, jwt, nil, &installation, false); err != nil {
		return AppIdentity{}, err
	}
	if installation.ID != c.installationID || installation.AppID != app.ID {
		return AppIdentity{}, &APIError{Code: ScopeMismatch}
	}
	input := struct {
		Permissions map[string]string `json:"permissions"`
	}{map[string]string{"metadata": "read"}}
	var token struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	_, tokenErr := c.doJSON(ctx, http.MethodPost, path+"/access_tokens", jwt, input, &token, false)
	if token.Token != "" && !strings.ContainsAny(token.Token, "\r\n") {
		defer func() {
			// 204 has no JSON body. Revocation authenticates with the temporary
			// installation token itself, not the App JWT. No background retry or
			// detached context may extend the operation or bypass caller authority.
			_, _, _ = c.do(ctx, http.MethodDelete, "/installation/token", token.Token, jsonMediaType, nil, true)
		}()
	}
	if tokenErr != nil {
		return AppIdentity{}, tokenErr
	}
	if token.Token == "" || strings.ContainsAny(token.Token, "\r\n") || !token.ExpiresAt.After(c.now().Add(time.Minute)) {
		return AppIdentity{}, &APIError{Code: InvalidResponse}
	}
	login := app.Slug + "[bot]"
	var bot User
	botPath := "/users/" + url.PathEscape(login)
	if _, err := c.doJSON(ctx, http.MethodGet, botPath, token.Token, nil, &bot, false); err != nil {
		return AppIdentity{}, err
	}
	if bot.ID <= 0 || bot.Type != "Bot" || !strings.EqualFold(bot.Login, login) {
		return AppIdentity{}, &APIError{Code: ScopeMismatch}
	}
	displayName := strings.TrimSpace(app.Name)
	if displayName == "" {
		displayName = bot.Login
	}
	return AppIdentity{
		AppID: app.ID, InstallationID: installation.ID, AppSlug: app.Slug,
		BotUserID: bot.ID, BotLogin: bot.Login, DisplayName: displayName,
	}, nil
}

func validAppSlug(slug string) bool {
	if slug == "" || len(slug) > 100 {
		return false
	}
	for _, ch := range slug {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-') {
			return false
		}
	}
	return true
}
