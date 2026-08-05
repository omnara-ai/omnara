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
	AppNameMaxRunes          = 35
	manifestResponseMaxBytes = 1024 * 1024
)

var manifestBotEvents = []string{
	"app_mention",
	"app_uninstalled",
	"channel_rename",
	"group_rename",
	"message.channels",
	"message.groups",
	"message.im",
	"message.mpim",
	"tokens_revoked",
	"user_profile_changed",
}

type AppManifest struct {
	DisplayInformation manifestDisplayInformation `json:"display_information"`
	Features           manifestFeatures           `json:"features"`
	OAuthConfig        manifestOAuthConfig        `json:"oauth_config"`
	Settings           manifestSettings           `json:"settings"`
}

type manifestDisplayInformation struct {
	Name            string `json:"name"`
	BackgroundColor string `json:"background_color"`
}

type manifestFeatures struct {
	BotUser manifestBotUser `json:"bot_user"`
	AppHome manifestAppHome `json:"app_home"`
}

type manifestBotUser struct {
	DisplayName  string `json:"display_name"`
	AlwaysOnline bool   `json:"always_online"`
}

type manifestAppHome struct {
	MessagesTabEnabled         bool `json:"messages_tab_enabled"`
	MessagesTabReadOnlyEnabled bool `json:"messages_tab_read_only_enabled"`
}

type manifestOAuthConfig struct {
	Scopes       manifestScopes `json:"scopes"`
	RedirectURLs []string       `json:"redirect_urls"`
}

type manifestScopes struct {
	Bot []string `json:"bot"`
}

type manifestSettings struct {
	EventSubscriptions manifestEventSubscriptions `json:"event_subscriptions"`
	Interactivity      manifestInteractivity      `json:"interactivity"`
	OrgDeployEnabled   bool                       `json:"org_deploy_enabled"`
	SocketModeEnabled  bool                       `json:"socket_mode_enabled"`
	TokenRotation      bool                       `json:"token_rotation_enabled"`
}

type manifestEventSubscriptions struct {
	RequestURL string   `json:"request_url"`
	BotEvents  []string `json:"bot_events"`
}

type manifestInteractivity struct {
	IsEnabled  bool   `json:"is_enabled"`
	RequestURL string `json:"request_url"`
}

type ManifestApp struct {
	AppID         string
	ClientID      string
	ClientSecret  string
	SigningSecret string
}

type manifestCreateResponse struct {
	OK          bool                `json:"ok"`
	Error       string              `json:"error"`
	Errors      []manifestError     `json:"errors"`
	AppID       string              `json:"app_id"`
	Credentials manifestCredentials `json:"credentials"`
}

type manifestError struct {
	Message string `json:"message"`
	Pointer string `json:"pointer"`
}

type manifestCredentials struct {
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	SigningSecret string `json:"signing_secret"`
}

func BuildAppManifest(appName, eventsURL, actionsURL, redirectURI string) AppManifest {
	return AppManifest{
		DisplayInformation: manifestDisplayInformation{Name: appName, BackgroundColor: "#000000"},
		Features: manifestFeatures{
			BotUser: manifestBotUser{DisplayName: appName, AlwaysOnline: true},
			AppHome: manifestAppHome{
				MessagesTabEnabled:         true,
				MessagesTabReadOnlyEnabled: false,
			},
		},
		OAuthConfig: manifestOAuthConfig{
			Scopes:       manifestScopes{Bot: RequiredBotScopes},
			RedirectURLs: []string{redirectURI},
		},
		Settings: manifestSettings{
			EventSubscriptions: manifestEventSubscriptions{RequestURL: eventsURL, BotEvents: manifestBotEvents},
			Interactivity:      manifestInteractivity{IsEnabled: true, RequestURL: actionsURL},
			OrgDeployEnabled:   false,
			SocketModeEnabled:  false,
			TokenRotation:      false,
		},
	}
}

func CreateManifestApp(
	ctx context.Context,
	config OAuthConfig,
	appConfigurationToken string,
	manifest AppManifest,
) (ManifestApp, error) {
	body, err := json.Marshal(manifest)
	if err != nil {
		return ManifestApp{}, err
	}
	values := url.Values{}
	values.Set("token", appConfigurationToken)
	values.Set("manifest", string(body))
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		endpointURL(config.APIURL, "apps.manifest.create"),
		strings.NewReader(values.Encode()),
	)
	if err != nil {
		return ManifestApp{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClientWithoutRedirects(config.HTTPClient).Do(req)
	if err != nil {
		return ManifestApp{}, fmt.Errorf("slack manifest create failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	responseBody, err := readResponseBody(resp.Body, manifestResponseMaxBytes)
	if err != nil {
		return ManifestApp{}, fmt.Errorf("read slack manifest create response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ManifestApp{}, slackStatusError("slack manifest create", resp.StatusCode, responseBody)
	}
	var out manifestCreateResponse
	if err := json.Unmarshal(responseBody, &out); err != nil {
		return ManifestApp{}, fmt.Errorf("decode slack manifest create response: %w", err)
	}
	if !out.OK {
		if out.Error == "" {
			out.Error = "unknown_error"
		}
		if len(out.Errors) > 0 {
			messages := make([]string, 0, len(out.Errors))
			for _, item := range out.Errors {
				if item.Pointer != "" && item.Message != "" {
					messages = append(messages, item.Pointer+": "+item.Message)
				} else if item.Message != "" {
					messages = append(messages, item.Message)
				}
			}
			if len(messages) > 0 {
				return ManifestApp{}, fmt.Errorf(
					"slack manifest create rejected: %s: %s",
					out.Error,
					strings.Join(messages, "; "),
				)
			}
		}
		return ManifestApp{}, fmt.Errorf("slack manifest create rejected: %s", out.Error)
	}
	if out.AppID == "" || out.Credentials.ClientID == "" || out.Credentials.ClientSecret == "" ||
		out.Credentials.SigningSecret == "" {
		return ManifestApp{}, errors.New("slack manifest create response missing credentials")
	}
	return ManifestApp{
		AppID:         out.AppID,
		ClientID:      out.Credentials.ClientID,
		ClientSecret:  out.Credentials.ClientSecret,
		SigningSecret: out.Credentials.SigningSecret,
	}, nil
}
