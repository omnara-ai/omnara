package discord

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

	"github.com/omnara-ai/omnara/internal/jsoncanonical"
)

const setupResponseBytes = 1024 * 1024

// Channel permissions only: view, send, attach files, history, public threads
// and messages in threads. Discord still applies each channel's overrides.
const (
	permissionViewChannel           uint64 = 1024
	permissionSendMessages          uint64 = 2048
	permissionAttachFiles           uint64 = 32768
	permissionReadMessageHistory    uint64 = 65536
	permissionCreatePublicThreads   uint64 = 34359738368
	permissionSendMessagesInThreads uint64 = 274877906944
	botPermissions                         = permissionViewChannel | permissionSendMessages | permissionAttachFiles |
		permissionReadMessageHistory | permissionCreatePublicThreads | permissionSendMessagesInThreads
	messageContentIntentFlags uint64 = 262144 | 524288 // GATEWAY_MESSAGE_CONTENT and its limited variant.
)

type SetupConfig struct {
	APIURL, AuthorizeURL, TokenURL string
	HTTPClient                     *http.Client
}

type SetupCredentials struct {
	ApplicationID, ClientSecret, BotToken string
}

type VerifiedInstall struct {
	ApplicationID, BotUserID, GuildID, GuildName string
	ShardCount                                   int
}

func AuthorizeURL(config SetupConfig, applicationID, redirectURI, state string) (string, error) {
	if !validID(applicationID) || redirectURI == "" || state == "" {
		return "", errors.New("discord application, redirect URI and state are required")
	}
	endpoint := config.AuthorizeURL
	if endpoint == "" {
		endpoint = "https://discord.com/oauth2/authorize"
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("client_id", applicationID)
	query.Set("scope", "bot")
	query.Set("response_type", "code")
	query.Set("redirect_uri", redirectURI)
	query.Set("state", state)
	query.Set("permissions", strconv.FormatUint(botPermissions, 10))
	query.Set("integration_type", "0")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// VerifyApplication catches settings the app owner must fix before sending a
// project user through Discord authorization. Completion rechecks these facts.
func VerifyApplication(ctx context.Context, config SetupConfig, credentials SetupCredentials) error {
	if !validID(credentials.ApplicationID) || credentials.BotToken == "" {
		return errors.New("discord application ID and bot token are required")
	}
	var application struct {
		ID                  string `json:"id"`
		Flags               uint64 `json:"flags"`
		BotRequireCodeGrant bool   `json:"bot_require_code_grant"`
	}
	if err := config.getBot(ctx, credentials.BotToken, "applications/@me", &application); err != nil {
		return err
	}
	if application.ID != credentials.ApplicationID || !application.BotRequireCodeGrant {
		return errors.New("discord app identity must match and Require OAuth2 Code Grant must be enabled")
	}
	if application.Flags&messageContentIntentFlags == 0 {
		return errors.New("discord Message Content Intent must be enabled")
	}
	return nil
}

// CompleteSetup uses the guild returned by Discord's authenticated code exchange.
// The browser's guild_id is never accepted as proof. Requiring Discord's code
// grant setting provides this relationship without requesting user guild access.
// https://docs.discord.com/developers/topics/oauth2#advanced-bot-authorization
func CompleteSetup(
	ctx context.Context, config SetupConfig, credentials SetupCredentials, code, redirectURI string,
) (VerifiedInstall, error) {
	var result VerifiedInstall
	if !validID(credentials.ApplicationID) || credentials.ClientSecret == "" || credentials.BotToken == "" || code == "" {
		return result, errors.New("discord setup credentials and code are required")
	}
	if err := VerifyApplication(ctx, config, credentials); err != nil {
		return result, err
	}
	var bot struct {
		ID  string `json:"id"`
		Bot bool   `json:"bot"`
	}
	if err := config.getBot(ctx, credentials.BotToken, "users/@me", &bot); err != nil {
		return result, err
	}
	if !validID(bot.ID) || !bot.Bot {
		return result, errors.New("discord token does not identify a bot")
	}
	values := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {credentials.ApplicationID},
		"client_secret": {credentials.ClientSecret}, "code": {code}, "redirect_uri": {redirectURI},
	}
	tokenURL := config.TokenURL
	if tokenURL == "" {
		tokenURL = "https://discord.com/api/oauth2/token"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return result, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var exchange struct {
		TokenType string `json:"token_type"`
		Scope     string `json:"scope"`
		Guild     struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"guild"`
	}
	if err := config.request(req, &exchange); err != nil {
		return result, err
	}
	hasBotScope := false
	for _, scope := range strings.Fields(exchange.Scope) {
		hasBotScope = hasBotScope || scope == "bot"
	}
	if !strings.EqualFold(exchange.TokenType, "Bearer") || !hasBotScope || !validID(exchange.Guild.ID) {
		return result, errors.New("discord code exchange did not establish a bot installation")
	}
	var guild struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := config.getBot(ctx, credentials.BotToken, "guilds/"+exchange.Guild.ID, &guild); err != nil {
		return result, err
	}
	if guild.ID != exchange.Guild.ID {
		return result, errors.New("discord bot is unavailable in the authorized server")
	}
	var gateway struct {
		Shards int `json:"shards"`
	}
	if err := config.getBot(ctx, credentials.BotToken, "gateway/bot", &gateway); err != nil {
		return result, err
	}
	if gateway.Shards < 1 {
		return result, errors.New("discord returned an invalid shard count")
	}
	return VerifiedInstall{
		ApplicationID: credentials.ApplicationID, BotUserID: bot.ID, GuildID: guild.ID,
		GuildName: guild.Name, ShardCount: gateway.Shards,
	}, nil
}

func (c SetupConfig) getBot(ctx context.Context, token, path string, output any) error {
	base := c.APIURL
	if base == "" {
		base = "https://discord.com/api/v10"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bot "+token)
	return c.request(req, output)
}

func (c SetupConfig) request(req *http.Request, output any) error {
	client := http.DefaultClient
	if c.HTTPClient != nil {
		client = c.HTTPClient
	}
	cloned := *client
	cloned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req.Header.Set("Accept", "application/json")
	response, err := cloned.Do(req)
	if err != nil {
		return fmt.Errorf("discord setup request failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("discord setup returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, setupResponseBytes+1))
	if err != nil {
		return errors.New("discord setup response could not be read")
	}
	if _, err := jsoncanonical.ParseObject(body, setupResponseBytes); err != nil || json.Unmarshal(body, output) != nil {
		return errors.New("discord setup returned an invalid response")
	}
	return nil
}

func validID(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	id, err := strconv.ParseUint(value, 10, 64)
	return err == nil && id != 0 && strconv.FormatUint(id, 10) == value
}
